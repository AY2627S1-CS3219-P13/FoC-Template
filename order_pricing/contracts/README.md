# Shared Pricing contracts

Status: agreed implementation boundary with shared fixtures and component
implementations in serving, formula and mlflow. Shared scaffolding alone does not
establish component correctness; run the complete [checks](../tests/README.md).
The external [API](../API.md) remains normative.

## Names and ownership

| Name | Boundary | Owner and contents |
| --- | --- | --- |
| `QuoteRequest` | Order -> framework, JSON | Caller facts from API section 3; no model features or source context. |
| `PolicyInput` | Framework -> policy, Python DTO | Normalized request, calculation time, resolved campus locations and a pinned immutable `ContextSnapshot`. |
| `PolicyResult` | Policy -> framework, Python DTO | Final recommended integer credits, mode, warning codes and versioned feature/provenance diagnostics. Not an issued quote. |
| `QuoteResponse` | Framework -> Order, JSON | API section 3 response. Framework adds quote identity, draft, timestamps, release/context IDs; merges warnings and durably commits before responding. |
| Model features | Inside policy only | Release-owned preprocessing output; never a shared framework or source-service contract. |

`schemas/v1.2.json` specifies the current serializable representation. v1.1 made
quote `orderExpiresAt` optional without changing older request semantics; v1.2
adds optional, paired WGS84 `latitude`/`longitude` to catalogue locations
(capability `coordinates.v1`). Missing coordinates stay omitted, and policies
must treat them as unknown. The
original `schemas/v1.json` remains the compatibility baseline. `python/pricing_contracts.py` defines the Python adapter DTOs and protocol.
Python uses snake_case attributes; serialized fixtures and HTTP use camelCase.
The framework decodes HTTP into normalized domain values, not FastAPI objects
passed to policies. It resolves supplier/location identity using the catalogue;
route/time, effort, availability estimates and feature engineering belong to the
policy. The policy must not use draft identity as a pricing feature.

## Context and compatibility

`ContextSnapshot` holds the validated catalogue, Order aggregates with explicit
freshness/availability, versioned route/footfall reference data and source stamps.
Reference data ships with the policy release; dynamic snapshots come from async
collectors. A request pins the complete release and snapshot. Policy code performs
no network/database reads and does not read the wall clock for business features.
Framework code recursively freezes mappings/sequences before invoking the policy.

The HTTP API, adapter, context and release-owned feature schema have separate
versions. Start adapter/context at `1.0`. Within a major version:

- New readers accept all older minor-version fixtures; old readers ignore unknown
  fields recursively, including additive context fields and source metadata.
- New fields are optional with a documented missing-value policy. A minor release
  never adds a mandatory input, reinterprets units or changes existing defaults.
- Missing quantity means unknown; missing optional fields stay omitted rather than
  being converted to null, zero or one. Unknown fields are excluded from normalized
  request fingerprints and policy inputs. Never strip release-owned feature data.
- Defined enums reject unknown values, except extensible warning codes and public
  response `pricingMode` as specified by the API. Enum expansion requiring new
  behaviour needs capability negotiation or a major version.
- Unavailable Order context has no aggregate payload. Stale context retains its
  actual payload and source time; it cannot be interpreted as fresh zero counts.
- Breaking changes require a new major and explicit migration. A loader rejects an
  unsupported major or missing required capability before readiness, not mid-quote.
- Manifests declare required capabilities and adapter/context major versions.
  Additive optional data does not imply a policy can require it. Adding a new
  capability requires the supporting framework to ship before the dependent policy.

The internal context's string `schemaVersion` is separate from the integer
`schemaVersion: 1` on Supplier/Order source API projections. Collectors validate
those source contracts before the framework assembles the enriched context.
Future Python DTO additions must also have backward-compatible defaults; schemas
alone do not make a newly required constructor argument compatible.

The v1 compatibility fixtures include an old/minimal input and a future-minor
input with unknown nested fields. Both must produce the same normalized business
values. Tests retain the original v1 schema as a compatibility baseline; do not
rewrite it to make a breaking change pass. Add a new version and migration tests.

## Adapter and lifecycle

Each package exports `load_policy(package_dir: Path) -> PricingPolicy`, then:

```python
policy.warm_up(example: PolicyInput) -> None
policy.calculate(value: PolicyInput) -> PolicyResult
policy.close() -> None
```

