# Automatic Supplier reconciliation — 2026-10-14

The recovery worker now uses Supplier’s authenticated internal catalogue to cancel
pre-delivery orders and queue refunds without a browser. No user cookie is stored.
Supplier and Order formatting, vet, full race-enabled suites and server builds
passed, including service credentials, admission deferral, protected states,
refund retries and isolation of failed place lookups. Shared gateway smoke passed.
A disposable live four-service check deactivated a supplier for a picked-up
30-credit order, then queried only Credit while waiting. Order cancelled the
errand, refunded 30 exactly once and freed courier capacity automatically.
The existing 30-second worker cadence applies; settlement may follow next tick.
The catalogue reuses Supplier’s existing port 8080; no extra listener is added.
Order’s initializer adds the shared Supplier credential without changing existing
settings. User and Credit files are unchanged relative to main. Credential setup
was checked for fresh and existing Order environments and repeat invocation.
Earlier sections below describe historical implementations and checks.

---

# Order usability and participant permissions — 2026-10-11

Order-only changes add the account/balance header and catalogue return link,
courier-capacity feedback, distinct status colours, cleared details on view
changes, and server-side all/current/past request filters. Admins assigned as
couriers retain pickup/delivery permission; disputed orders still require admin
resolution. No shared frontend or gateway files were edited for this change.
The catalogue's Orders entry was added in the subsequently authorized frontend follow-up.
Account labels use existing same-origin User/Credit routes through the gateway;
standalone access without those routes reports account/balance unavailable.

Backend validation passed: gofmt, vet, the complete race-enabled test suite
(60.331 seconds for Order), and build. Added backend regressions exercise all
nine states in the filters, capacity release, admin delivery and dispute access.
The browser regression for navigation now explicitly requires reselecting the
order after switching views, retaining its pending-action and replay assertions.
Final browser verification passed all four JavaScript helper tests and all
sixteen Chromium tests (2.3 minutes), including the new account, filter,
navigation, colour and admin-courier capacity regressions. `git diff --check`
passed. Tests used isolated dependencies; no live accounts or wallet data changed.

---

# Completion overlap and refresh deadlines — 2026-10-10

This is the current assessment; historical gap descriptions below describe the
earlier implementation.

**Overlapping disputes:** completion now prepares each order in a separate
transaction while leaving admission open. A final admission check immediately
before commit rolls back the prepared status, event and payout if any live
request registered during those writes. A valid dispute therefore beats manual
or automatic completion even when database writes stall or authentication takes
longer than the old one-second interval. Invalid/disconnected requests still
grant no authority and are cleaned up. The final gate defines the durable
decision boundary; a completed decision cannot be reopened by a later request.

**Five-second updates:** displayed lists resolve distinct places with up to eight
concurrent calls before row locks, then fence against intervening versions. List
and explicit-detail requests have a two-second total server budget including
authentication, connection waits and Supplier I/O. Browsers use a 2.5-second
refresh timeout and an independent two-second poll clock, so a slow session UI
check cannot suppress refreshes. Selected rows already reconciled by the list
avoid redundant explicit-refresh admissions. Request-start timestamps determine
freshness; late responses cannot clear staleness. The foreground watchdog reports
staleness by five seconds, and failures disable actions until a successful sync.
Open lists remain unpaginated; newly created orders after snapshot selection
appear in the next snapshot. Catalogue outages never imply deletion.

New regressions cover completion paused at payout insertion with a dispute on a
second instance (manual and automatic), delayed authentication, rollback of all
financial/event effects, 24 open orders with 48 distinct 100ms catalogue lookups,
bounded lookup concurrency, authentication/catalogue deadline failures, browser
updates during a six-second session stall, and rejection/recovery of late replies.

**Limits that cannot be implemented as unconditional guarantees:** no finite
service can deliver current data within five seconds through an unavailable
dependency/network, unlimited workload, or a suspended browser. Successful update
delivery requires foreground scheduling and the documented refresh budgets;
otherwise the implemented result is explicit stale/unavailable state. Likewise,
“all overlapping” cannot include requests first durably admitted after the final
decision point while also preserving irreversible completion. HTTP response
overlap or packets that have not reached durable admission do not establish
cross-replica order. These boundaries are documented rather than claiming the
literal unbounded requirements have been proven. Unattended Supplier integration
and shared gateway/live-service verification remain separate existing gaps.

Final verification passed: gofmt enforcement, `go vet ./...`, the complete
`go test -race -count=1 ./...` suite (57.463 seconds for Order), and the server
build. All four JavaScript helper tests and fourteen Chromium tests passed
(1.3 minutes). The browser timing test observes cancellation within five seconds
while session UI validation stalls for six seconds and snapshot replies incur
1.2 seconds of transport delay. The catalogue test verifies all 48 distinct
100ms lookups and all 24 open orders within the two-second server budget.
An initial late-response browser regression failed; response-age fencing was
added and the test now waits for its intercepted request before asserting the
deadline. The full rerun passed without removing the stale-state assertions.
`git diff --check` passed. Checks used isolated PostgreSQL schemas and HTTP fakes;
no live sibling accounts or shared wiring were changed.

