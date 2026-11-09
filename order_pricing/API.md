# Order Pricing integration contract — v1.1

Status: Pricing serving, policy and local training implementations are available;
Order quote proxy/UI and Supplier/Order source snapshots are implemented.
Order creation linkage, interaction ingestion and training producers remain planned. This document
is the shared contract for Pricing, Order integration, source APIs, collectors and
training. Changes to sibling services are separate work.
Examples use illustrative IDs/times/prices. Model algorithms are not specified.

## 1. Ownership and decisions

- Order authenticates the user through User, authorizes draft/order operations,
  validates actual creation and owns the selected offer and lifecycle. Credit
  alone owns reservation and settlement. Pricing never changes either.
- Pricing owns asynchronous context collection, feature preparation, all internal
  predictors and price selection. Its external result is a recommendation, not
  intermediate predictions. Order does not assemble model-specific features.
- Supplier owns place identity, current location and active status. Cached
  catalogue data is advisory context, never authoritative creation validation.
- Customer payment equals courier payout in credits. Prices are advisory:
  users confirm their chosen positive integer offer, which can differ from the
  recommendation. No wallet, fulfilment or earnings guarantee is made.
- Initial quotes cover immediate orders only. Scheduled-order pricing is deferred;
  the Schedule UI must not call this endpoint as if its future window were now.
  Existing scheduled orders can be excluded explicitly from immediate statistics.
- No delivery batching is modelled. Existing Order capacity of three assignments
  is unchanged. Footfall-derived supply is an estimate, not observed presence or
  a count of free assignment slots.

## 2. Common wire rules

- JSON UTF-8, camelCase fields. POST requires `Content-Type: application/json`.
  UTC RFC3339 timestamps are emitted; equivalent explicit offsets are accepted.
  Calendar features use `Asia/Singapore`, independent of a replica's timezone.
- Quote credits are positive JSON integers, not money or floating point. Quote
  transport bounds are 1 through 9007199254740991 (JavaScript safe integer).
  Baseline/optimizer bounds are release policy. Order creation remains unchanged;
  training event amounts use decimal strings to preserve its wider integer range.
  No conversion rate is implied.
- UUIDs are canonical lowercase strings. Campus location IDs are case-sensitive
  catalogue IDs. Treat cursors, release IDs and snapshot IDs as opaque strings.
- Private endpoints require a configured service bearer credential on every
  request. No browser cookies, cached user authorization or browser-visible
  service secrets. Separate write credentials from read-only source credentials.
  Health endpoints are reachable only on the private network without credentials.
- Public Order proxy endpoints retain Order's session, Origin and authorization
  rules. Gateway access to `/internal` stays rejected. Never expose Pricing
  directly to browsers; TLS is required across untrusted networks.
- Errors use `{"error":{"code":"invalid_request","message":"Invalid request."}}`.
  No stack traces, feature dumps, tokens or database details in errors.
- Duplicate JSON keys and explicit null for defined non-null fields are invalid.
  Unknown JSON fields are ignored recursively from the first implementation;
  unknown values of defined enums are rejected. Unknown fields cannot alter
  pricing, safety constraints or idempotency fingerprints. Unknown query
  parameters are rejected. Clients ignore unknown response/event fields.
- Default quote request body limit: 16 KiB. Service-to-service request deadline:
  1500 ms; the Order proxy uses a 2-second total budget including authentication.
  These are implementation budgets, not a measured latency guarantee.

## 3. Quote API: Order -> Pricing

`POST /internal/v1/price-quotes` returns 200 for first issuance or replay.
Required header: `Idempotency-Key: <UUID>`, generated once per calculation attempt.

```json
{
  "draft": {
    "id": "11111111-1111-4111-8111-111111111111",
    "revision": 4
  },
  "pickup": {"type": "supplier", "id": "22222222-2222-4222-8222-222222222222"},
  "delivery": {"type": "location", "id": "utown"},
  "orderExpiresAt": "2026-11-05T18:00:00Z",
  "totalItemQuantity": 2
}
```

