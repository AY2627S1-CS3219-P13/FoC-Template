# Friend on Campus frontend

TypeScript/Next.js with a navy/orange responsive design. Account flows use User
Service. The supplier catalogue uses live Supplier APIs and PostgreSQL records;
there are no mock suppliers or browser-generated record IDs/timestamps.

## Run and check

From the repository root, using Docker Compose (WSL Ubuntu on this workstation):

```sh
sh user-service/scripts/init-env.sh
sh credit-service/scripts/init-env.sh
docker compose up --build -d gateway
docker compose --profile test run --build --rm frontend-checks
```

Open `http://localhost:3000`. The gateway publishes the single browser address;
Next.js stays inside the container network. Checks run ESLint, TypeScript and a
production build; the runtime uses Next standalone output. No host npm install is needed.

## Account and catalogue flows

- Signup, eight-digit school-email verification, resend, login, session restoration
  and logout use User Service. Local codes are captured in Mailpit on port 8025.
- Logged-in users browse live records and details. Search submits to the server;
  category, campus location, status, sorting and page controls request filtered pages.
- All-status browsing includes inactive records with an explicit unavailable notice.
- Create/edit forms save through the API and show its generated metadata. Validation
  errors preserve the draft. Duplicate checking is authoritative in PostgreSQL.
- Deactivation is reversible. Deletion requires confirmation and removes the record
  from the catalogue; the backend retains the row for historical references.
- Loading, empty, network-error and retry states are included. Obsolete reads are
  cancelled so a slower response cannot replace a newer search.

At the step-3 checkpoint, management controls are present for signed-in users;
the backend already rejects non-admin writes. Step 4 applies role-specific UI controls.

The session is an HttpOnly cookie managed by the browser, sent with
`credentials: "include"`. Only the temporary signup token lives in sessionStorage.
Relative API URLs go through the gateway. Leave `NEXT_PUBLIC_USER_API_URL` unset
for the shared setup; a separate deployment may override it at build time with
matching origin/cookie settings. Internal service credentials never enter this app.

## Files

- `app/page.tsx`: shared header, account state, sign-in boundary.
- `app/catalogue.tsx`: live queries, detail reads, pagination and management actions.
- `app/supplier-dialog.tsx`: persisted create/edit forms and save summary.
- `app/modal.tsx`: native modal focus trapping, Escape behavior and focus restoration.
- `app/auth-dialog.tsx`: existing account dialogs.
- `lib/supplier-api.ts`: typed HTTP adapter, including error status/code.
- `lib/suppliers.ts`: API types and fixed category choices; no sample records.
- `app/globals.css`: shared desktop/mobile styles.

See [Supplier API](../supplier-service/API.md) and [D2 checkpoints](../docs/d2-supplier-progress.md).
