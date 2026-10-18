# Order Service

Order Service includes creation, live open listings, concurrency-safe acceptance and the nine-state lifecycle. Requesters can cancel, confirm and dispute; assigned couriers record pickup/delivery; admins resolve disputes. Credit reservations and settlement jobs recover durably. The standalone UI at `/orders` follows the existing User/Supplier visual language without importing or modifying those services or the shared frontend.

Supplier changes are reconciled in authenticated list/refresh/action requests and automatically by the recovery worker using Supplier’s private service-authenticated catalogue. No user credentials are retained for background polling. Receipt-order arbitration uses durable pre-authentication admissions; [API.md](API.md) documents its receipt boundary and timing limits.

The complete request/response contract is in [API.md](API.md).

Completion now keeps admission open throughout its database writes and performs
the final arbitration check immediately before committing each order. A live
overlapping admission rolls back the speculative status, event and timer acknowledgement
together. Valid disputes admitted before that decision point beat both manual
and automatic completion, including slow authentication; committed completion
remains final.

Live lists use eight concurrent catalogue lookups, a two-second server refresh
budget and a 2.5-second browser timeout. Their two-second polling clock runs
independently of session UI checks. Slow replies cannot reset freshness: overdue
or failed snapshots retain the last confirmed data with “List is not updated”.
The five-second delivery target applies when the foreground browser, network and
dependencies can meet those budgets. Arbitrary outages/load or browser suspension
cannot support an unconditional delivery guarantee.

## API

`POST /api/v1/orders` requires the User Service session cookie, the exact configured `Origin`, `Content-Type: application/json`, and an `Idempotency-Key` header (1–128 characters, no surrounding whitespace). Reuse the same key and identical body after a timeout or lost response. A retry returns the existing order; reusing a key with another body returns `409 idempotency_conflict`.

```json
{
  "pickup": {"type": "supplier", "id": "11111111-1111-4111-8111-111111111111"},
  "delivery": {"type": "location", "id": "utown"},
  "items": [{"name": "Milk Tea", "description": "Less sugar", "quantity": 2}],
  "offeredCredits": 30,
  "expiresAt": "2026-10-03T18:00:00+08:00"
}
```

Place `type` is `location` or `supplier`. Both IDs are resolved through Supplier Service using the user's session; suppliers must be active. The two type/ID pairs must differ (supplier UUIDs are case insensitive). Successful creation returns `201` with `{ "order": ... }` and status `open`. Responses include exact supplier/campus details, an order version, and UTC+08:00 timestamps. Authenticated detail reads and explicit refreshes are documented in API.md.

Items have a nonblank name, optional description, and positive signed-64-bit integer quantity. Credits are also positive signed-64-bit integers. There is no business maximum for item count, text length, quantity, or offer; the HTTP request-body infrastructure limit is 1 MiB. Expiry must be in the future when the database records the order.

`POST /api/v1/orders/items/add` supports the Add Item editing interaction:

```json
{"existingItems":[{"name":"Milk Tea","quantity":1}],"item":{"name":" milktea ","quantity":2},"decision":""}
```

Names are compared after removing Unicode whitespace and folding case. A duplicate with no `decision` returns `requiresDecision: true` without adding it. Resubmit with `decision: "proceed"` to add the duplicate, or `"do_not_proceed"` to keep the existing list. Description and quantity do not affect matching. Final creation permits duplicates because the editing decision has already been made.

Errors use `{ "error": { "code", "message" } }`. Notable results are `400 invalid_place/invalid_items/invalid_offer/invalid_expiry`, `401 invalid_session`, `409 insufficient_credits/wallet_not_ready/idempotency_conflict`, and `503 auth_unavailable/places_unavailable/credit_unavailable`. Temporary errors explicitly support retrying with the same idempotency key.

`GET /healthz` reports process liveness. `GET /readyz` reports database reachability; dependency outages are checked on protected requests and fail closed.

