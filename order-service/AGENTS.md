# Order Service working notes

## Scope and current status

Order Service owns the lifecycle of an errand: creation, open-request listings,
acceptance and courier assignment, pickup, completion, cancellation, and expiry
before acceptance. User owns identity; Supplier owns the campus catalogue;
Credit owns wallets, escrow and credit history.

This folder now contains a Go application, embedded UI, numbered migrations,
isolated backend/browser tests, environment initializer and standalone Compose
setup. Read the current audit at the top of `VALIDATION.md` for implemented and
unfinished requirements; historical sections describe earlier checkouts.
Root `../compose.yaml` and `../.github/workflows/ci.yml` still do not define Order
or `order-tests`. Use the service-local commands in `README.md` for current runs.

Read `../AGENTS.md` first. Keep changes inside `order-service/` unless explicitly
asked to change shared wiring or another service. The root Compose filename is
`compose.yaml`. Preserve existing work and read another service's `AGENTS.md`
but DO NOT change implementation of that service. Use `BACKLOG.md` and requirements supplied in the
task for feature rules; do not invent status names, expiry durations,
cancellation penalties or new API contracts beyond those requirements.

## Context to read before implementation

- `../user-service/API.md`: sessions, cookies, roles and private validation.
  Follow its `internal/user/` and `cmd/server/main.go` implementation patterns.
- `../supplier-service/API.md`: catalogue, locations and inactive/deleted records.
  `internal/supplier/auth.go` demonstrates per-request validation, bounded HTTP
  calls and dependency-error handling; `integration_test.go` demonstrates a
  real test database with a fake User HTTP server.
- `../credit-service/README.md` and `API.md`: Order's escrow workflow and retry
  semantics. Its `compose.yaml`, `Dockerfile`, `scripts/check.sh` and
  `internal/credit/helpers_test.go` provide the standalone service/test pattern.
- `../compose.yaml`, `../gateway/default.conf` and `../.github/workflows/ci.yml`:
  existing ports, routing, dependencies and container checks. Existing smoke
  scripts cover implemented services; they do not verify an Order lifecycle.

Existing dependencies use HTTP/JSON. Creation events use a durable PostgreSQL
publication feed and authenticated browser SSE, documented in `API.md`. Do not
assume gRPC, Redis, RabbitMQ or an external broker exists.

## Service boundaries and contracts

- Validate every protected request through
  `POST http://user-service:8081/internal/v1/sessions/validate`, with
  `Authorization: Bearer <USER_INTERNAL_TOKEN>` and JSON
  `{"sessionToken":"<cookie value>"}`. Local cookies are `foc_session`; secure
  cookies are `__Host-foc_session`. Use the returned `userId`, `roles` and
  `expiresAt`; never trust an acting-user ID in browser input or cache validation
  across requests. Order must also enforce requester/courier permissions.
  Reject invalid sessions; fail closed on unavailable or malformed validation.
  Invalid backend credentials are a dependency outage, not a user login error.
- Supplier currently has authenticated public catalogue routes on container
  port 8080, including `GET /api/v1/suppliers/{id}` and `/api/v1/locations`.
  Private port 8081 additionally exposes read-only `/internal/v1/suppliers/{id}`
  and `/internal/v1/locations` with a separate `SUPPLIER_INTERNAL_TOKEN`. The
  worker uses this documented contract; public routes still require the user's
  current cookie. Never retain user cookies for background reconciliation. Check `supplier.active`
  for new pickups; inactive records remain readable, deleted records return 404.
- Call Credit at `http://credit-service:8081` with its separate
  `CREDIT_INTERNAL_TOKEN`. Reserve using `PUT /internal/v1/escrows/{orderId}`
  with `{"userId":"<requester UUID>","amount":30}` before saving a new errand,
  as required by Credit's current contract. A balance lookup cannot guarantee
  funds; reject creation if reservation fails. Amounts are positive integers.
- Cancellation/expiry calls `POST /internal/v1/escrows/{orderId}/release`
  without a body. Requester-confirmed delivery calls
  `POST /internal/v1/escrows/{orderId}/payout` with
  `{"courierId":"<assigned courier UUID>"}`. Payout returns 204. Reuse the same
  order ID and payload on retries; handle conflicts explicitly. Order IDs must
  be 1–128 characters without surrounding whitespace. Do not assign the
  requester as courier. Post-pickup cancellation penalties are not implemented
  by Credit and require agreed rules.
- Order writes only its own PostgreSQL database. Never read another service's
  tables, alter wallet balances, grant initial credits, or duplicate credentials.
  Design recoverable handling for a successful reservation followed by an Order
  write failure, and durable retries for release/payout after transient errors.
  A local SQL transaction cannot atomically commit a remote HTTP operation.

## Implementation conventions

- Use the existing Go `net/http`, `pgx` and `log/slog` style. Current service
  images pin `golang:1.27.1-bookworm` and `postgres:18-bookworm`; use container
  tooling rather than installing Go, Node or system packages on the host.
