# Supplier Service working notes

- Match User Service's Go `net/http`, `pgx`, JSON errors and session-validation contract.
- User Service owns all credentials, sessions and roles. Never query its database or cache authentication results.
- Supplier owns its PostgreSQL database, schema and numbered embedded migrations. Add new migrations; do not edit already-applied migrations.
- Run from the repository root: `sh user-service/scripts/init-env.sh`, `sh credit-service/scripts/init-env.sh`, then `docker compose up --build -d gateway`. Supplier's direct port is 8083; Credit uses 8082.
- Check backend: `docker compose --profile test run --build --rm supplier-tests` (formatting, vet, race-enabled unit/integration tests, build).
- Format in a container: `docker compose --profile test run --rm --no-deps -v ./supplier-service:/app supplier-tests gofmt -w cmd internal`.
- Check frontend: `docker compose --profile test run --build --rm frontend-checks`.
- Check live integration: `sh scripts/smoke.sh`. This creates a generated local admin account and an inactive supplier; it does not touch external services.
- Redis and RabbitMQ are deliberately absent from this minimal Supplier flow. The shared Nginx gateway is local HTTP only; no AWS resources are provisioned.