## Recovery semantics

Order durably records the requester, offer and reservation intent before calling Credit. A PostgreSQL advisory lock serializes each attempt across creation and cleanup using a single connection. The order and completed-attempt marker commit together. A lost client reply returns that saved result; an uncertain reservation or failed order write instead abandons the attempt and returns its credits (Q10b). Such an attempt cannot be completed by a subsequent retry.

Cleanup first checks for a saved order, then commits its release intent. If Credit reports no escrow for an uncertain reservation, Order reconciles by repeating the **same** reservation ID/request and then releasing it. This establishes Credit's idempotency record so an original late reservation cannot leak credits after cleanup. Transient failures and lost release responses leave durable cleanup work; a restart does not create the errand in the background. A legacy attempt without a saved reservation payload can release an existing escrow, but a missing legacy escrow needs operator investigation and is never reported as refunded.

Open/accepted/picked-up expiry commits status, slot release and the refund-intent event together; a valid pre-deadline acceptance saved late immediately expires. The recovery worker runs at startup and every 30 seconds, handles at most 100 stale creation attempts per pass, and skips active creations. Creation cleanup never refunds a saved order. Separately, the worker retries durable refunds and payouts; attempted refunds rotate to avoid starving newer work. `creation_cleanup_pending` makes no refund-completion claim; retry with the same key to check the outcome. Use a fresh key after `creation_abandoned`. Definitive pending-wallet/insufficient-credit refusals remain retryable using the same key.

Creation cleanup, refunds and payouts run concurrently within the worker's bounded
deadline. A slow refund cannot consume the whole pass before payout retries begin;
failures in any queue retain durable work and are reported for the next pass.

## UI, status updates and events

The header shows the signed-in display name, role and available credits using
the gateway's existing User/Credit public APIs, and links back to the catalogue.
Failed account reads show unavailable labels; direct standalone access without
those gateway routes cannot display account details. Each status has a distinct
colour and text label. My requests filters all/current/past before pagination;
the four terminal states are completed, not completed, cancelled and expired.
Switching views clears the selected detail. Accept is disabled while an accepted
or picked-up delivery occupies the courier's slot, with an explanatory note.
Admins assigned as couriers may record pickup and delivery; dispute resolution
remains admin-only. Signed-in users can open Orders from the catalogue header.