| Field | Required | Meaning |
| --- | --- | --- |
| `draft.id` | Yes | Random per-editor-draft UUID, stable across edits; not a user identity. |
| `draft.revision` | Yes | Safe nonnegative integer, incremented whenever quote-relevant draft inputs change. |
| `pickup`, `delivery` | Yes | Each has required `type` (`supplier` or `location`) and `id`; same type/ID pair is invalid. |
| `orderExpiresAt` | No | Optional acceptance deadline for older callers. If supplied, must be strictly after first calculation receipt time and caps quote freshness. Omitted by the current Order UI; never inferred or required for price calculation. |
| `totalItemQuantity` | No | Positive safe integer sum of item quantities. Omitted means unknown; never silently one or zero. |

Only these business inputs are caller-owned. Do not send user identity, free-text
items, candidate prices, demand/supply counts, model features or selected model
versions. Pricing resolves suppliers to campus locations from its own snapshot;
location places resolve to themselves. Distinct suppliers at the same campus
location are allowed. No order needs to exist yet.

```json
{
  "quoteId": "33333333-3333-4333-8333-333333333333",
  "draft": {"id": "11111111-1111-4111-8111-111111111111", "revision": 4},
  "recommendedCredits": 30,
  "pricingMode": "baseline",
  "quotedAt": "2026-11-05T17:00:00Z",
  "validUntil": "2026-11-05T17:02:00Z",
  "releaseId": "pricing-001",
  "contextSnapshotId": "context-001",
  "warnings": ["estimated_supply"]
}
```

All response fields shown are required. `pricingMode` is `baseline` or `model`;
clients treat an unrecognized future value as an opaque display/telemetry value,
never a permission. `warnings` is an extensible string-code array; initial codes
are `stale_order_context`, `order_context_unavailable`, `estimated_supply` and
`unknown_item_quantity`. Unknown warning codes do not invalidate the response.
Footfall-based supply always carries `estimated_supply`.

Successful quotes are durably recorded before responding. Validity is
120 seconds from calculation, capped at order expiry only when supplied; it indicates recommendation
freshness, not a binding financial offer. Replays retain the original timestamps.

### Retry and replica semantics

- All replicas share durable issuance/idempotency storage owned by Pricing.
  Scope keys to the authenticated caller identity. Fingerprint normalized known
  fields, including omission versus presence, canonical UUIDs and the optional expiry instant.
- Same key/body returns the exact original response, including after its expiry
  or a deployment. A replay does not recalculate or create another event.
  Changed known inputs with the same key return 409 `idempotency_conflict`.
- Concurrent same-key attempts serialize; an unresolved attempt may return 503
  `quote_pending`, retryable with that key. No process-local deduplication.
- Replay protection lasts at least 24 hours from first receipt. Order does not
  retry older attempts; a genuinely new calculation gets a new key. Training
  records have a separate retention policy and must not be deleted with the cache.
- Different calculations may use different releases/snapshots. One calculation
  pins a single immutable context and complete model/policy release throughout.
- No server-side result cache is required initially. If added, cached inference
  may be reused only with matching normalized inputs, release and context. A new
  issuance still gets a new quote ID/event; a network replay does not.

### Failure policy

| HTTP/code | Behaviour |
| --- | --- |
| 400 `invalid_request` | Missing/invalid known fields, expired input on first calculation or invalid place pair. |
| 401 `invalid_service_credential` | Missing/incorrect private credential. |
| 409 `idempotency_conflict` | Existing key with changed normalized inputs. |
| 413 `request_too_large` / 415 `unsupported_media_type` | Invalid transport. |
| 422 `unsupported_place` | Missing/inactive/deleted place in a fresh catalogue, or no supported route. Does not authorize Order to cancel anything. |
| 429 `rate_limited` | Bounded capacity exceeded; include `Retry-After`. |
| 503 `context_unavailable` | Missing/stale required catalogue or route context. |
| 503 `quote_unavailable` | No valid baseline/model result or price inside configured bounds. |
| 503 `quote_recording_unavailable` | Durable quote/event commit failed; do not return a successful unrecorded quote. |
| 503 `quote_pending` | Retry the same attempt. |

Order never substitutes zero or an old draft's quote on failure. Since pricing
is advisory, show “Suggestion unavailable” and allow the normal manual-offer flow.
At most one automatic retry, with the same key, within the original deadline;
otherwise wait for the next periodic calculation or a new input revision. New
calculations use new keys; network retries reuse the original key. Respect 429
Retry-After before the next periodic calculation. Errors need not be replayed as
permanent outcomes unless the key already has a successful result.

## 4. Order-facing UI boundary (quote proxy/UI implemented; linkage/events planned)

