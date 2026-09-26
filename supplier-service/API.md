# Supplier API (minimal skeleton)

Public base: `http://localhost:3000` through the shared gateway. Direct development base: `http://localhost:8082` (or `SUPPLIER_HTTP_PORT`). JSON bodies; UUID supplier IDs; RFC3339 timestamps. Browser requests use `credentials: "include"`. Mutations require the exact configured `Origin` and `Content-Type: application/json`; local Origin is `http://localhost:3000`.

| Method and path | Body | Success | Permission |
| --- | --- | --- | --- |
| `GET /api/v1/suppliers` | None | 200 `{ "suppliers": [...] }` | Valid session |
| `GET /api/v1/suppliers/{id}` | None | 200 `{ "supplier": {...} }` | Valid session |
| `GET /api/v1/locations` | None | 200 `{ "locations": [{"id":"com2","name":"COM2"}, ...] }` | Valid session |
| `POST /api/v1/suppliers` | `name`, `category`, `locationId`; optional `description` | 201 `{ "supplier": {...} }`, Location header | Admin |
| `PATCH /api/v1/suppliers/{id}` | `active`: boolean | 200 `{ "supplier": {...} }` | Admin |
| `GET /healthz` | None | 200 if HTTP process is live | None, direct service |
| `GET /readyz` | None | 200 when Supplier database is reachable; otherwise 503 | None, direct service |

`/readyz` is local database readiness, not a guarantee that User is available. User failures are handled per protected request. Gateway `/healthz` checks only the gateway.

Supplier response:

```json
{
  "supplier": {
    "id": "11111111-1111-4111-8111-111111111111",
    "name": "Campus Cafe",
    "category": "food",
    "locationId": "com2",
    "description": "Floor 1; next to the entrance",
    "active": true,
    "createdAt": "2026-09-26T10:00:00Z"
  }
}
```

Names are trimmed, 1–100 characters. Descriptions are trimmed, 0–500 characters; control characters are rejected. Categories are `food`, `printing`, `retail`, `services`, `other`. Use a location ID from the locations endpoint. IDs, timestamps and initial active status are server assigned. Unknown JSON fields are rejected.

The list returns at most 100 active records ordered by case-insensitive name then ID. Search, filtering and pagination are not implemented. Detail reads hide inactive records (404). Status updates can reactivate a known ID unless its active name/location would conflict (409). There is no hard deletion or inactive-record listing yet.

Errors match User Service:

```json
{"error":{"code":"admin_required","message":"An administrator is required."}}
```

| Status | Cases |
| --- | --- |
| 400 | Invalid fields, location, UUID, JSON or missing boolean active status |
| 401 | Missing, expired or revoked session |
| 403 | Valid non-admin attempting a write; absent/untrusted mutation Origin |
| 404 | Supplier missing/inactive; unknown route |
| 409 | Another active supplier has the same case-insensitive name at that building |
| 415 | Mutation body is not JSON |
| 500 | Unexpected internal error, with no raw database error in the response/log |
| 503 | User validation unavailable/misconfigured or database readiness failure |

Unknown methods/routes use standard Go 404/405 responses. Every protected call validates the session through User port 8081. Supplier has no independent sessions, user tables, internal user directory or event API. Never log cookies or the internal service credential.