---

# Receipt-order arbitration follow-up — 2026-10-10

Implemented durable admission before authentication and order-row acquisition.
This section supersedes earlier findings that requests waiting for User validation
or a transaction connection are invisible to lifecycle arbitration.

- Migration 007 adds leased admission tickets without an order foreign key,
  so registration never waits for an order row lock. No credentials, command body
  or assumed actor are persisted. Authorization still uses User for each request.
- The runtime reserves four database connections for admission/cleanup/queue
  checks, independently of the ordinary twenty-connection transaction pool.
  Actions and explicit refreshes register before authenticating. A short per-order
  database gate serializes registration against background finalization; later
  actions cannot overtake earlier live tickets simply by authenticating faster.
- Handler transactions lock and validate their own receipt before mutation.
  They do not hold the registration gate during external I/O or row-lock waits,
  so further requests can still register. Workers consult live admissions and
  defer expiry/completion; list reconciliation returns `503 order_updating`
  instead of performing a competing Supplier cancellation.
- Pre-deadline acceptance remains accepted-then-expired when saved late; valid
  pre-deadline delivery remains delivered. Existing actor checks, terminal-state
  protection, successful action receipts and atomic Credit intents remain intact.
- A dispute admitted before completion finalizes now blocks completion during
  authentication, even beyond the one-second interval. Earlier final completion
  is never reopened. The existing interval still permits new overlapping disputes
  to arrive before finalization; it is no longer the sole protection for a request
  already waiting inside Order.
- Normal completion, rejection and disconnect clean up the ticket. Database-clock
  leases expire after thirty seconds, beyond the fifteen-second request budget.
  Restarted workers remove expired tickets and resume original deadlines. A stale
  handler cannot use an expired/deleted ticket. Admissions are not queued commands
  and cannot be executed after a crash.

**Receipt boundary:** arbitration is ordered by the committed database admission,
not a browser click or the arrival order of packets at different replicas. The
incoming server timestamp remains the deadline timestamp. Admission is the first
durable step before authentication; a request that cannot be admitted has no
success/protection guarantee. The shared gate makes a finalization committed
before admission final. This avoids promising distributed ordering for requests
that have not yet reached durable service state.

New regressions cover delayed User validation across expiry, opposite
cancellation/pickup arrival orders on separate app instances, an exhausted normal
connection pool, an external order-row lock, delayed disputes, invalid sessions,
disconnected requests, competing Supplier refresh, and stale-ticket fencing after
simulated lease expiry.

Final verification passed: gofmt enforcement, `go vet ./...`, the complete
`go test -race -count=1 ./...` suite (33.816 seconds for Order), and the server
build. All four JavaScript helper tests and all twelve Chromium tests passed
(33.4 seconds). `git diff --check` passed. Tests used the dedicated PostgreSQL
database and HTTP fakes; no live sibling accounts were used. The initial new
deadline test also crossed the automatic-completion window through its simulated
clock; its setup was corrected to keep that window open, retaining the assertions
that delivery alone never pays out and late acceptance queues exactly one refund.

Unattended Supplier integration, a strict five-second synchronization bound and
shared gateway/live integration remain separate gaps. All changes in this
follow-up stay within Order Service; no applied migration was edited.

---

# Creation event publication follow-up — 2026-10-09

Implemented the requested publication of newly created errands to other users.
This supersedes earlier statements that there is no publisher or SSE stream.

- Migration 006 adds a durable publication feed and a transactionally serialized
  cursor. The original creation transaction still commits its journal entry
  atomically with the order, after Credit reservation. Publication never queues
  a failed creation or repeats reservation.
- An independent worker runs at startup and every second. Failed publication
  remains pending; retry/restart and concurrent workers cannot duplicate feed
  entries. A late creation commit receives a later publication position, so
  cursor advancement cannot skip it. Events are retained in PostgreSQL.
- The authenticated `/api/v1/orders/stream` SSE endpoint sends `OrderCreated`
  metadata only for other requesters' currently open/unexpired orders. Session
  validation runs before each batch. Logout, auth outage or changed identity
  stops further delivery. Stream connections rotate within existing timeouts.
- `Last-Event-ID` replays missed feed entries; duplicate delivery is tolerated.
  Every reconnect requests an authoritative snapshot. Removed/private orders
  are filtered at delivery time, and events cannot overwrite current order state.
- The embedded UI shows a new-errand notice and refreshes the open list on an
  event. It retains snapshot polling for status/Supplier changes. A queued list
  refresh prevents an event arriving during another request from being lost.
