# Order Service backlog

All entries have priority **High** and sprint **S1**. This document incorporates
the supplied FR3 backlog and the subsequent Q1–Q10, C1–C8, R1–R5 and final
financial clarifications. It specifies intended behavior, not implemented
functionality. The original clarification questions are resolved. The 2026-10-09
decisions below supersede the earlier courier-slot and UI interaction rules;
the event approach is a recommendation, not an approved broker implementation.

Numbering changes: the duplicate open-list requirement is now **3.3.4**.
The original **3.1.3 (carrying limit) is removed**; the original 3.1.4 (credits)
is now **3.1.3**. There is no carrying-limit requirement or five-item cap.
New dispute requirements are documented under 3.5.2.

## Implementation audit — 2026-10-09

The implementation now includes creation, the embedded UI, list/acceptance
flows, all nine lifecycle states and durable Credit recovery. Requirements below
remain the acceptance target; they are not a declaration that every criterion
passes. See the current audit at the top of [VALIDATION.md](VALIDATION.md) for
evidence and limitations.

- Completed in this audit: reconcile Supplier details for every displayed
  requester/courier/admin-list entry, including unselected entries; fail closed
  on catalogue outages; preserve terminal states and queue cancellation refunds.
  Delivery and confirmation timestamps now also serialize in Singapore time.
  Independent creation-cleanup/refund/payout queues prevent slow refunds from
  exhausting the worker deadline before payout retries begin.
- Still partial: unattended Supplier synchronization (3.1.1) and a guaranteed
  five-second synchronization bound (3.2).
- Overlap/freshness follow-up (2026-10-10): completion now admits competing
  requests throughout speculative writes and vetoes status/event/payout together
  at its final decision gate. Refreshes use bounded parallel catalogue reads,
  two-second server budgets, independent polling and request-start freshness.
  Tests exercise delayed completion writes and delayed snapshots. This implements
  the five-second target under the documented operating conditions; unconditional
  delivery through outages or suspended browsers remains impossible. Neither the
  original acceptance rules nor terminal-state protection has been weakened.
- Receipt-order follow-up (2026-10-10): durable pre-authentication admission now
  protects actions waiting for authentication, transaction connections and row
  locks. Later same-order actions wait for earlier live admissions; workers defer
  expiry/completion and Supplier cancellation. Expired admissions fence stale
  handlers without replaying commands. See API.md for the database admission
  receipt boundary and VALIDATION.md for adversarial verification.
- Creation event publication is implemented: a durable feed and authenticated
  SSE notify other users, with replay and admin backlog monitoring. An external
  broker and publication of status/detail events remain separate follow-up work.
- Not verified here: live sibling/gateway integration and root CI execution of
  Order. Shared wiring is outside the service-local change scope.

The detailed reasons and follow-up requirements are recorded in VALIDATION.md;
none of these gaps changes the acceptance rules below.

## Implementation decisions — 2026-10-09

### Asynchronous UI updates

- Requester and courier screens remain responsive while creation or a status
  change is being processed. Send the request asynchronously and show an
  "Updating…" indicator on the affected errand/action.
- Disable duplicate or conflicting actions for that errand while its result is
  unresolved; unrelated navigation and interactions remain usable. Do not block
  the entire screen or discard form input.
- Keep the last confirmed status visible, alongside the pending indicator.
  Only show the new status or a success message after Order confirms the
  committed result. A local click or queued command is not a successful change.
- On rejection, show the reason and restore applicable actions. On a timeout or
  lost response, indicate an uncertain result and reconcile using the same
  action/creation identity; do not assume failure or submit a fresh duplicate.
- Refresh requester and courier views asynchronously from authoritative Order
  results. Follow the live-update and reconnection rules in 3.2; delayed updates
  must not overwrite a newer confirmed state.
- Asynchronous browser requests do not require background command processing
  or a message broker. Existing authorization, transition priorities and
  financial recovery requirements still apply.

**Validation:** With a deliberately slow dependency, both screens remain
interactive, only the affected actions show pending/disabled state, and no
premature success appears. Cover failed and lost responses, double clicks,
navigation back to an unresolved action, and delayed/out-of-order updates.

