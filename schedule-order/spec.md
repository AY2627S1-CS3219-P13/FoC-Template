# Scheduled orders

Architecture update (7 November 2026): scheduling is an embedded Order module in the separate top-level `schedule-order/` folder. This supersedes the independent-service, separate-database and HTTP-handoff decisions below; the remaining product rules are preserved. Acceptance uses a durable scheduling journal, independent consumer receipts/retries and a shared Order database transaction for assignment, outcome and receipt. See [current runtime and cutover](README.md). Earlier architecture text below is historical context.
Status: **Ready** — product decisions and acceptance criteria are confirmed. Concrete HTTP contracts and the recoverable asynchronous handoff protocol must be detailed in the implementation plan before coding.

## Source and user story

- Story ID: **US-001** (generated identifier).
- Source: the user's scheduled-order request and follow-up decisions in this chat, 2 November 2026. No separate developer guide was supplied; this document records that source.
- Story: As a requester, I want to place an order for a fixed future delivery window, with credits reserved immediately, so that a courier can commit to delivering it at the time I need.
- Original scope: at least one hour's notice; future dates; half-hour slots from 09:00–09:30 through 20:30–21:00; pickup/delivery opening-hours checks; regular order details; a new `scheduled` state; transition to `accepted` on courier acceptance with subsequent lifecycle owned by Order Service; Credit Service reservation; cancellation but no editing; scheduling implementation in a separate top-level `schedule-order/` directory.
- Confirmed follow-ups: a slot is a delivery window, not an exact instant; campus locations without hours are open 24 hours; convert defined supplier-hours text on the backend; require both places open for the entire window; show and allow acceptance immediately; raise the combined courier cap to three; allow overlapping commitments; omit requester-entered expiry and derive it from the window end, allowing acceptance during the window. Scheduling is an independently deployed microservice in `schedule-order/` (superseding the earlier feature-module decision). Blank supplier hours impose no restriction; nonempty unparseable hours block scheduling/acceptance. Confirmed changed hours that invalidate a commitment cause cancellation and full credit release.

## Purpose

Let users arrange future errands without changing the regular order's item, place, identity or credit rules. Make the delivery commitment visible to requesters and couriers, reserve funds when scheduling succeeds, and preserve a single authoritative lifecycle after acceptance.

Latest confirmed refinements: the main order page contains a **Schedule order** child page; the hours grammar is accepted; there is no maximum advance horizon; no pickup timing checks are added; only `Delivered` is constrained to the delivery window. On courier acceptance, Schedule Order asynchronously transfers the order data/control to Order Service through authenticated HTTP, with pending feedback until acceptance is committed.

## Existing context and boundaries

These are existing capabilities, not claims that scheduling is already implemented:

- [Order API](../order-service/API.md): regular creation, requester-scoped idempotency, item validation, place validation, lifecycle actions, expiry, events and settlement.
- [Order model](../order-service/internal/order/model.go): pickup, delivery, items, offered credits and `expiresAt`; no delivery-window fields or `scheduled` status yet.
- [Order operating guidance](../order-service/README.md): durable workflow, creation cleanup, financial retries and isolated backend/browser checks.
- [Credit API](../credit-service/API.md): idempotent escrow reserve, release, payout and creation-abort fence.
- [Supplier API](../supplier-service/API.md): supplier `openingHours` is optional text, with no enforced hours grammar; campus locations currently have no hours field. The existing UI example is not backend format enforcement.
- [Repository guidance](../AGENTS.md) and [Order guidance](../order-service/AGENTS.md): service-owned data, internal authenticated HTTP, per-request User validation and admission/concurrency invariants.

All scheduling-specific backend implementation must live in an independently deployable Schedule Order Service in top-level `schedule-order/`, not inside `order-service/`. The scheduling page belongs in the main order frontend (REQ-024). Schedule Order owns its own PostgreSQL database and numbered embedded migrations for scheduling records, idempotency and recovery. It must not access Order's database or duplicate the authoritative courier-capacity counter. Schedule Order owns unaccepted scheduled orders; Order Service owns the accepted order and subsequent lifecycle. Cross-service calls to Order, User, Supplier and Credit use authenticated HTTP. Minimal Order integration and shared capacity changes are required; this specification does not authorize implementing them. The acceptance/handoff protocol must be defined in the implementation plan and must meet REQ-012,022,023,025 without assuming a transaction across service databases.

