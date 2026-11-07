# Nearby errand alerts (Web Push) — design

Status: implemented (migration 012, `alerts.go`, `webpush.go`, `web/sw.js`); API.md has the contract. Builds on nearest-pickup sorting
(campus coordinates from Supplier, `distanceMeters` in `internal/order/proximity.go`).

## 1. Purpose and scope

A courier opts in to "alert me when a new errand appears near my starting
location". Order Service sends a Web Push notification that the courier's
browser shows as a system notification, even when the FoC site is closed.
Tapping it opens **Help a friend**, where all nearby errands are listed.

N2H mapping: N1 Communication — "Real-time status update push notifications to
user"; N1 Courier enhancements. It adds a new event consumer to the existing
event-driven workflow (M6).

In scope: regular errands (created through `POST /api/v1/orders`); Chrome,
Edge and Firefox on desktop and Android; Safari on macOS 13+. Out of scope for this iteration:
scheduled requests (section 11), live courier location (excluded by the
project brief) and iOS home-screen installation (section 13).

## 2. Team decisions

| Topic | Decision |
| --- | --- |
| Delivery channel | True Web Push (service worker + browser push service), not email or in-page only |
| Implementation | Go standard library for RFC 8291 encryption and RFC 8292 VAPID; no new module dependency |
| Preference owner | Order Service, keyed by the User UUID from the validated session |
| Preference granularity | One preference per courier, shared by all their devices |
| Radius | Fixed set: 200, 500 or 1000 m |
| Rate limit | Per-courier cooldown of 2 minutes. Missed alerts are acceptable: the notification leads to the full list |
| Delivery history | Delete delivery rows 7 days after creation |
| Busy couriers | Skipped when matching if they already have three active orders, the acceptance limit; no cooldown is used |
| Browsers | Chrome, Edge, Firefox and Safari on macOS (Apple's push host is allowed; the team tests it on a Mac) |
| VAPID contact | `https://github.com/AY2627S1-CS3219-P13/FoC-Template`, the team's public repository |

## 3. Architecture

```text
POST /api/v1/orders ──► orders + order_events(OrderCreated)      one transaction (existing)
                                   │
          ┌────────────────────────┼─────────────────────────────┐
  workflow-publication     workflow-settlement      workflow-nearby-alerts  (NEW consumer)
  (SSE feed, existing)     (Credit, existing)        coordinates snapshot taken before the pass
                                                     match pickup ↔ preferences, apply cooldown
                                                     insert push_deliveries (one per device)
                                                                   │
                                                     push sender loop (NEW, every 1 s)
                                                     claim → re-check order → encrypt + sign → POST
                                                                   │ HTTPS
                                                     browser push service (FCM / Mozilla / WNS / Apple)
                                                                   │
                                                     /orders/sw.js → system notification → Help a friend
```

Matching runs inside the existing workflow framework (`ConsumeWorkflow`): per
order ordering, per-consumer receipts, advisory locks and exponential retry are
reused unchanged. As with settlement, network calls never run inside a workflow
transaction; the consumer only writes delivery jobs and a separate loop sends them.

## 4. Data model (migration `012_nearby_alerts.sql`)

```sql
CREATE TABLE nearby_alert_preferences (
    user_id uuid PRIMARY KEY,                 -- User UUID from the session; no cross-service FK
    enabled boolean NOT NULL,
    location_id text NOT NULL,                -- Supplier campus location ID, checked when saved
    radius_meters integer NOT NULL CHECK (radius_meters IN (200, 500, 1000)),
    last_alerted_at timestamptz,              -- cooldown
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE push_subscriptions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id uuid NOT NULL,
    endpoint text NOT NULL UNIQUE,            -- secret capability URL; never logged or returned
    p256dh bytea NOT NULL CHECK (length(p256dh) = 65),
    auth bytea NOT NULL CHECK (length(auth) = 16),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX push_subscriptions_user ON push_subscriptions(user_id);

CREATE TABLE push_deliveries (
    event_id text NOT NULL REFERENCES order_events(event_id),
    subscription_id bigint NOT NULL REFERENCES push_subscriptions(id) ON DELETE CASCADE,
    order_id text NOT NULL,
    distance_meters integer NOT NULL CHECK (distance_meters >= 0),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sent','failed','skipped')),
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_error text,                          -- short category only
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (event_id, subscription_id)   -- an event can never alert a device twice
);
CREATE INDEX push_deliveries_due ON push_deliveries(next_attempt_at) WHERE status = 'pending';

-- Migration 009 allows only the three existing consumer names; widen it (009 stays unedited).
ALTER TABLE workflow_receipts DROP CONSTRAINT workflow_receipts_consumer_name_check,
    ADD CONSTRAINT workflow_receipts_consumer_name_check CHECK (consumer_name IN
        ('workflow-timers','workflow-settlement','workflow-publication','workflow-nearby-alerts'));

-- Existing orders must never alert: acknowledge every current event for the new consumer.
INSERT INTO workflow_receipts(consumer_name, event_id)
SELECT 'workflow-nearby-alerts', event_id FROM order_events WHERE domain_type IS NOT NULL;
```

`endpoint` is unique, so a browser shared by two accounts belongs to whichever
account enabled alerts on it last. A courier keeps at most 5 devices; adding a
sixth replaces the oldest.

## 5. API

All routes use the existing session validation, exact `Origin` check for
mutations, JSON-only bodies with unknown fields rejected, and
`{"error":{"code","message"}}` errors. The gateway's `/api/v1/orders/` route
already covers them.

| Method and path | Body / response | Notes |
| --- | --- | --- |
| `GET /api/v1/orders/alerts` | `{pushAvailable, vapidPublicKey, preference: {enabled, locationId, radiusMeters} \| null, devices}` | `pushAvailable:false` when not configured |
| `PUT /api/v1/orders/alerts` | `{enabled, locationId, radiusMeters}` | Location checked through Supplier with the user's session; radius ∈ {200,500,1000} → `400 invalid_radius` / `invalid_location` |
| `PUT /api/v1/orders/alerts/subscription` | `{endpoint, keys: {p256dh, auth}}` → 204 | Upsert by endpoint; validated per section 9 |
| `DELETE /api/v1/orders/alerts/subscription` | `{endpoint}` → 204 | Removes only the caller's own row |

When push is not configured, the mutations return `503 push_unavailable`.

## 6. Matching consumer `workflow-nearby-alerts`

Registered in `workflow.go` beside the existing three consumers and shown in
`workflow_status.go`, so the admin publication-status view reports its backlog.

1. Before each pass, the loop fetches a coordinate snapshot from Supplier's
   internal `/internal/v1/locations` with the service token (no user session).
   If Supplier is unavailable, the pass is skipped and events stay unacknowledged.
2. Every domain event is acknowledged in order. Only `OrderCreated` has an
   effect; acceptance, cancellation, expiry and later events are acknowledged
   with no alert. An order gets exactly one `OrderCreated` (no transition
   returns an order to `open`, and legacy bootstrap emits no events), so an
   errand can trigger matching only once.
3. For `OrderCreated`, inside the consumer transaction:
   - skip unless the order is still `open`, unexpired and was created under 10
     minutes ago, using the order's own `created_at` (stale alerts are worse
     than none when the consumer catches up after an outage);
   - resolve the pickup's campus location (supplier's registered location or the
     location itself); skip if it has no coordinates;
   - select enabled preferences of other users whose `distanceMeters(location,
     pickup) <= radius_meters` and whose `last_alerted_at` is null or at least
     2 minutes old, locking those rows;
   - leave out couriers who already have three active orders (`accepted` or
     `picked up`), the same limit acceptance enforces with `409 courier_busy`;
     they keep their cooldown unused;
   - insert one `push_deliveries` row per device (`ON CONFLICT DO NOTHING`) and
     set `last_alerted_at` for couriers that received at least one row.
