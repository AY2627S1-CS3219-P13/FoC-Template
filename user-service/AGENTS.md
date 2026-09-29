# User Service working notes

- User Service owns accounts, credentials, sessions and roles. It is the only service that stores passwords, and the only source of truth for who a request belongs to.
- Public API on port 8080; `/internal/v1/*` on port 8081, bearer-authenticated with `USER_INTERNAL_TOKEN` and never exposed through the gateway.
- Handlers are `func(http.ResponseWriter, *http.Request) error` wrapped by the shared endpoint helper; every failure is returned as `{"error":{"code","message"}}`.
- Configuration is validated at startup and the process refuses to boot on an invalid origin, cookie, secret or duration.
- Passwords use Argon2id; tokens, verification codes and password comparisons must stay constant-time. Never log or return credentials, session tokens or verification codes.
- Migrations are numbered, embedded and applied under an advisory lock. Add new migrations; do not edit applied ones.
- Run from the repository root: `sh user-service/scripts/init-env.sh`, `sh credit-service/scripts/init-env.sh`, then `docker compose up --build -d gateway`.
- Check backend: `docker compose --profile test run --build --rm user-tests` (formatting, vet, race-enabled tests, build).
- Format in a container: `docker compose --profile test run --rm --no-deps -v ./user-service:/app user-tests gofmt -w cmd internal`.
- Operator-only recovery/fixture grant: `docker compose exec -T user-service user-service promote-admin <verified-email> <operator-label> <reason>`; every successful grant is audited.
- Outgoing mail in development goes to Mailpit at `http://localhost:8025`.