## Requirements

### Scheduling and regular-order parity

| ID | Requirement |
| --- | --- |
| REQ-001 | Offer a scheduled-order flow with a date and fixed half-hour delivery slot. Dates may be later than today. Do not impose a same-day restriction or business maximum advance horizon; ordinary supported date/timestamp bounds still apply. |
| REQ-002 | In Singapore time (`Asia/Singapore`), generate exactly 24 candidate slots per date: 09:00–09:30, 09:30–10:00, …, 20:30–21:00. Opening-hours and notice checks may remove candidates. No arbitrary starts or durations, and no cross-midnight slot. |
| REQ-003 | For a new attempt, the selected window start must be at least 60 minutes after authoritative server receipt of the scheduling request. Equality is valid. Revalidate server-side; client clocks or a previously loaded selector cannot establish eligibility. An identical retry of an already successful attempt returns its order without applying a new one-hour check. |
| REQ-004 | Persist an immutable delivery date, start and end. The assigned courier may click `Delivered` only within `[start, end)`; disable the action outside that period and enforce the same rule server-side against direct API calls. Do not introduce pickup timing checks, pickup windows or a check of opening hours against the actual pickup timestamp. Existing actor, state, deadline and place-validity rules still apply; the full-delivery-window opening-hours checks in REQ-008–010 remain required. Store unambiguous instants and display Singapore date/time to both participants regardless of device timezone. |
| REQ-005 | Preserve regular-order fields and validation: authenticated requester, distinct pickup/delivery `(type,id)` pairs, existing campus locations or active suppliers, nonempty item list, item names/descriptions/quantities, duplicate-item warning/decision flow and positive whole-number offered credits. No requester ID, courier ID or status supplied by the client is trusted. No new item-count cap. |
| REQ-006 | Scheduled orders have no editable expiry field. Derive `expiresAt` from window end; reject a conflicting client expiry. An order may be accepted immediately after successful scheduling, including during its delivery window, strictly before the end. Regular orders retain their existing expiry behavior. |
| REQ-007 | After successful scheduling, order content, offered credits, places, date and slot cannot be edited through any UI or API. To change them, the requester must cancel where permitted and create a new order with a fresh reservation. Authoritative place-name refresh and lifecycle updates are not requester edits. |

### Opening hours

| ID | Requirement |
| --- | --- |
| REQ-008 | Evaluate both pickup and delivery places against the selected date. Both must be open for the full selected window. A window may start exactly at opening and end exactly at closing. A window that crosses a closed interval is unavailable. Campus locations with no hours are treated as open 24 hours. |
| REQ-009 | Convert supplier-hours text on the backend to explicit weekday/time intervals using the format below. Do not compare times as strings or infer ambiguous prose. Display only eligible slots and revalidate at scheduling and acceptance. Blank supplier hours impose no extra restriction. Nonempty unparseable hours block scheduling/acceptance with an actionable error until corrected. |
| REQ-010 | Reconcile scheduled places as regular pre-delivery orders are reconciled. Confirmed inactive/deleted places, or confirmed changed valid hours that no longer cover the fixed window, cancel pre-delivery orders and record durable intent to release the full reserved credits. This includes scheduled, accepted and picked-up orders; it is system reconciliation, not requester cancellation. Do not reschedule automatically. Schedule Order Service owns pre-acceptance reconciliation; Order Service owns it after acceptance. Dependency outages, malformed responses or unparseable hours must not be treated as confirmed closure/deletion or a reason to release funds. Delivered/disputed/terminal outcomes retain existing protections. |

**Confirmed hours grammar (ASM-001 resolved):**

- One rule per line: `<day-list> <interval-list>` or `<day-list> Closed`.
- Days are `Mon`, `Tue`, `Wed`, `Thu`, `Fri`, `Sat`, `Sun`. Lists use commas; ranges use a dash, e.g. `Mon-Fri` or `Sat,Sun`. Ranges are inclusive in weekday order; wraparound ranges are rejected.
- Intervals use 24-hour `HH:mm-HH:mm`. Multiple intervals are comma-separated, e.g. `Mon-Fri 09:00-12:00,13:00-17:00`. `Sat 10:00-14:00` and `Sun Closed` can be separate lines.
- Normalize surrounding whitespace, day-letter case and ASCII/en-dash separators (`-`/`–`) before parsing. Parse the entire value; never silently ignore an invalid suffix.
- Opening must precede closing; use `00:00-24:00` for all day. `24:00` is valid only as an end boundary. Reject overnight intervals; they require separate day rules if later needed.
- Reject overlapping/contradictory rules for the same day. Days absent from a nonempty valid schedule are closed. Adjacent intervals may be merged; a lunch break cannot be bridged by a slot.
- Evaluate the selected date's weekday in Singapore time. No new holiday-calendar or date-exception feature is required by this story; see scope.

