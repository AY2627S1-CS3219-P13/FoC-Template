# Order Service API

Public base: `http://localhost:8084` when run standalone. Browser requests use the User Service session cookie. Every POST requires the exact configured `Origin` and `Content-Type: application/json`; creation also requires `Idempotency-Key`.

## Endpoints

| Method and path | Success | Purpose |
| --- | --- | --- |
| `POST /api/v1/orders` | 201 for first completion, 200 for an identical retry | Validate and create an open errand after reserving credits |
| `POST /api/v1/orders/items/add` | 200 | Validate an item and perform the duplicate warning/decision interaction |
| `GET /api/v1/orders/session` | 200 | Current validated User identity, without session token |
| `GET /api/v1/orders/places?page=1` | 200 | Active suppliers plus campus locations for selectors |
| `GET /api/v1/orders?scope=mine&page=1` | 200 | Paginated mine or courier history; unpaginated open view |
| `GET /api/v1/orders?scope=disputes&page=1` | 200 | Admin-only dispute queue |
| `GET /api/v1/orders?scope=user&userId={id}&page=1` | 200 | Admin-only orders where the user is requester or courier |
| `GET /api/v1/orders/stream` | 200 | Authenticated creation and eligible lifecycle SSE invalidations |
| `GET /api/v1/orders/publication-status` | 200 | Admin-only publication/workflow backlog monitoring |
| `GET /api/v1/orders/{id}` | 200 | Authorized detail snapshot |
| `POST /api/v1/orders/{id}/refresh` | 200 | Reconcile current Supplier details/cancellation |
| `POST /api/v1/orders/{id}/actions` | 200; 202 for pending confirmation | Authorized courier, requester and dispute-resolution actions |
| `GET /api/v1/orders/{id}/events?afterVersion=0` | 200 | Read committed per-order event records |
| `GET /orders` | 200 | Standalone Order screen (data remains protected) |
| `GET /healthz` | 200 | Process liveness |
| `GET /readyz` | 200 or 503 | PostgreSQL readiness |

### Create

```json
{
  "pickup": {"type": "supplier", "id": "11111111-1111-4111-8111-111111111111"},
  "delivery": {"type": "location", "id": "utown"},
  "items": [{"name": "Milk Tea", "description": "Less sugar", "quantity": 2}],
  "offeredCredits": 30,
  "expiresAt": "2026-10-03T18:00:00+08:00"
}
```

The authenticated user is always the requester; no requester ID is accepted. Place pairs compare both `type` and `id`. Locations must exist, suppliers must exist and be active, and the two pairs must differ. Quantity and offer are positive JSON integers representable as signed 64-bit values. `items` is nonempty and has no business maximum. Expiry is RFC 3339 and strictly after creation. Responses render times at UTC+08:00.

Creation response:

```json
{
  "order": {
    "id": "order-...",
    "requesterId": "11111111-1111-4111-8111-111111111111",
    "pickup": {"type": "supplier", "id": "...", "name": "Campus Cafe", "locationId": "com2", "locationName": "COM2"},
    "delivery": {"type": "location", "id": "utown", "name": "UTown"},
    "items": [{"name": "Milk Tea", "description": "Less sugar", "quantity": 2}],
    "offeredCredits": 30,
    "expiresAt": "2026-10-03T18:00:00+08:00",
    "status": "open",
    "createdAt": "2026-10-03T17:00:00+08:00",
    "courierId": null,
    "version": 1
  }
}
```

The idempotency key is scoped to the requester, contains 1–128 Unicode characters, and has no surrounding whitespace or control characters. Retry an unresolved request with the same key and body. A completed attempt returns its existing order even after expiry; a changed body returns `409 idempotency_conflict`.

An uncertain Credit response or failed persistence abandons the attempt instead of completing it later. While cleanup is unresolved, retries return `503 creation_cleanup_pending`; after cleanup, `409 creation_abandoned` instructs the client to use a new key. Neither response claims a successful creation. A definitively rejected reservation (`wallet_not_ready` or `insufficient_credits`) can be retried with the same key. Equivalent RFC3339 offsets and supplier UUID letter cases are normalized for idempotency.

Place information is initially resolved at creation, then updated by explicit refresh or courier transitions. The `/orders` editor implements the item warning, page retention and same-key retry behavior. Neither stored snapshots nor events replace authoritative validation before a command.

### Reads and refresh

`GET /api/v1/orders/session` returns the validated `userId`, `roles`, `expiresAt`
and `courierBusy`. Capacity is occupied by accepted/picked-up assignments and is
released at delivery; acceptance still enforces capacity transactionally.

