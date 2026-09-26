# CS3219 — Software Design and Architecture (AY2627 Sem 1)

## Friend on Campus (FoC)

**Friend on Campus (FoC)** is a peer-to-peer campus errand platform where
students can request items to be collected from stores or facilities on
campus, and other students can fulfil (and deliver) those requests. The
platform runs on a closed credit economy — credits cannot be bought,
withdrawn, or exchanged for money, and only circulate within the platform.

---

## Team Members

| Name | Role |
| ----- | ----- |
| Your Name | Your ownership |
| Your Name | Your ownership |
| Your Name | Your ownership |
| Your Name | Your ownership |
| Your Name | Your ownership |

---

## Repository Structure

User Service is available for local development. See [setup and design](user-service/README.md) and the [API guide for teammates](user-service/API.md).

The [Supplier skeleton](supplier-service/README.md) adds its own Go/PostgreSQL service, session validation through User Service and a shared Nginx gateway.

Credit Service is also available for local development. See its [setup and design](credit-service/README.md) and [API reference](credit-service/API.md).

The [Next.js frontend](frontend/README.md) has real User Service signup, email verification, login and logout. Its navy/orange Supplier screens read and modify live PostgreSQL-backed records through the Supplier API, including search, filtering, sorting, pagination and details.

To run the shared UI, User and Supplier services:

```sh
sh user-service/scripts/init-env.sh
sh credit-service/scripts/init-env.sh
docker compose up --build -d gateway
```

Open `http://localhost:3000`. The gateway owns port 3000 and forwards page requests to Next.js and API requests to the appropriate backend. User's internal authentication endpoint remains private. Direct development ports are User 8080 (configurable), Credit 8082 and Supplier 8083 (configurable).

Both environment setup scripts are required because root Compose includes Credit's configuration. To also start Credit, run `docker compose up --build -d credit-service`. Existing Supplier checkouts with `SUPPLIER_HTTP_PORT=8082` in `.env` should change it to `8083` to avoid Credit's port.

See [Supplier API](supplier-service/API.md) for CRUD and catalogue queries, [verification instructions](supplier-service/README.md#verification) for container checks and the live integration smoke test, and the [D2 implementation steps](docs/d2-supplier-progress.md). Redis, RabbitMQ workflows and cloud deployment remain later work.

This repository follows a **one-service-per-folder** structure: each
microservice (`user-service/`, `supplier-service/`, `order-service/`,
`credit-service/`) lives in its own top-level folder.

```text
.
├── frontend/
├── user-service/
├── supplier-service/
├── order-service/
├── credit-service/
├── gateway/
├── <n2h-service>/
└── README.md
```

- Any **nice-to-have (N2H)** feature that warrants its own service should
  be added as an **additional folder** at the same level, following the
  same per-service structure.
- Files for agentic coding tools (e.g. agent configs, prompts, skills)
  may be added as needed, but must still **respect the
  one-service-per-folder skeleton** for core implementation.

---