### Release courier capacity at delivered

- The one-request limit covers only accepted and picked-up errands.
- Pressing Mark Delivered starts an asynchronous transition. Order releases the
  slot atomically when it successfully records picked up → delivered, not merely
  when the browser button is clicked. A rejected or unresolved transition must
  not optimistically free the slot.
- After that commit, the courier may accept another errand immediately, without
  waiting for requester confirmation, automatic completion, dispute resolution
  or payout. Multiple delivered errands may await settlement.
- Delivery retains the original courier assignment for confirmation, disputes
  and payout. Releasing capacity does not release escrow or pay the courier.
- Later actions or retries on the old errand must never release, reacquire or
  otherwise change a workload slot held by a newer errand.

### Events for posting order requests — proposed approach

**Implementation follow-up:** Creation-event delivery is now authorized and
implemented using a PostgreSQL publication feed and browser SSE, without an
external broker. Other signed-in users receive `OrderCreated` announcements for
open/unexpired errands, and their UI refreshes from an authoritative snapshot.
Publication retries, restart recovery, serialized cursors, session revalidation,
reconnection and admin backlog monitoring are covered by the contract in API.md.
The broker/consumer recommendations below remain relevant to a future external
integration; they do not require adding a broker to the browser delivery path.

Yes: events can announce newly created requests to other consumers and update
connected lists. The recommended flow is:

1. The browser asynchronously submits an authenticated, idempotent creation
   command to Order over HTTP.
2. Order validates the details, reserves credits through Credit's existing HTTP
   API, and commits the open errand together with an outbox record in its own
   database. An outbox is a durable record of an event still to be published.
3. After commit, a worker publishes an `OrderCreated` event. Publication failures
   retry independently; they do not undo a saved errand or reserve credits again.
4. Consumers process events idempotently and update views. A browser-facing
   stream, such as Server-Sent Events, can deliver authorized updates without
   exposing the internal broker. Reconnection retrieves an authoritative
   snapshot before removing the stale-list warning.

**Proposed reliability requirements**

- Events describe committed facts. Do not publish `OrderCreated` for an
  unresolved, failed or abandoned creation, and never let an event bypass
  authentication, place validation or credit reservation.
- Persist the order and outbox record in the same local transaction. Consumers
  tolerate at-least-once delivery: event IDs identify duplicates; an order
  version identifies stale/out-of-order updates. Replay must not duplicate
  orders, assignments or financial effects.
- Event contracts include an event ID, type, schema version, order ID, order
  version and occurrence time. Publish only the data each authorized consumer
  needs; never include session cookies or service credentials.
- Use the same pattern for committed status changes. Broker outages must not
  freeze the UI; preserve the existing stale-view indication and synchronization
  behavior. Monitor pending publication and recover it after restart.
- Test crashes between database commit and publish, publication retries,
  duplicate/out-of-order delivery, and reconnect synchronization.

Posting a creation command directly to a queue is also technically possible,
but would be a separate design decision. It requires durable operation tracking,
pending/success/failure results, an agreed authorization policy at processing
time, and revised Q10b/Q10c semantics: the current rules explicitly prohibit
queueing failed creation to finish later. Non-blocking screens alone do not
justify that change. No broker technology or direct queued-creation workflow is
selected by this backlog update.

## FR3 — Manage the errand lifecycle

**Acceptance criteria**

- States apply to the whole request: `open`, `accepted`, `picked up`,
  `delivered`, `dispute`, `completed`, `cancelled`, `expired`, and `not completed`.
- Successful creation produces an open errand.
- The normal sequence is open → accepted → picked up → delivered → completed.
  Pickup must precede delivery. States cannot be reversed.
- Completed, cancelled, expired and not completed are terminal. Cancelled remains
  cancelled after its original deadline.
- Dispute is nonterminal and can transition only to completed or not completed,
  by an admin. The courier workload slot was already released at delivered;
  entering dispute does not change capacity or settle escrow.
- State changes are limited to the actors and conditions below.