- Admins can inspect pending/published counts and the oldest pending creation
  through `/api/v1/orders/publication-status`.

This implements browser delivery using PostgreSQL and SSE; it does not add an
external broker, OS notifications, a per-user inbox, or publication of status/
detail events. “Published” means available in the feed, not acknowledged by all
users. Users outside the Order UI must subscribe to this endpoint to receive live
events; the shared frontend and gateway were not modified. Other unfinished
Supplier/receipt-arbitration/timing requirements below are unchanged.

Backend verification passed: gofmt enforcement, `go vet ./...`,
`go test -race -count=1 ./...` (56.431 seconds for Order), and the server build.
New tests exercise failed publication and restart, concurrent publishers, late
commits, abandoned creation exclusion, audience/expiry filtering, invalid cursors,
replay and session invalidation. A test fixture initially omitted the required
creation-attempt row for its simulated late commit; that fixture was corrected
before the passing full run. `git diff --check` passed.

Final browser verification passed: all four JavaScript helper tests and all 12
Chromium tests (32.3 seconds). A two-browser test disables snapshot polling to
verify event-driven notification/list refresh and own-event exclusion; another
checks recovery after a rejected stream. All tests use the dedicated
PostgreSQL database and HTTP fakes. No live sibling accounts or shared wiring
were changed.

---

# Implementation audit before event publication — 2026-10-09

**Verdict: substantial implementation, with remaining acceptance gaps.** This
section supersedes the assessments below. Older test timings describe older
checkouts, not proof of the current implementation. The audit compared the full
BACKLOG.md with production handlers, workers, embedded UI, migrations, dependency
contracts and tests. Existing uncommitted work was preserved; changes stayed in
Order Service.

## Requirement assessment

| Requirement | Assessment and code evidence |
| --- | --- |
| FR3, 3.4: nine states, forward transitions, terminal states | Implemented in `lifecycle.go`, `changes.go`, `settlement.go`; transition/actor matrix and lifecycle verification tests. Priority qualifications below still apply. |
| 3.1: validated, idempotent creation | Implemented in `create.go`, `model.go`, `recovery.go`; same-key retries, uncertain reservation cleanup, retained browser input and lost-response tests. |
| 3.1.1: active distinct places, exact details, ongoing changes | Creation/transition validation and request-driven reconciliation implemented. This audit adds reconciliation of every displayed history/dispute row. Unattended and off-page synchronization remain incomplete. |
| 3.1.2: items and duplicate editor decisions | Implemented in `model.go`, `http.go`, `web/app.js`; Unicode comparison, Proceed/Do not proceed, retained editor, large integers and text-only rendering tests. Signed-64-bit quantities and 1 MiB request bodies are documented infrastructure limits. |
| 3.1.3: credits and reservation | Implemented against existing Credit HTTP contract; no balance calculations or sibling database access. Actual Credit behavior is not established by Order's HTTP fakes. |
| 3.2/3.2.1: open list and information | Unpaginated other-user open/unexpired list, stable sorts, complete details, Singapore display, two-second polling and stale warning implemented. Strict five-second bound remains unproven under dependency latency/load. |
| 3.3–3.3.4: acceptance and capacity | Row locks, unique courier-slot index, self-acceptance rejection, durable successful receipts, one winner and delivery slot release implemented. Receipt-before-lock deadline case remains incomplete. |
| 3.4.1: courier-only pickup/delivery | Implemented including skipped-action rejection and preservation of old assignment history. Receipt-order cancellation/expiry qualification below remains. |
| 3.5/3.5.1: requester confirmation and automatic completion | Implemented with pending 202 confirmation, original delivery timestamp, five-minute deadline plus one-second arbitration and worker scheduling. This is not exact completion at five minutes or general simultaneous-dispute arbitration. |
| 3.5.2: disputes and admin resolution | Implemented: requester dispute, admin-only queue/resolution, one committed admin outcome, no settlement in dispute. Final completion cannot reopen. |
| 3.6: requester cancellation | Open/accepted cancellation and durable full release implemented. Receipt-order priority before authentication completes remains partial. |
| 3.7/3.7.1: expiry | Worker covers open/accepted/picked-up, preserves terminal/delivered/dispute states and original deadlines after restart. Pre-deadline delivery protection currently starts at row-lock acquisition. |
| Q10a–c: retries and partial creation failures | Durable identity, receipts and reserve/release recovery implemented and tested. No queued creation or optimistic success. |
| Q10d: competing actions | Transaction/slot/financial atomicity implemented; ingress ordering and arbitrary overlap priority are not fully implemented (details below). |
| Q10e and Credit settlement | Durable jobs and restart recovery implemented. This audit separates retry queues so blocked refunds/creation cleanup cannot consume all payout retry time. |
| Asynchronous UI decisions | Pending state, retained last-confirmed state, same-action retry, usable navigation and version-aware merging implemented; existing Chromium coverage retained. |
| Proposed events | Atomic journal and authorized per-order HTTP replay implemented. No publisher, broker, consumer monitoring or SSE; proposed work, not an approved external integration. |