Load validates assets/configuration; warm-up is representative inference without
quote issuance or storage writes. Close is idempotent. FastAPI lifespan owns these
calls. Formula and model packages share this interface; MLflow pyfunc is optional
behind it. Repeated calculation with fixed input/release must be reproducible;
any stochastic algorithm uses an explicitly recorded deterministic seed.

`PolicyUnavailable` signals expected calculation unavailability; `UnsupportedRoute`
signals no supported route. Framework maps these to the API's sanitized 503
`quote_unavailable` and 422 `unsupported_place`. Missing/stale required catalogue
is rejected by the framework as 503 `context_unavailable`. Missing/stale Order
context invokes the release's baseline and applicable warning. Unexpected errors
return sanitized 503; a policy output outside configured bounds is rejected,
never silently clipped by the framework. The formula itself may apply its declared
bounded pricing rule. Successful quote/event persistence remains mandatory.

Use one active calculation per process initially, bounded admission and synchronous
inference off the event loop. Calculation budget is 1 second within the API's
1500 ms total Pricing budget; persistence and transport consume the remainder.
Measure and revisit allocation before pilot. Timing out a thread does not stop it:
keep its slot occupied until it finishes, discard late output and never accumulate
unbounded work. A timed-out request cannot issue an unrecorded successful quote;
retries retain their key and follow shared issuance semantics.

## Release package and MLOps

```text
package/
  manifest.json
  requirements.lock
  policy/                 # loader + preprocessing + inference/optimizer code
  artifacts/              # selected model/formula assets and static references
```

The manifest declares `entryPoint` (`module:load_policy`), release identity,
adapter/context majors, required capabilities, feature schema version, bounds,
provenance (code commit, dataset IDs and model runs, possibly empty for formula)
and SHA-256 hashes of every payload file. `requirements.lock` pins all runtime
dependencies; it can be empty for a standard-library-only formula. The lock file
uses one exact `name==version` requirement per non-comment line, including
transitive dependencies; the shared export gate rejects ranges and remote URLs.
The manifest does not hash itself; image digest is recorded by the deployment
release record after build, avoiding circular hashes. Verify paths/hashes before loading trusted
code. No remote artifact or dependency downloads at runtime.

MLOps exports an explicitly selected package implementing this adapter, not just
a raw estimator. Train-time preprocessing and inference preprocessing are packaged
together. Build a new immutable image using a compatible framework and dependency
lock, test it, then deploy through readiness/rollback. Do not replace files in a
running replica. Shared issuance/events survive image replacement. Migration and
rollback must retain old records and support old/new replicas concurrently.

## Component acceptance gates

The executable tests and fixtures live in [tests](../tests/README.md). A passing
scaffolding suite validates contracts/test infrastructure, not an unbuilt service.

| Component | Required before implementation is accepted |
| --- | --- |
| Shared contracts | Old/new minor readers, unknown nested fields ignored, missing values preserved, unsupported majors rejected, immutable DTOs and distinct HTTP/policy types. |
| Serving | API validation/auth, normalization, async context/pinning, lifecycle/readiness, bounded execution, output checks, shared replay/concurrency, atomic quote/event commit, sanitized failures and HTTP compatibility. |
| Formula policy | Adapter conformance; explicit effort assumptions/coefficients; 5–30 whole-credit output; reproducibility; no distance/time double charge; unknown quantity and stale Order fallback; versioned relative-traffic lookup with Singapore-time factors and at most 20% pre-rounding premium. Preserve the earlier effort-only release. |
| MLOps/export | Loadable complete package, compatible manifest, safe artifact paths/checksums, locked dependencies, startup/warm-up, identical preprocessing offline/serving, reproducible lineage and explicit promotion/rollback. Synthetic evaluation never establishes real price effectiveness. |

The complete local check runs the shared adapter suite against the actual formula
and exported model, then exercises HTTP replicas and PostgreSQL with source API
fixtures. Production source/Order/UI wiring, calibration, archival and AWS
deployment gates remain separate; `tests/acceptance.json` records that inventory.

With v1.1, an omitted quote expiry stays absent from normalized requests and
policy input. Serving issues 120-second advice without inventing an order deadline;
optional expiry from older clients still caps freshness. New expiry-free clients
require updated serving; old serving returns an advisory failure after rollback.