| From | To | Actor / condition |
| --- | --- | --- |
| open | accepted | Order Service after successful courier assignment |
| accepted | picked up | Assigned courier only |
| picked up | delivered | Assigned courier only; atomically releases this errand's courier slot |
| delivered | completed | Requester confirms delivery; a simultaneous dispute takes precedence |
| delivered | completed | Order Service after 5 minutes without confirmation, unless disputed |
| delivered | dispute | Requester raises a dispute; cancels timer; courier slot already released |
| dispute | completed | Admin determines the errand was completed |
| dispute | not completed | Admin determines the errand was not completed |
| open or accepted | cancelled | Requester |
| open, accepted or picked up | cancelled | Order Service when either selected place is deactivated/deleted |
| open, accepted or picked up | expired | Order Service at expiry, unless a valid Mark Delivered request reached Order before expiry |

**Edge cases:** Request-level statuses cannot record separate item completion.
No general admin override is authorized outside dispute. Earlier cancellation or
expiry remains final when it reaches Order before pickup or delivery respectively;
see Q10d for the accepted priorities. Only completed pays the courier; cancelled,
expired and not completed return escrow to the requester.

## 3.1 — Create an errand

**Acceptance criteria**

- A valid user supplies pickup location, delivery location, at least one item,
  offered credits and expiry time.
- Pickup supplier/location is one field. Delivery is a separate single field.
- Creation succeeds only if 3.1.1–3.1.3 pass and expiry is strictly later than
  creation time in Singapore time.
- Invalid or missing required details prevent creation.
- Requesters have no errand-count limit while available credits suffice.
- Repeated submissions for the same creation attempt return its one existing
  result, not multiple errands.
- Dependency failure shows a clear failure/retry message. The page stays in
  place and does not display success while creation is unresolved.

**Edge cases:** Test double clicks, a lost successful response, missing/pending
wallets and reservation followed by a failed Order write. The recovery rules below
distinguish an already-created errand from an attempt that must be abandoned.

## 3.1.1 — Active, distinct pickup and delivery places

**Acceptance criteria**

- Each selector accepts either a campus location or a supplier from Supplier
  Service. Campus locations are always active; suppliers must be active.
- Exactly one pickup and one delivery are required; compare selected records,
  including their entity type and ID, not physical campus location.
- Different suppliers at the same campus location, or a supplier and its campus
  location record, are distinct valid selections.
- Missing, unknown, inactive or deleted selections fail creation.
- Couriers see the exact Supplier location information before acceptance.
- Deactivation or deletion of either selected place cancels open, accepted and
  picked-up errands through Order Service.
- Deactivation/deletion does not cancel delivered or dispute errands, or alter
  any of the four terminal states.
- Renaming or relocating a place updates existing errand details without
  cancelling the errand.

**Edge cases:** Later relocation keeps an errand valid because the selected
records remain different. Distinguish deletion from a failed Supplier lookup.
Pickup precedence also covers competing system cancellation from supplier
changes; earlier cancellation remains final (Q10d).

## 3.1.2 — Items and duplicate warnings

**Acceptance criteria**

- At least one item is required. There is no maximum number of item entries.
- Every item has a compulsory name and a positive integer quantity.
- Names consisting only of whitespace are rejected.
- Description is optional longer text. No name/description length limit or
  quantity upper bound is specified as a business rule.
- Missing quantity, zero, negative values and fractions are rejected.
- On Add Item, compare the new name against existing item names after removing
  whitespace and comparing case-insensitively. Description and quantity do not
  affect duplicate detection.
- If a duplicate is detected, warn and offer Proceed / Do not proceed.
- Proceed adds the item to the list, including duplicates.
- Do not proceed leaves the item unadded and returns to the same editing view.
  This warning happens on adding an item, not only on final errand submission.

**Edge cases:** One and more than five items are valid; zero is invalid.
Names such as “Milk Tea” and “ milktea ” match for duplicate detection.
A different description or quantity does not avoid the warning.
Do not silently truncate text or overflow quantities. Infrastructure limits
must not be misrepresented as an agreed business carrying limit.

## 3.1.3 — Valid offer and sufficient available credits

**Acceptance criteria**

