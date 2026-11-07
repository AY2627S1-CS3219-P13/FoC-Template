# Schedule Order module and event migration conversation log

Recorded: 7 November 2026 (Asia/Singapore).

This is a condensed record of the user requests, architectural decisions, implementation and verified migration outcomes in this chat, following the existing conversation-log format. It is not a verbatim transcript. Credentials, connection URLs containing passwords, session tokens and database row contents are excluded.

## Initial request: extend Order Service while keeping a separate folder

The user requested:

> Can you help me change schedule order such that it is not a microservice on its own but extends out from order service. However, I want it in its separate folder schedule-order. Keep all the functional implementation the same. Provide me how you are going to implement this change before doing any changes.

The assistant inspected the repository without changing files and proposed an embedded scheduling module:

- Keep the scheduling backend, migrations and tests in top-level `schedule-order/`.
- Expose a public Go package because Order cannot directly import another module's `internal/schedule` package.
- Let the Order process initialize scheduling, serve the existing scheduling routes and start its workers.
- Remove the independent Schedule runtime/container and update gateway, Compose, CI and deployment wiring.
- Preserve public API paths, UI, validation, asynchronous pending feedback, recovery, financial behavior and shared courier capacity.

The initial recommendation was to retain the separate Schedule database and replace the worker's internal HTTP handoff with an application function call. This storage/handoff proposal was subsequently superseded by the event-based design below.

## Function calls versus messages and events

The user asked:

> You say that order will call schedule order with a function call, is it possible for this to be a message/event instead? Or would a function call make more sense?

The assistant clarified the direction: Order hosts the scheduling module, while Schedule initiates the acceptance handoff to Order.

Three approaches were compared:

| Approach | Discussion |
| --- | --- |
| Durable worker plus function interface | Keep persisted transfer intent and asynchronous execution, replacing HTTP with a typed function interface. Initially recommended as the smallest change. |
| PostgreSQL-backed messaging | Store durable acceptance work, consume it independently and reconcile recorded outcomes. More explicit separation with additional receipt/retry machinery. |
| Broker-backed messaging | Use a broker and transactional outbox. Possible, but adds infrastructure with limited benefit for this single application. |

In-memory channels or an event bus alone were insufficient because queued work must survive restart. The assistant distinguished an acceptance request from a final accepted outcome; an event must not claim assignment has committed before Order's capacity and lifecycle transaction succeeds.

## Final decision: follow Order's durable internal event pattern

The user asked:

> What if we do it with an event like how the internal order-service is structured?

After inspecting Order's event workflow, the assistant recommended its existing PostgreSQL-backed pattern: durable journals, missing-receipt discovery, independent consumers, advisory locks, receipts and retry records.

The agreed acceptance flow was:

1. Atomically save pending acceptance and `ScheduledOrderAcceptanceRequested`, then return the existing 202 response.
2. Have a background consumer prepare dependencies and enforce Order's import, expiry, admission and shared three-order capacity rules.
3. Commit assignment, existing Order lifecycle events, durable import outcome, scheduling handoff state and receipt in one database transaction.
4. Retain pending work and retry metadata on transient failure; keep cancellation/expiry fenced until a definitive outcome.

To obtain those local transaction guarantees, scheduling tables would move into Order's database. Scheduling code and embedded migrations would remain in `schedule-order/`, with a separate migration ledger.

A separate scheduling journal was needed because existing `order_events` consumers assume that an Order record already exists. Unaccepted schedules do not yet have one. Functions would still execute inside a consumer, while coordination between scheduling and Order would be driven by durable events.

This explicitly superseded the earlier independent-service/separate-database/HTTP-transfer architecture and the initial function-call-only proposal. Product behavior remained unchanged.

## Implementation authorization and completion

The user authorized implementation:

> Yes okay, help me implement this change to schedule-order using the latest event pattern mentioned

Implemented changes:

- Added the public module facade in `schedule-order/module.go` and a local module dependency from Order.
- Embedded scheduling routes in Order's public listener, preserving `/api/v1/scheduled-orders` and the `/orders/schedule` child page.
- Started event consumption and scheduling recovery in independent worker loops, so acceptance work cannot consume the refund-recovery budget.
- Added `schedule_events`, `schedule_receipts` and `schedule_retries` through migration 002, leaving existing numbered migration SQL unchanged.
- Used `schedule_schema_migrations` to keep scheduling migration bookkeeping separate from Order's ledger in the shared database.
- Wrote acceptance intent and its event atomically; preserved stable transfer identities, generation changes, pending fencing and definitive outcomes.
- Prepared Supplier HTTP lookups before the event transaction. Order's database effects, Schedule handoff state and consumer receipt commit or roll back together.
- Preserved financial HTTP recovery outside event transactions and avoided a second credit reservation during import.
- Retained the private authenticated HTTP import adapter for compatibility; the embedded scheduling runtime does not call it or require its token.
- Removed Schedule's standalone server and runtime Compose/image deployment entries while keeping isolated Schedule test containers.
- Updated gateway routing, container build contexts, environment initialization, CI/image publication, EC2 deployment scripts and architecture/API documentation.
- Added an offline `import-schedules` maintenance command and a deployment guard against proceeding while a legacy Schedule application container remains.