### Visibility, courier capacity and lifecycle

| ID | Requirement |
| --- | --- |
| REQ-011 | On successful scheduling, create the order in `scheduled` with no assigned courier. It is immediately discoverable and acceptable to eligible authenticated couriers other than its requester; there is no delayed release window and no intermediate transition to `open`. Show the full delivery date/window, places, items and offered credits in authorized views. |
| REQ-012 | A courier acceptance request initiates the asynchronous handoff in REQ-025. Acceptance becomes final when Order Service atomically imports the data, enforces the combined capacity limit, assigns exactly one courier and commits `accepted`. The business transition is `scheduled → accepted`; pending transfer is operational metadata, not an additional lifecycle status. From final acceptance onward, Order Service owns pickup, delivery, cancellation, expiry, confirmation, disputes and settlement intent, preserving scheduled metadata and the same order/escrow ID. Never create a second financial reservation at handoff. Handoff must not expose two independently writable authoritative states. |
| REQ-013 | Replace the one-order courier limit with a combined maximum of **three** accepted or picked-up orders, including regular and scheduled orders. Accepted future orders occupy capacity immediately. Unaccepted scheduled orders occupy no courier capacity. Delivery, cancellation or pre-delivery expiry frees only the affected assignment's slot. Delivered/pending-confirmation/disputed/history orders do not occupy capacity, matching the current lifecycle. |
| REQ-014 | Allow overlapping delivery windows within the combined cap. Show each commitment's date/window and current capacity so couriers can assess feasibility. Do not silently enforce a non-overlap rule or claim that acceptance proves route feasibility. Enforce the cap transactionally across all order types and concurrent requests. |
| REQ-015 | Requesters may cancel their own scheduled order, and their own accepted order before pickup, using regular cancellation rules. No new scheduled-order cancellation fee/cutoff. Requester cancellation after pickup is rejected, matching the current implementation; admins have no general cancellation override. Normal Supplier reconciliation remains distinct. |
| REQ-016 | At/after window end, an unaccepted `scheduled` order expires and cannot be accepted. An accepted/picked-up order not validly delivered before that deadline follows Order Service's existing expiry and credit-release rules. Expiry is not extended by late acceptance, restart or retries. Delivered/disputed/terminal orders remain governed by existing rules. Preserve admission ordering and pre-deadline delivery protections. |
| REQ-017 | Requester current/history views include scheduled orders with their fixed window. Courier discovery includes eligible scheduled orders and regular open orders; assigned courier views include accepted scheduled commitments. Apply equivalent discovery/detail/event visibility permissions to scheduled orders, with participant-only progress after acceptance. No publication of unsuccessful creation attempts or internal credentials. |

**Lifecycle boundary:**

```text
scheduled ──courier accepts──> accepted ──pickup──> picked up ──delivery in window──> delivered
    │                            │                    │
    ├──requester cancel──────────>cancelled            │
    └──window end───────────────> expired <──window end before delivery───────────┘
```

The diagram abbreviates the lifecycle: courier acceptance includes the pending asynchronous transfer in REQ-012,025; it does not become `accepted` merely because the worker was queued. Accepted orders may also be requester-cancelled before pickup or expire, and Supplier cancellation follows existing rules. After delivery, existing requester confirmation, automatic completion, dispute/admin resolution and payout/release behavior apply. A `scheduled → accepted` transition is never an editable status field.

### Credits, failure handling and security