- Offered credits are a positive integer without a business maximum.
- Order does not distinguish item cost from courier compensation.
- Available credits equal to the offer suffice if all other creation rules pass;
  less than the offer prevents creation.
- Reserved credits do not count as available for another errand.
- Concurrent creations cannot consume the same available credits.
- Order uses Credit's reservation endpoint before saving the errand; a balance
  lookup alone is insufficient.
- Credit owns all balance calculations and ledger changes; its implementation
  must remain unchanged.

**Edge cases:** Credit uses signed 64-bit integers; compatibility still requires
rejecting unrepresentable amounts instead of overflowing or truncating them.
Test pending wallets, concurrent reservations, lost responses and failed
creation cleanup as specified in the recovery section.

## 3.2 — View open requests

**Acceptance criteria**

- Couriers and requesters are valid system users, not separate account roles.
- The courier list contains only other users' open, unexpired errands.
- There is no pagination. Updates appear live without manual refresh.
- Accepted, cancelled and expired requests leave the open list.
- A 2–5 second update delay is acceptable; updates should arrive within 5 seconds
  while connected. Faster delivery is allowed.
- If the live connection is lost, display “List is not updated” at the top.
  Remove that warning only after the displayed list has been synchronized.
- Show an empty result when no eligible errands match.
- Authorized usability choices: optional pickup-place and delivery-place filters,
  with all eligible places shown by default.
- Default order is soonest expiry first, then creation time ascending, then order
  ID. Offer highest-credits-first as an alternative, with expiry and the same
  tie-breakers.

**Edge cases:** A stale entry never bypasses server-side acceptance checks.
Use the same accepted 2–5 second delay for detecting Supplier changes.
The delay in displaying updates is not an extension of the acceptance deadline.

## 3.2.1 — Information for evaluating an errand

**Acceptance criteria**

- Show exact pickup/delivery locations, item names and quantities, descriptions
  when present, offered credits and expiry.
- All displayed and interpreted times use Singapore time
  (`Asia/Singapore`, UTC+08:00); do not substitute the viewer's timezone.
- Supplier rename/relocation updates the corresponding existing errand details.

**Edge cases:** A location change must not itself cancel the errand. Campus and
supplier records remain distinguishable even when they describe the same place.

## 3.3 — Accept an open, unexpired errand

**Acceptance criteria**

- A valid user can accept another user's open, unexpired errand if their courier
  workload slot is free.
- Assignment causes Order Service to change open → accepted.
- Reject acceptance received at or after expiry or from a non-open state.
- If a valid acceptance reaches Order before expiry but is recorded afterward,
  record acceptance then immediately expire the errand.
- A courier can have only one errand across accepted and picked up.
- The slot is released atomically when Order records delivered. The courier can
  then accept another errand while the previous one awaits confirmation or payout.
- Delivered, dispute, completed, cancelled, expired and not completed do not
  occupy a courier slot. Later delivery retries, confirmation, automatic
  completion or admin resolution must not alter a slot held by a newer errand.
- Concurrent attempts by one courier to accept different errands cannot bypass
  the one-errand limit.
- Acceptance before expiry does not protect an errand that is still accepted
  when its deadline arrives; it expires normally.

**Edge cases:** Repeated acceptance returns its existing outcome without assigning
again. A conflicting assignment is explained to the losing user.
An acceptance recorded after its deadline immediately expires and frees its slot.
Test delivery racing with acceptance of a new errand: acceptance succeeds only
once the prior slot release has committed. Duplicate delivery and later
completion/dispute must leave the new assignment untouched.

## 3.3.1 — Prevent self-acceptance

**Acceptance criteria**

- A user cannot see their own errands in the courier open list or accept them.
- Rejected self-acceptance changes neither assignment nor state.
- Validated session identity is authoritative, not submitted IDs.

**Edge cases:** Different sessions for one account still identify the same user.
Enforce this on the server even if the UI already hides those errands.

## 3.3.2 — At most one assigned courier

**Acceptance criteria**

- An accepted errand has exactly one assigned courier.
- Another courier cannot replace that assignment by accepting.
- Repeating the same successful acceptance does not create another assignment.
- Withdrawal and reassignment remain out of scope.

