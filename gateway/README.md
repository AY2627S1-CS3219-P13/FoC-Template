# Shared gateway

Nginx routes browser requests to User, Supplier, Credit, Order and the frontend
using their normal service names on the existing Compose network.

## Authentication rate limits

The gateway uses the connection's client IP (`$binary_remote_addr`), never a
client-supplied `X-Forwarded-For` or `X-Real-IP`, to share one authentication budget
across `/api/v1/auth/` (except logout), `/api/v1/users/me/password`, and
`/api/v1/admin/users/` role-change routes. Nginx permits an initial burst of 60
requests, then replenishes at 60 per minute (one per second). This is a leaky-bucket
policy, not User's former fixed one-minute window. Excess traffic immediately
receives JSON `429 rate_limited`, `Retry-After: 1` and `Cache-Control: no-store`.
Ordinary reads, Order polling, other service routes and logout are outside this
budget. Two clients behind the same NAT still share its public IP budget.

User separately enforces PostgreSQL-backed email/account limits and a four-slot
password-work cap. Consequently, changing IP addresses cannot reset the ten
login attempts per email per ten minutes. Account counters are shared across
User replicas; Nginx IP counters are local to each gateway instance and reset
when it restarts. Additional gateway replicas need an explicit distributed quota
policy if a fleet-wide IP budget is required.

Root Compose keeps loopback backend ports for development; direct requests bypass
the gateway IP limit but retain User's account limits. Production must expose only
the gateway to public clients. If an ingress/load balancer precedes Nginx, configure
its trusted client-address handling there before treating its forwarded address
as a client identity; by default this policy counts the connecting peer.

## Regression check

After starting a disposable real stack and running normal smoke, run:

```sh
sh scripts/gateway-rate-check.sh
```

CI runs this check after smoke. Three temporary Node clients have distinct Docker
addresses without fixed IPs or additional networks. One exhausts its quota while
spoofing forwarding headers; another still registers with 202. The check verifies
JSON/retry headers, protected write routes, unaffected reads/logout, and an account
limit shared across two client IPs. The unique pending signup fixture and clients
are removed on exit; no admin promotion or bootstrap claim is needed by this check.
User's backend suite separately checks removal of the shared proxy-address quota,
retained account limits and password-work capacity.
