# User Service

Accounts and authentication for Friend on Campus, written in Go with PostgreSQL.
Start here for setup; [API.md](API.md) describes the HTTP API for frontend and backend teammates.

PlantUML sequence diagrams: [registration and verification](docs/registration-verification.puml), and [login, session validation and logout](docs/login-session-logout.puml).

## What is implemented

- Register with a school email, display name and password.
- Verify ownership using an emailed code; resend if needed.
- Log in, read/update your display name, change your password and log out.
- Store sessions in PostgreSQL so logout takes effect on subsequent requests.
- Let a pre-designated, verified school account claim the first admin role once; admins can change another verified user's role.
- Let other services validate sessions through a separate, authenticated internal API.

**Credit integration is deferred.** Verification does not create a wallet, award credits, call Credit Service or publish an event. There is no broker or outbox dependency. Existing verified accounts will need onboarding when that integration is added.

## Run locally

From the repository root, with Docker Desktop running:

```sh
sh user-service/scripts/init-env.sh
docker compose up --build -d user-service
```

The setup script creates a git-ignored `.env` with random local secrets. It preserves existing values and appends Supplier database credentials if missing. For other settings missing from an older `.env`, use `.env.example` as a reference.

- Public API: `http://localhost:8080`
- Local verification inbox: `http://localhost:8025`
- Ready check: `http://localhost:8080/readyz`
- Internal API, from another Compose service: `http://user-service:8081`

PostgreSQL data is stored in a named volume and survives container restarts. Database and internal API ports are not published to your computer. Mailpit captures email locally; nothing is sent to a real mailbox. A minimal shared Next.js frontend and gateway are now available through the [Supplier setup](../supplier-service/README.md); `docker compose up --build -d gateway` serves the UI and both public APIs at `http://localhost:3000`. The direct User API defaults to port 8080; change `USER_HTTP_PORT` in `.env` if that host port is occupied.

Set `USER_APP_ORIGIN` to the frontend's exact origin. The default is `http://localhost:3000`; frontend requests need `credentials: "include"`. Every public POST, PUT or PATCH needs the matching `Origin` header, including command-line requests. This, JSON-only request bodies and SameSite cookies protect browser mutations against CSRF.

## Chosen defaults

| Area | Behaviour |
| --- | --- |
| Email | Trimmed and lowercased; one account per email; exact domain allowlist |
| Allowed domains | `u.nus.edu,nus.edu.sg` locally; confirm and narrow this to the team's intended student population |
| Display name | 1–50 characters; trimmed; case-insensitive uniqueness among verified accounts |
| Password | 12–128 characters; salted Argon2id hash; never returned or logged |
| Verification | Random 8-digit code; expires after 30 minutes; at most 5 failed attempts per code |
| Resend | At least 60 seconds apart; a successful resend replaces the old code |
| Sessions | Random opaque cookie; only its SHA-256 digest is stored; absolute 24-hour expiry by default |
| Multiple devices | Allowed; logout revokes the current browser session |
| Password changes | Require current password and revoke every session, including the current one |
| Roles | Every account has `user`; administrators also have `admin`; requester/courier are not separate account types |
| Role changes | Require the acting admin's current password, revoke the target's sessions, and write an audit event; an admin cannot change their own role |

Session cookies are HttpOnly and SameSite=Lax. HTTPS deployments use Secure and the `__Host-` prefix. Local HTTP uses `foc_session`; production uses `__Host-foc_session`. JavaScript does not need to read the cookie.

Registration also returns a random `registrationToken`. The frontend keeps it for that signup attempt and sends it with the emailed code; it is not a login session. This prevents a user from accidentally verifying an account someone else preregistered with a password they chose. Restarting a pending registration replaces its password, code and registration token; a verified account is never overwritten. If the token is lost, restart registration. Do not put it in URLs or logs.

Account activation and code consumption happen in one transaction. Database constraints handle concurrent email registration and display-name claims. If two pending users chose the same display name, the second verification can provide another name without losing the valid code.

Rate limits are stored in PostgreSQL: 60 authentication requests/minute per connected IP; per email, 10 login attempts, 5 registrations, 5 resends and 20 verification requests per 10 minutes. Password changes allow 5 attempts per user per 10 minutes; admin role changes allow 10 attempts per admin per 10 minutes. Limits count successful and failed attempts. A 429 includes `Retry-After: 60`; the longer account window may require waiting longer. Up to four expensive authentication operations run at once; excess requests return 503.

Verification email is sent after committing the pending account. If SMTP fails, registration returns 503 with the `registrationToken` and the account remains pending; keep the token and use resend. There is no background delivery retry yet. Resend during cooldown, or for an unknown/already verified account, returns the same 202 response. Resend retains the registration token. Expired challenges remain unusable and can be replaced by resend. Expired sessions/rate-limit records are cleaned hourly; their expiry is enforced on every request even before cleanup.

## Create the first admin

Before starting User Service, set `USER_BOOTSTRAP_ADMIN_EMAIL` in the deployment environment to the exact school address of the designated initial admin. It must belong to `USER_ALLOWED_EMAIL_DOMAINS`. This is optional; leaving it blank disables self-service bootstrap. The setup script preserves an existing `.env`, so add the value yourself if `.env` already exists, then recreate User Service to load it.