`POST /api/v1/orders/price-quotes` proxies the request/response in section 3 and
authenticates through User on each call. Public idempotency keys are scoped to
the authenticated user. Order maps each scoped key to a stable opaque downstream
UUID (durable mapping or deterministic keyed derivation) so different users cannot
replay each other's quote by reusing a public key. Retries preserve that mapping;
no user identity is included in Pricing's request.
It does not fetch model features or make a new quote attempt after a timeout.

- Generate draft identity on editor creation; debounce valid-input edits by
  400 ms. Echo revision/identity and display only responses matching the current
  draft. Increment revision on pickup, destination or quantity changes; expiry edits do
  not affect quote inputs or revision.
- Show “Suggested credits”; the user explicitly confirms their chosen offer.
  Do not overwrite a manually edited offer when a late response arrives.
- Recalculate after relevant input changes and every 30 seconds while the immediate
  order editor is visible, or sooner at quote freshness expiry. Pause when hidden,
  browsing another view, signed out or posting. No manual refresh control is exposed.
  Failures retry on the same periodic schedule, respecting Retry-After. Expired
  advice never invalidates an otherwise valid manually chosen offer.
- Add optional `pricingQuoteId` and `pricingDraft` (`id`, `revision`) together to
  regular Order creation. Omission preserves existing clients. Never trust these
  fields to set price, identity, validity or wallet permissions. Persist the link
  with order creation and its durable event; changed linkage is a changed create
  idempotency payload. Missing/mismatched quote linkage is marked unusable for
  training, not grounds to reject an otherwise valid advisory order.
- `offeredCredits` remains the authoritative selected price. Never replace it
  from a quote lookup. Unsupported Schedule UI does not send quote linkage yet.

`POST /api/v1/orders/pricing-interactions` accepts one interaction from section 7
with session/Origin validation. Order records it durably and forwards it through
its outbox. A browser reporting “displayed” is telemetry, never financial proof.
Rate-limit quote/interaction endpoints per authenticated user; do not persist
their session credentials or user identity in Pricing's quote input.

## 5. Asynchronous context APIs (implemented source projections)

These are authenticated internal read-only endpoints.
Existing browser APIs/SSE are not bulk training feeds. No direct database reads.
Source teams implement these fixed business projections; adding derived model
features inside Pricing does not require changing these endpoints.

### Supplier: `GET /internal/v1/pricing-catalogue`

```json
{
  "schemaVersion": 1,
  "snapshotId": "catalogue-001",
  "asOf": "2026-11-05T17:00:00Z",
  "locations": [{"id": "com2"}, {"id": "utown"}],
  "suppliers": [{"id": "22222222-2222-4222-8222-222222222222", "locationId": "com2", "active": true}]
}
```

Return a full, transactionally consistent catalogue: all current campus locations
and non-deleted suppliers, including inactive suppliers. Missing supplier in a
successfully replaced full snapshot means deleted/unavailable. A failed request
never means deletion. Names, free text, user data and coordinates are unnecessary
for v1. Versioned campus routing/footfall configuration supplies travel context.

### Order: `GET /internal/v1/pricing-context`

```json
{
  "schemaVersion": 1,
  "snapshotId": "orders-001",
  "asOf": "2026-11-05T17:00:00Z",
  "windowStart": "2026-11-05T16:45:00Z",
  "windowEnd": "2026-11-05T17:00:00Z",
  "areas": [{"locationId": "com2", "openRequestCount": 8, "createdOrderCount": 5}]
}
```

Use one consistent database snapshot and database time. Areas are existing stored
pickup campus location IDs (not supplier IDs). For regular, non-scheduled orders:
`openRequestCount` counts status=open and expiresAt>asOf; `createdOrderCount`
counts successful creations in `[asOf-15 minutes, asOf)`, regardless of subsequent
status. It counts booked orders, not quote requests or potential demand. Exclude
scheduled imports, including future commitments. Missing area means zero counts
only after a successful complete snapshot, never after a failed refresh.
No inference of actual online deliverers is included.

### Snapshot operation and collector defaults

- Full snapshots are intentionally small campus aggregates, not order exports.
  Maximum 1,000 locations and 10,000 suppliers/aggregate rows, 4 MiB response.
  If limits cannot be met, return 503 `snapshot_too_large`; never truncate.
  Pagination would require a separately specified stable-snapshot protocol.