- `/orders` provides New errand, My requests, Help a friend and My deliveries. Sign in through the existing gateway first; the local host-only session cookie is shared across localhost ports. Selectors load all Supplier pages, show exact location/description/opening hours, and retain unavailable previous selections for correction.
- Browser requests are asynchronous. Only affected/conflicting actions lock; navigation remains usable. The last committed status stays visible while an action is pending. Lists and selected details refresh every two seconds while visible; outages retain the last snapshot and mark it stale. Responses with older order versions cannot overwrite newer ones.
- Drafts and unresolved command identities are scoped by user in session storage (this browser tab only). A lost creation reply retains the exact serialized body/key across reload. No session cookie is readable or stored by JavaScript. Names/descriptions render as text, never HTML. Quantity/offer values preserve signed-64-bit precision rather than passing through JavaScript's lossy number representation.
- The lifecycle guard allows only forward whole-request transitions across all nine states. Assigned couriers pick up/deliver, requesters cancel before pickup or confirm/dispute after delivery, and admins resolve disputes. The Disputes tab is admin-only. Delivery releases courier capacity atomically; later retries and settlement never alter a newer assignment.
- A separate one-second lifecycle loop expires pre-delivery orders and completes delivered orders after five minutes. Confirmation returns pending first; a one-second arbitration interval lets overlapping disputes win before completion records payout intent. The UI retains delivered status until completion is committed. Original timestamps and dispute cancellation survive restart. Financial retries run independently so slow Credit calls do not block lifecycle processing.
- Successful command receipts survive restart and later states. Repeated pickup after cancellation/expiry returns the current outcome without reopening it. Failed actions leave no receipt. Terminal states cannot reopen; only admins can resolve disputes, and only the winning resolution records financial intent.
- Actions and explicit refreshes receive a durable admission before authentication. Later actions for the same errand wait for earlier admitted requests to resolve. Workers defer expiry/completion and Supplier cancellation while an admission is live, including during transaction-pool and row-lock waits. A pending dispute can therefore protect against completion even when authentication exceeds the one-second arbitration interval. Admission grants no authority and stores no credentials or command body. The separate four-connection admission pool avoids contention with the twenty-connection transaction pool. Tickets expire after thirty seconds; the existing request deadline is fifteen seconds. Expired tickets cannot authorize a resumed handler, and no command is executed in the background after a crash. The database admission commit defines ordering across service instances; the original server receipt timestamp still governs expiry eligibility.
- An actionable `OrderCreated` event commits with creation. An independent worker publishes committed lifecycle events to a durable PostgreSQL browser feed every second. `/api/v1/orders/stream` delivers Server-Sent Events to authenticated users other than the requester, while the errand remains open and unexpired. The UI shows a new-errand notice and refreshes Help a friend without waiting for its polling timer. Events contain metadata only; authoritative snapshots supply current details. Session checks run before each stream batch, and reconnects synchronize snapshots and replay missed events using `Last-Event-ID`. Stable event IDs/versions and publication positions support deduplication. Status/detail invalidations also use this feed, with current participant/dispute-admin visibility and minimal open-list removal metadata. Internal domain data is never sent through SSE. No external broker or queued creation command is required.
- Supplier refresh serializes against transitions with a row lock. Confirmed absence/inactivity cancels only open/accepted/picked-up orders and commits a refund-intent event atomically; outages do not imply deletion. Names/locations refresh without changing place identity. Delivered/disputed/terminal orders retain their status. All eligible open-list entries, every displayed history/dispute-list entry and the selected detail are refreshed. Lookups are shared only within a snapshot. Off-page history is not polled. Open lists have no pagination, default to earliest expiry, and offer highest credits first. A failed/offline/stalled list shows “List is not updated” until its next successful snapshot.

Publication survives a restart between order commit and publication. Concurrent
publishers serialize a transactionally committed cursor, so a late creation commit
cannot be skipped by an earlier allocated sequence number. Publication failures
leave pending journal entries for the next worker pass; they never repeat credit
reservation. Admins can inspect pending/published counts and the oldest pending
event at `GET /api/v1/orders/publication-status`. Published means available in the
durable feed, not acknowledged by every browser. Streams rotate after eight
seconds and reconnect automatically within the existing server timeouts. Snapshot
polling remains a fallback and handles status changes and Supplier reconciliation.
Offline users see an authoritative snapshot when they return; there are no OS
push notifications, emails or persistent per-user notification inboxes. The shared
frontend/gateway remains unchanged; this is available through Order's `/orders` UI.

## Run and test

Order is included in the repository-root Compose stack. From the repository root:

```sh
sh user-service/scripts/init-env.sh
sh credit-service/scripts/init-env.sh
sh order-service/scripts/init-env.sh
docker compose up --build -d gateway
docker compose --profile test run --build --rm order-tests
```

Sign in at `http://localhost:3000`, then open `http://localhost:3000/orders`.
The root gateway serves Order's UI/assets and `/api/v1/orders` routes, including
unbuffered SSE. The root include supplies `USER_APP_ORIGIN` and gives the root
User/Credit credentials precedence over standalone copies. No sibling service
implementation is modified. The separate `foc_order` database and `order-data`
volume belong to the root project; existing standalone `foc-order` data is not
moved automatically. Root CI includes Order's backend checks. The commands below
remain available for a standalone Order project; do not run both projects on
port 8084 together.

The standalone Compose project needs network access to real dependencies. The optional `compose.integration.yaml` attaches only Order to the existing `foc_default` network; its database remains private. Start the existing stack using its documented procedure first. No root Compose, gateway or sibling code changes are required.