The designated person registers and verifies that address, logs in, opens `/admin/setup` on the frontend, and selects **Claim administrator access**. Their current sessions are revoked; they log in again to use `/admin`. The claim is consumed once in PostgreSQL, including if the account later loses admin access or the deployment setting changes. Other signed-in users cannot claim it. Existing installations with an admin have the claim marked consumed during migration.

Alternatively, an operator with access to the User Service container can grant a verified account admin access for emergency recovery (or local test setup). The command now requires an operator label and reason; it revokes the target's sessions, writes an audit event, and permanently consumes the bootstrap claim:

```sh
docker compose exec -T user-service user-service promote-admin your-email@u.nus.edu operator-name "emergency recovery after admin access loss"
```

The account must log in again afterward. Restrict container-execution and database credentials to designated operators; the label is self-declared and must be corroborated with platform/IAM logs. For production recovery, verify the incident, obtain independent approval, run the command, review the audit event, and notify the restored admin. The command is not a public API or a replacement for normal admin-to-admin promotion. There is no public admin signup or shared default admin password. Locally, Mailpit captures every verification email, so the claim demonstrates the workflow but does not prove real mailbox ownership; deployment needs real email delivery or institutional identity verification. Keep the designated mailbox private.

Each successful bootstrap, admin role change, and operator grant writes a row to `admin_role_events` in the same transaction as the privilege change. Admins can read recent events through `GET /api/v1/admin/role-events` or the admin page. The table preserves actor and target identifiers even if an account is later removed. Events predating migration `003_admin_role_events.sql` cannot be reconstructed automatically. Database administrators can still modify local tables, so production audit integrity also depends on restricted database access and external log retention.

## Test

```sh
docker compose --profile test run --build --rm user-tests
```

This checks formatting, runs `go vet`, runs unit and PostgreSQL integration tests with Go's race detector, and builds the binary. The test database is separate from application data and uses temporary storage. Each integration test gets its own schema. Integration tests cover the full account lifecycle, one-time and concurrent admin claims, concurrent verification, display-name conflicts, code expiry/reuse/attempt limits, email failures, session revocation, role permissions, CSRF and internal API authentication.

To format after changing Go files without installing Go on the host:

```sh
docker compose --profile test run --rm --no-deps --volume ./user-service:/app user-tests gofmt -w cmd internal
```

## Where to read the code

| File | Responsibility |
| --- | --- |
| `cmd/server/main.go` | Startup, migration, two HTTP listeners, shutdown and admin setup command |
| `internal/user/auth.go` | Registration, verification, resend, login and logout |
| `internal/user/profile.go` | Own profile/password and admin role changes |
| `internal/user/http.go` | Routes, cookies, Origin checks, request limits and internal session validation |
| `internal/user/crypto.go` | Password hashing and secure token/code generation |
| `internal/user/store.go` | Migrations, session lookup and housekeeping |
| `internal/user/mail.go` | SMTP delivery, with STARTTLS for deployment |
| `internal/user/migrations/001_initial.sql` | Users, challenges, sessions and rate limits |
| `internal/user/migrations/002_admin_bootstrap.sql` | Durable one-time admin setup state |
| `internal/user/migrations/003_admin_role_events.sql` | Durable role-change audit history |

SQL is parameterised and kept beside the relevant operation. There is no ORM or extra HTTP framework. Migration files are embedded in the binary and applied once at startup under a database lock; add a new numbered file for future changes instead of editing an applied migration.

## Moving to EC2 later

The supplied Compose setup is **for local development**. Build the same application image for EC2, then provide deployment configuration:

1. Put the public API behind HTTPS and set `USER_APP_ORIGIN` to the actual frontend origin and `USER_COOKIE_SECURE=true`. Prefer serving frontend and API through the same site.
2. Keep PostgreSQL and port 8081 private. Do not route `/internal` through the public proxy. Authenticate internal callers with `USER_INTERNAL_TOKEN`; across hosts, also protect transport with TLS.
3. Replace Mailpit with the team's email provider. Configure SMTP credentials and `USER_SMTP_TLS=true` (STARTTLS, typically port 587).
4. Use deployment-specific secrets and database credentials, persistent storage and backups. RDS versus PostgreSQL on EC2 remains open. Use verified database TLS when connecting across hosts; run with a dedicated database account.
5. Configure proxy-level rate limiting before exposing the app. The application deliberately ignores `X-Forwarded-For`; behind a proxy its IP limit applies to the proxy address. Establish a trusted-proxy policy before changing that behaviour.

The current absolute session lifetime, small concurrency cap and rate limits are starting points, not a claim that the project's 100-user performance target has been verified. Password recovery, email changes, frontend screens, automatic email retries and Credit onboarding remain separate work.

Security references: [OWASP password storage](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html), [session management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html), and [CSRF prevention](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html). Toolchain: Go 1.27.1, pgx v5.11.0, x/crypto v0.57.0; local database PostgreSQL 18.