## Completed during this audit

1. **Unselected history entries now reconcile Supplier changes.** Previously
   `listOrders` returned saved data for requester, courier and admin pages;
   only open lists and selected details refreshed. Every displayed row now
   resolves current places, sharing lookups within that request only. A known
   missing/inactive place cancels eligible states and queues one refund. Renames
   update snapshots, protected states remain protected, and outages return 503.
   A pickup that won while the snapshot waited retains the existing priority.
   Query connections close before starting reconciliation transactions.
2. **All order lifecycle timestamps serialize in Singapore time.** Creation and
   expiry already did; `deliveredAt` and `completionRequestedAt` now do too.
3. **Financial retry queues make progress independently.** Previously
   `Recover` processed creation cleanup, then refunds, then payouts under one
   ten-second deadline. Repeated slow refunds could exhaust every pass before
   payouts began. The three queues now run concurrently under that bounded
   deadline, retain durable failed jobs, and aggregate errors.

`internal/order/audit_test.go` covers whole-page rename/inactivity/outage behavior,
per-request lookup reuse/current cookies, protected states, repeat refund intent,
Singapore lifecycle timestamps, and payout progress while refund I/O blocks.

## Unfinished work and reasons

| Gap | Why it remains / what is needed |
| --- | --- |
| **Unattended Supplier changes, including off-page accepted/picked-up errands** (3.1.1) | Supplier exposes session-authenticated public reads only (`supplier-service/internal/supplier/http.go`). There is no service-authenticated read/change feed. Order cannot persist browser sessions or inspect Supplier tables. Requires a Supplier-owned authenticated contract and agreement on replay/deletion semantics; sibling implementation changes are prohibited by this service's guidance. |
| **Receipt-order arbitration before row-lock acquisition** (3.3, 3.4.1, 3.7, Q10d) | `middleware` records receipt time, but `protected` calls User before the handler obtains the order lock. During slow authentication/pool contention, expiry can commit before an earlier valid delivery/acceptance obtains its lock. Similarly, a later pickup may beat an earlier cancellation still authenticating. Existing tests delay catalogue calls *after* locking and do not establish the broader claim. A durable admission/arbitration mechanism shared by handlers and workers is still needed, with authorization, crash cleanup and multi-instance tests. This is unfinished Order work, not an external impossibility; simply increasing a timeout or reopening final states would violate other requirements, so neither was used as a workaround. |
| **All overlapping disputes beat completion** (3.5, Q10d) | Addressed at the durable admission boundary by the 2026-10-10 follow-up above: admission remains open during speculative completion writes, and the final gate rolls everything back if a live admission exists. Tests cover blocked writes and slow authentication for manual/automatic completion across instances. Requests admitted after the final decision point cannot reopen completion. |
| **Five-second update guarantee** (3.2) | The 2026-10-10 follow-up adds bounded parallel lookups, two-second server budgets, independent polling and response-age fencing, with simulated-latency and browser tests. Successful delivery remains conditional on responsive dependencies, network and foreground scheduling. Unconditional delivery through outages, unlimited load or browser suspension cannot be implemented; missed budgets produce explicit stale state. |
| **External event publication and monitoring** | The backlog explicitly calls this proposed. No broker, destination, consumer authorization or operational ownership is selected. Existing `order_events` is a durable journal, not proof that an event was published. Requires an approved transport/consumer contract before implementing publication retries, acknowledgements, backlog monitoring and crash/replay tests. |
| **Root CI/gateway integration and live end-to-end verification** | Root Compose/CI do not include Order and were not edited. Order guidance requires explicit shared-wiring scope. The first-admin bootstrap status is also unknown, so root smoke fixtures were not run. Service-local isolated tests do not establish live User/Supplier/Credit compatibility or deployment readiness. |

No business carrying/credit maximum was invented. Signed-64-bit transport and
the 1 MiB body ceiling remain explicit limits. Optional place filters are absent
but are optional in the backlog. Withdrawal, reassignment, reminders and penalties
remain outside scope.

## Verification for this audit

Final backend verification **passed**: gofmt enforcement, `go vet ./...`,
`go test -race -count=1 ./...` (Order package 25.683 seconds), and server build.
PostgreSQL integration tests used the dedicated database; the browser fixture
remains intentionally opt-in in the normal Go suite. `git diff --check` passed.
Chromium: all 10 tests passed (57.0 seconds), and all
four JavaScript helper tests passed. Those browser tests exercised the list and
timestamp fixes; the financial worker change is covered by the backend suite.