- Supplier refresh every 60 seconds, maximum age 10 minutes. Order refresh every
  15 seconds, maximum age 60 seconds. Add +/-20% jitter; refresh asynchronously.
  Maximum age is measured from source `asOf`, not the last failed retry.
- Validate the full response before atomic replacement. Keep prior snapshots on
  failure; never replace data with an empty response fabricated from an error.
  Reject `asOf` more than 5 seconds in the future; reject regressions in `asOf`.
- Missing/stale Order context selects the baseline with the applicable warning;
  stale/missing Supplier catalogue makes quoting unavailable. Startup requires a
  catalogue and valid routing/footfall configuration, not Order availability.
- A model release declares whether it requires fresh Order context. No silent
  use of missing counts as zero. Each quote archives the actual context/features
  used and component source timestamps; a composite snapshot ID alone is not a
  reproducible training record unless its contents remain retrievable.
- Replicas may poll separately initially. Shared collection is a later internal
  change. Snapshots may differ between requests but are pinned within a quote.

## 6. Predictor and training boundary

The agreed internal Python adapter, enriched context schema, compatibility rules,
release package and component acceptance gates are specified in
[contracts/README.md](contracts/README.md). `QuoteRequest`/`QuoteResponse` refer to
this document's HTTP boundary; `PolicyInput`/`PolicyResult` refer to the distinct
framework/policy boundary. Internal changes do not change the v1 HTTP contract.

The current [baseline release policy](formula/FOOT_TRAFFIC.md) combines estimated
delivery effort with a local relative-activity lookup and Singapore-time profile.
Its quieter-pickup premium is bounded to 20% before rounding, with recommendations
still limited to 5–30 credits. These are release-owned heuristics, not mandatory
Order offer bounds or observed pedestrian/courier counts. No caller inputs or
shared context fields are reinterpreted; the earlier effort-only release remains
available for replay and rollback.

The serving adapter receives normalized quote inputs, calculation time and the
immutable composite context. It returns credits and internal diagnostics. Multiple
predictors and the optimizer remain private; no external predict-per-model APIs.

Footfall, participation and capacity assumptions carry `estimated` provenance;
synthetic observations carry `synthetic`; actual recorded decisions/outcomes carry
`observed`. Never relabel estimates as observed availability or synthetic outputs
as evidence of real booking behaviour.

Package preprocessing, predictors, optimizer policy, baseline, feature-schema
version and static configuration together. The release manifest identifies model
versions/MLflow runs, dataset snapshots, code commit and API compatibility. Dynamic
context is versioned separately. Changing architecture does not change this API.
Feature representation is release-owned, not a contract imposed on Order/Supplier.

## 7. Durable events and training linkage

At-least-once delivery, deduplicate by `(source,eventId)`. Ordering is not assumed
across sources. Producer retries preserve ID and immutable payload. The event
envelope is:

```json
{
  "eventId": "44444444-4444-4444-8444-444444444444",
  "source": "pricing",
  "type": "QuoteCalculated",
  "schemaVersion": 1,
  "occurredAt": "2026-11-05T17:00:00Z",
  "data": {}
}
```

All envelope fields are required. Sources are `pricing` and `order`. Every event
has explicit UTC occurrence time and a server ingestion time stored separately.
Source events are committed with their corresponding state/outbox transaction.
Ordinary stdout logs are not the durable training transport. No passwords,
session tokens, item free text, photos or user identities enter these payloads.

In Order-produced event data, `offeredCredits` is a canonical positive decimal
string (no sign or leading zeros), within Order's signed-64-bit positive range;
`totalItemQuantity` is the exact positive decimal string sum of item quantities.
This preserves values from existing Order clients beyond JavaScript's safe range.
Quote request quantities and response recommendations remain safe JSON integers.
Do not confuse a string event representation with a different credit unit.