- Follow the layout `cmd/server/`, `internal/order/`, embedded numbered
  `internal/order/migrations/`, and `scripts/check.sh`. Add migrations rather
  than editing applied ones. Parameterize SQL values and allowlist sort columns.
- Wrap `func(http.ResponseWriter, *http.Request) error` handlers with the endpoint
  helper and return `{"error":{"code","message"}}`. Bound request bodies,
  reject unknown JSON fields, and use HTTP timeouts and request contexts.
- Validate configuration at startup. Keep secrets in ignored, generated `.env`
  files; never log cookies, tokens, passwords or credential-bearing URLs.
  Require the configured exact Origin for cookie-authenticated mutations.
- Runtime containers must be non-root, read-only, drop all capabilities and use
  `no-new-privileges`. Publish only the public port on loopback; keep databases
  and any internal listener private. Provide `/healthz` and `/readyz` with
  documented liveness/readiness semantics.
- Make assignment and state transitions concurrency-safe. Test simultaneous
  acceptance, cancellation/expiry races and repeated completion so only one
  valid transition wins and credits are never released or paid twice.
- Keep lifecycle mutations behind `guardAdmission`. Action and explicit-refresh
  routes must register durable admission before User validation; background and
  list reconciliation must defer while earlier live admissions exist. The runtime
  uses a separate admission pool. Admissions are leases, never stored commands or
  cached authentication; preserve fencing, cleanup and cross-replica tests when
  adding new state transitions.
  Completion alone prepares speculative writes before its final guard, in one
  transaction per order; a failed guard must roll back status, event and payout
  together. Never hold the admission gate through those preparatory writes.
  List snapshots resolve distinct places before row locks with bounded parallelism
  and version fencing; keep their two-second end-to-end request budget.

## Independent run setup to implement

Provide `order-service/compose.yaml` with an isolated project name, Order and
its own PostgreSQL, plus `scripts/init-env.sh` that generates local secrets
without overwriting existing values. Use Credit's standalone setup as a
reference. Define and document Order's actual environment variables in the
config loader and README; no Order variable names are established yet.

Reserve a configurable public host port that avoids User 8080, Credit 8082,
Supplier 8083, gateway 3000 and Mailpit 8025; 8084 is a proposed default only.
For standalone development, provide explicit test-only HTTP dependency stubs
or a documented connection to real User, Supplier and Credit services. Do not
bypass authentication when a dependency is absent. Compose service DNS names
work only on a shared network; a separate Order project does not automatically
join the root stack's network. Internal port 8081 is not published to the host.

Once those files and the application exist, the intended commands, from the
repository root, are:

```sh
cd order-service
sh scripts/init-env.sh
docker compose -f compose.yaml up --build -d order-service
docker compose -f compose.yaml ps
docker compose -f compose.yaml logs --tail=100 order-service
curl --fail http://localhost:8084/healthz
curl --fail http://localhost:8084/readyz
docker compose -f compose.yaml down
```

Use the configured port if it differs. Explicit `-f compose.yaml` prevents
accidentally using the parent Compose file. `down` should preserve development
data; do not add `--volumes` unless a database reset is explicitly intended.
Health checks alone do not verify authentication or escrow integration.

To start the existing real backend dependencies without the frontend/gateway,
these commands are already supported from the repository root:

```sh
sh user-service/scripts/init-env.sh
sh credit-service/scripts/init-env.sh
docker compose up --build -d supplier-service
```

Supplier starts User, which starts Credit, plus the required databases and
Mailpit. This does not start Order or connect a future standalone Order network.
For manual integration, register, verify through Mailpit and log in through
User's API; use a cookie jar and the configured Origin. A verified user's wallet
may still be pending initial allocation. Follow the root first-admin guidance
before running smoke/browser fixtures against a freshly reset User database.

## Independent tests to implement and run

Define a `test` profile with `order-tests` built from the Dockerfile's
`development` stage and a healthy, disposable `order-test-db`. Supply an explicit
test database URL; ensure the suite actually runs database tests rather than
skipping them. Follow the dedicated database/per-test-schema safeguards in
Supplier/Credit. Use `httptest` fakes for User, Supplier and Credit so this suite
needs no live sibling service, gateway, frontend or production credentials.

After implementing that setup, run from `order-service/`:

```sh
docker compose -f compose.yaml --profile test run --build --rm order-tests
docker compose -f compose.yaml --profile test run --rm --no-deps \
  -v "${PWD}:/app" order-tests gofmt -w cmd internal
```

The check script must enforce `gofmt`, run `go vet ./...`,
`go test -race -count=1 ./...`, and build `./cmd/server` into `/tmp`, matching
existing services. Write unit and PostgreSQL/HTTP integration tests for new
handlers and data access. Cover authorization, invalid input, state transitions,
expiry, concurrent assignment, inactive/missing suppliers, insufficient or
pending wallets, dependency outages, retries and partial cross-service failures.
Do not weaken tests to pass. Keep manual real-service checks distinct from the
isolated automated suite and document both in README/API as they are added.

Changes to shared wiring require explicit scope and the root smoke test in
addition to affected service checks. Root CI currently checks only User,
Supplier and Credit backends; passing it is not evidence that Order was tested.