| ID | Requirement |
| --- | --- |
| REQ-018 | Before reporting successful scheduling, reserve exactly the offered credits through Credit's authenticated `PUT /internal/v1/escrows/{orderId}` with `{userId,amount}`. A balance lookup alone is insufficient. Missing/unready wallet or insufficient credits must not create a successful scheduled order. Hold funds until the normal release/payout outcome. |
| REQ-019 | Use a stable requester-scoped idempotency key and order ID. Identical retries, concurrent duplicates and lost responses must yield at most one scheduled order and one reservation. Changed immutable details/date/slot under the same key conflict. Cancellation/expiry releases with `POST /internal/v1/escrows/{orderId}/release`; payout is performed under the existing Order workflow with the same ID. |
| REQ-020 | Recover from reservation success followed by persistence/handoff failure and from uncertain Credit responses. Record durable cleanup/reconciliation work, use Credit's abort fence for abandoned creation where appropriate, and never report success before a durable recoverable scheduled order exists. Do not refund a successfully saved order as abandoned creation. Retried release/payout cannot cause duplicate financial effects. Pending settlement must be visibly distinguishable from completed refund. |
| REQ-021 | Authenticate every protected request through User's existing validation endpoint and enforce actor/role permissions and exact Origin for cookie-authenticated mutations. Fail closed on validation outages. Use authenticated internal HTTP across service boundaries; no shared database access, cached authentication or retained user cookies for workers. Preserve standard JSON error shape, secret handling, container-first tooling and service-owned migrations. |
| REQ-022 | Timed work, acceptance/handoff and cancellation must survive process restart and concurrent replicas. No duplicate assignment, cap overflow, lost deadline, terminal-state reopening or double settlement. Persist state/version and actionable event/intent atomically within its owning service; preserve Order's durable admission and event-consumer invariants. |
| REQ-023 | Run Schedule Order as an independent service with its own database, configuration, migrations, container and recovery workers. Own creation, immediate discovery, pre-acceptance cancellation/expiry and reservation recovery there. Coordinate acceptance through authenticated Order APIs; Order Service alone enforces the combined three-order cap and commits courier assignment/accepted lifecycle. Fence competing pre-acceptance cancellation/expiry during handoff and resolve uncertain outcomes before releasing credits or allowing another acceptance. Retain a durable handoff record so Schedule Order cannot mutate or settle an order after ownership transfers. No shared SQL access, duplicate active order, or second reserve at handoff. |
| REQ-024 | Provide a page labeled **Schedule order** under the application's main order page, with navigation from that page and a way back. Keep it in the main application's frontend/navigation rather than requiring users to open a separate service UI or port. The backend remains the independent Schedule Order Service. Reuse regular-order details/validation and add date/slot selection, the fixed window, credit-reservation feedback, creation retry and cancellation status. The concrete nested URL is an implementation choice. |
| REQ-025 | On a valid courier acceptance request, Schedule Order durably records a transfer intent and dispatches authenticated HTTP to Order Service asynchronously through a restart-safe worker. Transfer the existing order/escrow ID, requester and selected courier identities, pickup/delivery, items, offered credits, fixed window and derived expiry, plus an idempotent transfer identity/version. Return a pending acknowledgement only after recording the intent; the UI shows acceptance pending and does not claim assignment or enable pickup/delivery yet. Serialize competing requests for that schedule and fence cancellation/expiry while transfer outcome is unresolved. Order alone commits final acceptance and the capacity check, strictly before window end; queuing a request does not extend that deadline or reserve capacity. Repeated deliveries of the same intent return the same outcome; conflicting payloads are rejected. Reconcile lost responses before retrying alternatives or releasing credits. On confirmed acceptance, mark the local record transferred/read-only and route subsequent lifecycle operations to Order. On definitive rejection, retain a scheduled order only if still valid; otherwise reconcile cancellation/expiry and release once. Never abandon an uncertain accepted transfer and refund it as failed creation. |

## Recommendations and confirmed decisions

