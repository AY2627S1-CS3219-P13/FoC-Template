# Credit Service

Wallets, escrows and credit history for Friend on Campus (backlog `FR4`, `NFR3`), written in Go with PostgreSQL. This page is the overview; [API.md](API.md) is the endpoint-by-endpoint reference.

**Status:** all credit operations and their endpoints are implemented. User Service does not call the initial allocation yet, and Order Service does not exist yet.

## API

### Public — port 8080

For the frontend, published locally at `http://localhost:8082`. Authenticated by User Service's session cookie.

| Endpoint | Purpose |
| --- | --- |
| [`GET /api/v1/wallets/me`](API.md#get-apiv1walletsme) | Available and reserved credits (`FR4.1.2`) |
| [`GET /api/v1/wallets/me/transactions`](API.md#get-apiv1walletsmetransactions) | Credit history, newest first (`FR4.2.1`) |
| [`POST /api/v1/admin/wallets/{userId}/debits`](API.md#post-apiv1adminwalletsuseriddebits) | Admin removes available credits |
| `GET /healthz`, `GET /readyz` | Server running; database reachable |

### Internal — port 8081

For other services only, as `http://credit-service:8081` on the Compose network. Never route it through the public proxy.

| Endpoint | Caller | Purpose |
| --- | --- | --- |
| [`GET /internal/v1/wallets/{userId}`](API.md#get-internalv1walletsuserid) | Order | A user's balance |
| [`GET /internal/v1/wallets/{userId}/transactions`](API.md#get-internalv1walletsuseridtransactions) | Any | A user's history |
| [`POST /internal/v1/wallets/{userId}/initial-allocation`](API.md#post-internalv1walletsuseridinitial-allocation) | User | Initial credits, exactly once (`FR4.1.1`) |
| [`PUT /internal/v1/escrows/{orderId}`](API.md#put-internalv1escrowsorderid) | Order | Reserve credits for a new errand (`FR4.1.3`) |
| [`POST /internal/v1/escrows/{orderId}/release`](API.md#post-internalv1escrowsorderidrelease) | Order | Return them on cancellation or expiry (`FR4.1.5`) |
| [`POST /internal/v1/escrows/{orderId}/payout`](API.md#post-internalv1escrowsorderidpayout) | Order | Pay the courier on completion (`FR4.1.4`) |

An **escrow** holds a requester's credits for one order, from errand creation until they are released back or paid out. Credits only move through an escrow, never directly between users (`FR4.1.8`). Every write is [safe to retry](API.md#retries).

### Authentication

- **Public:** the browser sends User Service's session cookie (`credentials: "include"`); Credit validates it through User Service on every request. The acting user always comes from the session. POST requests also need the frontend's exact `Origin`. If User Service is unreachable, requests fail closed with 503.
- **Internal:** `Authorization: Bearer <CREDIT_INTERNAL_TOKEN>`, from server-side configuration. Never expose it to the frontend.

### Order Service flow

1. **Create errand:** `PUT /internal/v1/escrows/{orderId}` before saving the order. On 409 `insufficient_credits`, reject the errand (`FR3.1.4`).
2. **Cancelled or expired:** `POST …/{orderId}/release`.
3. **Requester confirmed delivery:** `POST …/{orderId}/payout` with the assigned courier.

Retry steps 2 and 3 until they succeed. They may later be driven by events (`FR5`); the broker is not decided yet.

## Design

**History and logging.** The `transactions` table is the credit history, and it cannot disagree with balances or logs:

- **One write path.** Every balance change goes through `ledgerTx.apply` (`ledger.go`), which updates the wallet and appends the history entry in the same transaction. Each kind has a fixed effect on the two balances (the `effects` table), and each entry stores the balances after it. Operations never write `wallets` or `transactions` themselves.
- **Logged once, centrally.** `inTx` logs each change as a `credits moved` event after the transaction commits (`NFR6`); rolled-back work is never logged. Unexpected request errors are logged by the HTTP error handler.
- **Enforced by the database.** Constraints forbid negative balances and entries that don't match their kind's effect. A trigger makes history append-only.
- **Checked by every test.** After each test, every wallet must equal the sum of its entries and the balances on its newest entry.

To add a new kind of balance change: add a `Kind` and its row in `effects`, extend the `transactions_kind_effect` constraint in a new migration, and call `tx.apply`.

**Concurrency.** Each write locks the rows it changes, so concurrent requests cannot overspend or pay out twice (`NFR3.2`). Locks are taken in a fixed order (escrow, then wallets by user ID) to avoid deadlocks. A payout changes both wallets in one transaction, so both update or neither does (`NFR3.3`).

| File | Responsibility |
| --- | --- |
| `cmd/server/main.go` | Startup, migrations, the two HTTP listeners |
| `internal/credit/credit.go` | Balance, allocation, reserve, release, payout, admin debit |
| `internal/credit/ledger.go` | The single write path, central logging, history reads |
| `internal/credit/http.go` | Routes, authentication, request parsing, error codes |
| `internal/credit/sessions.go` | Session validation through User Service |
| `internal/credit/migrations/` | Wallets, escrows (`reservations`) and history (`transactions`) |

## Run locally

With Docker Desktop running, from the **repository root**:

```sh
sh user-service/scripts/init-env.sh     # root .env (User Service secrets); never overwrites
sh credit-service/scripts/init-env.sh   # credit-service/.env (Credit secrets); never overwrites
docker compose up --build -d            # User and Credit Service, their databases, Mailpit
curl http://localhost:8082/readyz
```

The root `compose.yaml` includes this folder's `compose.yaml`. Credit Service publishes only port 8080, as `127.0.0.1:8082`; its internal port and its database (`credit-db`, data in the `credit-data` volume) stay on the Compose network. It validates sessions at `http://user-service:8081` with the root `.env`'s `USER_INTERNAL_TOKEN`. Other settings are in `compose.yaml`; secrets are in `credit-service/.env`.

To work on Credit Service alone, run `docker compose up --build -d` inside `credit-service/`. Without User Service, the public routes answer 503; the internal API works normally.

## Test

```sh
docker compose --profile test run --build --rm credit-tests   # from credit-service/
```

Runs `gofmt`, `go vet`, race-enabled tests against a throwaway PostgreSQL, and a build (`scripts/check.sh`). Each test gets its own schema.

- `credit_test.go`: each operation called directly: boundaries, invalid input, retries, conflicts and concurrency (no overspending, no double payout, no deadlock).
- `http_test.go`: the same cases through the endpoints, checking the status and error codes in [API.md](API.md), plus authentication.
- `ledger_test.go`: history contents, paging and the 100-concurrent-reads target (`NFR1.2`), plus the database guarantees.
- `helpers_test.go`: per-test schema, a fake User Service, direct-SQL setup, and the balance-versus-history check run after every test.
