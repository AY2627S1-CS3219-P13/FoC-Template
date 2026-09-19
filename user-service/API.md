# User Service API

All request/response bodies use JSON. Timestamps use RFC 3339 and IDs are UUIDs.
Passwords, verification codes, registration tokens and session tokens must never be logged by callers.

## Public API — port 8080

For requests with a body, send `Content-Type: application/json`. All POST, PATCH and PUT requests require `Origin: http://localhost:3000` locally (the configured frontend origin in deployment). In a browser, set `credentials: "include"` so cookies are sent and accepted. GET requests do not require Origin.

| Method and path | Request body | Success | Authentication |
| --- | --- | --- | --- |
| `POST /api/v1/auth/register` | `email`, `displayName`, `password` | 202, generic message and `registrationToken` | None |
| `POST /api/v1/auth/verify-email` | `email`, `code`, `registrationToken`; optional `displayName` | 200, verified message | Code and registration token |
| `POST /api/v1/auth/resend-verification` | `email` | 202, generic verification message | None |
| `POST /api/v1/auth/login` | `email`, `password` | 200, `user` and `expiresAt`; sets session cookie | Credentials |
| `POST /api/v1/auth/logout` | No body | 204; clears cookie and revokes current session | Cookie, if present; repeat calls are safe |
| `GET /api/v1/users/me` | No body | 200, `user` | Session cookie |
| `PATCH /api/v1/users/me` | `displayName` | 200, updated `user` | Session cookie |
| `PUT /api/v1/users/me/password` | `currentPassword`, `newPassword` | 200, message; all sessions revoked | Session cookie and current password |
| `PUT /api/v1/admin/users/{id}/role` | `role`: `user` or `admin` | 200, `userId` and `roles`; target sessions revoked | Admin session cookie |
| `GET /healthz` | No body | 200 if HTTP server is running | None |
| `GET /readyz` | No body | 200 if database is reachable; otherwise 503 | None |

Unknown JSON fields are rejected. Registration never accepts roles. A verified account is required to log in. Repeated registration restarts a pending signup with a new password/code/token; verified accounts are never changed. Use resend to get a new code while keeping the same signup attempt. A generic 202 with a token is also returned for an already verified email, without changing that account.

Keep the returned `registrationToken` in the signup flow (for example, browser sessionStorage), pass it alongside the code, and remove it after verification. Losing it requires restarting registration. It binds the email code to the signup attempt that set the password. It grants no login access. On registration's email-delivery 503, the response still includes `registrationToken` so the user can retry delivery.

Example profile response:

```json
{
  "user": {
    "id": "fffd7e13-1234-4567-8901-012345678901",
    "email": "student@u.nus.edu",
    "displayName": "Student",
    "roles": ["user"],
    "verifiedAt": "2026-09-18T10:00:00Z"
  }
}
```

Example local registration (these are sample values, not a created account):

```sh
curl -i http://localhost:8080/api/v1/auth/register \
  -H 'Origin: http://localhost:3000' \
  -H 'Content-Type: application/json' \
  -d '{"email":"student@u.nus.edu","displayName":"Student","password":"example password only"}'
```

Open Mailpit at `http://localhost:8025`, read the code and submit it with the email and the returned `registrationToken` to `/api/v1/auth/verify-email`. Then call `/api/v1/auth/login`. Login returns a cookie, **not a session token in the JSON body**. Frontend JavaScript should let the browser manage that cookie.

## API for other services — port 8081

**One endpoint is exposed to backend teammates:**

```http
POST http://user-service:8081/internal/v1/sessions/validate
Authorization: Bearer <USER_INTERNAL_TOKEN>
Content-Type: application/json

{"sessionToken":"<value of the browser's session cookie>"}
```

On success:

```json
{
  "userId": "fffd7e13-1234-4567-8901-012345678901",
  "roles": ["user"],
  "expiresAt": "2026-09-19T10:00:00Z"
}
```

How Order, Supplier or Credit Service should use it:

1. Read the incoming session cookie on the backend (`foc_session` locally, `__Host-foc_session` with secure cookies).
2. Send its value to the endpoint above, using the service credential from server-side configuration. Never expose that credential to the frontend.
3. On 200, use the returned `userId` as the acting user and check permissions for your operation. For example, Order must still check that this user owns the order.
4. On 401, reject the unauthenticated request. On a timeout, network failure or 5xx, fail closed and return a temporary-unavailable response. Never assume a user is authenticated when validation fails.

Validate once per protected incoming request and reuse that result within the request. Do not positively cache validation across requests initially; that would delay logout and role revocation. A request already authorised before revocation may still finish. No other service should query the User database or trust an acting-user ID supplied by the browser.

The service credential identifies a trusted backend, while the cookie identifies the user. Both are needed. The current simple setup uses one shared backend credential; per-service credentials can be added later. The internal listener has no browser CORS support and is not published by Compose. It exposes no email address, password hash or general user directory.

## Errors

Domain errors have this shape:

```json
{
  "error": {
    "code": "invalid_session",
    "message": "The session is missing, expired or revoked."
  }
}
```

| Status | Meaning / useful codes |
| --- | --- |
| 400 | Bad input, unknown fields, invalid/expired/used code (`invalid_verification`), invalid role, self role change |
| 401 | `invalid_credentials`, `invalid_session` or `invalid_service_credentials` |
| 403 | `origin_rejected` or `admin_required` |
| 404 | Verified target user not found; unknown route |
| 409 | `display_name_taken`; verification may retry with an optional new `displayName` |
| 415 | `json_required` |
| 429 | `rate_limited`; wait before retrying |
| 500 | `internal_error`; request failed without exposing internal details |
| 503 | `email_unavailable`, `busy`, `auth_unavailable` or `not_ready` |

Unknown routes/methods use Go's standard 404/405 responses. Registration/resend's 202 does not reveal whether an email already belongs to an account. Login does not distinguish a wrong password from an unknown or unverified account. Credit allocation is not part of any response or endpoint yet.