| Decision | Recommendation / tradeoff | Resolution |
| --- | --- | --- |
| Courier maximum | A combined cap avoids separate quotas bypassing capacity. A distant accepted order consumes one of three slots until delivery/cancellation/expiry; overlapping commitments remain the courier's responsibility. | User confirmed combined cap of three and overlapping windows allowed. |
| Courier release | Immediate listing and acceptance improve advance matching; distant bookings can occupy slots for days. Delayed release would reduce that cost but prevent early commitment. | User selected immediate listing and acceptance. |
| Fixed delivery timing | Use a half-hour delivery window with start at least one hour ahead; enforce no early delivery and no deadline extension. | User confirmed. |
| Expiry | Hide manual expiry for scheduled orders; use window end as acceptance cutoff and delivery deadline. A late acceptance has only the remaining time to fulfill the order. | User confirmed acceptance during the window and shared end deadline. |
| Place hours | Parse the confirmed hours grammar on the backend; campus locations without hours are 24-hour; require both places open for the full window. Blank supplier hours impose no restriction; nonempty unparseable hours block scheduling/acceptance. | User confirmed the full grammar and policy; ASM-001/002 resolved. |
| Packaging | Independently deploy Schedule Order from top-level `schedule-order/`, with its own database and authenticated HTTP integration. Keep combined capacity and the accepted lifecycle authoritative in Order Service. | User selected a separate microservice, superseding the earlier feature-module choice. |
| Changed hours | Cancel and release all reserved credits when confirmed changed hours invalidate the fixed window; preserve outage handling and post-delivery protections. | User confirmed cancellation and credit release. |
| Frontend | Provide a **Schedule order** child page under the main order page, integrated with the application's navigation. | User confirmed; separate backend deployment does not imply a separate frontend. |
| Advance horizon and pickup | No business maximum advance horizon. Restrict the `Delivered` action to the delivery window; add no pickup timing checks. | User confirmed ASM-003 and clarified ASM-004. |
| Handoff | After a courier requests acceptance, Schedule Order asynchronously calls Order Service to transfer data/control. Show pending until Order commits acceptance under the shared cap. | User selected asynchronous service-to-service transfer; durable retries and ownership fencing satisfy existing recovery/capacity requirements. |

## Acceptance criteria

All time examples use Singapore time. Unless stated otherwise, the session, input details, hours and funds are valid.

