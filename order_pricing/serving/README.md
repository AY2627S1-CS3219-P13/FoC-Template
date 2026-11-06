# Pricing serving framework

Implemented: a private FastAPI quote/event service, asynchronous source collectors,
the shared Python policy adapter, PostgreSQL issuance/events, health/readiness and
immutable policy-image packaging. The [API contract](../API.md) and
[adapter/context contract](../contracts/README.md) define its boundaries.

Order-facing proxy/UI and source API changes in sibling services remain separate
work. The local Pricing integration stack uses explicitly synthetic source mocks;
it does not establish a production campus map, real outcome capture or model quality.

## Boundary and policy replacement

Order sends `QuoteRequest`, containing only its business inputs. The framework
normalizes it, resolves locations and combines it with asynchronously collected
catalogue/Order snapshots and release-owned static context into immutable
`PolicyInput`. It calls the offline policy, validates `PolicyResult`, then issues
`QuoteResponse` only after recording the quote and calculation event atomically.
Model-specific feature engineering stays inside the policy package.

Unknown fields are ignored recursively; omitted quantity stays unknown. Input
expiry instants are normalized to UTC before fingerprinting. Adapter/context major
and capability checks fail startup before loading unsupported packages. Formula
and trained packages use the same interface; no MLflow tracking server is needed
in serving. A package must be trusted: checksum validation establishes integrity,
not trust in executable code.

Build the formula image from the repository root:

```sh
docker build -f order_pricing/serving/Dockerfile --target runtime -t foc-pricing-serving order_pricing
```

To build an exported model instead, add `--build-arg POLICY_PACKAGE=<directory>`;
the directory is relative to `order_pricing`. The build installs both pinned
framework and policy dependencies and checks their compatibility. It bundles
`manifest.json`, `policy/`, and `artifacts/context.json`/`warmup.json` in the image.
One release is loaded/warmed per process. Updating a policy means a new image and
readiness-gated deployment; running files are not replaced.

## Configuration

Configuration is validated at startup. Supply production secrets through the
deployment secret mechanism; never commit them or expose them to browsers.

| Variable | Meaning |
| --- | --- |
| `PRICING_DATABASE_URL` | Pricing-owned PostgreSQL connection URL. |
| `PRICING_PACKAGE_DIR` | Trusted immutable package directory; default `/opt/policy`. |
| `PRICING_QUOTE_TOKEN` | Order quote bearer token; caller scope is `order`. |
| `PRICING_ORDER_WRITE_TOKEN` | Separate Order event-ingestion token. |
| `PRICING_EXPORT_TOKEN` | Separate read-only export token. |
| `PRICING_SUPPLIER_URL`, `PRICING_SUPPLIER_TOKEN` | Supplier base URL and read-only source credential. |
| `PRICING_ORDER_URL`, `PRICING_ORDER_TOKEN` | Order base URL and read-only source credential. |
| `PRICING_CATALOGUE_REFRESH_SECONDS` | Default 60; optional 0.1–3600 for controlled tests. |
| `PRICING_ORDER_REFRESH_SECONDS` | Default 15; optional 0.1–3600 for controlled tests. |
| `PRICING_SOURCE_PROVENANCE` | `observed` for actual projections; `synthetic` for test sources. |

Write/quote/export credentials must be distinct and cannot reuse source read
credentials. Base URLs may include a prefix; collectors append the internal
contract endpoint. Health needs no token and must remain private. Run non-root
with read-only filesystem, `cap_drop: [ALL]` and no-new-privileges. The runtime
listens on 8080; no public gateway route is added here.

## Context, deadlines and failures

Collectors poll with +/-20% jitter, cap source bodies at 4 MiB, validate whole
snapshots and atomically replace them. Invalid, future or regressing source times
and failed requests preserve prior data. Freshness is source-asOf based: catalogue
maximum age is ten minutes; Order maximum age is sixty seconds. Missing/stale
catalogue prevents quotes/readiness. Missing Order data is explicitly unavailable;
stale data retains its counts/timestamps for the policy's baseline fallback.
Static route/footfall provenance is retained from the package; source provenance
comes from the configured source environment.

Quote requests have a 1500 ms total budget, with policy results rejected if
calculation exceeds one second. There is one active issuance per process, at most
eight worker operations and thirty-two active HTTP work requests. Worker slots
remain occupied after a request timeout until the actual thread finishes; late
inference output is discarded. Health remains responsive during saturation.
A database commit racing a response timeout may still finish durably; callers
retry the same key and receive that original result. No success precedes commit.

Errors use the sanitized envelope. Invalid output is rejected, not silently
clipped. Missing supported routes return `unsupported_place`; capacity returns
`rate_limited` with `Retry-After`. Expected policy unavailability returns
`quote_unavailable`; recording failure returns `quote_recording_unavailable`.

Readiness is cached from background storage checks plus catalogue freshness;
probes do not fan out to sources. PostgreSQL startup/migration retries happen in
the background. Shutdown stops readiness/collection, drains work and closes the
policy after its live inference worker completes.

## Durability and events

Numbered SQL migrations live in `pricing_server/migrations`; startup serializes
migrations across replicas. Tables are `pricing_quotes`, `pricing_events` and
`pricing_schema_migrations`. Transaction advisory locks serialize scoped keys
across replicas. Responses replay after expiry and release changes; unknown
request fields do not change fingerprints. Records are retained indefinitely in
this first implementation, preserving at least the 24-hour replay window.

Event batches validate before one transaction, deduplicate by `(source,eventId)`,
and roll back entirely on conflicts. Export includes quote calculations and Order
events, permits out-of-order linkage, and supplies opaque cursors/recordedAt.
Event positions are allocated under a transaction lock retained through commit,
so later commits cannot appear behind an exported cursor. Calculation records
archive full context, release-owned features and source stamps. Order amounts
remain decimal strings; absent outcomes are not interpreted as rejections.

## Checks and remaining rollout gates

```sh
docker build -f order_pricing/serving/Dockerfile --target tests -t foc-pricing-serving-tests order_pricing
docker run --rm --network none --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges foc-pricing-serving-tests
```

Focused tests cover normalization, immutable input, event amounts/privacy, source
validation, durable-response semantics using fault-injected storage doubles,
invalid/slow policy handling, stale catalogue and retained worker capacity.
Real PostgreSQL/two-replica/release-swap checks run in the dedicated Pricing
integration stack. The [shared HTTP suite](../tests/README.md) can target a ready
test app. In-memory storage is only a test double; runtime uses PostgreSQL.

Still required before production pilot: authoritative sibling source endpoints,
Order proxy/UI/outbox integration, real campus references, deployment secrets and
networks, backup/export archival and operational retention/reconciliation policy.
Indefinite database retention is not a backup strategy. AWS infrastructure,
promotion/rollback automation and service-level latency/quality alarms are not
provisioned by this local framework. Acceptance gates must remain truthful about
those missing integrations.
