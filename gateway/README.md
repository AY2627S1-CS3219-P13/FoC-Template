# Shared gateway

Nginx routes browser requests to User, Supplier, Credit, Order and the frontend
using their normal service names on the existing Compose network.

## Authentication rate limits

By default the gateway uses the connection's client IP (`$binary_remote_addr`),
ignoring supplied `X-Forwarded-For` or `X-Real-IP`, to share one authentication budget
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

## CloudFront and internal ALB ingress

The EC2 deployment can mount `cloudfront-ingress.conf` over the default
`local-ingress.conf`. This opts into trusting the two private ALB/CloudFront ENI
subnets, `10.20.16.0/24` and `10.20.17.0/24`. The EC2 security group must accept
gateway port 3000 only from the ALB security group, and the internal ALB must
accept HTTP only from CloudFront's VPC-origin service security group. Review both
CIDR lists in the file if these infrastructure subnets change.

CloudFront appends the viewer IP to `X-Forwarded-For`; ALB appends its connecting
CloudFront peer. Recursive Nginx real-IP handling skips trusted private hops and
selects the rightmost untrusted address. A forged prefix therefore cannot choose
the rate-limit identity. Requests from untrusted peers still use their socket IP.
The resulting address is forwarded as `X-Real-IP` and a normalized
`X-Forwarded-For`, without retaining a caller-supplied identity chain.

`CloudFront-Forwarded-Proto` is accepted only when the original connecting peer
is trusted and the value is exactly `http` or `https`; other requests use the
gateway's connection scheme. This preserves the viewer's HTTPS scheme through
the two private HTTP hops. The public distribution uses `CachingDisabled` and
`AllViewerAndCloudFrontHeaders-2022-06` so cookies, Host, Origin, query strings and
the CloudFront protocol header reach the application. All seven HTTP methods
must be allowed. See the root AWS deployment notes for the EC2 environment keys.

`sh scripts/check-cloudfront-ingress.sh` uses disposable Nginx, an echo upstream
and isolated trusted/untrusted Docker networks. It tests protocol/Host handling,
forged header rejection, independent viewer quotas and health checks without
accessing application accounts or databases. CI runs it after the normal gateway
regression check. Local development continues using the no-trust policy.

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