For `scope=mine`, `filter=all` (default), `current`, or `past` filters before
pagination. Past includes completed, not completed, cancelled and expired;
current includes the other five states, including disputes. Unknown filters
return `400 invalid_filter`.

Every data route validates the current session through User. Mine/courier listings return `{orders: [], page: 1, hasMore: false}` in descending creation time/ID order, 20 per page. `scope=mine` (default) selects the requester; `courier` selects original courier assignments, including delivered history; `open` selects unexpired open orders from other requesters. Pages are positive integers up to 100000; this is pagination, not an order-count cap.

Mine/courier/dispute snapshots reconcile every displayed entry against Supplier
using the current session, with per-request lookup reuse. Renames update details;
inactivity/deletion cancels only open/accepted/picked-up entries and records durable
refund intent. Protected statuses retain their last known details for missing places.
Catalogue outages return 503 rather than a falsely synchronized list. History
on other pages is not refreshed. A dispute resolved concurrently is omitted
from the admin snapshot.

`scope=open` returns all eligible orders as `{orders: [], hasMore: false}` without pagination (`page` is ignored). Sort with `sort=expiry` (default) or `sort=credits` (highest first); ties use expiry, creation time ascending, then ID. Unsupported sort values return `400 invalid_sort`. Each snapshot refreshes every listed order’s places using the current session, sharing lookups only within that request. Inactive/deleted places remove orders and record refund-intent events; Supplier outages fail the snapshot with 503.

An open order is readable by authenticated users. Administrators can also inspect disputed orders and their events. Otherwise only its requester or assigned courier may read it, its events or refresh it; other users receive the same `404 order_not_found` as an unknown ID. No arbitrary requester/courier IDs are accepted from the browser. Reads do not contact Credit.

Places return `{places: [Place], hasMore: boolean}`; Supplier pages are 100 records, and campus records appear on page 1. Fetch successive pages until `hasMore` is false. Each place retains its entity type and record ID. A failed/malformed catalogue response returns 503, not an empty catalogue.

Refresh accepts `{}`. It uses the current session to check both selected records through Supplier, then commits changed details, an incremented order version and an event together. Confirmed disappearance/inactivity cancels open/accepted/picked-up orders and commits a durable Credit release intent; no cancellation occurs on Supplier 503/malformed responses. The response may include `cancellationReason`, which describes the pending refund, not completed settlement. A worker retries releases independently. Refresh never changes delivered/dispute/terminal status; active renamed/relocated places may still update those details. Failed refresh preserves the last committed snapshot.

The recovery worker also reconciles pre-delivery orders through Supplier’s private service-authenticated catalogue. It never stores or reuses browser credentials. Displayed lists poll independently every two seconds while visible, including when the session UI check is slow. List reads and explicit detail refresh have a two-second server budget including authentication and database waits; deadline failures return `503 snapshot_timeout`. The browser abandons refreshes after 2.5 seconds. Failed, offline or overdue snapshots display “List is not updated” and disable actions until synchronization succeeds. Freshness is measured from the request start, never the arrival time of a delayed response; the 500ms watchdog marks a snapshot stale at age 4.5 seconds (by five seconds with normal foreground scheduling).

Snapshots resolve distinct places with at most eight concurrent lookups before acquiring order locks, using only the current session. Version checks reject a snapshot overtaken by another mutation. Open lists remain unpaginated; orders created after snapshot selection appear on the next refresh. The five-second delivery target assumes a responsive foreground browser, network, and dependencies that fit the refresh budget. During outages, excessive load or suspended browser execution the service cannot promise delivery; it reports stale state instead. The backlog's unconditional timing requirement is not claimed as proven for those conditions.

### Lifecycle actions

Send `{ "action": "..." }`; identity and roles always come from the freshly
validated session. There is no arbitrary status setter or item-level status.

| Action | Actor | Required state → result |
| --- | --- | --- |
| `accept` | Another user with a free courier slot | open → accepted |
| `pick_up` | Assigned courier, including admins | accepted → picked up |
| `deliver` | Assigned courier, including admins | picked up → delivered |
| `cancel` | Requester | open/accepted → cancelled |
| `confirm` | Requester | delivered → pending confirmation (202), then completed |
| `dispute` | Requester | delivered → dispute |
| `resolve_completed` | Admin | dispute → completed |
| `resolve_not_completed` | Admin | dispute → not completed |