**Edge cases:** Preserve one assignment across concurrent requests and retries.

## 3.3.3 — Concurrent acceptance has one winner

**Acceptance criteria**

- Two otherwise-eligible couriers simultaneously accepting one open, unexpired
  errand produce exactly one winner when no other lifecycle event/failure competes.
- The loser is neither assigned nor told acceptance succeeded.
- Stored assignment and authorization agree on the winner.
- No fairness or special winner-selection policy is required.

**Edge cases:** Test more than two contenders and simultaneous attempts by one
courier on different errands.

## 3.3.4 — Remove accepted errands from the open list

**Acceptance criteria**

- New open-list reads after acceptance exclude that errand.
- Connected, already-displayed lists remove it within the live-update allowance.
- Disconnected lists show the stale-list warning.
- A previously loaded entry cannot be accepted by another courier.

**Edge cases:** On reconnection, synchronize the list before removing its warning.

## 3.4 — Forward-only, request-level states

**Acceptance criteria**

- Support the nine states listed in FR3.
- Normal progress is open → accepted → picked up → delivered → completed,
  plus the explicitly authorized cancellation, expiry and dispute branches.
- Reject accepted → delivered, all backward transitions and unauthorized changes.
- Cancelled never becomes expired. All four terminal states cannot reopen.
- After delivered → dispute, only an admin can change the state.
- A repeated successful action returns its existing result without replaying
  state changes or financial effects.

**Edge cases:** Do not reverse an earlier final cancellation/expiry to honor a
later pickup/delivery. Apply Q10d priorities before finalizing competing outcomes.

## 3.4.1 — Assigned courier records pickup and delivery

**Acceptance criteria**

- Only the assigned courier can perform accepted → picked up → delivered.
- Recording delivered releases this errand's courier workload slot in the same
  transaction. Keep the assignment for eventual settlement. An unsuccessful
  Mark Delivered request does not release the slot.
- Neither the requester nor an admin may perform these courier transitions.
- Couriers cannot directly complete, cancel or reopen errands.
- Pickup wins against competing requester or supplier-deactivation cancellation,
  unless cancellation reached Order first and is final.
- A valid Mark Delivered request received by Order before expiry protects the
  errand even if delivery is recorded afterward. Earlier expiry remains final.
- Unavailable items, withdrawal and reassignment are out of scope.

**Edge cases:** Test skipped/reversed actions, repeated successful transitions
and the race outcomes in the recovery section.

## 3.5 — Requester confirmation or automatic completion

**Acceptance criteria**

- After delivery, the requester can confirm receipt to change delivered → completed.
- The courier's workload slot is already free at delivered. Confirmation,
  automatic completion and payout must not change capacity for a newer errand.
- Without confirmation, Order automatically completes the errand five minutes
  after delivery was recorded, unless a dispute cancelled the timer.
- Original errand expiry does not interrupt delivered, dispute or terminal states.
- Completion wins against expiry; neither a delivered nor a completed errand
  becomes expired.
- On completion, Order calls Credit's payout endpoint for the assigned courier.
  This applies to requester confirmation, automatic completion and an admin's
  completed dispute outcome.
- Order completion is not gated on payout completion; financial effects remain
  Credit's responsibility and failed calls require recovery.

**Edge cases:** Repeated confirmation and competing completion triggers must not
cause double payout. Recover overdue automatic completion after downtime without
resetting its deadline. A simultaneous dispute wins over completion; do not queue
payout for the losing completion attempt.

## 3.5.1 — Confirmation only after delivery

**Acceptance criteria**

- Ask for requester confirmation only after the courier marks delivered.
- Reject confirmation before delivered or by another user.
- The five-minute timer begins at recorded delivery, not when the prompt is viewed.
- Order does not handle reminders.

**Edge cases:** After delivered → dispute, requester confirmation is prohibited;
only an admin may resolve the errand.

## 3.5.2 — Dispute and admin resolution

**Acceptance criteria**

- Only the requester may raise a dispute while the errand is delivered and has
  not already completed.
- Change delivered → dispute, flag it for admins and cancel the five-minute timer.
  The old errand's courier slot was released at delivered; do not alter a slot
  now occupied by another errand.