| ID | Given / When / Then | Requirements |
| --- | --- | --- |
| AC-001 | Given unrestricted places, when selecting a date, then the candidate grid contains exactly 24 half-hour slots with first 09:00–09:30 and last 20:30–21:00; arbitrary 09:15 starts, 45-minute and cross-midnight windows are rejected by the server. | REQ-001,002 |
| AC-002 | Given server receipt at 08:00:00, 09:00–09:30 passes notice validation; at 08:00:01 it fails and 09:30–10:00 is the earliest candidate. A slot that became too near after the selector loaded is rejected on submission. | REQ-003 |
| AC-003 | Given receipt at 20:45 today, a valid 09:00 slot tomorrow or a later date is selectable and succeeds. No arbitrary maximum advance date is imposed within supported timestamp bounds. A device set to another timezone displays the same Singapore delivery date/window. | REQ-001,003,004 |
| AC-004 | Given the regular-order editor validations, scheduled creation retains the same item details/quantities, duplicate decision, place identities and offered credits; empty items, invalid quantities, identical places and inactive/deleted suppliers are rejected without successful scheduling. | REQ-005,018 |
| AC-005 | Given a 09:00–09:30 window, the form has no manual expiry input, stored expiry equals 09:30, and a conflicting API expiry is rejected. A regular order still accepts its existing explicit expiry. | REQ-006 |
| AC-006 | Given a successful scheduled order, attempts to change items, credits, places, date or window through UI or API are refused without modifying state or funds. Allowed cancellation followed by new creation uses a new order ID/reservation. | REQ-007,015,019 |
| AC-007 | Given pickup hours 09:00–17:00 and delivery hours 10:00–16:00, 09:30–10:00 and 16:00–16:30 are unavailable, while 10:00–10:30 and 15:30–16:00 pass. If closing is instead 15:45, 15:30–16:00 fails. A slot ending exactly at closing passes. | REQ-008,009 |
| AC-008 | Given campus locations without hours, they impose no extra restriction beyond the 09:00–21:00 grid and notice check; valid supplier hours still restrict a campus-to-supplier or supplier-to-campus order. | REQ-008,009 |
| AC-009 | Using the confirmed grammar, weekday rules apply to the selected date, en-dash example text parses, explicit `Closed` and omitted days offer no slots, and a slot crossing a lunch closure fails. Blank supplier hours impose no extra restriction. Nonempty invalid/ambiguous hours block scheduling/acceptance with an actionable error and are not silently interpreted as open. | REQ-008,009 |
| AC-010 | Given valid funds and a future slot, successful scheduling returns one durable `scheduled` order with no courier, immutable window and reserved offered credits. Another eligible user can discover and accept it immediately, even days ahead; its requester cannot accept it. | REQ-011,018,021 |
| AC-011 | Given competing couriers accepting one scheduled order, exactly one becomes assigned and observes `accepted`; all views converge on that result. Subsequent pickup/delivery/actions use Order Service's lifecycle and the original order/escrow ID, without another reserve call. | REQ-012,019,022 |
| AC-012 | Given a courier with any mix of two accepted/picked-up orders, acceptance of one more regular or scheduled order succeeds. With three, the next acceptance fails. Concurrent fourth-order attempts cannot exceed three. Unaccepted scheduled and delivered/disputed/history orders do not count. | REQ-013,014,022 |
| AC-013 | Given two accepted commitments with overlapping windows, a third overlapping scheduled order may be accepted within the combined cap. Its date/window and occupied capacity are visible. No non-overlap rejection is applied. | REQ-013,014,017 |
| AC-014 | Given a courier at capacity, delivery/cancellation/expiry frees exactly one affected slot. Retrying an older delivery or settlement cannot free a newer assignment or exceed the cap. | REQ-013,019,022 |
| AC-015 | Given own scheduled or accepted-before-pickup order, requester cancellation commits `cancelled` and durable release intent. Another requester cannot cancel it. After pickup, requester cancellation is rejected; no new fee is introduced. | REQ-015,019,021 |
| AC-016 | Given competing acceptance/cancellation/expiry commands, there is one valid authoritative outcome; a cancelled/expired order cannot later be accepted, and no courier is assigned to a refunded terminal order. Retrying a successful action does not repeat financial effects. | REQ-012,015,016,019,022 |
| AC-017 | Given a 09:00–09:30 window, acceptance at 09:10 is allowed if capacity permits, acceptance at 09:30 is rejected, and expiry is never extended. Unaccepted and accepted/picked-up undelivered orders expire at/after 09:30 with durable release intent. | REQ-006,016 |
| AC-018 | Given an accepted 09:00–09:30 order that was picked up, the UI enables `Delivered` only during the window; API delivery before 09:00 is rejected, delivery at 09:00 or 09:29:59 is eligible, and a new delivery at 09:30 is rejected. A pre-deadline delivery obeys existing durable-admission arbitration even if it commits later. | REQ-004,012,016,022 |
| AC-019 | Given a delivered scheduled order, requester confirmation, automatic completion, disputes and admin resolution follow the regular lifecycle. Delivery frees capacity; completion pays the assigned courier once through the existing Credit escrow. | REQ-012,013,019 |
| AC-020 | Given insufficient credits, unready wallet or a definitively refused reserve, scheduling reports failure, creates no discoverable order and does not claim funds reserved. | REQ-018,020 |
| AC-021 | Given successful scheduling followed by a lost response, an identical same-key retry returns the same order even when the slot is now less than one hour away; changing its slot/items/credits under that key conflicts. Concurrent same-key requests cannot double reserve/create. | REQ-003,019,022 |
| AC-022 | Given reserve success with failed persistence or uncertain reserve response, no success is reported; restart/retry resolves durable cleanup and prevents a late reservation from leaving orphaned funds. A saved successful order is not refunded by creation cleanup. Credit outages keep unresolved release/payout work retryable without duplicate effects. | REQ-020,022 |
| AC-023 | Given scheduled-order histories, discovery/detail reads and event streams, requester sees their commitment, eligible other couriers see unaccepted discoverable orders, and unrelated users cannot inspect private progress after acceptance. Invalid sessions, forged actor IDs and foreign Origins fail without mutation. | REQ-017,021 |
| AC-024 | Given a scheduled place becomes confirmed inactive/deleted before acceptance, reconciliation cancels and records release intent. A Supplier timeout/malformed response fails closed without cancellation or a false empty availability list. Acceptance revalidates current places/hours. | REQ-009,010,020,021 |
| AC-025 | Given restart, repeated timer processing or a race at window end, deadlines remain original, committed state/events agree, and recovery produces no duplicate assignment, lost financial intent or double release/payout. | REQ-016,020,022 |
| AC-026 | Given a scheduled, accepted or picked-up order whose fixed window is no longer covered by confirmed changed valid hours at either place, reconciliation commits cancellation and durable full-release intent without changing the window. Retried settlement releases the credits once. Delivered/disputed/terminal orders retain their outcome; outages, malformed responses and unparseable hours do not trigger this cancellation. | REQ-009,010,019,020,022 |
| AC-027 | Given the deployed Schedule Order Service, its runtime and database are separate from Order Service, and each writes only its own database. Scheduling and pre-acceptance recovery run in Schedule Order; accepted lifecycle commands and combined capacity enforcement run in Order. Internal integration rejects absent/invalid service credentials. | REQ-012,021,023 |
| AC-028 | Given Order commits acceptance but the response is lost or Schedule Order crashes before acknowledging it, retries/restart reconcile the same order ID to that acceptance without another reservation or assignment. Competing cancellation/expiry cannot release an accepted order's credits through the old scheduling authority. A definitive capacity rejection creates no accepted assignment and leaves the schedule available unless another valid terminal outcome won. | REQ-012,013,019,020,022,023 |
| AC-029 | Given the main order page, selecting **Schedule order** opens its child page within the main application's navigation. The page contains the regular-order fields plus date/slot selection, no manual expiry and scheduling/reservation feedback; users can return to the main order page without opening a separate service UI. | REQ-005,006,024 |
| AC-030 | Given a valid accepted order before its delivery window, the assigned courier can pick up if normal actor/state/deadline/place-validity rules permit. No delivery-window or actual-pickup-time opening-hours check is added to pickup. `Delivered` remains disabled/rejected until window start; full-window place availability checks still apply. | REQ-004,008,010 |
| AC-031 | Given a valid acceptance request, Schedule Order records transfer work durably and returns pending without waiting for successful import. While Order is unavailable, the pending state survives restart, cannot be accepted by another courier and cannot release credits on an uncertain outcome. After recovery, one idempotent import transfers all immutable details and commits acceptance only if capacity and the original deadline still permit. The UI then enables the normal Order-owned actions. | REQ-012,013,016,020,022,023,025 |