Only accepted/picked-up errands occupy a courier slot. Delivery atomically frees
that slot while preserving assignment and escrow. Confirmation, disputes and
resolution cannot alter a newer assignment. Pickup cannot be skipped. All four
terminal states remain terminal; dispute can leave only through admin resolution.
An admin has no general lifecycle override. Admins acting as the requester or
assigned courier retain that participant's normal permissions. Only admins can
resolve a disputed order.

Successful actions have a durable `(order ID, action, actor)` receipt committed
with their result. Replays return the current order without new events or Credit
operations, including pickup retried after later cancellation/expiry. A failed or
unauthorized action has no success receipt. A confirmation superseded by dispute
returns 409, even when a pending confirmation was previously accepted. Legacy
pre-migration actions are inferred only where the saved state proves success.

Confirmation returns 202 with the still-delivered order and
`completionRequestedAt`; this means pending, not completed. The lifecycle worker
checks every second. It allows a one-second arbitration interval before finalizing
confirmation or the five-minute delivery deadline, so overlapping disputes can
win before any payout is queued. A dispute received after final completion cannot
reopen it. `deliveredAt` records delivery commit time; automatic completion uses
that original time after restart. Disputes clear confirmation intent and are
excluded from automatic completion and expiry. Competing admin outcomes have one
committed winner; the other returns 409.

Receipt time governs acceptance and delivery. At/after expiry, a new transition
is rejected and expiry/refund intent is committed. A valid acceptance or pickup
received before expiry but saved afterward is recorded then immediately expired.
A valid pre-deadline delivery holding the row lock is protected from the expiry
worker and may commit afterward. Earlier final expiry/cancellation cannot reopen.
The expiry worker skips in-flight row locks and covers open, accepted and picked-up
orders. Cancelled, delivered, disputed and completed outcomes cannot expire.

Pickup takes precedence over deactivation discovered by its own lookup. A refresh
that observed accepted before waiting for a competing pickup must not cancel the
winning pickup. An earlier committed cancellation remains final; a later distinct
Supplier deactivation may cancel picked-up orders. Requester cancellation is
allowed only before pickup. Supplier failures never imply deactivation.

### Admission and competing requests

Actions and explicit refreshes now commit a short-lived admission **before User
validation**, command-body processing, or acquisition of an order row lock. The
runtime reserves a separate four-connection pool for these short admission and
queue checks; its normal twenty-connection transaction pool cannot hide a waiting
request from another replica or a lifecycle worker. No session cookie, token,
command body or trusted actor identity is persisted in an admission.

An advisory transaction lock serializes admission with background finalization
for each order. The committed admission ticket defines the arbitration receipt
boundary across instances; browser clicks, network packets, and requests that
cannot reach durable admission do not constitute a successful command. The
server's incoming-request timestamp is retained for the existing before/at/after
expiry checks. If background finalization acquired the gate first and committed,
its terminal outcome remains final.

Once admitted, later actions for that order wait for earlier live tickets even
if their authentication finishes sooner. Each action still validates its session,
actor, state and deadline; admission grants no authorization. An earlier invalid,
failed or disconnected request drops its ticket and does not change state.
Successful action receipts continue to make lost-response retries idempotent.

Expiry, completion and Supplier reconciliation check the same admissions.
Workers skip protected orders; list reconciliation returns `503 order_updating`
instead of cancelling ahead of an earlier pending action. An admitted pre-deadline
delivery remains protected while waiting for authentication, a transaction
connection or its row lock. Pre-deadline acceptance saved late still records
acceptance then immediate expiry. Admitted disputes prevent final completion
while they authenticate, beyond the existing one-second arbitration interval.
A dispute arriving after final completion cannot reopen it.

Completion uses a separate transaction per order. It prepares status and event
writes while admission remains open, then checks admissions under the
gate immediately before commit. Any live admission registered during those
writes vetoes the entire speculative transaction. Thus a valid overlapping
dispute still wins when completion's database writes stall or its own validation
exceeds one second. The final gate is the decision's linearization point;
requests admitted after that point do not reopen a committed terminal decision.
Browser response overlap alone cannot define priority across replicas.

Actions have a fifteen-second processing deadline; explicit refreshes have the
two-second snapshot budget. Admissions expire
after thirty seconds on the database clock, exceeding that processing budget.
Normal completion/failure removes them immediately; workers remove expired
records after a crash. A handler must lock and validate its live ticket before
mutating an order. An expired/missing ticket returns `409 admission_expired` and
cannot later revive an order. This is a fence against stale handlers, not a
background command queue: after a crash, no saved command is executed. Deadlines
are not reset, and overdue lifecycle work resumes when the stale admission
expires. Retry the same action to reconcile an uncertain result.

