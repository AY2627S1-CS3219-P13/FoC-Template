# Schedule Order module

Scheduling runs inside Order Service, with backend implementation and tests retained in the top-level `schedule-order/` Go module. It has no standalone server, runtime container, public port or runtime database. Order imports the public module facade, mounts `/api/v1/scheduled-orders` and starts the scheduling event/recovery workers. The existing page remains `/orders/schedule`.

## Durable event workflow

Acceptance atomically stores `transfer_pending` and a versioned `ScheduledOrderAcceptanceRequested` event in Order's database, then returns 202. The event references the immutable scheduling record and stable transfer ID. Pending acceptance fences cancellation, expiry and another courier attempt.

The `workflow-schedule-acceptance` consumer discovers events missing receipts, uses nonblocking per-schedule advisory locks across replicas and retries transient errors with bounded backoff. Supplier lookups are prepared before its database transaction. Order rechecks expiry, admission and the combined three-order capacity cap in the transaction. Assignment, existing Order lifecycle events, durable import outcome, scheduling handoff state and receipt commit together. A failed receipt rolls all effects back. A definitive rejection returns the schedule to availability; a new attempt receives a new transfer generation. No second credit reservation occurs.

The scheduling journal is separate from `order_events`, which requires an existing Order record. `schedule_events`, `schedule_receipts` and `schedule_retries` follow Order's durable workflow pattern. The original Order timer, settlement and publication consumers remain independent. Credit HTTP effects stay in recovery workers outside event transactions.

Creation, idempotency, cancellation, expiry, refund recovery, place reconciliation and Singapore delivery-window rules remain unchanged. User validates every browser request; workers never store user cookies. Supplier/User/Credit remain HTTP dependencies with their own service credentials.

## Running and configuration

```sh
sh user-service/scripts/init-env.sh
sh credit-service/scripts/init-env.sh
sh order-service/scripts/init-env.sh
docker compose up --build -d gateway
```

Scheduling uses Order's validated database URL, Origin, session cookie and User/Supplier/Credit configuration. `ORDER_SUPPLIER_INTERNAL_URL` and `ORDER_SUPPLIER_INTERNAL_TOKEN` are required for background scheduling. Root Compose supplies these. Existing `SCHEDULE_*` settings and `schedule-order/.env` are no longer used by the runtime. The compatibility initializer delegates to Order's initializer. `/healthz` and `/readyz` belong to the combined Order process; readiness checks its shared database.

Migrations remain embedded here and use `schedule_schema_migrations`, separate from Order's migration ledger. Existing numbered migration SQL is unchanged. Runtime remains non-root/read-only with capabilities dropped.

## Existing-data cutover

Do this offline before starting the combined Order application against existing data. Do not run old and new writers together, and do not delete the legacy Schedule volume.

1. Back up both databases. Using the old checkout/Compose configuration, stop Schedule and Order application containers, leaving their databases running.
2. Build the new Order image. The old Schedule database must remain reachable on Order's Compose network (normally `schedule-db:5432`). Keep its generated password private.
3. Set `ORDER_LEGACY_SCHEDULE_DATABASE_URL` in your shell to the legacy Schedule connection URL, then run:

```sh
docker compose run --build --rm --no-deps -e ORDER_LEGACY_SCHEDULE_DATABASE_URL order-service import-schedules
unset ORDER_LEGACY_SCHEDULE_DATABASE_URL
```

For EC2, pull the new immutable image first and use the EC2 Compose overlay and omit `--build`. Both target and legacy databases must already be running. The command applies migrations and copies scheduling rows in one target transaction, preserving IDs, keys, fingerprints, timestamps, courier generations, refund flags and unresolved transfers. It reconstructs events for pending transfers and leaves the source untouched. A nonempty target is rejected rather than overwritten. Migration errors are sanitized; the runtime never logs connection URLs.

4. Verify the copied row/state counts in the database, then remove the stopped legacy Schedule application container and start the combined application. Keep the legacy database/volume backup until cutover is verified. Existing accepted orders and durable import outcomes already remain in Order's database.

Deployment refuses to proceed while a legacy Schedule application container remains, including a stopped one. No live data migration is performed by normal startup or deployment scripts. For a fresh installation there is nothing to import.

## Checks

```sh
docker compose -f schedule-order/compose.yaml --profile test run --build --rm schedule-tests
docker compose -f order-service/compose.yaml --profile test run --build --rm order-tests
docker compose -f order-service/compose.yaml --profile ui-test run --build --rm order-ui-tests
sh scripts/smoke.sh
```

Isolated scheduling tests use disposable `schedule_order_test` schemas and dependency fakes. Order integration tests verify shared transaction rollback, concurrent replay and no duplicate reservation. Scheduling tests cover atomic event creation and offline import. See `TDD.md` for the original implementation history; `logs/` retains historical conversations.

The [7 November conversation log](logs/2026-11-07-order-module-event-migration-chat.md) records the embedded-module decision, durable event implementation, validation and completed local offline migration.
