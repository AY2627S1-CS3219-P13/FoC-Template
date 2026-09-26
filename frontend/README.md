# Shared FoC frontend

Minimal Next.js App Router / TypeScript UI for the Supplier skeleton. Start the shared stack from the repository root with `docker compose up --build -d gateway`, then open `http://localhost:3000`.

The browser calls relative `/api/v1` routes through Nginx. Go services perform all authentication and authorization. There are no frontend secrets, duplicate session storage or Next.js API handlers. Login requires an account already verified through User Service; see [Supplier setup](../supplier-service/README.md).

`docker compose --profile test run --build --rm frontend-checks` runs lint, type checking and the production build. The runtime image uses Next.js standalone output as a non-root user. Supplier features are deliberately minimal: browse, admin create/deactivate, login/logout. Registration, filtering and full profile editing are not included.
