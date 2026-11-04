# Schedule Order Service

Independent Go/PostgreSQL service for the [scheduled-order specification](spec.md). The shared Order screen provides **Schedule order** at `/orders/schedule`; the backend is a separate container and owns a separate database. No host Go/Node installation is required.

Conversation records are kept in [logs/](logs/), starting with the [requirements, implementation and UI follow-up chat](logs/2026-11-04-schedule-order-chat.md).

Initialize environments and start the shared application:

```sh
sh user-service/scripts/init-env.sh
sh credit-service/scripts/init-env.sh
sh order-service/scripts/init-env.sh  # also initializes schedule-order/.env and shared import token
sh schedule-order/scripts/init-env.sh
docker compose up --build -d gateway
```

Open `http://localhost:3000/orders/schedule`. Choose both places, a Singapore date and an available half-hour window. The start must be at least one hour ahead. The offered credits are reserved before creation succeeds. Requests are immutable; cancel and recreate to change them. Blank hours impose no restriction; invalid nonempty supplier hours block availability. The accepted hours grammar is recorded in the specification.

The main order screen merges scheduled requests with regular requests/discovery. Future accepted commitments count immediately toward the combined maximum of three. Overlapping windows are allowed. Only the delivered action is restricted to the delivery window; no extra pickup-time check is added.

## Ownership and recovery

Creation records the immutable financial request before calling Credit. A successful save marks the order scheduled. Uncertain reservations or failed saves are aborted through Credit's durable abort fence; they are not silently completed by recovery. Definitively insufficient/unready wallets remain retryable with the same key. Successful same-key retries return the saved order even after the original one-hour threshold.

Acceptance records durable transfer intent and returns pending. A worker uses authenticated HTTP to call Order's private import listener, reusing the same transfer identity/payload after a crash or timeout. While unresolved, conflicting acceptance/cancellation/expiry cannot release credits or create another assignment. Order commits its import, capacity check, assignment and journal events together and records a durable accepted/rejected result. A rejected transfer cannot later succeed on retry; a new courier attempt uses a new generation. A successful transfer retains the same order/escrow ID, never reserves again, and makes the Schedule copy read-only. Order then owns all lifecycle and financial settlement intent. Existing browser cookies are never persisted for workers.

Workers run every second with bounded HTTP calls and a bounded batch, rotating attempted work. Cancelled/expired orders retain refund-pending status until Credit confirms release. Confirmed place inactivity/deletion or hours invalidating the full window cancels pre-delivery orders. Dependency failures and invalid hours text never imply closure.

Stop old Order instances before applying migration 010; mixed old/new capacity writers are unsupported. No existing numbered migration is edited. Databases and Order's internal 8081 listener are private to Compose; the gateway rejects `/internal`.

## Configuration

All configuration is validated at startup. `SCHEDULE_DATABASE_URL`, `SCHEDULE_APP_ORIGIN`, `SCHEDULE_USER_SERVICE_URL`, `SCHEDULE_USER_INTERNAL_TOKEN`, `SCHEDULE_SUPPLIER_SERVICE_URL`, `SCHEDULE_SUPPLIER_INTERNAL_TOKEN`, `SCHEDULE_CREDIT_SERVICE_URL`, `SCHEDULE_CREDIT_INTERNAL_TOKEN`, `SCHEDULE_ORDER_SERVICE_URL` and `SCHEDULE_ORDER_INTERNAL_TOKEN` are required. Dependency URLs are HTTP(S) origins without paths/credentials. Tokens require at least 32 characters. `SCHEDULE_SESSION_COOKIE` defaults from the origin: `foc_session` for HTTP or `__Host-foc_session` for HTTPS. Root Compose supplies shared credentials and a private Schedule database URL. `.env` files remain untracked.

`/healthz` reports process liveness; `/readyz` checks only Schedule's database. Dependency failures fail protected commands closed. Runtime is non-root/read-only, drops capabilities and uses no-new-privileges.

## Checks

```sh
docker compose -f schedule-order/compose.yaml --profile test run --build --rm schedule-tests
docker compose -f order-service/compose.yaml --profile test run --build --rm order-tests
docker compose -f order-service/compose.yaml --profile ui-test run --build --rm order-ui-tests
sh scripts/smoke.sh
```

Schedule tests require the disposable `schedule_order_test` database, create isolated schemas, and use HTTP fakes. The check target enforces gofmt, vet, race tests and build. See [TDD evidence](TDD.md) for red/green history. Live smoke fixtures require the designated first-admin role already claimed; follow root guidance.
