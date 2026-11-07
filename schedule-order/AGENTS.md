# Schedule Order module

Read root AGENTS.md, README.md and API.md. Scheduling backend code stays in this top-level module but runs inside Order Service. The current README architecture supersedes historical independent-service decisions in spec.md/plan.md. Order owns the shared database; scheduling migrations use a separate ledger. User owns authentication, Supplier the catalogue, Credit balances. Validate every browser request through User; workers never retain cookies.

Acceptance records an event atomically with pending state. The consumer prepares dependencies before the event transaction and commits Order import, durable outcome, handoff state and receipt together. Preserve missing-receipt discovery, per-schedule locks, retries, rollback, stable identities and fencing against cancellation/expiry. Never reserve again on import. Keep other Order workflow consumers independent and financial HTTP outside event transactions.

Use container tooling and tests first for further changes. Run schedule-tests, Order backend/browser checks and root smoke for wiring changes. Add migrations rather than editing applied SQL. Offline import must preserve financial intent, refuse overwriting target data and retain the legacy source.