The offline importer copies all scheduling fields without decoding JSON numbers through floating point, preserves financial and acceptance intent, refuses a nonempty target and leaves the legacy source intact.

## Verification of the implementation

Final validation reported in this chat:

- Order backend checks passed: formatting, vet, race tests and build.
- Schedule backend checks passed, including atomic event/pending-state writes, legacy import, refusal to overwrite existing target data and exact large-integer JSON preservation.
- Shared-database integration tests verified rollback on receipt failure, concurrent replay, one committed assignment/receipt and no duplicate reservation.
- The latest scheduling-specific Order tests, vet and combined application build passed after final consumer/worker changes.
- All 25 Playwright browser tests and 10 JavaScript tests passed.
- All 13 deployment ordering/failure tests passed, including the legacy-container cutover guard.
- Shared HTTP/HTTPS session configuration, gateway ingress configuration and EC2 logging checks passed.
- Gateway smoke passed in a disposable Compose project after claiming that project's designated first administrator.
- The real Order/Credit lifecycle check passed, including scheduled creation, idempotent retry, asynchronous acceptance, cancellation and exactly one refund/reservation.
- The disposable database contained the expected single accepted scheduling receipt.
- `git diff --check` passed.

During validation, simultaneous full-stack builds and regression runs caused timing-sensitive failures. The competing build was canceled and images were subsequently built serially; backend and browser reruns passed without weakening assertions or increasing test timeouts. A legacy-import test connection initially omitted its isolated schema and was corrected. The disposable smoke stack and its four data volumes were removed after verification; original application data was preserved.

## Offline migration request and execution

The user then requested:

> can you help me run the offline migration instructions

The assistant inspected the real local `foc` stack. Both application writers were stopped. The legacy Schedule database contained four rows: one expired schedule and three transferred schedules, with no unresolved transfer or refund work. Order contained 18 orders and three scheduled import outcomes; its scheduling table did not yet exist.

Executed cutover:

1. Started only the existing Order and legacy Schedule databases.
2. Saved PostgreSQL custom-format backups of both databases under the private `.migration-backups/2026-11-07-schedule-order/` directory and added that directory to `.gitignore`.
3. Restored both backups into disposable verification databases, checked their counts and removed only those temporary databases.
4. Recorded deterministic full-row fingerprints for the legacy schedules and existing Order records/import outcomes.
5. Built the combined Order image and ran `import-schedules`, passing the source credential through a private environment variable without logging it.
6. Compared all copied scheduling fields with the source and verified the source was unchanged. Existing Order records/import outcomes also matched their pre-import fingerprints.
7. Confirmed that all three transferred schedules referenced existing Order records. No pending transfer events needed reconstruction.
8. Retained previous application image references, removed the stopped legacy Schedule application container and started the updated gateway application stack.
9. Stopped the legacy Schedule database container while retaining its database volume and the restore-tested backups.
10. Verified the migrated scheduling rows still matched after application startup and both scheduling migrations were applied.

Verified final state:

| Check | Result |
| --- | --- |
| Copied schedules | 4: one expired, three transferred |
| Existing Order records | 18 retained |
| Existing import outcomes | 3 retained |
| Missing Order records for transferred schedules | 0 |
| Scheduling data fingerprints | Exact match before import, after import and after startup |
| User, Credit, Supplier and combined Order readiness | Passed |
| Gateway `/orders/schedule` | HTTP 200 |
| Protected scheduling API without a session | Expected HTTP 401 |
| Legacy Schedule application | Removed |
| Legacy Schedule database | Stopped and retained |
| Combined application | Running through `http://localhost:3000` |

No synthetic accounts or financial test transactions were added to the real local stack during this cutover. The full financial lifecycle test described earlier ran in a separate disposable project. Private backup files, verification digests and a migration record remain in the ignored backup directory.

## Conversation logging request

The user requested:

> Log the chat in schedule-order/logs

Created this conversation record and linked it from the module README. This logging change is documentation-only.

Related documents:

- [Current module operation and offline cutover](../README.md)
- [Public API and internal acceptance events](../API.md)
- [Specification, including the architecture update](../spec.md)
- [Implementation plan](../plan.md)
- [Verification history](../TDD.md)
- [Earlier Schedule Order conversation](2026-11-04-schedule-order-chat.md)
