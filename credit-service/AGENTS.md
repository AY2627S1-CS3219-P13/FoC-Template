# Credit Service working notes

- Credit Service owns wallets, escrows and the credit ledger. Credits are a closed economy: they only move through an escrow, never directly between users, and never in or out of the platform.
- Every balance change goes through the ledger transaction path in `internal/credit`. Do not update balances directly, and keep the documented lock ordering to avoid deadlocks.
- Internal writes are idempotent by order id / idempotency key; retries must not double-spend.
- Public API on port 8080 (published locally at 8082, gateway routes `/api/v1/wallets/*` and `/api/v1/admin/wallets/*`). `/internal/v1/*` on port 8081 uses `CREDIT_INTERNAL_TOKEN` and is never routed through the gateway.
- Sessions are validated through User Service on every request and fail closed with 503 if it is unreachable. Never cache identities or query another service's database.
- Migrations are numbered and embedded. Add new migrations; do not edit applied ones.
- Run from the repository root: `sh user-service/scripts/init-env.sh`, `sh credit-service/scripts/init-env.sh`, then `docker compose up --build -d gateway`.
- Check backend: `docker compose --profile test run --build --rm credit-tests` (formatting, vet, race-enabled tests, build).
- Format in a container: `docker compose --profile test run --rm --no-deps -v ./credit-service:/app credit-tests gofmt -w cmd internal`.
