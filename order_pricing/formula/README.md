# Relative-effort baseline

The first real policy is implemented as an offline standard-library Python module
behind the shared [Pricing adapter](../contracts/README.md). It uses no learned
price model. The default release now adds the bounded relative-traffic adjustment
documented in [FOOT_TRAFFIC.md](FOOT_TRAFFIC.md). The original checked-in
`package/` is an unchanged effort-only release with synthetic campus reference data,
not a production campus routing map or calibrated pricing evidence.

## Policy and initial assumptions

```text
travelMinutes = walkableMetres / walkingMetresPerMinute
totalEffortMinutes = approachMinutes + pickupMinutes + travelMinutes
effortCredits = creditsPerHour * totalEffortMinutes / 60
handlingCredits = extraItemCredits * min(max(quantity - 1, 0), maxExtraItems)
price = clamp(ceil(baseCredits + effortCredits + handlingCredits), 5, 30)
```

The equation above describes the effort-only core/earlier release. The current
`releases/effort-traffic-001` multiplies its unrounded result by a local traffic
factor from 1 to 1.2 before rounding and bounding. [The lookup, hourly factors and
revised equation](FOOT_TRAFFIC.md) define the current behaviour.

The uncalibrated launch defaults in `artifacts/config.json` are:

| Coefficient | Default | Meaning |
| --- | --- | --- |
| `baseCredits` | 2 | Fixed allowance for taking a task. |
| `creditsPerHour` | 15 | Value of estimated task time, not guaranteed net earnings. |
| `walkingMetresPerMinute` | 75 | Assumed walking speed. |
| `approachMinutes` | 5 | Assumed approach time; accepting courier location is unknown. |
| `pickupMinutes` | 3 | Assumed waiting/pickup time. |
| `extraItemCredits` | 0.5 | Carrying allowance per additional item; not added handling time. |
| `maxExtraItems` | 6 | Caps the carrying adjustment at 3 credits with defaults. |
| `minimumCredits`, `maximumCredits` | 5, 30 | Inclusive recommendation bounds for this first policy. |

Travel distance is charged exactly once through travel time. Quantity adds a
carrying allowance without also adding time. Missing quantity remains unknown:
its diagnostic quantity/handling fields are null, no carrying allowance is added,
and `unknown_item_quantity` is emitted. It is never converted to quantity one.
Decimal arithmetic precedes upward integer rounding, then the bounds apply.

| Example | Walkable metres | Items | Effort minutes | Recommendation |
| --- | ---: | ---: | ---: | ---: |
| Short | 150 | 1 | 10 | 5 |
| Typical | 900 | 2 | 20 | 8 |
| Shared contract route | 1,800 | 2 | 32 | 11 |
| Long with carrying allowance | 3,600 | 7 | 56 | 19 |
| Outside ordinary campus effort | 9,000 | 20 | 128 | 30, capped |

These examples test the policy's assumptions. They do not establish willingness
to pay, acceptance, fulfilment or earnings. The credit/dollar comparison is only
price intuition, not a redemption or conversion promise. Bounds affect advice,
not the user's manually chosen offer. Calibration against real outcomes remains
required before claiming effectiveness.

## Adapter and diagnostics

`policy/formula.py` exports `load_policy(Path)`, returning an implementation of
`warm_up(PolicyInput)`, `calculate(PolicyInput)` and idempotent `close()`. The loader
validates all configured coefficients, directed routes, static source stamps and
footfall references. Invalid assets fail startup. The framework separately checks
the release inventory/checksums and major/capability compatibility before import.

The framework passes an immutable enriched context. This policy looks up the
directed route by resolved campus location IDs. A release with a nonzero
`walkingDetourFactor` (see below) falls back to catalogue coordinates when no
reference route exists; otherwise a missing route raises `UnsupportedRoute`. It never makes a source call, reads a clock for features,
writes storage or uses draft identity. Stale/missing Order context returns the
same baseline with `stale_order_context`/`order_context_unavailable` warnings.
Legacy people-count footfall values and Order counts do not affect the effort
core. The current release's separate estimated index affects its final price.