```sh
sh user-service/scripts/init-env.sh
sh credit-service/scripts/init-env.sh
cd order-service
sh scripts/init-env.sh
docker compose -f compose.yaml -f compose.integration.yaml up --build -d order-service
curl --fail http://localhost:8084/healthz
curl --fail http://localhost:8084/readyz
# Log in at http://localhost:3000, then open http://localhost:8084/orders
docker compose -f compose.yaml --profile test run --build --rm order-tests
docker compose -f compose.yaml --profile ui-test run --build --rm order-ui-tests
docker compose -f compose.yaml -f compose.integration.yaml --profile test --profile ui-test down
```

Both test profiles are isolated: they use disposable PostgreSQL and HTTP fakes. No live accounts, wallets or Supplier data are used. `scripts/check.sh` enforces formatting, vet, race-enabled tests, and a server build. The UI profile runs Node unit tests and real Chromium tests with a pinned Playwright image and lockfile; it temporarily exposes the test-only fixture on localhost:8085. The fixture is compiled only into a test binary, requires `ORDER_UI_FIXTURE=1`, and exits after a 30-minute test timeout. Stop the fixture/database after testing; do not deploy this profile. The runtime image has no fixture routes or Node dependency.

The root gateway smoke test remains separate and creates an admin fixture. Do not run it before the designated first-admin account is claimed. The isolated test profiles do not establish live sibling-service integration or first-admin bootstrap status.

## Configuration

Required variables are `ORDER_DATABASE_URL`, `ORDER_APP_ORIGIN`, `ORDER_SESSION_COOKIE`, `ORDER_USER_SERVICE_URL`, `ORDER_USER_INTERNAL_TOKEN`, `ORDER_SUPPLIER_SERVICE_URL`, `ORDER_CREDIT_SERVICE_URL`, and `ORDER_CREDIT_INTERNAL_TOKEN`. Tokens must be at least 32 characters. Configuration is validated before startup.

Compose defaults `ORDER_PUBLIC_PORT=8084` and `ORDER_APP_ORIGIN=http://localhost:8084`. Change both together when moving the UI. The optional integration overlay accepts `ORDER_DEPENDENCY_NETWORK` (default `foc_default`). The environment initializer reads User's token from the repository-root `.env` and Credit's separate `.env`, preserves existing credentials, and adds the Supplier catalogue token to the root `.env` when missing.

Order’s recovery worker uses Supplier’s authenticated internal read-only catalogue on its existing port 8080 with a separate `SUPPLIER_INTERNAL_TOKEN`. Order’s environment initializer adds that token to the root `.env` without changing existing credentials, and root Compose shares it; browser cookies are never retained for background work. Each sweep checks up to 100 orders, rotating oldest checks first. Inactive/deleted places cancel open, accepted and picked-up orders and durably record refund intent; Credit performs the idempotent release. Delivered, disputed and terminal orders are protected. Outages or malformed responses do not imply deactivation. Default recovery runs every 30 seconds; event consumption materializes settlement queues before an independent financial retry tick delivers refunds. Standalone Order must configure `ORDER_SUPPLIER_INTERNAL_URL` and `ORDER_SUPPLIER_INTERNAL_TOKEN` to enable the sweep.

The internal catalogue routes reuse Supplier’s existing HTTP listener; there is no additional port or service. The gateway rejects `/internal` paths, and the routes require their service credential even when called directly. Run `sh order-service/scripts/init-env.sh` after initializing User and Credit for the root stack.

Creation can fail after Credit has reserved funds. Pending cleanup is reported as `503 creation_cleanup_pending`, so the UI retains the attempt key until its financial outcome is confirmed; expiry alone does not imply a completed refund.

