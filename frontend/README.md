# Supplier UI showcase

A small TypeScript/Next.js frontend for trying the Supplier catalogue while the Supplier Service API is in progress. Login, logout and email verification use the real User Service. The Supplier catalogue is a **local UI demo**: the records are fictional sample data in `lib/suppliers.ts`, and create/edit/deactivate changes exist only in the current browser tab. Refreshing resets them. The Manage demo view does not enforce admin permissions.

## Run it

From the repository root, with Docker Desktop running, create the local `.env` files once if they are missing:

```sh
sh user-service/scripts/init-env.sh
sh credit-service/scripts/init-env.sh
```

Then start the frontend and its local dependencies:

```sh
docker compose up --build -d frontend
```

Open `http://localhost:3000`. Compose starts the frontend, User Service, PostgreSQL and Mailpit together. To see logs, run `docker compose logs -f frontend user-service`.

Select **Sign up** to follow the wireframe's account flow: school email, display name and password; an 8-digit email code; then an account-active confirmation. The live User Service uses eight digits, although the wireframe shows six as a placeholder. For a new account, read the code in local Mailpit at `http://localhost:8025`. Select **Log in** after verification; the header shows the signed-in user and offers **Log out**. Refreshing the page checks the existing session with `GET /api/v1/users/me`.

The wireframe also depicts joining credits and a ledger entry. Those are not shown as completed in the live UI because Credit Service integration is deferred. Likewise, registration returns a generic response for an email that already has an account, so the UI does not reveal whether that email exists.

The User Service stores the session in an HttpOnly cookie. The frontend sends it with `credentials: "include"`; JavaScript never reads the cookie or receives a session token. The separate registration token is kept in this tab's `sessionStorage` only until email verification. The frontend's default API URL is `http://localhost:8080`; set `NEXT_PUBLIC_USER_API_URL` before `docker compose up --build` to use a different browser-facing API URL. The User Service's configured frontend origin and cookie settings must match the browser deployment.

The build runs ESLint, TypeScript checking, and `next build`. Node and npm run inside Docker; no host installation is required.

## What you can show

- Browse active suppliers, search by name/description/location, and filter by category or location.
- Open a supplier to view its details.
- Use **Manage demo** to add or edit suppliers with the wireframe's name, category, location, status, description and opening-hours fields. The form previews read-only ID/time details, defaults new records to active, and blocks duplicate active names at the same location. Successful saves show the locally generated ID and timestamp. Inactive suppliers disappear from Browse.

When the Supplier Service contract is ready, replace the sample-data state in `app/page.tsx` with API calls, and enforce authentication and admin permission in the Supplier Service. The browser should send its User Service session cookie with `credentials: "include"`; the backend must validate it through User Service's internal API. The Manage demo remains a UI preview and is accessible without login; it is not evidence of backend RBAC. Do not use a client-side role toggle as an authorization control or put internal service credentials in this frontend.

## Project files

- `app/page.tsx`: catalogue and local management interactions
- `app/auth-dialog.tsx`: login, signup and email-verification forms
- `app/supplier-dialog.tsx`: wireframe-based Supplier create/edit form and save preview
- `app/globals.css`: responsive demo styling
- `lib/suppliers.ts`: sample supplier type and data
- `lib/user-api.ts`: public User Service API client
- `Dockerfile`: build and run Next.js in a Node container