## Edge cases

- **Clock and selector staleness:** server receipt time governs one-hour notice, not the browser clock; successful idempotent replay is exempt from fresh validation. REQ-003; AC-002,021.
- **Future date/weekday:** apply the selected date's hours, including different weekend rules. No same-day cap or business maximum advance horizon. REQ-001,009; AC-003,009.
- **Boundary equality:** one-hour equality passes; window-end expiry equality fails acceptance/delivery; exact opening/closing fits the whole-window test. REQ-003,004,008,016; AC-002,007,017,018.
- **No remaining slots:** show that the date has no eligible delivery slots and let the requester choose another date; never substitute a different date/time or reserve funds for an invalid slot. REQ-001–003,008,018; AC-001–003,007,020.
- **Capacity and overlap:** future accepted orders count immediately; overlapping slots are permitted but never permit a fourth active assignment. REQ-013,014; AC-012–014.
- **Cross-service partial failure:** a local transaction cannot atomically commit a remote reservation or handoff. Persist recoverable intent and reconcile the same ID; never invent success from a timeout. REQ-012,019,020,022; AC-011,016,021,022,025.
- **Hours changes after scheduling:** preserve the fixed window rather than silently rescheduling. Confirmed deletion/inactivity or changed valid hours invalidating the window cause cancellation and full release under REQ-010. Outages and unparseable hours do not prove closure. AC-024,026.

## Out of scope

- Recurring orders, arbitrary-length slots, editing/rescheduling an existing order, partial item fulfillment and new cancellation penalties.
- Route planning, travel-time guarantees, overlap prevention, courier reservations beyond the combined cap, or an unassigned acceptance-waitlist state.
- New payment methods, wallet ownership changes, shared databases, or replacement of the established confirmation/dispute/settlement flows.
- Holiday calendars and date-specific hours overrides unless separately requested. Existing hours data must not be represented as guaranteeing holiday opening.
- Recurring automation or additional scheduling features beyond the confirmed requirements.

## Assumptions — resolved decisions

No outstanding behavioral assumptions. Stable ASM identifiers are retained to trace the user's confirmations.

| ID | Confirmed interpretation | Resolution |
| --- | --- | --- |
| ASM-001 (resolved) | Use the exact hours grammar above. | User confirmed the supplied format; unsupported legacy variations block scheduling/acceptance rather than being guessed. |
| ASM-002 (resolved) | Blank supplier hours impose no extra restriction; nonempty unparseable hours block scheduling/acceptance until corrected. | User confirmed this policy; it is now required behavior in REQ-009 and AC-009, not an outstanding assumption. Identifier retained for traceability. |
| ASM-003 (resolved) | No business maximum future date; ordinary supported date/timestamp bounds still apply. | User confirmed. REQ-001 and AC-003. |
| ASM-004 (resolved) | Restrict the courier's `Delivered` button and API action to the fixed delivery window. Add no pickup timing checks. | User clarified. Existing lifecycle/place-validity checks and full-window availability validation remain; REQ-004 and AC-018,030. |