| Type / producer | Required data |
| --- | --- |
| `QuoteCalculated` / Pricing | `quoteId`, normalized `request` from section 3, exact `response`, `releaseManifestId`, `featureSchemaVersion`, `featureSnapshot` (release-owned), `contextSources` (source/snapshotId/asOf), `inferenceReused` boolean. One per issuance, including baseline quotes. |
| `QuoteDisplayed` / Order | `quoteId`, `draft` (id/revision). Means the browser reported rendering this revision's recommendation, not merely receiving the HTTP response. |
| `OfferConfirmed` / Order | `draft`, `offeredCredits`; nullable `quoteId`. Means the user explicitly confirmed/submitted an offer, not successful order creation. |
| `PricingOrderCreated` / Order | `orderId`, `orderVersion`, `createdAt`, `expiresAt`, `pickup`, `delivery`, `totalItemQuantity`, `offeredCredits`, nullable `pricingQuoteId`, nullable `pricingDraft`. Places include resolved `locationId`; kind is immediate only. |
| `PricingOrderOutcome` / Order | `orderId`, `orderVersion`, `status`, `offeredCredits`, nullable `pricingQuoteId`. Emit for each committed lifecycle/status transition. |

Order event status strings use the existing exact API values: `open`, `accepted`,
`picked up`, `delivered`, `dispute`, `completed`, `cancelled`, `expired`,
`not completed`. Pending financial confirmation is not an invented extra status.
Consumers quarantine unrecognized future statuses and never guess labels.
Creation emits `PricingOrderCreated`; later status changes emit outcomes with
monotonically increasing Order versions. Snapshot reconciliation cannot recreate
missing transition times. Event occurrence time is the committed domain event's
timestamp, not a later collection time.

`POST /internal/v1/training-events` on Pricing accepts Order batches:

```json
{"events": [{"eventId": "44444444-4444-4444-8444-444444444444", "source": "order", "type": "QuoteDisplayed", "schemaVersion": 1, "occurredAt": "2026-11-05T17:00:00Z", "data": {"quoteId": "33333333-3333-4333-8333-333333333333", "draft": {"id": "11111111-1111-4111-8111-111111111111", "revision": 4}}}]}
```

Require 1–100 events and at most 1 MiB per batch. Authenticate the Order write credential;
reject forged Pricing-source events. Validate the whole batch before atomic
durable insertion. Return 200 `{"accepted":1,"duplicates":0}`; counts sum to batch
length. Identical existing events count as duplicates, including duplicates
within a batch; conflicting same-ID payload returns 409 `event_conflict` with no
new inserts. Malformed data returns 400 `invalid_event`; storage failure returns
503 `event_recording_unavailable`. Unknown event type/schema returns 422
`unsupported_event`; producer retains it and alerts rather than dropping it.
Retries use the same immutable events, with bounded backoff. No cursor advances
or outbox acknowledgment until durable acceptance. Events can arrive before the
quote/order linkage and must be joined later, not rejected as unknown references.

Public interaction body is `{eventId,type,occurredAt,data}`, limited to
`QuoteDisplayed` and `OfferConfirmed`. Order validates known fields, stamps source
and server ingestion time, and returns 202 only after durable local recording.
Same-ID conflicts return 409; retries retain IDs. Client timestamps are untrusted
and retained as reported, never used alone to order lifecycle events.

There is no deliverer offer/rejection instrumentation today: self-selected
acceptance of an open request is not a rejected assignment when someone ignores
it. Training must not invent rejection labels. Booking/abandonment likewise needs
an explicit observation window; no order yet is not immediately a negative label.
Joins verify draft revision and normalized inputs; user-selected prices are the
actual treatment. Quotes, renders and retries are not independent demand counts.

### Training export

`GET /internal/v1/training-events?after=<opaque cursor>&limit=100` uses a separate
read-only export credential, never direct production database access. Omit `after`
to start at the oldest retained record; `limit` defaults to 100, range 1–100.
Return `{"events":[],"nextCursor":null,"hasMore":false}` with the same envelopes
plus server-assigned `recordedAt` on each event. With events, `nextCursor` points
after the last returned record; without events it echoes the supplied cursor (or
null on an empty initial feed). `hasMore` describes committed records at read time;
poll again later even when false. Retain cursors as strings and advance only after
durable local ingestion. The feed includes Pricing's own calculation events.

Feed positions follow durable ingestion order and must form a committed prefix:
no transaction may later insert a record behind an already returned cursor. Bare
database sequence allocation without commit-order fencing is insufficient. Remote
domain timestamps need not follow this ingestion order. Source event IDs provide
deduplication. Invalid cursors return 400 `invalid_cursor`; purged cursors return
410 `cursor_expired`, requiring reconciliation with an archived export before
resuming. Never silently skip missing history. Retention and export archival must
be configured before the pilot; expiry is unrelated to quote validity/replay TTL.

