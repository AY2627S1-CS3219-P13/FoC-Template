# Implementation plan

Source: [spec.md](spec.md). Implementation is authorized by the user's 3 November request. Use TDD: record a failing containerized test run before each behavior is implemented.

Schedule Order owns its database and creation/escrow intent, scheduled records, cancellation and asynchronous transfer work. Order owns a new authenticated idempotent import endpoint and durable outcomes keyed by transfer ID. The Schedule worker never returns an uncertain transfer to the available pool: retry the same immutable request until Order returns its persisted accepted/rejected outcome. Cancellation/expiry are fenced by a persisted transfer intent. Import rejection is durable so a late retry cannot accept after Schedule releases funds. Order serializes all acceptance using a courier advisory lock and checks the combined count of accepted/picked-up orders. No second reservation is made during import.

The import endpoint authenticates a dedicated service token, receives the Schedule-validated courier command (no browser actor fields), validates places using Supplier's internal API, and commits the assigned accepted record with journal events in one local transaction. The worker persists no session cookie. The implementation plan treats this as service-to-service command delegation after per-request User validation at Schedule, not cached session validation for later browser requests.

Stage 1: pure Singapore slot and deterministic hours tests; capture red, implement and capture green.
Stage 2: real isolated PostgreSQL plus fake HTTP dependency tests for creation/idempotency/reservation cleanup, auth, cancellation, expiry, durable transfer and lost-response recovery; capture red before implementation.
Stage 3: Order import/capacity/window tests first, then implementation and migration; adapt existing one-slot expectation tests to the explicitly changed three-slot product contract without removing their race/isolation assertions.
Stage 4: child page, unified discovery/history and pending feedback; browser tests first. Runtime service wiring, docs, CI and end-to-end smoke. All tooling runs in containers. Preserve unrelated deleted files and output directory.