## Open questions and dependencies

No outstanding product questions. The following decisions are resolved; technical contracts remain implementation-plan dependencies.

1. **Q-001 — Resolved: independent service and asynchronous HTTP transfer.** Schedule Order asynchronously calls Order Service after a courier requests acceptance to transfer the order data and control. Use durable transfer intent/retries, pending UI feedback, idempotent import/outcome reconciliation and fencing against cancellation/expiry (REQ-012,023,025). The current Order API needs a new authenticated import/outcome contract; specify exact payloads, authorization delegation, fencing and error semantics in the implementation plan. Keep capacity enforcement in Order Service and never use cross-service SQL. No external message broker is required merely to dispatch asynchronous HTTP work.
2. **Q-002 — Resolved: hours compatibility.** Use the confirmed grammar and blank/invalid policy in REQ-009. Legacy values outside the supported grammar block scheduling/acceptance until corrected. Supplier currently accepts arbitrary short text; conversion does not imply all existing values satisfy the format. No structured-hours or date-exception feature is required.
3. **Q-003 — Resolved: cancel and release credits.** Confirmed changed valid hours invalidating a fixed window cause pre-delivery cancellation and full credit release, including after acceptance via Order Service. Preserve post-delivery protections and never cancel because of an outage or unparseable text. See REQ-010 and AC-026.
4. **Q-004 — Resolved: horizon and pickup.** User confirmed no maximum advance horizon and no added pickup timing checks; only `Delivered` is constrained to the delivery window. See ASM-003/004 and REQ-001,004.

Required integration work, to be separately scoped in an implementation plan:

- Order-owned capacity enforcement changes from one to three everywhere, including session/UI capacity indicators, regular acceptance, scheduled handoff, concurrency and lifecycle regression coverage.
- Schedule Order/Order HTTP contracts must preserve immutable window and expiry, event versions, admission arbitration, authorized discovery and exactly-once financial effects through idempotency and recovery. Define requester/courier authorization for forwarded commands and unified list/history behavior without trusting browser-supplied identities. Endpoint paths are intentionally not invented in this spec.
- A backend hours converter and consistent server validation for selectors, creation and acceptance; no requirement to modify Supplier's data ownership.
- Schedule Order owns pre-acceptance cancellation/expiry and restart recovery in its own database. Acceptance transfers authoritative lifecycle control to Order using the same order/escrow ID and immutable details. A retained scheduling record is historical/reconciliation data, not a second active order authority.
- Add independent service configuration, Docker/Compose and containerized checks, plus gateway routing and CI integration where needed. Use non-root/read-only containers with capabilities dropped and private internal listeners; validate configuration at startup. Update affected README/API documentation and run the root wiring smoke test.
- Add the **Schedule order** child page and navigation within the main order frontend. Route scheduling requests to Schedule Order through the gateway and post-transfer lifecycle actions to Order; show pending/failed/reconciled handoff outcomes without exposing internal service details to users.

## Verification and document handoff

This document defines the product contract. Implementation and verification history are recorded in [README.md](README.md) and [TDD.md](TDD.md); acceptance criteria alone are not evidence that a check passed.

Before implementation, write the concrete integration plan, including authenticated asynchronous transfer/outcome contracts and ownership fencing. Keep scheduling backend source in `schedule-order/`, shared frontend work in the main order frontend, and minimum Order integration in Order Service. Update affected README/API documents when those contracts are implemented.

Verification must cover AC-001–031 through container-based unit/integration/browser checks, including real database concurrency, fake dependency failures, clock boundaries, restart recovery and end-to-end escrow identity. Provide a separate containerized Schedule Order check target with a disposable database and authenticated HTTP dependency fakes; no such target exists yet. Run existing Order backend and UI checks documented in its README, affected Credit/Supplier/frontend checks where touched, and the root smoke test for wiring changes. Include child-page navigation, delivery-button timing, asynchronous pending/recovery behavior and cross-service lost-response/ownership-transfer races, not just isolated service checks. Preserve first-admin bootstrap guidance before live fixture checks.