4. If push is not configured, events are acknowledged without effect.

## 7. Push sender loop

Runs every second in `cmd/server/main.go`, like the other workers.

1. **Claim:** select up to 20 due `pending` rows `FOR UPDATE SKIP LOCKED` and push
   their `next_attempt_at` 30 seconds ahead as a lease, then commit. Replicas
   never claim the same row at the same time.
2. **Re-check:** if the order is no longer open or has expired, mark `skipped`.
3. **Build** the payload (at most 3 KB of JSON):
   `{"v":1,"orderId":"…","title":"New errand nearby","body":"Campus Cafe (COM2) · about 320 m from your alert location. Open Help a friend to see all nearby errands.","url":"/orders?view=open&order=…"}`
4. **Encrypt** per RFC 8291 (`aes128gcm`): ephemeral P-256 ECDH with the
   browser's key, HKDF-SHA-256 with its auth secret, AES-128-GCM, one record.
5. **Sign** a VAPID JWT per RFC 8292 (ES256; `aud` = push service origin, `exp`
   = 1 h, `sub` = configured contact), reused per audience for 30 minutes.
6. **POST** to the endpoint with `Content-Encoding: aes128gcm`, `TTL` = seconds
   until the errand expires, `Urgency: high`, `Topic` = 32-character hash of the
   order ID (a newer message for the same order replaces an undelivered one),
   `Authorization: vapid t=…, k=…`. Timeout 5 s; redirects are not followed.

