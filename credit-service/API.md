# Credit Service API

Wallets, escrows and credit history for Friend on Campus (backlog `FR4`, `NFR3`). Conventions follow [User Service's API](../user-service/API.md): JSON bodies, the same error shape, and UUIDs for user IDs.

- **Amounts** are whole numbers of credits, greater than zero.
- **User IDs** are User Service UUIDs in the canonical 36-character form. Any letter case is accepted; responses use lowercase.
- **Order IDs** are chosen by Order Service: 1–128 characters, no leading or trailing spaces.
- **Request IDs** (admin debits) follow the same rules as order IDs. Use a fresh UUID per debit.

## Overview

### Public API — port 8080

For the frontend. Published locally at `http://localhost:8082`.

| Method and path | Function | Request body | Success | Authentication |
| --- | --- | --- | --- | --- |
| [`GET /api/v1/wallets/me`](#get-apiv1walletsme) | `Balance` | No body | 200, `Wallet` | Session cookie |
| [`POST /api/v1/admin/wallets/{userId}/debits`](#post-apiv1adminwalletsuseriddebits) | `AdminDebit` | `amount`, `reason`, `requestId` | 200, updated `Wallet` | Admin session cookie |
| `GET /healthz` | — | No body | 200 if the HTTP server is running | None |
| `GET /readyz` | — | No body | 200 if the database is reachable; otherwise 503 `not_ready` | None |

Planned: `GET /api/v1/wallets/me/transactions?limit=30` for transaction history (`FR4.2.1`).

### API for other services — port 8081

Reachable only from containers on the same Compose network, as `http://credit-service:8081`. Never route it through the public proxy.

| Method and path | Function | Caller | Request body | Success |
| --- | --- | --- | --- | --- |
| [`GET /internal/v1/wallets/{userId}`](#get-internalv1walletsuserid) | `Balance` | Order | No body | 200, `Wallet` |
| [`POST /internal/v1/wallets/{userId}/initial-allocation`](#post-internalv1walletsuseridinitial-allocation) | `AllocateInitial` | User | No body | 200, `Wallet` |
| [`PUT /internal/v1/escrows/{orderId}`](#put-internalv1escrowsorderid) | `Reserve` | Order | `userId`, `amount` | 200, requester's `Wallet` |
| [`POST /internal/v1/escrows/{orderId}/release`](#post-internalv1escrowsorderidrelease) | `Release` | Order | No body | 200, requester's `Wallet` |
| [`POST /internal/v1/escrows/{orderId}/payout`](#post-internalv1escrowsorderidpayout) | `Transfer` | Order | `courierId` | 204 |

An **escrow** holds a requester's credits for one order, from errand creation until they are released back or paid out to the courier. Each order has at most one escrow, identified by its order ID. Credits only move through an escrow: no endpoint moves credits directly between users (`FR4.1.8`).

## Authentication

**Public API.** The browser sends User Service's session cookie (`foc_session` locally, `__Host-foc_session` with secure cookies); use `credentials: "include"`. Credit Service validates it on every request through User Service's internal session endpoint. The acting user always comes from the session, never from the request.

- POST requests need `Content-Type: application/json` and the frontend's exact `Origin` (`http://localhost:3000` locally). Any request with a different `Origin` is rejected with 403 `origin_rejected`.
- If User Service is unreachable, requests fail closed with 503 `auth_unavailable`.

**Internal API.** Every request needs `Authorization: Bearer <CREDIT_INTERNAL_TOKEN>`, from server-side configuration; never expose it to the frontend. Requests with a body need `Content-Type: application/json`. A missing or wrong token returns 401 `invalid_service_credentials`.

## Retries

Every write is **safe to retry** with the same request, e.g. after a timeout:

| Endpoint | Key | A repeat of the same request | The same key with different values |
| --- | --- | --- | --- |
| `initial-allocation` | user ID | Returns the wallet; grants nothing | — |
| `PUT /escrows/{orderId}` | order ID | Returns the wallet; reserves nothing | 409 `idempotency_conflict` (other `userId` or `amount`) |
| `release` | order ID | Returns the wallet; releases nothing | — |
| `payout` | order ID | 204; pays nothing | 409 `escrow_settled` (other `courierId`) |
| Admin debit | `requestId` | Returns the wallet; debits nothing | 409 `idempotency_conflict` (other user or `amount`) |

Concurrent requests are serialised per wallet and per escrow, so they cannot overspend or pay out twice (`NFR3.2`).

## Wallet

Every endpoint except payout returns a wallet:

```json
{
  "userId": "fffd7e13-1234-4567-8901-012345678901",
  "available": 70,
  "reserved": 30
}
```

`available` can be spent on new errands. `reserved` is held in escrow for the user's open errands (`FR4.1.2`). Neither is ever negative.

## Public endpoints

### `GET /api/v1/wallets/me`

The signed-in user's wallet.

```http
GET http://localhost:8082/api/v1/wallets/me
Cookie: foc_session=<session token>
```

Returns 200 with the `Wallet`.

| Status | Code | When |
| --- | --- | --- |
| 401 | `invalid_session` | No cookie, or the session is expired or revoked |
| 404 | `wallet_not_found` | The user has not received an initial allocation |
| 503 | `auth_unavailable` | User Service is unreachable |

There is no public route to another user's wallet.

### `POST /api/v1/admin/wallets/{userId}/debits`

An admin removes credits from a user's **available** balance. Reserved credits are never touched, so open errands can still be paid out or released.

> **Not in the D1 backlog.** `FR4.1.7` lists the only permitted balance changes, and an admin debit is not one of them. The team must agree to this and add it to the backlog before it is used.

```http
POST http://localhost:8082/api/v1/admin/wallets/fffd7e13-1234-4567-8901-012345678901/debits
Cookie: foc_session=<admin session token>
Origin: http://localhost:3000
Content-Type: application/json

{"amount": 30, "reason": "Duplicate account", "requestId": "5f0c6f2e-8d1b-4c1a-9a57-2b1e6b0c9d11"}
```

| Field | Rules |
| --- | --- |
| `amount` | Whole number greater than zero |
| `reason` | Required; up to 500 characters. Stored in the ledger with the admin's user ID |
| `requestId` | Required; a new value per debit. Makes retries safe |

Returns 200 with the user's updated `Wallet`.

| Status | Code | When |
| --- | --- | --- |
| 400 | `invalid_amount` | `amount` is zero, negative or missing |
| 400 | `invalid_input` | Malformed `userId`; missing `reason` or `requestId` |
| 400 | `invalid_json` | Malformed JSON, a fractional amount, or an unknown field |
| 401 | `invalid_session` | Not signed in |
| 403 | `admin_required` | The caller is not an admin |
| 403 | `origin_rejected` | Missing or foreign `Origin` |
| 404 | `wallet_not_found` | The user has no wallet |
| 409 | `insufficient_credits` | `amount` exceeds the available balance |
| 409 | `idempotency_conflict` | `requestId` was already used with a different user or amount |
| 503 | `auth_unavailable` | User Service is unreachable |

## Internal endpoints

Examples use `curl` from another container on the Compose network:

```sh
curl -H "Authorization: Bearer $CREDIT_INTERNAL_TOKEN" http://credit-service:8081/internal/v1/wallets/<userId>
```

### `GET /internal/v1/wallets/{userId}`

Any user's wallet, for example so Order Service can check a balance before offering to create an errand.

Returns 200 with the `Wallet`.

| Status | Code | When |
| --- | --- | --- |
| 400 | `invalid_input` | Malformed `userId` |
| 404 | `wallet_not_found` | The user has no wallet |

### `POST /internal/v1/wallets/{userId}/initial-allocation`

Creates the user's wallet with the initial credits (`CREDIT_INITIAL_CREDITS`, currently 100), exactly once (`FR4.1.1`). User Service calls it after an account is verified. No request body.

```http
POST http://credit-service:8081/internal/v1/wallets/fffd7e13-1234-4567-8901-012345678901/initial-allocation
Authorization: Bearer <CREDIT_INTERNAL_TOKEN>
```

Returns 200 with the `Wallet`. A repeat never adds credits, even after some were spent.

| Status | Code | When |
| --- | --- | --- |
| 400 | `invalid_input` | Malformed `userId` |

User Service does not call this yet; that integration is deferred.

### `PUT /internal/v1/escrows/{orderId}`

Reserves credits for an errand: they move from the requester's available balance to reserved (`FR3.1.4`, `FR4.1.3`). Order Service calls it **before** saving a new errand, and rejects the errand if this fails.

```http
PUT http://credit-service:8081/internal/v1/escrows/order-8f3a
Authorization: Bearer <CREDIT_INTERNAL_TOKEN>
Content-Type: application/json

{"userId": "fffd7e13-1234-4567-8901-012345678901", "amount": 30}
```

Returns 200 with the requester's updated `Wallet`.

| Status | Code | When |
| --- | --- | --- |
| 400 | `invalid_amount` | `amount` is zero, negative or missing |
| 400 | `invalid_input` | Malformed or missing `userId`, or an invalid `orderId` |
| 400 | `invalid_json` | Malformed JSON, a fractional or string amount, or an unknown field |
| 404 | `wallet_not_found` | The requester has no wallet |
| 409 | `insufficient_credits` | `amount` exceeds the available balance (`FR4.1.9`) |
| 409 | `idempotency_conflict` | This order already has an escrow with a different user or amount |

### `POST /internal/v1/escrows/{orderId}/release`

Returns an errand's escrowed credits to the requester's available balance when the errand is cancelled or expires (`FR4.1.5`). No request body.

Returns 200 with the requester's updated `Wallet`.

| Status | Code | When |
| --- | --- | --- |
| 400 | `invalid_input` | Invalid `orderId` |
| 404 | `escrow_not_found` | No escrow exists for this order |
| 409 | `escrow_settled` | The escrow was already paid out |

The penalty for cancelling after pickup (`FR3.6.1`) is not implemented; its rules are not agreed yet.

### `POST /internal/v1/escrows/{orderId}/payout`

Pays an errand's escrow to the courier once the requester confirms delivery (`FR4.1.4`). The requester's reserved balance and the courier's available balance change in one transaction: both update or neither does (`NFR3.3`).

```http
POST http://credit-service:8081/internal/v1/escrows/order-8f3a/payout
Authorization: Bearer <CREDIT_INTERNAL_TOKEN>
Content-Type: application/json

{"courierId": "0b6f5c2d-3e4a-4b5c-8d6e-7f8091a2b3c4"}
```

Returns 204 with no body.

| Status | Code | When |
| --- | --- | --- |
| 400 | `invalid_input` | Malformed or missing `courierId`, or an invalid `orderId` |
| 400 | `invalid_json` | Malformed JSON or an unknown field |
| 404 | `escrow_not_found` | No escrow exists for this order |
| 404 | `wallet_not_found` | The courier has no wallet; nothing changes |
| 409 | `escrow_settled` | The escrow was released, or paid out to a different courier |
| 409 | `courier_is_requester` | `courierId` is the requester (`FR3.3.1`) |

## Order Service flow

1. **Create errand:** `PUT /internal/v1/escrows/{orderId}`. On 409 `insufficient_credits`, reject the errand (`FR3.1.4`).
2. **Cancelled or expired:** `POST …/{orderId}/release`.
3. **Requester confirmed delivery:** `POST …/{orderId}/payout` with the assigned courier.

Retry steps 2 and 3 until they succeed; repeats are harmless. They may later be driven by events (`FR5`) instead of HTTP calls; the broker is not decided yet.

## Errors

Same shape as User Service:

```json
{ "error": { "code": "insufficient_credits", "message": "insufficient available credits" } }
```

| Status | Codes |
| --- | --- |
| 400 | `invalid_amount`, `invalid_input`, `invalid_json` |
| 401 | `invalid_session`, `invalid_service_credentials` |
| 403 | `admin_required`, `origin_rejected` |
| 404 | `wallet_not_found`, `escrow_not_found`; unknown route (plain-text body) |
| 405 | Wrong method for a known route (plain-text body) |
| 409 | `insufficient_credits`, `escrow_settled`, `courier_is_requester`, `idempotency_conflict` |
| 415 | `json_required` |
| 500 | `internal_error`; details are logged without request data |
| 503 | `auth_unavailable`, `not_ready` |
