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
sh scripts/browser-check.sh
```

Open `http://localhost:3000`. The gateway publishes the single browser address;
Next.js stays inside the container network. Checks run ESLint, TypeScript and a
production build; the runtime uses Next standalone output. No host npm install is needed.

## Account and catalogue flows

- Signup, eight-digit school-email verification, resend, login, session restoration
  and logout use User Service. Local codes are captured in Mailpit on port 8025.
- Verification confirms a 100-credit wallet before reporting success; login retries pending setup after an outage. The home-page header reads the wallet through Credit Service and shows available and reserved credits.
- Logged-in users browse live records and details. Search submits to the server;
  category, campus location, status, sorting and page controls request filtered pages.
- All-status browsing includes inactive records with an explicit unavailable notice.
- Create/edit forms save through the API and show its generated metadata. Validation
  errors preserve the draft. Duplicate checking is authoritative in PostgreSQL.
- Deactivation is reversible. Deletion requires confirmation and removes the record
  from the catalogue; the backend retains the row for historical references.
- Loading, empty, network-error and retry states are included. Obsolete reads are
  cancelled so a slower response cannot replace a newer search.
- The pre-designated, verified first admin can open `/admin/setup` after login and claim admin access once. Their sessions are revoked, so they must log in again. Local Mailpit is only a workflow demonstration, not proof of real mailbox ownership.
- `/admin` is checked against the current User Service session during server rendering
  and again in the browser. A non-admin receives no admin page. Admins can look up
  one verified user by exact school email and promote or demote them after entering
  their own current password; the target's sessions are revoked, so they must log in again. The operator-only recovery command remains available as an alternative.
- The admin page also shows recent durable role-change audit events, including bootstrap and operator grants.
- The admin page has preset, read-only GET checks for the gateway, current user,
  suppliers and paginated User Service account summaries. It shows the actual
  status, duration and response body. The account summary includes pending and
  verified users, but never password hashes, session tokens or verification codes.
  Gateway health checks only Nginx; supplier requests also exercise User Service
  session validation and Supplier Service's database. Credit and Order are not
  routed through this page yet. No arbitrary URLs or internal credentials are used.

Only users whose User Service response includes `admin` see create/edit/status/delete
controls. Ordinary users retain all browsing tools. The backend independently
authorizes every request. An expired/revoked session clears the catalogue and forms;
returning to the tab rechecks the session. In-flight session reads cannot restore a
previous account after logout or a new login. No role toggle or localStorage identity exists.

The session is an HttpOnly cookie managed by the browser, sent with
`credentials: "include"`. Only the temporary signup token lives in sessionStorage.
Relative API URLs go through the gateway. Leave `NEXT_PUBLIC_USER_API_URL` unset
for the shared setup; a separate deployment may override it at build time with
matching origin/cookie settings. Internal service credentials never enter this app.

## Files

- `app/page.tsx`: shared header, account state, sign-in boundary.
- `app/admin/`: server-gated admin page, promotion flow and GET request presets.
- `app/catalogue.tsx`: live queries, detail reads, pagination and management actions.
- `app/supplier-dialog.tsx`: persisted create/edit forms and save summary.
- `app/modal.tsx`: native modal focus trapping, Escape behavior and focus restoration.
- `app/auth-dialog.tsx`: existing account dialogs.
- `lib/supplier-api.ts`: typed HTTP adapter, including error status/code.
- `lib/suppliers.ts`: API types and fixed category choices; no sample records.
- `app/globals.css`: shared desktop/mobile styles.

See [Supplier API](../supplier-service/API.md) and [D2 checkpoints](../docs/d2-supplier-progress.md).

The [D2 demo guide](../docs/d2-supplier-demo.md) includes roles, schema, component and
sequence diagrams, API-only verification and the live presentation flow. Browser
checks run in a dedicated Playwright container with Linux/WSL host networking;
screenshots/results are saved under ignored `.agent/tmp/d2-browser/`.