- Entering dispute does not release or pay out escrow; it stays reserved.
- Dispute is nonterminal, exempt from expiry, and must not auto-complete,
  including after a restart.
- Once disputed, only an admin may change dispute → completed or
  dispute → not completed. The requester cannot subsequently confirm it.
- A dispute wins when simultaneous with requester or automatic completion.
- If two admins choose different outcomes, the first valid committed decision
  wins; the other cannot reverse it.
- Admin-completed errands follow the normal completion/payout workflow.
- Not completed is the fourth terminal state. It does not occupy a courier slot.
  Order calls Credit's release endpoint to return the full escrow to the
  requester's available credits; the courier receives none.

**Edge cases:** Test dispute at the auto-completion boundary, repeated disputes,
conflicting admin decisions and restart with an unresolved dispute. An admin
resolving an old dispute must not affect the courier's newer active errand.
Only the winning resolution may initiate a Credit operation.

## 3.6 — Requester cancellation without penalty

**Acceptance criteria**

- The requester can cancel only open or accepted errands.
- They cannot cancel picked up, delivered, dispute or any terminal errand;
  a repeat of their own successful cancellation remains an idempotent result.
- A successful cancellation changes the state to cancelled and never reopens it.
- Repeating that successful cancellation returns its existing result.
- Order calls Credit's release endpoint to return the full escrow unchanged to
  the requester's available credits, without a penalty or courier payout.
- Pickup takes precedence for competing requests, but cancellation arriving at
  Order before pickup remains final.

**Edge cases:** System cancellation on deactivation/deletion is a separate rule
and may apply in picked up. No penalty calculations or Credit changes are added.

## 3.7 — Expiry

**Acceptance criteria**

- Requester expiry must be strictly in the future at creation, in Singapore time.
- Open and accepted errands become expired when the deadline is reached.
- Picked-up errands expire unless Order received a valid Mark Delivered request
  before expiry. The relevant event is server receipt, not starting physical travel
  or clicking in the browser; recording delivery afterward retains that protection.
- Delivered, dispute and all terminal states are exempt from new expiry.
  Cancelled stays cancelled and not completed stays not completed.
- Delivered errands retain their five-minute confirmation window unless disputed.
- Expiry calls Credit's release endpoint to return escrow to the requester and
  removes the errand from the open list subject to the accepted live-update delay.
- After downtime, process overdue expiry/completion promptly on restart without
  resetting deadlines, while respecting terminal states and cancelled dispute timers.

**Edge cases:** A Mark Delivered request arriving after expiry cannot revive an
expired errand. A request at the deadline is not before it. Reception before the
deadline protects only an authorized, otherwise-valid delivery transition.

## 3.7.1 — Prevent accepting expired errands

**Acceptance criteria**

- Reject acceptance received at or after expiry even if stored state or the
  displayed list has not yet caught up.
- Rejection does not assign a courier.
- Being accepted earlier does not extend expiry.

**Edge cases:** Test just before, exactly at and just after expiry. Acceptance
received before expiry but recorded afterward is recorded then immediately expired.

## Reliability and failure acceptance criteria (Q10)

### Q10a — Duplicate requests and lost success responses

- Identify retries of one creation attempt and return its single result.
- If creation actually succeeded but its reply was lost, return that saved errand.
- Repeated successful Accept/Pickup/Delivered/Confirm/Cancel actions return their
  existing outcomes without duplicate mutations or Credit effects.
- Explain conflicts when another, different action has already won.
- Do not treat a retry as a new creation merely because the response was lost.

### Q10b — Reservation succeeds but creation fails or is uncertain

- Abandon the failed creation attempt and return any reserved credits to the
  requester through Credit's release endpoint; do not finish creating the errand
  in the background.
- Show the requester a failure message and stay on the same page.
- While reservation/refund status is unresolved, show no success message and do
  not claim that credits have already been returned.
- Keep the same order ID throughout reservation reconciliation and release;
  never reserve under a new order ID to recover the uncertain attempt.
- If Credit's reply was lost, determine/reconcile the effect rather than assuming
  the reservation failed. Recovery must survive a service restart.
