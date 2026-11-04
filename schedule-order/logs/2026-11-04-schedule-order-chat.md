# Schedule Order conversation log

Recorded: 4 November 2026 (Asia/Singapore).

This is a condensed record of the requests, decisions and reported outcomes in this chat, rather than a verbatim transcript. Earlier implementation work is summarized from the conversation record. No credentials, session tokens or environment secrets are included.

## Initial request

The user requested a specification and acceptance criteria in a separate top-level `schedule-order/` directory for:

1. Scheduling orders at least one hour ahead, including on future dates.
2. Fixed 30-minute delivery windows.
3. Windows from 09:00–09:30 through 20:30–21:00, checked against pickup and delivery opening hours.
4. The same order details and validation as regular orders.
5. A `scheduled` state, followed by `accepted` when a courier accepts; Order Service then owns the lifecycle.
6. Reserving offered credits through Credit Service when scheduling succeeds.
7. Cancellation under existing rules, with no editing of scheduled orders.
8. Scheduling code outside Order Service.

The user also requested recommendations on courier capacity and when scheduled orders should become visible and available for acceptance.

## Confirmed product decisions

| Topic | Decision recorded in the chat |
| --- | --- |
| Window meaning | A fixed delivery window, rather than an exact delivery instant. Its start must be at least one hour after scheduling. |
| Timezone | Singapore time for the delivery date, window and displayed times. |
| Future dates | No business maximum advance horizon; normal supported timestamp bounds apply. The user confirmed ASM-003. |
| Campus hours | Campus locations without hours are treated as open 24 hours. |
| Supplier hours | Convert the required text format in the backend. Use weekday/day ranges and 24-hour intervals, separate rules on separate lines, and `Closed` for closed days. The user accepted ASM-001. |
| Blank/invalid hours | Blank supplier hours impose no extra restriction. Nonempty unparseable hours block scheduling/acceptance until corrected. |
| Full-window opening | Both pickup and delivery places must be open for the entire delivery window. |
| Changed hours | Confirmed changed opening hours invalidating a scheduled window cause cancellation and full credit release, without automatic rescheduling. |
| Discovery | Show scheduled orders and allow acceptance immediately after successful scheduling. |
| Courier capacity | Raise the cap to three accepted/picked-up orders across regular and scheduled orders combined. Allow overlapping scheduled windows. |
| Expiry | Remove user-entered expiry for scheduled orders. The window end is the shared expiry and delivery deadline. Acceptance is permitted during the window, strictly before its end. |
| Delivery action | Only allow the courier to mark an order delivered within its delivery window. Do not add pickup timing checks. This was the user's clarification of ASM-004. |
| Service packaging | An initial feature-module choice was explicitly superseded: Schedule Order must be a separate microservice. |
| Frontend | Keep a **Schedule order** child page under the main order page. A separate service does not require a separate frontend. |
| Acceptance handoff | Schedule Order asynchronously calls Order Service to transfer order data and control after courier acceptance. Order Service owns subsequent lifecycle and shared capacity. This resolved Q-001. |

The agreed expiry example was 09:00–09:30: acceptance at 09:10 is allowed, but delivery must still occur before 09:30.

## Implementation request and reported completion

The user asked: “Ok can you help me implement the schedule-order service. Use TDD, so create tests first and ensure failure before proceeding to implementation”.

The implementation was reported complete with an independent Schedule Order service and database, credit reservation/recovery, opening-hours validation, asynchronous handoff to Order Service, combined three-order capacity, the scheduling child page, and deployment/test integration. The conversation record reports passing service, browser and live smoke checks, including scheduled transfer and credit-release verification. Detailed implementation validation is recorded in [TDD.md](../TDD.md).

Before the live smoke test, the user confirmed that the designated first-admin account had already claimed its role in the local User database. This avoided consuming that bootstrap claim with a test fixture.

Related project documents:

- [Specification and acceptance criteria](../spec.md)
- [Implementation plan](../plan.md)
- [Service operation and recovery](../README.md)
- [API and handoff contract](../API.md)
- [TDD evidence](../TDD.md)

## UI follow-up — 4 November 2026

The user supplied an annotated screenshot and requested:

> “when creating scedule order, leave the tab there and not refelcted as a new errand? (see pic)”
>
> “scheduled tag should be a different colour than any of the other tags”

The screenshot annotation requested the white active-tab highlight on **Schedule order** and removal of **Back to orders**.

Implemented changes:

- Highlight **Schedule order** while its creation form is shown, instead of **New errand**.
- Remove the extra **Back to orders** link.
- Make **New errand** return to regular order creation.
- Give Scheduled badges a distinct dark navy background with white text.
- Correct the scheduling preview's status from **Open** to **Scheduled**.

Verification recorded in this chat:

- Added browser regression assertions first. Both scheduled-order tests failed before the implementation: the schedule tab lacked its active attribute, and the old badge colour was too close to other status badges.
- After the fixes, all 20 Order browser tests and five data-format tests passed.
- The full browser suite included scheduled form submission, active-tab navigation, removal of the extra back link, preview status and distinct badge colours.
- `git diff --check` passed.
- Rebuilt the local Order service. Its first restart failed because the local dependencies were stopped; starting the existing application stack restored it, and the Order readiness check passed.
- The local scheduling page was available at `http://localhost:3000/orders/schedule`.

## Conversation logging request — 4 November 2026

The user requested: “Add a logs folders under schedule-order and log this chat.”

Created `schedule-order/logs/` and this Markdown conversation record, and linked it from the service README. This documentation-only change does not alter application behavior.
