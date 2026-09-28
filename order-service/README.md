# Order Service

**Status:** not implemented. This folder is a placeholder that reserves the
one-service-per-folder slot for errand orders (`FR3`).

When work starts, follow the shape of the implemented services:

```text
order-service/
├── cmd/server/main.go        # configuration, wiring, graceful shutdown
├── internal/order/           # handlers, store, embedded migrations
├── scripts/check.sh          # gofmt, go vet, go test -race, build
├── Dockerfile                # development / build / runtime stages
├── AGENTS.md                 # service-specific working notes
├── README.md
└── API.md
```

Orders own their own PostgreSQL database and never read another service's
tables. Sessions are validated through User Service's internal endpoint, and
credits move only through Credit Service's escrow endpoints
([credit-service/API.md](../credit-service/API.md#internal-endpoints)).