After acceptance, pickup or delivery, the UI marks delivery capacity unknown and immediately checks `/api/v1/orders/session`. The aggregate server result controls acceptance, including when replaying an older delivery after another tab accepts a newer order. A failed check keeps new acceptance disabled until capacity can be confirmed; existing polling also retries the check.
Root Compose shares `USER_APP_ORIGIN` with this service. When the session-cookie setting is empty, an HTTP origin selects `foc_session` and an HTTPS origin selects `__Host-foc_session`, matching User. Standalone runs may still set the cookie name explicitly. HTTPS also requires User secure-cookie and deployed SMTP/TLS settings; this change does not configure TLS termination.

## Transactional lifecycle workflow

Migration 009 extends each new journal row with private domain schema 1. The
public event ID, version and generic event types are unchanged. Three independent
consumers poll in bounded one-second passes: `workflow-timers` creates durable
schedules, `workflow-settlement` creates refund/payout jobs, and
`workflow-publication` creates authorized browser invalidations. Their effects
and individual receipts commit in one SQL transaction, without HTTP. Failures
retain sanitized retry categories with exponential backoff capped at 60 seconds;
unknown payloads/types/schemas remain pending for investigation. No broker is
required and the orders table remains authoritative.

Commands can succeed while consumers are paused. Cancellation, expiry, completion
and admin resolution atomically commit **settlement intent in the event**; a
financial queue row may not exist yet. Consumption eventually bridges that gap.
A completed order does not imply a paid courier. Independent financial workers
retain jobs until Credit confirms success, including after lost replies. Credit
conflicts remain pending and visible; they never switch the chosen outcome.
Creation reservation and uncertain-creation cleanup retain their existing HTTP
contracts and durable attempt locks. Consumers never reserve again.

Schedules use original timestamps. Pre-delivery expiry uses `expiresAt`;
automatic completion uses `deliveredAt + 5 minutes + 1 second`. Confirmation
uses `min(deliveredAt + 5 minutes, completionRequestedAt) + 1 second`. Dispute and
terminal events remove timers, retaining projection versions as tombstones.
Restart or delayed consumption does not restart a countdown. Executors lock and
recheck current state, original deadlines and admissions; stale work cannot force
a transition. Completion's final admission gate rolls back speculative state,
event and timer acknowledgement together when a competing request is admitted.
Foreground actions still enforce receipt-time expiry, including freeing older
expired courier assignments before acceptance.

For upgrade, stop every old Order instance before deploying the new binary.
Migration 009 is additive; do not reset the database or remove volumes. Startup
runs idempotent, per-order legacy bootstrap before ordinary workers. It preserves
old journal/publication IDs and existing financial jobs, initializes original
active/delivered deadlines, and records bootstrap versions. Disputed/terminal
snapshots receive no timer. Missing terminal jobs are retried using stable Credit
identities even if already settled; job absence is not proof of nonpayment.
Malformed legacy snapshots stop bootstrap with an order-ID diagnostic, never a
guessed timestamp/courier. Correct the data through an operator-reviewed database
repair and restart; already bootstrapped orders remain intact. Mixed old/new
writers and automatic queue pruning are unsupported.

Admin `GET /api/v1/orders/publication-status` adds `workflow` diagnostics: per
consumer pending/failed event counts and oldest time, schedules and due/overdue
counts, failed timers, pending/failed refund/payout jobs and uninitialized legacy
orders. It exposes no domain payloads or credentials. Readiness still measures
database reachability rather than Credit availability or drained queues.

The deterministic restart/failure scenario is
`TestWorkflowPausedConsumersAndRestartRecovery`: create and deliver with consumers
paused, reconstruct the app, materialize the original completion deadline,
confirm/finalize, assert no direct payout job, consume settlement, lose Credit's
successful response, restart and retry to one financial effect. The backend suite
also covers migration/bootstrap recovery, consumer and timer races, poison-event
isolation, admission fencing, authorization and settlement conflicts. The browser
suite covers lifecycle invalidations, snapshot recovery and freshness fences.