Commands run from the repository root:

```sh
docker compose -f order-service/compose.yaml --profile test run --build --rm order-tests
docker compose -f order-service/compose.yaml --profile ui-test run --build --rm order-ui-tests
# Final backend rerun reuses the running test database without recreating it:
docker compose -f order-service/compose.yaml --profile test run --build --rm --no-deps order-tests
```

A preliminary backend run was interrupted when a later Compose invocation
recreated its disposable database. That run is not counted as validation.
No live sibling services/accounts were used. No migrations, Compose files,
Dockerfiles, environment scripts or shared wiring were changed by this audit.

---

# Historical: Backlog 3.4 and authorized lifecycle workflows — 2026-10-09

Implemented 3.4/3.4.1 plus the later workflows explicitly requested to exercise
all nine states: requester cancellation, pending confirmation, automatic
completion, disputes, admin resolution, pre-delivery expiry and durable payout.
All changes are confined to Order Service. Earlier assessments below are retained
as historical records; their missing-lifecycle statements are superseded here.

## Implemented and verified

- A shared forward-transition guard rejects skipped/backward transitions and
  reassignment. Completed, cancelled, expired and not completed are terminal.
  Dispute leaves only through an authorized admin resolution command.
- Each public command validates the current session and checks actor permissions.
  Courier pickup/delivery require the assigned courier and reject admins and
  requesters. Requesters cancel only before pickup and confirm/dispute only after
  delivery. Admins have a dispute queue and no general override.
- Successful actions have durable receipts in new migration 005. Repeating a
  pickup after subsequent cancellation/expiry returns the confirmed current
  outcome without mutation. Failed actions leave no successful receipt.
- Pickup wins against deactivation discovered by its own lookup; a refresh that
  observed accepted before waiting for pickup does not undo the winning pickup.
  Earlier final cancellation remains final. Later independent deactivation can
  still cancel picked-up errands.
- Valid pre-deadline delivery holds the order lock through commit; expiry workers
  skip it. At/after-deadline delivery cannot revive an expired order. Delivery
  retains assignment while releasing only that order's workload slot.
- Confirmation responds 202 with delivered status and a persisted intent.
  Completion and the five-minute automatic deadline have a one-second arbitration
  interval, allowing overlapping disputes to win before payout is queued. Final
  completion is never reopened by a later dispute. Dispute removes pending
  confirmation and excludes the order from expiry/automatic completion.
- Lifecycle advancement runs separately from Credit retries. Completion commits
  a payout job; cancellation, expiry and not-completed commit release jobs.
  Both use Credit's existing idempotent endpoints and survive lost responses and
  restart. Competing admin resolutions commit one outcome and one financial job.

## Verification performed after implementation

A separate adversarial suite, `internal/order/lifecycle_verification_test.go`,
checks observable command and Credit outcomes rather than reusing the transition
matrix as its oracle. It verifies lost payout response followed by restart,
concurrent dispute/confirmation, old-order settlement while a newer courier slot
is occupied, final expiry resisting replay, failed delivery retaining capacity,
and an expiry worker unable to overtake an in-flight pre-deadline delivery.
This is automated verification, not a claim of review by a separate person.

The full backend run passed gofmt enforcement, `go vet ./...`,
`go test -race -count=1 ./...` (23.021 seconds for the package), and the server
build. Additional lifecycle tests cover all nine states, actor rejection across
states/actions, all graph edges, before/at/after deadline behavior, timer/dispute
priority, competing admin outcomes, and successful pickup replay after restart.

All four JavaScript helper tests and ten Chromium tests passed (32.8 seconds).
Browser coverage includes requester cancellation, pending confirmation becoming
completed, requester disputes and the admin resolution controls, alongside the
existing creation, open-list, stale-view and lost-response regressions.
`git diff --check` passed. No sibling service or shared wiring was edited or
started; these checks use Order's dedicated PostgreSQL database and HTTP fakes.
Live sibling integration was not exercised. Unattended Supplier-change detection
still requires an external authenticated change/read contract.

---

# Backlog 3.2 and 3.3 validation — 2026-10-09

Implemented within Order Service only. The assessments below this section are
historical 3.1 notes; their selected-detail-only polling and missing open/accepted
expiry statements are superseded by this slice.

- Open snapshots contain every eligible other-user errand, with no pagination,
  deterministic expiry/creation/ID sorting and an alternative highest-credit sort.
  Every listed place is refreshed using the current session; repeated place
  lookups are shared only within that snapshot. Supplier changes update details
  or cancel/remove entries and queue refunds; outages fail closed.
- The browser polls every two seconds while visible, keeps times in Singapore,
  distinguishes campus places from suppliers, and shows “List is not updated”
  after offline/failure/stalled synchronization. A detail refresh cannot clear
  that warning; a successful list snapshot is required. Request generations and
  order versions prevent delayed snapshots undoing confirmed actions.