| Push service response | Result |
| --- | --- |
| 201 / 202 | `sent` |
| 404 / 410 | Device unsubscribed: delete the subscription (its deliveries cascade) |
| 429, 5xx, timeout, network error | Retry after `min(60, 2^attempts)` s (or `Retry-After`, capped at 60 s); `failed` after 5 attempts |
| 400 / 401 / 403 / 413 | `failed`, no retry; 401/403 usually means a VAPID key mismatch |

Delivery is at least once. A crash after sending but before recording can resend
once after the lease expires; `Topic` and the notification `tag` collapse the
duplicate on the device. Every 10 minutes the loop deletes non-pending rows
older than 7 days.

## 8. Browser

- `/orders/sw.js` is a service worker served by `ui.go`. Its scope is `/orders/`
  and it needs no gateway change. On `push` it shows the notification with
  `tag` = order ID; on `notificationclick` it focuses an open Orders tab and
  posts it the order ID, or opens the payload URL. The `/orders` page sits
  outside that scope, which push does not need, so the page waits for the
  worker to activate before subscribing.
- `/orders?view=open&order=<id>` opens Help a friend and selects that errand.
- **Nearby alerts** panel in Help a friend: on/off toggle, location (prefilled
  from **Start from**) and radius 200/500/1000 m. Enabling runs:
  permission request → `register('/orders/sw.js')` → `pushManager.subscribe({
  userVisibleOnly: true, applicationServerKey })` → `PUT …/subscription` →
  `PUT /alerts`.
- **Turn off on this device** unsubscribes the browser and deletes its row; when
  it was the last device, the account preference is switched off too.
- The saved preference fills the location and radius until the courier changes
  them, even when this browser has a different **Start from**. Browsers can drop
  a subscription themselves (site permission reset, "turn off" from a
  notification); the two-second refresh notices it and offers to turn alerts on again.
- The panel shows clear states: unsupported browser, insecure origin,
  permission blocked (with how to re-enable), service unavailable, on for this
  device, and on for the account but not this device.
- CSP adds `worker-src 'self'`; everything else stays same-origin.

## 9. Security and privacy

- **SSRF:** the endpoint is supplied by the browser and the server POSTs to it.
  Require `https`, port 443, no user info, no IP literal, at most 1024
  characters, and a host on the allowlist: `fcm.googleapis.com`,
  `updates.push.services.mozilla.com`, `*.notify.windows.com`,
  `web.push.apple.com`.
- `p256dh` must decode to a valid uncompressed P-256 point; `auth` to 16 bytes.
- Endpoints and keys are never logged or returned by any API. Logs record only
  outcome categories.
- The payload is end-to-end encrypted; push services see only size and timing.
  It contains place names and a distance, never items, credits or user IDs.
- The VAPID private key exists only in the git-ignored `.env`. Its public key is
  not secret.
- Signing out does not unsubscribe a device by itself. Couriers turn alerts off
  per device; a shared browser moves to whoever enables alerts there next.

## 10. Configuration

| Variable | Rule |
| --- | --- |
| `ORDER_VAPID_PRIVATE_KEY` | Base64 PKCS#8 DER EC P-256 key; the public key is derived from it |
| `ORDER_VAPID_SUBJECT` | `mailto:` with a real address, or an `https://` URL; `init-env.sh` defaults it to `https://github.com/AY2627S1-CS3219-P13/FoC-Template` |

Both are optional, but they must be set together, and they also require the
existing `ORDER_SUPPLIER_INTERNAL_URL`/token. An invalid value stops startup,
following the repository convention.

`scripts/init-env.sh` appends a key to `order-service/.env` when one is missing,
including on existing installations:

