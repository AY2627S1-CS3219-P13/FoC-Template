# Credit Service API

Conventions follow [User Service's API](../user-service/API.md): JSON bodies, UUID user IDs, amounts as whole-number credits, and the same error shape.

## Public API — port 8080

For the frontend. Every route needs the session cookie; Credit Service validates it through User Service. The acting user always comes from the session, never from the request body. POST requests need `Content-Type: application/json` and the frontend's exact `Origin` (`http://localhost:3000` locally).

| Method and path | Function | Request body | Success | Authentication |
| --- | --- | --- | --- | --- |
| `GET /api/v1/wallets/me` | `Balance` | No body | 200, `Wallet` | Session cookie |
| `POST /api/v1/admin/wallets/{userId}/debits` | `AdminDebit` | `amount`, `reason`, `requestId` | 200, updated `Wallet` | Admin session cookie |
| `GET /healthz` | — | No body | 200 if the HTTP server is running | None |
| `GET /readyz` | — | No body | 200 if the database is reachable; otherwise 503 | None |

Planned: `GET /api/v1/wallets/me/transactions?limit=30` for transaction history (`FR4.2.1`), once a `History` function exists.

`AdminDebit` is **not in the D1 backlog** (`FR4.1.7` lists the only permitted balance changes). Agree it with the team before implementing it.

## API for other services — port 8081

Not published to the host and never routed through the public proxy. Every request needs `Authorization: Bearer <CREDIT_INTERNAL_TOKEN>`; requests with a body need `Content-Type: application/json`.

| Method and path | Function | Caller | Request body | Success |
| --- | --- | --- | --- | --- |
| `GET /internal/v1/wallets/{userId}` | `Balance` | Order | No body | 200, `Wallet` |
| `POST /internal/v1/wallets/{userId}/initial-allocation` | `AllocateInitial` | User | No body | 200, `Wallet` |
| `PUT /internal/v1/escrows/{orderId}` | `Reserve` | Order | `userId`, `amount` | 200, requester's `Wallet` |
| `POST /internal/v1/escrows/{orderId}/release` | `Release` | Order | No body | 200, requester's `Wallet` |
| `POST /internal/v1/escrows/{orderId}/payout` | `Transfer` | Order | `courierId` | 204 |

An **escrow** holds a requester's credits for one order, from errand creation until they are released back or paid out to the courier. Each order has at most one escrow, identified by the order ID.

Every internal write is **safe to retry** with the same request:

- `initial-allocation` grants credits only the first time for a user (`FR4.1.1`).
- `PUT /escrows/{orderId}` reserves once per order. Repeating it with a different `userId` or `amount` returns 409 `idempotency_conflict`.
- `release` and `payout` succeed without changing balances if the escrow is already in that state (`NFR3.2`). Release after payout, or the reverse, returns 409 `escrow_settled`. Payout to a different courier than the first payout also returns `escrow_settled`.

Credits only move through an escrow tied to an order. There is no endpoint that moves credits directly between users (`FR4.1.8`).

### Order Service flow

1. Create errand: `PUT /internal/v1/escrows/{orderId}` before saving the order. A 409 `insufficient_credits` rejects the request (`FR3.1.4`).
2. Cancelled or expired: `POST …/{orderId}/release`.
3. Requester confirmed delivery: `POST …/{orderId}/payout` with the assigned courier.

Steps 2 and 3 may later be driven by events (`FR5`) instead of HTTP calls; the broker is not decided yet.

## Wallet

```json
{
  "userId": "fffd7e13-1234-4567-8901-012345678901",
  "available": 80,
  "reserved": 20
}
```

## Errors

Same shape as User Service:

```json
{ "error": { "code": "insufficient_credits", "message": "…" } }
```

| Status | Codes |
| --- | --- |
| 400 | `invalid_amount` (not a positive whole number), `invalid_input` (malformed ID or missing field), `invalid_json` |
| 401 | `invalid_session`, `invalid_service_credentials` |
| 403 | `admin_required`, `origin_rejected` (public writes need the frontend's `Origin`) |
| 404 | `wallet_not_found`, `escrow_not_found` |
| 409 | `insufficient_credits`, `escrow_settled`, `courier_is_requester`, `idempotency_conflict` |
| 415 | `json_required` |
| 500 | `internal_error` |
| 503 | `auth_unavailable` (User Service unreachable; fails closed), `not_ready` |