- Acceptance uses the authenticated actor, row locks and the existing unique
  courier-slot index. Pre-deadline acceptance saved late records accepted then
  expired atomically, retaining assignment and queuing one recoverable refund.
  Open/accepted expiry is recovered after restart; accepting another errand
  also clears overdue older assignments. Delivery releases capacity only on
  commit, and old retries preserve the newer assignment.

Validation passed: gofmt, `go vet ./...`, `go test -race -count=1 ./...`
(test package 9.558 seconds), and server build. Added PostgreSQL tests cover
25-row snapshots, sort ties, supplier rename/deactivation/outage, eight competing
couriers, receipt immediately before/at/after expiry, refund recovery, expired
capacity, and delivery racing with a new acceptance. All four JavaScript helper
tests and eight Chromium tests passed (36.0 seconds), including the 22-entry
open list, live acceptance removal, and failed-list/reconnect warning behavior.
`git diff --check` passed. No sibling service or shared wiring was edited; tests
used only Order's dedicated database and HTTP fakes. Live sibling integrations
were not exercised. Confirmation, disputes, payout and picked-up expiry remain
subsequent lifecycle work; unattended Supplier detection still needs its external
contract, as described in the historical notes.

---

# Backlog 3.1 validation — updated 2026-10-09

## Current implementation assessment

**Verdict: substantially extended, but whole 3.1 is still not fully met.** The
creation UI, read APIs, user-driven Supplier reconciliation and delivered-slot
decision are now implemented. Unattended Supplier-change detection remains an
external contract gap, not a completed feature. The original audit below is
retained as historical evidence; this section supersedes its missing-UI/read
findings.

| Area | Current result | Evidence |
| --- | --- | --- |
| 3.1 editor and retained input | Implemented | Embedded `/orders` UI; responsive selectors, items, credits and SGT expiry; per-user tab-scoped drafts. |
| 3.1 async creation / lost success / double click | Implemented | Chromium tests hold a request pending while navigating, verify only one submission, drop a saved response and reload/retry the same key/body. |
| 3.1.1 exact courier place details | Implemented | Authenticated detail/list APIs and pre-acceptance screen show location, description and opening hours. |
| 3.1.1 active selectors and current validation | Implemented | Paginated Supplier adapter; each create/transition resolves current records using the current session. |
| 3.1.1 rename/relocation and cancellation | Partial overall | Explicit/selected-detail refresh updates snapshots; known absence/inactivity cancels open/accepted/picked-up only. No unattended change source exists. |
| 3.1.1 delivered/dispute/terminal protection | Implemented in reconciliation | Database/HTTP tests cover all nine statuses; original courier remains attached; no cancellation/refund for protected statuses. |
| 3.1.2 duplicate editing and quantities | Implemented | Browser tests exercise Proceed/Do not proceed, retained editor text, text-only rendering and quantities above JavaScript's safe integer range. |
| 3.1.3 Credit reservation/recovery | Existing coverage retained | Original financial tests plus atomic outbox failure/refund, cancellation refund retry and repeated-release tests. Signed-64-bit transport limit remains explicit. |
| Courier slot release at delivered | Implemented | Partial unique index counts accepted/picked-up; concurrent assignments, old-action replay after a new assignment, no Credit movement at delivery. |
| Async status updates | Implemented for available actions | Courier browser test checks pending/old status with usable navigation. Version merging rejects stale results; reconnect reads authoritative snapshots. |
| Events for posting | Durable foundation, not broker delivery | Creation/status/detail events commit atomically, survive application reconstruction, and replay through an authorized per-order HTTP feed. No publisher/broker/SSE or queued creation is claimed. |

## Review and coupling decisions in this continuation

- All edits stay within `order-service/`. User, Supplier, Credit and the shared
  frontend are unchanged; existing service screens were read only as a visual
  reference. The Order UI is embedded in its binary, with no runtime Node or
  sibling frontend imports.
- Supplier access remains a consumer-owned HTTP adapter using the current
  cookie only. No database sharing, saved session, auth cache or invented
  privileged Supplier endpoint is used. UI polling refreshes only the selected
  order; unviewed orders are **not** claimed synchronized.
- New numbered migrations preserve applied migrations 001/002. Database locks,
  constraints and the partial unique index enforce transition/capacity safety.
  Status, version, event and cancellation refund intent commit together.
- Credit cleanup remains HTTP-only and idempotent. Refund retries are durable;
  failed attempts rotate so one failing escrow does not monopolize worker passes.
- UI logic separates pure serialization/version helpers from browser state.
  Server-controlled text uses text nodes; strict CSP disallows inline scripts,
  framing and foreign network connections. BigInt-aware serialization avoids
  rounding valid Credit/quantity integers.