Completion and its actionable event commit together. Cancelled, expired and not
completed outcomes instead commit a release-intent event. The independent
settlement consumer atomically inserts the existing financial job and its own
receipt later; committed intent can precede queue materialization. The financial worker retries
Credit's existing idempotent endpoints with the same order ID/courier after failure
or lost responses; only Credit changes balances. A completed status does not claim
that payout has already finished. Delivery and dispute perform no settlement.

409 `courier_busy`, `status_conflict` or `order_expired` describe rejected actions;
403 `action_forbidden` / `admin_required` reject unauthorized actors. Retry an
uncertain action with the same identity; never assume a requested target status
merely because the request was sent. The UI displays committed status and keeps
conflicting actions disabled while a network result is unresolved.

### Events

Events return `{events: [{eventId, orderId, orderVersion, type, schemaVersion, createdAt}]}`. Types are `OrderCreated`, `OrderDetailsChanged` and `OrderStatusChanged`; schema version is 1. Read at most 100 in ascending order-version order and advance `afterVersion` only through received events. Authorization uses current visibility, not historical events. Events contain no session or dependency credentials.

Order and event insertions commit atomically. Failure to write the creation event rolls back the order and triggers reservation cleanup. Repeated commands do not insert duplicate events. A restarted consumer can replay this durable journal, deduplicate by `eventId`, and fetch an authoritative detail snapshot; never replace a newer snapshot with an older `version`. Existing orders predating migration 003 may have no version-1 event, so always start/reconnect with a snapshot. Status/detail events remain available through this per-order journal. Creation events also use the live publication contract below.

### Lifecycle event publication

`GET /api/v1/orders/stream` uses the current session cookie and responds with
`text/event-stream`, `Cache-Control: no-store, no-transform` and
`X-Accel-Buffering: no`. User validation runs at connection time and before every
batch (normally once per second); invalid/unavailable authentication or changed
identity stops delivery. Creation announcements reach other requesters' currently
open, unexpired orders. Status/detail invalidations reach the current requester,
assigned courier, and admins while the order is currently disputed. Unrelated
viewers may receive metadata-only updates while an order is open or removals
from its open state; later private progress is filtered. Admins lose private
dispute visibility after resolution unless they are a participant. Eligibility
is checked when reading the batch; a later
transition can still occur before the browser receives it, so fetch a snapshot
before displaying or acting on it. No item contents, identity details or secrets
are broadcast.

```text
id: 42
event: OrderCreated
data: {"eventId":"<orderId>:1","orderId":"<orderId>","orderVersion":1,"type":"OrderCreated","schemaVersion":1,"createdAt":"2026-10-09T18:00:00+08:00"}
```

The SSE `id` is a decimal publication position, separate from `eventId` and
`orderVersion`. Keep it as a string/BigInt. Send `Last-Event-ID` to resume strictly
after that position. A nonnumeric, negative or future cursor returns
`400 invalid_event_cursor`; discard it and open a fresh connection/snapshot.
Publication positions are assigned under a shared database row lock and committed
with each effect and receipt, rather than allocated at creation time. Concurrent publishers,
late commits, rollback and restart cannot leave an acknowledged cursor ahead of
an unpublished earlier allocation. Each event is published once in the feed;
network replay may deliver it more than once, so consumers must deduplicate.

Without a cursor, start at the current publication head. Every connection emits
`snapshot` with `{ "userId": "<current user>" }`: fetch the authoritative open
list, including on reconnect, before clearing a stale warning. `checkpoint`
events with `{}` advance the cursor over entries filtered from this user's feed
and serve as heartbeats. `session_invalidated` ends a session whose authorization
can no longer be verified; close the stream and revalidate before reconnecting.
Streams rotate after eight seconds, advertise a one-second reconnect interval,
and fit the normal request/write deadlines. Errors after streaming starts close
the connection instead of appending a JSON error response.

The independent publisher runs at startup and every second, in batches of at most
100 events. It reads only committed journal entries; retrying publication
does not create another order or reserve credits again. No browser connection is
required for publication. A database failure leaves entries pending for retry.
`GET /api/v1/orders/publication-status` requires an admin and returns
the existing `published`, `pending`, `oldestPendingAt` fields plus the workflow
section documented below. A published event
is durably available, not necessarily read by any particular user. No retention
pruning, external broker or per-user inbox is configured. The browser keeps
snapshot polling for status changes and Supplier refresh alongside lifecycle SSE.
`OrderStatusChanged` and `OrderDetailsChanged` listeners coalesce authoritative
snapshot refreshes. Notifications never directly overwrite cached order state.

### Add item

