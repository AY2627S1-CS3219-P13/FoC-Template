# Schedule Order API

These routes are served by Order Service. Scheduling is an embedded module; all public paths and response semantics remain unchanged.

Public routes use the current User session cookie, never browser-supplied actor IDs. Mutations require the configured exact Origin, JSON, and creation requires a requester-scoped `Idempotency-Key` (1–128 characters, no surrounding whitespace/control characters). Errors use `{"error":{"code","message"}}`. There is no editing route.

| Method/path | Result |
| --- | --- |
| `POST /api/v1/scheduled-orders` | 201 reserved/saved scheduled order; 200 identical saved retry before transfer; 303 to authoritative Order detail after transfer |
| `GET /api/v1/scheduled-orders?scope=mine` | Requester's scheduled/cancelled/expired records before transfer, newest updates first |
| `GET /api/v1/scheduled-orders?scope=open` | Immediately available scheduled orders from other requesters |
| `GET /api/v1/scheduled-orders/slots?date=2030-11-04&pickupType=location&pickupId=com2&deliveryType=location&deliveryId=utown` | `{slots:[RFC3339 start,...]}` filtered against notice and both places' full-window hours |
| `GET /api/v1/scheduled-orders/{id}` | Authorized snapshot; transferred records redirect to Order's protected detail route |
| `POST /api/v1/scheduled-orders/{id}/actions` | `{"action":"accept"}` returns 202 transfer pending; `{"action":"cancel"}` returns 200 with refund intent |
| `GET /healthz`, `GET /readyz` | Combined Order process liveness / database readiness |

Creation body:

```json
{
  "pickup":{"type":"location","id":"com2"},
  "delivery":{"type":"location","id":"utown"},
  "items":[{"name":"Tea","description":"Less sugar","quantity":1}],
  "offeredCredits":10,
  "windowStart":"2030-11-04T09:00:00+08:00"
}
```

Expiry is derived as start +30 minutes; unknown fields including manual expiry are rejected. The response is `{order:{...regular fields,windowStart,transferPending,refundPending}}`. `status` starts as `scheduled`. `transferPending` means assignment has not yet been confirmed; the selected courier is not allowed to pick up before final acceptance. `refundPending` means release remains unconfirmed. Cancelled/expired records cannot be accepted. Same-key changed details return 409 `idempotency_conflict`; an abandoned creation requires a fresh key after cleanup. A timeout is never evidence of success.

Lists return `{orders:[],hasMore:false}`. Transferred records are omitted so the frontend displays the authoritative Order record instead. These scheduling lists currently return all matching records; regular Order histories retain their existing pagination. Main screen polling merges them on the first page. All availability lookups use Supplier's authenticated internal catalogue. A malformed/unavailable dependency fails the request rather than producing an empty successful availability response.

After transfer, an identical creation retry uses **303 See Other** to `/api/v1/orders/{id}`. Follow the redirect to retrieve the current Order-owned snapshot with the current user's session; do not treat Schedule's retained historical record as current lifecycle state. The main frontend follows this automatically. Detail GET uses a 307 redirect to the same protected Order route.

While cleanup of an uncertain reservation or failed save remains unresolved, retries return 503 `creation_cleanup_pending` and must keep the same key. Only after Credit confirms the abort fence does a retry return 409 `creation_abandoned`, allowing a fresh attempt. Neither response claims successful scheduling.

## Internal acceptance events

Acceptance commits `ScheduledOrderAcceptanceRequested` (schema 1) to `schedule_events` alongside pending state. The event identity is the stable transfer ID; its schedule reference points to immutable stored request/courier data. The private `workflow-schedule-acceptance` consumer resolves Supplier dependencies before its SQL transaction and applies Order's import using that same transaction. Import, assignment, lifecycle events, transfer state and receipt commit atomically. Transient failures leave pending work and sanitized retry metadata; definitive rejections are persisted. The public API still returns 202 pending, and transferred details/creation retries retain their 307/303 redirects.

The previous authenticated private HTTP import adapter remains available for compatibility and existing regression coverage, with `ORDER_SCHEDULE_INTERNAL_TOKEN` if explicitly configured. The embedded module does not call it or require this token. It uses the same durable `scheduled_imports` outcome records, so pending legacy transfers can safely replay after offline migration. This adapter is never routed through the gateway.