- The environment initializer now reads User's actual root `.env`. An opt-in
  service-local network overlay connects Order to existing dependencies without
  editing shared wiring. The public UI Origin defaults to its actual port 8084.
- The narrow accept/pick-up/deliver path exists to implement the updated slot
  decision. Full expiry timers, requester completion, disputes and payout are
  later backlog work, not completed by this continuation. Do not treat this as
  the complete production lifecycle.

## Current verification

Final result: **PASS** for gofmt enforcement, `go vet ./...`, 47 top-level
backend tests under `go test -race -count=1 ./...` (37.371 seconds for the test
package), and server build. The opt-in browser fixture is the sole skipped test
in the normal suite; PostgreSQL tests ran against the dedicated database.

**PASS** for all four UI helper tests and seven Chromium tests (20.4 seconds),
including lost creation and delivery responses across reload, action-level
pending state, double-submit prevention, duplicate decisions, account-scoped
drafts and precise large integers. Desktop/mobile screenshots were inspected;
there was no horizontal overflow at 390px. The runtime Docker image also built
successfully. `git diff --check` and shell syntax validation passed. Protected
sibling/shared directories have no changes.

Run isolated checks from `order-service/`:

```sh
docker compose -f compose.yaml --profile test run --build --rm order-tests
docker compose -f compose.yaml --profile ui-test run --build --rm order-ui-tests
docker compose -f compose.yaml --profile test --profile ui-test down
```

The browser profile exercises the actual embedded UI and Go endpoints against
the dedicated test database and fake dependencies. It never creates live User
accounts. The user explicitly requested isolated tests only because the local
first-admin bootstrap status is unknown; the root gateway smoke test and
real-service network-overlay integration were therefore **not run**.

## Remaining decisions

1. Agree on a service-authenticated Supplier change feed/read endpoint before
   promising automatic cancellation/refresh with no connected users. That needs
   coordination outside the permitted Order-only code scope.
2. Select a publication contract/consumer before adding a broker worker or SSE.
   Current durable HTTP event replay and snapshot polling do not implement the
   proposed external publication/retry monitoring requirements.

---

## Historical audit — 2026-10-04 (before this continuation)

**Verdict: partially met, not complete.** The backend creation path is implemented
and has regression coverage. The previous completion summary overstated the
scope: no creation/editor UI, courier detail read, or ongoing Supplier-change
handling exists. A passing backend suite cannot establish those requirements.

## Acceptance audit

| Backlog criterion | Result | Evidence / remaining work |
| --- | --- | --- |
| 3.1 authenticated user; required pickup, delivery, items, offer, expiry | Backend covered | Session validation per request; required-field, unknown-field, numeric and expiry tests in `validation_test.go`. |
| 3.1 one pickup field and separate delivery field | Backend covered | Typed place references; missing/null references fail. |
| 3.1 successful creation is open; strictly future Singapore expiry | Backend covered | Response timestamps use UTC+08:00; migration 002 uses actual insertion time; expiry-during-reservation regression verifies refund. |
| 3.1 no requester errand-count cap | Backend covered | Ten creations consuming the available balance; the next fails for insufficient credits. |
| 3.1 repeated submission / double click / lost client response | Backend covered | Same-key serialization, payload conflicts, equivalent timestamp offsets, concurrent double-click test and saved-result retrieval after expiry. |
| 3.1 dependency failures have a clear message and do not show success | API covered; UI missing | 503/error codes fail closed; no page exists to verify page retention or absence of a success banner. |
| 3.1.1 both place types accepted; compare type and record ID | Backend covered | Supplier/campus record at the same physical place remains distinct. Supplier UUIDs normalize case before comparison. |
| 3.1.1 missing, inactive, deleted or unknown records fail | Backend covered | Supplier 404/inactive and campus unknown handling; malformed/503 responses remain dependency errors, not deletion. |
| 3.1.1 courier sees exact Supplier location before acceptance | Partial | Creation response contains Supplier description, opening hours, campus ID/name; no courier-facing read route or screen exists. |
| 3.1.1 deactivate/delete cancels open, accepted and picked-up orders | Missing | No Supplier-change consumer or cancellation transition exists. Schema currently permits only open orders. |
| 3.1.1 delivered/dispute/terminal orders are unaffected by changes | Missing | Those lifecycle states and corresponding Supplier-change protections are not implemented. |
| 3.1.1 rename/relocation updates existing details | Missing | Place JSON is a creation-time snapshot, never refreshed. |
| 3.1.2 at least one item; nonblank name; positive integer quantity | Backend covered | Missing/zero/negative/fractional/unrepresentable input tests. |
| 3.1.2 no five-item cap or business text limit; descriptions optional | Backend covered | Six large duplicate items with long multiline text and maximum representable quantity round-trip unchanged. Request body is bounded to 1 MiB, returning 413 rather than a carrying-limit message. |
| 3.1.2 quantity upper bound | Infrastructure limit | Current quantity representation is signed 64-bit and rejects overflow. This is an implementation limit, not an agreed business maximum; changing to arbitrary precision would be a separate compatibility change. |
| 3.1.2 duplicate comparison ignores whitespace/case, quantity/description | Backend covered | Unicode whitespace and simple case folding; tests include final sigma and long s, different descriptions and quantities. |
| 3.1.2 Add Item warns; Proceed appends; decline preserves editor | API covered; UI missing | Item-add API reports a pending decision and preserves/appends the list correctly. There are no actual buttons, warning dialog, or editing page to validate. |
| 3.1.3 offer positive integer; no item-cost split or business maximum | Backend covered | Signed-64-bit maximum accepted; overflow, fractions, strings, zero/negative values rejected. |
| 3.1.3 exact balance suffices; reserved funds unavailable; concurrency safe | Contract covered | Independent Credit HTTP fake serializes its escrow/balance changes; tests verify exact balance and two competing offers. This tests Order's use of the contract, not Credit's implementation. |
| 3.1.3 reserve before saving; Credit owns ledger | Backend covered | Only HTTP reserve/release calls; no sibling imports, database reads, ledger calculations, or balance lookups in production Order code. |
| Q10a saved success versus Q10b uncertain reservation | Backend covered | Saved orders return on retry; uncertain reservation attempts can only be refunded/abandoned, never completed later. |
| Q10b failed persistence / late reserve / lost release response | Backend covered | Durable intent, exact reserve payload, shared attempt lock, idempotent reconciliation, failure-trigger test, and restart tests. |
| Q10c pending/missing wallet; dependency unavailable | Backend covered | Explicit retry errors; a definitive wallet refusal is retryable without reserving. No background creation is queued. |

