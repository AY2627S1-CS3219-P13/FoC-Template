# CS3219 — Software Design and Architecture (AY2627 Sem 1)

## Friend on Campus (FoC)

**Friend on Campus (FoC)** is a peer-to-peer campus errand platform where
students can request items to be collected from stores or facilities on
campus, and other students can fulfil (and deliver) those requests. The
platform runs on a closed credit economy — credits cannot be bought,
withdrawn, or exchanged for money, and only circulate within the platform.

---

## Run locally

Run the following commands from the repository root. You need Docker running,
Docker Compose with support for `include` and `up --wait`, a POSIX shell and
OpenSSL. Go, Node.js and PostgreSQL run in containers; no host package install
for those tools is needed.

```sh
sh scripts/start.sh
```

The first run downloads images and builds the application, so allow time for it
to finish. The script generates the git-ignored `.env`, `credit-service/.env`
and `order-service/.env`, then starts all services and their databases. Running
it again rebuilds the application while preserving existing settings and data.
There are no default user accounts or admin passwords.

| What to open | Address | Purpose |
| --- | --- | --- |
| Application | [http://localhost:3000](http://localhost:3000) | Signup, login, supplier catalogue and wallet balance |
| Mailpit inbox | [http://localhost:8025](http://localhost:8025) | Read local verification emails |
| Orders | [http://localhost:3000/orders](http://localhost:3000/orders) | Request and deliver errands after login |
| Admin setup | [http://localhost:3000/admin/setup](http://localhost:3000/admin/setup) | Claim the designated first-admin account |
| Admin console | [http://localhost:3000/admin](http://localhost:3000/admin) | Manage roles and view audit events as an admin |

Use `http://localhost:3000` consistently in the browser. The default allowed
origin is that exact address; switching to `127.0.0.1` or a different port can
cause authentication requests to be rejected.

### Sign up and verify email with Mailpit

1. Open the application and select **Sign up**. Use a unique school email such
   as `local-requester@u.nus.edu`, a unique display name, and a password between
   12 and 128 characters. The default allowed email domains are `u.nus.edu`
   and `nus.edu.sg` (configured by `USER_ALLOWED_EMAIL_DOMAINS` in `.env`).
2. Select **Send verification code** and keep the signup tab open. In a second
   tab, open Mailpit, find the message addressed to your test email, and copy
   its eight-digit verification code.
3. Return to the signup dialog, enter the code and select **Verify**. Then
   log in with the email and password you registered.
4. Check that the supplier catalogue loads, then select **Wallet** beside
   **Orders** to view available, frozen and total credits plus recent transactions.
   New verified accounts receive 100 credits. If the wallet page shows
   **Setting up your credits…**, you can keep using the app while allocation
   retries in the background; the balance refreshes automatically.
5. Refresh the page to check that your session persists, then use **Log out**
   and confirm that browsing requires login again.

Mailpit captures SMTP email inside the local stack; no message reaches a real
mailbox, so local test addresses do not need to exist. Anyone with access to
this local inbox can read its codes. This setup tests the email-authentication
workflow, rather than real mailbox ownership.

Codes expire after 30 minutes and allow at most five failed attempts. If needed,
wait at least 60 seconds before selecting **Resend code**, then use the newest
message; a successful resend invalidates the previous code. The signup tab keeps
a temporary registration token in browser session storage. If you lose that
signup state, restart signup to obtain a new token and code. If verification's
response is lost, try logging in first: verification may already have succeeded.

### Create the first admin

To test supplier management, role management and dispute resolution, create an
admin account as well as ordinary test accounts. On a fresh User database,
claim the first-admin role **before running smoke or browser checks**: their
fixture promotion consumes the same one-time claim, even after fixture cleanup.

1. Generate the root `.env` if it does not exist:

   ```sh
   sh user-service/scripts/init-env.sh
   ```

2. Edit `.env` and set `USER_BOOTSTRAP_ADMIN_EMAIL` to your chosen test address,
   for example `local-admin@u.nus.edu`. It must belong to an allowed domain.
   Leaving this setting blank disables self-service admin setup.
3. Run `sh scripts/start.sh` to load the setting, including if the stack is
   already running. The initializers preserve your edited values.
4. Sign up with that exact address, verify through Mailpit and log in. Open
   `/admin/setup` and select **Claim administrator access**.
5. Log in again because the claim revokes existing sessions. Open `/admin`
   to inspect roles and audit events; return to the catalogue to use its
   admin create, edit, deactivate and delete controls.

The claim can be used only once per User database. Changing the configured
email does not reset it. For an existing database whose claim is already used,
see the [operator recovery command](user-service/README.md#create-the-first-admin).

### Test suppliers, orders and credits in the browser

- As an ordinary user, browse seeded suppliers, search, filter, sort, page
  through results and open supplier details. Management controls require admin
  access. As an admin, create a test supplier, edit it, deactivate it and delete
  it when finished.
- Register and verify a second ordinary account with a different email and
  display name. Use a separate browser profile or private window for it so the
  requester and courier have independent login sessions; tabs in the same
  profile share a session.
- As the requester, open **Orders**, select **New errand**, choose distinct
  active pickup/delivery places, add an item, offer credits within your available
  balance and choose a future expiry. Check the request under **My requests**
  and check the wallet's available/reserved amounts.
- As the second account, open **Help a friend** and accept the request. Use
  **My deliveries** to record pickup and delivery. As the requester, confirm
  delivery, then refresh the catalogue in both browsers to inspect the resulting
  balances. Use another request to test cancellation or a delivery dispute;
  admins resolve disputes from the Orders **Disputes** tab.

See the [Order guide](order-service/README.md#ui-status-updates-and-events) and
[Credit API](credit-service/API.md) for lifecycle and settlement details.

### Automated checks

After claiming the intended first admin, run the end-to-end smoke check:

```sh
sh scripts/smoke.sh
```

It creates temporary account and supplier fixtures in the local application
databases and removes them when it passes. For automated browser checks on
Linux/WSL, run `sh scripts/browser-check.sh`; screenshots and results are saved
under `.agent/tmp/d2-browser/`. If a check fails and leaves fixtures behind, run:

```sh
sh scripts/cleanup-test-data.sh
```

The cleanup script targets generated test fixtures; your manually registered
accounts and suppliers are preserved. Service-level checks run separately:

```sh
docker compose --profile test run --build --rm user-tests
docker compose --profile test run --build --rm supplier-tests
docker compose --profile test run --build --rm credit-tests
docker compose --profile test run --build --rm order-tests
docker compose --profile test run --build --rm frontend-checks
```

### Troubleshooting and stopping

Inspect container status and recent logs from the repository root:

```sh
docker compose ps
docker compose logs --tail=100 gateway frontend user-service credit-service supplier-service order-service mailpit
```

- **Startup fails or times out:** check that Docker is running and your user can
  access it. Inspect logs for build, configuration or database errors, then
  rerun `sh scripts/start.sh` after fixing the cause.
- **Port already allocated:** free ports 3000 and 8025 for the gateway and
  Mailpit. User, Credit, Supplier and Order also publish development ports
  8080, 8082, 8083 and 8084 by default. User/Supplier overrides are documented
  in the root `.env.example`.
- **No verification email:** check the recipient in Mailpit and inspect User
  Service/Mailpit logs. The local SMTP default is `USER_SMTP_ADDRESS=mailpit:1025`;
  Mailpit's browser inbox uses port 8025. After correcting `.env`, rerun startup
  and resend the code after the cooldown.
- **Login or verification rejected:** confirm the exact email/password, latest
  code and original signup tab. An unverified account cannot log in. Repeated
  authentication attempts can hit rate limits; wait before retrying.
- **Admin setup unavailable:** check the configured email, verify that account
  and confirm that the one-time claim has not already been consumed.

To stop the stack while keeping database data:

```sh
docker compose down
```

Start it again with `sh scripts/start.sh`. Named database volumes survive this
shutdown, so accounts, balances and records remain. `docker compose down -v`
deletes the stack's database volumes and their data. For a targeted User/Credit
reset, follow the [fresh local data instructions](user-service/README.md#fresh-local-user-and-credit-data).

## Team Members

| Name | Role |
| ----- | ----- |
| Your Name | Your ownership |
| Your Name | Your ownership |
| Your Name | Your ownership |
| Your Name | Your ownership |
| Your Name | Your ownership |

---

## Repository Structure

User Service is available for local development. See [setup and design](user-service/README.md) and the [API guide for teammates](user-service/API.md).

The [Supplier skeleton](supplier-service/README.md) adds its own Go/PostgreSQL service, session validation through User Service and a shared Nginx gateway.

Credit Service is also available for local development. See its [setup and design](credit-service/README.md) and [API reference](credit-service/API.md).

Order service is available for local development. See [setup and design](order-service/README.md) and the [API guide for teammates](order-service/API.md). 

The [Next.js frontend](frontend/README.md) has real User Service signup, email verification, login and logout. Verification starts a best-effort 100-credit grant, but a verified user can log in while wallet setup is pending; the header then shows a temporary setup message until the balance is available. Its navy/orange Supplier screens read and modify live PostgreSQL-backed records through the Supplier API, including search, filtering, sorting, pagination and details.

To run the shared UI, User, Credit, Supplier and order services:

```sh
sh scripts/start.sh
```

Open `http://localhost:3000`. The gateway owns port 3000 and forwards page requests to Next.js and API requests to the appropriate backend. User's internal authentication endpoint remains private. Direct development ports are User 8080 (configurable), Credit 8082, Supplier 8083 (configurable) and Order (8084).

The startup script checks Docker access, runs the User, Credit and Order environment
initializers in order, validates Compose configuration and builds/starts the gateway
with all its dependencies. User's initializer also sets up Supplier database
credentials; Supplier needs no separate initializer. Existing settings and database
volumes are preserved. Docker must already be running; OpenSSL is used by the
existing environment initializers. Compose waits up to 120 seconds after building
for containers to run and configured health checks to pass. Each backend applies
its own embedded migrations at startup. Tests and account/admin creation are
separate from startup; see the service READMEs for their checks and account setup.

Credit's script adds User Service's private Credit credential to the root `.env`.
Credit starts as a dependency of User Service. Existing Supplier checkouts with
`SUPPLIER_HTTP_PORT=8082` in `.env` should change it to `8083` to avoid Credit's port.

See [Supplier API](supplier-service/API.md) for CRUD and catalogue queries, [verification instructions](supplier-service/README.md#verification) for container/API/browser checks, and the [D2 design and demo guide](docs/d2-supplier-demo.md). Changes are tracked in [separate implementation steps](docs/d2-supplier-progress.md). Redis, RabbitMQ workflows and cloud deployment remain later work.

This repository follows a **one-service-per-folder** structure: each
microservice (`user-service/`, `supplier-service/`, `order-service/`,
`credit-service/`) lives in its own top-level folder.

```text
.
├── frontend/
├── user-service/
├── supplier-service/
├── order-service/
├── credit-service/
├── gateway/
├── <n2h-service>/
└── README.md
```

- Any **nice-to-have (N2H)** feature that warrants its own service should
  be added as an **additional folder** at the same level, following the
  same per-service structure.
- Files for agentic coding tools (e.g. agent configs, prompts, skills)
  may be added as needed, but must still **respect the
  one-service-per-folder skeleton** for core implementation.

---

CI smoke additionally runs `scripts/order-lifecycle.mjs` through the real four services and gateway. It creates two verified accounts, waits for their wallets, and asserts exact balances and ledger entries for creation/retry, acceptance, pickup, delivery, confirmation/payout/retry and cancellation/refund/retry. Delivery must free courier capacity before payout; the total across both wallets stays 200. CI destroys its disposable databases afterward. Run this control only against disposable data because it creates account/order/history fixtures.
Smoke setup waits up to 65 seconds for initial wallet allocation after successful verification/login. Only `404 wallet_not_found` is retried; unexpected failures and incorrect balances still fail. Its immediate and delayed-allocation regression controls run in CI before the live smoke check.