```sh
openssl ecparam -name prime256v1 -genkey -noout \
  | openssl pkcs8 -topk8 -nocrypt -outform DER | base64 | tr -d '\n'
```

`compose.yaml` passes both variables through. On EC2, run the same script or set
the values in `/opt/foc/order-service/.env`. Keep the key stable: changing it
invalidates every subscription, so a rotation must also clear
`push_subscriptions`.

## 11. Scheduled requests (not covered)

Scheduled requests live in Schedule Order Service, in status `scheduled`, from
creation until a courier accepts them. Order Service first sees one at
acceptance: `importScheduled` (`schedule.go`) records `OrderCreated` and moves
it to `accepted` in the same transaction. That `OrderCreated` therefore
describes an errand that is already taken. The consumer's "still open" check
skips it, so no wrong alert is sent, but scheduled requests do not trigger
alerts either.

Covering them would need Schedule Order Service to notify Order (for example a
private, service-authenticated "scheduled request published" call from its own
durable worker), which Order would match through the same preferences and
sender. That is a cross-service contract owned by the Schedule feature, left as
a follow-up.

## 12. Failure handling and observability

| Failure | Behaviour |
| --- | --- |
| Supplier unavailable during matching | Pass skipped; events retried next second; 10-minute freshness cap |
| Push service slow or down | Exponential retry up to 5 attempts; other deliveries continue |
| Device unsubscribed or browser data cleared | 404/410 removes the subscription |
| Order accepted or cancelled before sending | `skipped` at re-check |
| Process crash mid-send | Lease expires and the row is retried; at most one duplicate, collapsed on device |

Admin `GET /api/v1/orders/publication-status` gains the consumer's
pending/failed counts (automatically) and push delivery counts (pending, sent
and failed over 7 days). Logs warn with the consumer name or delivery category only.

## 13. Testing

| Layer | Coverage |
| --- | --- |
| Go unit | RFC 8291 Appendix A test vector reproduced byte for byte; VAPID JWT verifies against the derived public key; endpoint allowlist rejects `http`, IP literals, other ports and hosts; radius validation |
| Go integration (fake push service via `httptest`) | Radius matching; requester excluded; cooldown; replayed event sends nothing twice; existing events never alert after migration; 429/5xx retry and give-up; 404/410 deletes subscription; skipped when accepted or expired; API session, Origin and validation; 7-day cleanup |
| Node (`core.test.mjs`) | Panel states, subscription body, deep link; the real `sw.js` run in a VM: payload → notification, unsafe URLs and malformed payloads fall back, clicks reuse an Orders tab or open the errand |
| Playwright (`alerts.spec.mjs`) | Turn on, persist across reload, change radius, turn off; blocked permission; server without push; another device; notification deep link. A real service worker registers; only `PushManager` and the permission prompt are stubbed, since headless Chromium has no push service. The spec reaches the fixture through `localhost`, a secure origin |
| Manual | Real delivery through FCM in desktop Chrome; Firefox; Safari on macOS 13+ (team Mac) |

iOS requires "Add to Home Screen" and a web-app manifest; it is out of scope
unless a team member can test on iOS 16.4+.

## 14. Demo

Public HTTPS ingress is not active yet (it waits on real SMTP), so the demo runs
on a laptop. `localhost` and an SSM tunnel are secure origins.

1. Chrome profile B (courier): enable alerts for **Central Library, 500 m**, then
   close the Orders tab (Chrome keeps running).
2. Chrome profile A (requester): create an errand from a COM2 supplier (~320 m).
3. A system notification appears within a few seconds; clicking it opens Help a
   friend with that errand selected.
4. Create an errand at Prince George's Park (~800 m): no notification arrives.
5. Show the consumer in publication-status and the delivery log lines.

With ingress active, the same flow works on an Android phone through the
CloudFront URL.

## 15. Build order

1. Migration 012, configuration and `init-env.sh`.
2. Web Push crypto (RFC 8291/8292) with its test vectors.
3. Alerts API and subscription validation.
4. Matching consumer.
5. Sender loop, retries and cleanup.
6. Service worker, alerts panel and deep link.
7. Tests, `API.md`/`README.md`, AWS notes.

## 16. Open items

None. The VAPID contact, busy-courier handling and Safari support are recorded
in section 2. If the repository is made private or renamed, change
`ORDER_VAPID_SUBJECT` to a team mailbox: the contact must stay reachable.