## Review changes

- Fixed refund/creation races: creation and recovery now share a session advisory
  lock. Intent commits before HTTP reservation; final order and completion marker
  commit atomically. A failed cleanup leaves recoverable state.
- Fixed orphan-reservation risk: a missing escrow after a timeout is not treated
  as a successful refund. Reconcile the same reservation before release, so a
  late original reserve sees Credit's idempotency record.
- Fixed uncertainty behavior: a reservation whose response was lost can no longer
  be turned into a successful order on retry. Saved orders are never refunded.
- Fixed expiry timing: `clock_timestamp()` replaces transaction-start `now()`
  for insertion time through a new migration. Applied migration 001 is unchanged.
- Normalized supplier UUID casing and timestamp offsets for identity/idempotency.
- Added Unicode simple case folding; preserved multiline text and numeric values.
- Validated dependency success/error responses; malformed responses fail closed.
  Error status and error code must agree before a refusal is treated as definitive.
- Removed raw internal error logging and tightened URL configuration validation,
  including rejecting embedded credentials, query strings and fragments.
- Removed the misleading Location header that pointed to an unimplemented GET.
- Added consumer-owned dependency interfaces; HTTP adapters remain the only
  components aware of sibling wire contracts. PostgreSQL is intentionally
  explicit because its locking/transaction guarantees are part of correctness.
- Recovery uses one database connection per attempt, bounded batches and
  nonblocking locks. The one-connection-pool test checks for deadlock and leaked
  advisory locks. The check script refuses to silently skip database tests.

## Remaining integration boundary

Supplier exposes only authenticated public catalogue reads, requiring a live
user session. It has no service-authenticated catalogue API or change feed.
Reliable background cancellation/refresh cannot be inferred from a failed
lookup, or implemented by reading Supplier tables. It needs an agreed
authenticated change/read integration and Order lifecycle work. Storing a
requester's session for perpetual background access would introduce credential
coupling and still fail after logout/expiry, so this review does not do that.

The UI needs frontend work for selectors, item editing, duplicate decisions,
Singapore-time input/display and retry/page-retention behavior. No sibling code
or frontend/shared wiring was changed by this review.

## Verification

Final result: **PASS** for gofmt enforcement, `go vet ./...`, all 37 top-level
tests (including PostgreSQL/HTTP regressions) under `go test -race -count=1 ./...`,
and the server build. The final test package completed in 5.189 seconds.
`git diff --check` also passed. User, Supplier and Credit directories had no
changes. These results validate the implemented backend scope, not the missing
acceptance criteria above.

Run from `order-service/`:

```sh
docker compose -f compose.yaml --profile test run --build --rm order-tests
```

This runs gofmt enforcement, vet, race-enabled tests against disposable
PostgreSQL 18 and independent HTTP fakes, then builds the server with the pinned
Go 1.27.1 image. The original tests remain, augmented by validation and financial
failure/recovery regressions. Live sibling-service and browser behavior are not
proven by this suite; the missing criteria above remain open.
