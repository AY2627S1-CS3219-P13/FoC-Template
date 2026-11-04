# Schedule Order Service

Read root AGENTS.md, spec.md, README.md and API.md. Keep scheduling backend code in this independent service; User owns authentication, Supplier owns catalogue and Credit owns balances. Validate each browser request through User. Workers use service credentials and never persist cookies.

Own only the Schedule database. Order alone owns courier capacity and lifecycle after acceptance. Transfer intent must be durable before returning pending; unknown HTTP outcomes stay fenced and retry the same transfer ID. Never refund or reassign an order while its import outcome is uncertain. Definitive import outcomes are stored durably by Order.

Use TDD and container tooling. Run the isolated schedule-tests target (gofmt, vet, race tests, build); changes to Order need its backend/browser suites, and wiring changes need the root smoke test. Add migrations, never edit applied migrations. Preserve failed-intent recovery and append/extend tests rather than weakening assertions.