Request fields are `existingItems`, `item`, and optional `decision`. When the new name matches after Unicode whitespace removal and case folding, omitting `decision` returns `duplicate: true`, `requiresDecision: true`, `added: false`, and the unchanged list. `decision: "proceed"` appends it; `decision: "do_not_proceed"` does not. Nonduplicates are appended immediately.

## Errors

Errors are `{"error":{"code":"...","message":"..."}}`. Input errors use 400, invalid sessions 401, foreign/missing mutation origins 403, wallet/idempotency conflicts 409, the 1 MiB transport limit 413, unsupported JSON media 415, unexpected errors 500, and dependency outages 503. `wallet_not_ready`, `insufficient_credits`, `auth_unavailable`, `places_unavailable`, and `credit_unavailable` distinguish the principal retry/failure cases. Malformed dependency responses fail closed. No success is returned merely because Credit returned a malformed HTTP 200.

Automatic Supplier reconciliation uses authenticated internal catalogue reads on Supplier’s existing port 8080, without a browser. A confirmed inactive/deleted place cancels open/accepted/picked-up orders and records refund intent atomically in the cancellation event. Admission and version guards protect competing transitions. Dependency errors leave orders unchanged for a later sweep. Refunds retry independently and may complete after cancellation.
If persistence fails after reservation, a definitive `400 invalid_expiry` is returned only after cleanup is confirmed. A failed refund returns `503 creation_cleanup_pending`; keep the same idempotency key and retry it. The durable worker continues cleanup after the request ends. A successful refund restores reserved credits exactly once.

An omitted session-cookie setting derives its name from the application origin: `foc_session` for HTTP and `__Host-foc_session` for HTTPS. The root stack shares User’s origin.

### Internal lifecycle processing and diagnostics

The public journal remains schema 1 with the same three generic event types.
Private `domain_type`, `domain_schema_version` and `domain_payload` are nullable
only for legacy events. New events carry schema 1 with explicit nullable
`previousStatus`, `courierId`, `deliveredAt`, `completionRequestedAt` and
`cancellationReason`, plus `status`, `requesterId`, `expiresAt`. Their envelope
uses the journal's persisted `created_at` as occurrence time and the existing
`<orderId>:<version>` identity. No extra journal row is inserted per version.
Items, credentials and financial amounts are excluded. Private payloads never
appear in public journal or SSE responses.

| Private domain type | Additional durable effect |
| --- | --- |
| OrderCreated, OrderAccepted, OrderPickedUp | Original expiry schedule |
| OrderDelivered | Replace expiry with original delivery completion deadline |
| OrderCompletionRequested | Advance completion using persisted confirmation time |
| OrderDisputed | Remove timers; keep escrow reserved |
| OrderCompleted | Remove timers; materialize payout to recorded courier |
| OrderCancelled, OrderExpired, OrderNotCompleted | Remove timers; materialize refund |
| OrderDetailsChanged | Preserve applicable deadline and advance projection version |

`workflow-timers`, `workflow-settlement` and `workflow-publication` process
missing receipts in ascending order version per consumer/order. Their effects
and `(consumer_name,event_id)` receipt commit together; retry failure does not
acknowledge an event. Retries retain attempts, next eligibility and sanitized
`invalid_domain_event`/`effect_failed` categories, with backoff capped at 60s.
Legacy null-domain rows are not consumed. Financial job failures retain
`credit_conflict` or `credit_unavailable` categories and remain pending.

The existing admin status response preserves `published`, `pending` and
`oldestPendingAt` for the publication feed, now covering lifecycle publication.
It adds a `workflow` object with `consumers` (each has `consumer`, `pending`,
`failed`, `oldestPendingAt`), `timers`, `dueTimers`, `overdueTimers`,
`failedTimers`, `pendingRefunds`, `pendingPayouts`, `failedRefunds`,
`failedPayouts` and `legacyUninitialized`. Due and overdue count schedules at or
before the current clock boundary. A failure count indicates retained work;
failed financial jobs are also included in pending counts. Monitoring conveys
lag and investigation needs, never a promise that credits already moved.

At-least-once retries depend on recovery of the database, workers and dependencies.
Credit owns duplicate-safe financial effects. No unconditional completion timing
or exactly-once transport guarantee is made. See README for the stop-old-instances
upgrade procedure and resumable legacy bootstrap; no new public replay endpoint
or additional configuration is introduced.

Creation reconciliation releases visible escrows or aborts missing ones through Credit’s durable `/abort` endpoint. It does not retry reservation merely to obtain a cleanup record. Terminal abandonment requires Credit confirmation that no late request can freeze funds; dependency failures keep `release_pending` retryable.