- This differs from Q10a: a lost client reply after a saved creation returns the
  existing success; a failed creation after reservation is abandoned/refunded.

### Q10c — Dependency or wallet unavailable

- Unavailable User, Supplier or Credit, including a pending/missing initial wallet,
  produces a clear failure with a retry message.
- Do not queue creation to finish later or show a successful errand.
- If a reservation might already have occurred, apply Q10b cleanup.

### Q10d — Competing actions

- If cancellation reaches Order before pickup, cancellation remains final.
  Otherwise pickup has precedence for competing pickup/cancellation, including
  system cancellation caused by supplier deactivation.
- If expiry reaches Order before Mark Delivered, expiry remains final.
  A valid Mark Delivered received before the expiry deadline takes precedence
  even if it is saved after the deadline. Do not let a delayed worker extend
  the deadline for newly received Mark Delivered requests.
- Acceptance received before expiry but recorded afterward is recorded and then
  immediately expired; acceptance does not extend an errand deadline.
- Delivered/completed cannot expire. Dispute also does not expire.
- A simultaneous dispute beats manual or automatic completion.
- First valid committed admin resolution wins if admin decisions compete.
- Apply these priorities without reopening a terminal state or initiating
  financial effects for a losing action.

### Q10e — Downtime recovery

- On restart, promptly process overdue expiry and automatic completion using their
  original deadlines; do not grant a new timeout window.
- Do not restart an automatic-completion timer cancelled by a dispute.
- Respect terminal states and recover unfinished Credit calls without duplicate
  effects.

## Credit integration

Order calls the existing internal endpoints with Credit's service credential.
Credit alone changes balances; no Credit implementation changes are authorized.

| Trigger | Order's call to Credit | Effect owned by Credit |
| --- | --- | --- |
| Creation | PUT /internal/v1/escrows/{orderId}, requester userId and amount | Reserve offer |
| Failed creation after reservation | POST /internal/v1/escrows/{orderId}/release | Return reservation to requester |
| Cancelled or expired | POST /internal/v1/escrows/{orderId}/release | Return reservation to requester |
| Completed by requester, timer or dispute admin | POST /internal/v1/escrows/{orderId}/payout, assigned courierId | Pay courier |
| Admin rules not completed | POST /internal/v1/escrows/{orderId}/release | Return reservation to requester; no courier payout |
| Delivered | No settlement call | Escrow remains reserved; this errand's courier slot is released |
| Dispute | No settlement call | Escrow remains reserved; no further workload-slot change |

**Acceptance criteria / edge cases**

- Only completed triggers payout to the courier. Cancelled, expired and not
  completed release escrow back to the requester's available balance.
- No other lifecycle states settle escrow. Delivered releases the courier's
  workload slot only; credits remain reserved pending completion or dispute
  resolution. Entering dispute makes no further workload-slot change.
- Creation still reserves credits, and failed-creation cleanup refunds those
  credits to the requester even when no errand was successfully created.
- Credit implementation stays unchanged; Order invokes its existing endpoints.
- Reuse the same order ID and payload for retries; preserve durable recovery of
  required release/payout calls after network failures and restarts.
- Do not call both payout and release for competing outcomes. Credit reports
  settled-escrow conflicts; it cannot reverse a release into payout or vice versa.
- A lost release/payout response is not evidence that the operation failed.
- Never read/write Credit tables, grant initial allocations or calculate penalties.
- See `../credit-service/API.md` for positive-integer amounts, idempotency and
  conflict behavior; `../user-service/API.md` for session identity.
- The new automatic/admin completion triggers use existing payout capabilities;
  no new financial endpoint or implementation is requested.

## Scope

The original backlog revision updated documentation only. Implementation is now
assessed separately in VALIDATION.md. No changes to Credit or Supplier implementation,
reminders, courier withdrawal/reassignment, unavailable-item workflow or carrying
limits are authorized. UI requirements describe expected behavior; their current
coverage is recorded in the implementation audit.
The event section records a proposed integration approach. It does not implement
a broker, event publisher, consumer, or change to the existing service APIs.
