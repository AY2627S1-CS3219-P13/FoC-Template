# Credit Service

Wallets, reservations, transfers and transaction history for Friend on Campus (backlog `FR4`, `NFR3`).

**Status:** all six operations and their endpoints are implemented. Transaction history (`FR4.2.1`) and the User/Order Service integrations are not yet built.

## Operations

All live in `internal/credit/credit.go`. Each runs in one database transaction with row locks, so a balance change and its ledger entry commit together (`NFR3.3`).

| Operation | Purpose | Idempotency key | Endpoint |
| --- | --- | --- | --- |
| `Balance` | Available and reserved credits (`FR4.1.2`) | read-only | `GET /api/v1/wallets/me`, `GET /internal/v1/wallets/{userId}` |
| `AllocateInitial` | Starting credits after verification (`FR4.1.1`) | user ID | `POST /internal/v1/wallets/{userId}/initial-allocation` |
| `Reserve` | Freeze credits when an errand is created (`FR4.1.3`) | order ID | `PUT /internal/v1/escrows/{orderId}` |
| `Release` | Unfreeze on cancellation/expiry (`FR4.1.5`) | order ID | `POST /internal/v1/escrows/{orderId}/release` |
| `Transfer` | Pay the courier on completion, atomically (`FR4.1.4`) | order ID | `POST /internal/v1/escrows/{orderId}/payout` |
| `AdminDebit` | Remove available credits (admin) | request ID | `POST /api/v1/admin/wallets/{userId}/debits` |

`/api/…` routes are public (port 8080, session cookie); `/internal/…` routes are for other services (port 8081). Request bodies, responses and error codes are in [API.md](API.md).

`AdminDebit` is **not in the D1 backlog**: `FR4.1.7` lists the only permitted balance changes. Agree it with the team before implementing it.

Credits move through an escrow (the `reservations` table) tied to an errand, never directly between arbitrary users (`FR4.1.8`). Schema: `internal/credit/migrations/001_initial.sql` (wallets, reservations, append-only transactions ledger).

## Containers

| Container | Built from | Port |
| --- | --- | --- |
| `credit-db` | `db/Dockerfile` (PostgreSQL 18) | 5432, Compose network only |
| `credit-service` | `Dockerfile` (Go) | 8080 in the container, published as `127.0.0.1:8082` |

The service reaches the database at `credit-db:5432` through `CREDIT_DATABASE_URL`. Data is kept in the `credit-data` volume.

## Run locally

With Docker Desktop running, from the **repository root**:

```sh
sh user-service/scripts/init-env.sh     # root .env (User Service secrets); never overwrites
sh credit-service/scripts/init-env.sh   # credit-service/.env (Credit secrets); never overwrites
docker compose up --build -d            # User and Credit Service, their databases, Mailpit
curl http://localhost:8082/readyz
```

The root `compose.yaml` includes this folder's `compose.yaml`, so both services share one network. Credit validates sessions at `http://user-service:8081`, using the root `.env`'s `USER_INTERNAL_TOKEN`.

- Public API: `http://localhost:8082` (container port 8080).
- Internal API: `http://credit-service:8081`, from containers on the same Compose network only. Callers send `Authorization: Bearer <CREDIT_INTERNAL_TOKEN>` from `credit-service/.env`.
- Settings: `CREDIT_INITIAL_CREDITS`, `CREDIT_APP_ORIGIN`, `CREDIT_SESSION_COOKIE` and `CREDIT_USER_SERVICE_URL` are in `compose.yaml`; secrets are in `credit-service/.env`.

To work on Credit Service alone, run `docker compose up --build -d` inside `credit-service/`. Without User Service, the public `/api` routes answer 503 `auth_unavailable`; the internal API works normally.

## Test

```sh
docker compose --profile test run --build --rm credit-tests   # from credit-service/
```

Runs `gofmt`, `go vet`, race-enabled tests against a throwaway PostgreSQL, and a build (`scripts/check.sh`). Tests skip unless `CREDIT_TEST_DATABASE_URL` is set; the Compose profile sets it. Each test gets its own schema.

The tests were written before the implementation, as the target behaviour:

- `internal/credit/credit_test.go`: each operation called directly: happy path, boundaries, invalid input, retries with the same key, conflicts, and concurrency (no overspending, no double payout, no deadlock between opposite transfers).
- `internal/credit/http_test.go`: the same cases through the endpoints, checking the status and error code from [API.md](API.md), plus service-token, session, admin-role and Origin checks.
- `internal/credit/helpers_test.go`: per-test schema, a fake User Service (`fakeSessions`), and direct-SQL setup so each operation is tested independently of the others.