## 8. Scalability, health and compatibility gates

- Replicas have no sticky sessions. Local immutable model/context caches are
  disposable; issuance/event storage is shared and durable. Private load balancer
  can route any request to any ready replica. No training in request processes.
- `GET /healthz`: 200 while process is live. `GET /readyz`: 200 only after model,
  baseline, static configuration and fresh catalogue are ready and quote recording
  storage is usable; otherwise 503. Body is `{"status":"ok"}` on success, standard
  error envelope on failure. Background readiness checks do not fan out to sources
  on every probe. Graceful shutdown stops new traffic and drains bounded requests.
- API v1 never makes an optional field required or changes field meaning/units.
  Missing quantity stays unknown across releases. Additive optional fields need
  documented defaults; a new mandatory floor requires an explicit new contract,
  not reinterpretation of `recommendedCredits` or `validUntil`.
- Test old Order -> new Pricing and new Order -> rollback Pricing, including
  unknown fields and mixed replicas. Shared storage migrations must use expand/
  contract sequencing, with rollback-compatible readers and writers throughout.
- Roll back the complete image/model/policy release; do not delete quote records
  or replay protection. Delayed events continue to accept supported older schemas.
  Training preserves schema/release versions instead of rewriting old records.
- Conformance fixtures must cover minimum/full inputs, invalid known fields,
  optional-field omission, replay after quote expiry, changed-key payload conflict,
  concurrent replicas, stale/missing snapshots, durable event failure, event replay
  and out-of-order linkage, old/new contract compatibility and manual-offer fallback.

## 9. Parallel implementation ownership

| Workstream | Implements | Can use mocks until |
| --- | --- | --- |
| Serving | Section 3 quote API, health, adapter, shared issuance/event durability | Context APIs and trained model are ready; use contract-shaped context and baseline. |
| Order integration | Section 4 proxy/UI/linkage, source snapshot, durable event producer | Pricing is ready; mock quote/error/replay responses. |
| Supplier integration | Section 5 full catalogue projection | Collector is ready; verify fixed example snapshots. |
| Collection | Section 5 refresh/freshness/composite snapshots | Source endpoints are ready; use complete snapshot fixtures. |
| Training | Section 6 model package, section 7 feed consumer and labels | Real events exist; use explicitly synthetic fixtures. |
| Deployment | Private routing, shared storage, health-gated rollout | Trained model exists; deploy the baseline image first. |

The API contract precedes these implementations. Model/feature changes stay
behind their adapters; source teams do not implement model-specific formulas.

## 10. Remaining decisions that do not change the v1 wire contract

- Baseline formula, bounded candidate prices, predictor algorithms, objective
  targets, route/footfall assumptions and validation thresholds.
- Durable storage implementation, outbox workers, backup/export and retention.
  No pilot claiming complete training capture until all producer paths are durable.
- AWS sizing, ports, environment variable names, credentials provisioning,
  per-user rate limits and infrastructure definitions. Validate config at startup.
- Scheduled-order quotes, actual deliverer presence, mandatory floor enforcement
  and new behavioural experimentation require follow-up specifications.

### v1.1 expiry compatibility

`contracts/schemas/v1.1.json` relaxes quote expiry to optional; the original v1
schema and its fixtures remain unchanged. Older requests with expiry preserve
their normalization, freshness cap and replay fingerprints. Omitted expiry stays
omitted in policy input, archived events and fingerprints and defaults only the
quote freshness to 120 seconds; no artificial order deadline is generated. Null
or malformed supplied expiry remains invalid. Deploy the updated serving API
before an expiry-free Order client; rollback to older serving cannot quote new
expiry-free requests, so the advisory UI falls back to manual offers. Actual
order creation still requires its own future expiry.

### v1.2 catalogue coordinates

`contracts/schemas/v1.2.json` adds optional `latitude` and `longitude` (WGS84
decimal degrees, both or neither) to catalogue locations. Supplier's internal
pricing catalogue supplies them; serving validates and passes them to policies,
and older readers ignore them. Serving advertises capability `coordinates.v1`, so
a release requiring it fails readiness on older serving instead of quoting
without coordinates. Formula release `effort-traffic-002` uses them to estimate
routes for location pairs without a reference route.