The earlier private `effort-1` feature snapshot contains distance, travel/approach/pickup/
total effort minutes, quantity-known state, nullable quantity/carrying allowance,
effort credits, unrounded credits and source provenance. Assumed effort is marked
`estimated`; synthetic routes retain `synthetic` provenance. Snapshots are
recursively immutable. No observed courier presence is inferred.

`load_config(package_dir)` and `effort_features(value, config)` are reusable helpers
for model-package preprocessing. Config returned by `load_config` is validated,
immutable and uses Decimal coefficients. A model exporter can copy this single
module and its config/static assets into its own release for baseline fallback.
The model release owns its final feature schema and must return that schema ID,
including when it wraps a baseline result.

The current helper accepts an optional third argument containing the validated
relative-traffic lookup. Its `effort-traffic-1` features add the lookup identity,
local time, effective relative index and bounded multiplier. The builder includes
`artifacts/foot_traffic.json` in the checksum inventory. It accepts `--foot-traffic`
to select an explicit versioned lookup. Updated serving builds and integration
training select `formula/releases/effort-traffic-001` by default.

## Build and test in containers

From the repository root:

```sh
docker build -f order_pricing/formula/Dockerfile -t foc-pricing-formula-tests order_pricing
docker run --rm --network none --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges foc-pricing-formula-tests
```

This runs focused unit/export/traffic tests and shared adapter/formula conformance
checks. Only the test image installs shared test dependencies; the exported
formula runtime has an empty `requirements.lock` and needs no MLflow runtime.

Run the builder from the `order_pricing` working directory inside a container:

```sh
python -m formula.build --output /out/effort-002 --code-commit FULL_GIT_COMMIT --release-id effort-002
```

That is a container command, not host Python. Supply a new writable output
directory and the actual code commit. The builder refuses to overwrite an existing
release and stages the complete export before renaming. `--config` accepts a
coefficient JSON file; `--fixture` accepts an explicitly synthetic shared input.
The default fixture is `tests/fixtures/policy-input.json`. All payload files are
hashed in the manifest. The checked-in example records its base Git commit plus
the actual exported source SHA-256, so uncommitted source is identifiable.

The release layout is `manifest.json`, `requirements.lock`, `policy/formula.py`,
`policy/__init__.py` and `artifacts/{config,context,warmup}.json`. Static context has
routes/footfall and their source stamps; warm-up is serialized PolicyInput. Mount
or COPY the selected release read-only at the serving package path. Never modify
a running release; rebuild and deploy a new image.

## Coordinate route estimates

Reference routes cannot keep up with an admin-edited location catalogue, so
`releases/effort-traffic-002` estimates unlisted pairs from Supplier's location
coordinates (contract v1.2, capability `coordinates.v1`):

```text
straightLineMetres = haversine(pickup coordinates, delivery coordinates)
walkableMetres     = straightLineMetres * walkingDetourFactor   # default 1.3
```

The 1.3 detour factor is an uncalibrated assumption for campus paths, stairs and
covered walkways; it must stay within 1–2, and 0 or an omitted value disables the
estimate (older releases are unchanged). A listed reference route always wins.
Locations without coordinates still raise `UnsupportedRoute`. Estimated routes
add `routeMethod`, `straightLineMetres` and `walkingDetourFactor` to the
`effort-traffic-coordinates-1` feature snapshot and mark `routeProvenance` as
`estimated`. Supplier coordinates are approximate building positions, so prices
for very short hops are dominated by the fixed approach/pickup minutes.

Straight-line estimates were chosen over a routing API: Google Maps' Routes API
would need a billed key, network egress from the private Pricing container and a
per-pair cache, and NUSMods publishes venue coordinates but no walking routes.
A measured route table can later be shipped as reference routes without changing
the fallback.

Build a release from a committed policy with the coordinate warm-up fixture:

```sh
python -m formula.build --output formula/releases/<new-id> --release-id <new-id> \
  --code-commit <full commit containing formula/policy> --fixture formula/data/coordinate-warmup.json
```

