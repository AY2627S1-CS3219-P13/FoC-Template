# Order pricing

Local implementations include the reusable FastAPI framework, relative-effort
formula package and reproducible synthetic training/selection/export pipeline.
See the [shared adapter/context decisions](contracts/README.md),
[formula](formula/README.md), [serving](serving/README.md) and
[training](mlflow/README.md). AWS resources and the planned Order/UI/Supplier
source integrations are not implemented here.

The foundation separates the Order-facing quote contract from an enriched internal
policy contract. The reusable serving framework owns authentication, asynchronous
context collection and durable quote/event issuance; formula and model packages
own feature preparation and price decisions. Versioned fixtures and conformance
tests let these components evolve independently. Immutable policy images can be
replaced while shared PostgreSQL preserves quote replay and training history.
The local training/export workflow exercises that replacement without making
production serving depend on a training process or MLflow tracking server.
The [v1 integration contract](API.md) is the shared specification for parallel
implementation. Pricing's private endpoints are implemented by the serving
component; sibling-service additions in that contract remain separate work.

## Context and boundaries

Provide model-backed order pricing through a stable interface, independently of
model architecture. Initial assumptions: customer price equals deliverer payout;
each delivery serves exactly one request. Model algorithms are intentionally
unspecified. Pricing owns predictors and the pricing optimizer behind one advisory
quote API. Order owns the user-selected offer and remains the creation authority.

Order remains responsible for order lifecycle and Credit for balances and
settlement. This component must not write their databases. Cross-service access
uses authenticated internal HTTP, following the repository's service boundaries.

## First prediction container decision

The first container uses a transparent relative-effort pricing policy, not a
trained price model. Estimate effort from walkable route/travel time, documented
pickup/approach-time assumptions and a modest item-handling adjustment. Avoid
counting distance and its derived travel time twice. Missing quantity remains
unknown. The [relative foot-traffic policy](formula/FOOT_TRAFFIC.md) adds a bounded
premium for quieter pickup locations/times using an estimated local lookup and
Singapore-time profile. It is separate from physical effort and is not observed
courier availability. The original effort-only release remains available.

Use 5–30 inclusive whole credits as the initial recommendation bounds. Treat
1 credit approximately like 1 dollar for price intuition only, not as a redemption
or conversion promise. The range is a policy expectation, not evidence of
willingness to pay or accept. Typical deliveries need not span the entire range.
These bounds apply to recommendations, not the user's manually selected offer.

Choose formula coefficients using representative short, typical and difficult
deliveries before implementation is accepted. Exact coefficients, route data and
time assumptions remain to be agreed; earlier numerical examples are illustrative.
Synthetic scenarios test consistency and sensitivity, not real-world price
effectiveness. Outcome-based price optimization is a later replacement, not a
required validation stage for the first formula.

## Illustrative pricing objective and MVP scope

An example objective, pending agreement on targets and decision ownership:

```text
Choose the lowest customer price p such that:
  P(order fulfilled | price = p) >= target fulfilment probability
  E(deliverer net earnings/hour | price = p) >= minimum hourly earnings
```

Customer price equals deliverer payout under the initial assumptions. Account
for pickup, delivery, idle time and operating costs when defining net hourly
earnings. Specify a fallback when no candidate price meets both constraints.

Suggested MVP predictions, without choosing model implementations:

- Pre-price quote-request volume by area and time. Booking probability converts
  those requests to bookings; do not count the price effect twice.
- Available deliverer count by area and time.
- Customer booking probability conditional on price, demand, supply and order
  details.
- Deliverer acceptance probability conditional on the offered payout and job
  context; availability alone does not imply acceptance.
- Pickup and total service duration, linking orders to earnings and capacity.

Add cancellation/completion prediction if cancellations materially affect
fulfilment. Longer-term supply response to price can extend the supply forecast.
Bootstrap assumes no historical customer or deliverer training data; data
collection and cold-start calibration need to be designed before model release.

## Architecture

```text
LOCAL (containers)
Data snapshot -> training/evaluation -> local MLflow -> selected model version
                                                           |
                               build/test serving image <--+
                                           |
                          local release script pushes to ECR
                                           |
AWS                                        v
                         new ECS task definition + service update
                                           |
Caller -> stable internal ALB endpoint -> ECS Fargate serving containers
                                               ^
Order / Supplier internal snapshots -> async collectors -> context cache
```

- [mlflow/](mlflow/README.md): local experiment tracking, artifacts, training and
  selection. Persist MLflow state outside ephemeral containers.
- [serving/](serving/README.md): stable prediction API and a model adapter,
  packaged with the selected model and dependencies in an immutable image.
- No production dependency on the local MLflow server. ECS deployment is
  explicitly triggered after the push; ECR pushes alone do not deploy.
- Track API/schema version independently from model version. Record training
  run, model version, data snapshot, code commit and image digest per release.

## Implementation plan

The three implementation briefs are [serving](tasks/serving.md),
[formula](tasks/formula.md) and [MLOps](tasks/mlops.md). They share the versioned
adapter and have separate component/conformance tests.

Run local validation from the repository root:

```sh
sh order_pricing/scripts/check.sh
```

This runs fixtures/contracts, formula checks, MLOps checks and a fresh synthetic
training/export before building formula/model serving images. An isolated Compose
stack then exercises real PostgreSQL and HTTP replicas with synthetic source
APIs. It verifies the policy interface across images, shared replay, source
fallback, event recording/export, training ingestion, restart recovery and a
recording-failure injection. It publishes no ports and removes its test stack;
generated training runs/packages remain gitignored in `.integration/` for review.

Next: implement the real source/Order/UI integrations and durable producers;
calibrate the baseline and validate models on observed outcomes; finalize
archival/retention and operational policy; then add image promotion, AWS
infrastructure, monitoring and tested deployment rollback. Synthetic training
demonstrates the pipeline and cannot establish real pricing effectiveness.

## Decisions for the next pass

- Choose baseline coefficients within the initial 5–30 recommendation bounds,
  fulfilment/earnings targets and model validation thresholds; synthetic supply
  assumptions need calibration.
- Implement source snapshot APIs, durable quote/outcome recording and authenticated
  export per the contract before claiming complete pilot training capture.
- Define measured availability targets, promotion criteria, archival/retention,
  rate limits and AWS budget/region. See the contract and component documents for
  narrower decisions. Scheduled pricing and mandatory floors remain follow-ups.

## Local Order form integration

From the repository root, `sh scripts/start.sh` now starts the optional local
Pricing stack with `compose.yaml` and `compose.pricing.yaml`. Order proxies quotes
and serves a debounced suggestion panel at `http://localhost:3000/orders`.
Supplier provides the full internal catalogue projection; Order provides immediate
activity aggregates on its private listener. Browser requests carry only the
session cookie; service credentials stay private. Quote and source credentials
are initialized by `sh order_pricing/scripts/init-env.sh` in the ignored root `.env`.

The `local-demo` serving image builds a separate policy with synthetic distances
for all fifteen existing location IDs. Their ordering and 150 m steps are arbitrary
UI fixtures, not campus geography. Package provenance stays synthetic for routes;
live Supplier/Order projections remain observed. The form displays credits and
freshness without a demo label. Existing immutable policy packages are unchanged. The demo
stage must not be used for cloud publishing. EC2 release wiring is implemented separately from the local override; validated
campus routes, creation quote linkage and durable Order training producers remain
outstanding. See the cloud deployment notes below.

Inspect with `docker compose -f compose.yaml -f compose.pricing.yaml ps`.
Pricing has no published host port. Its database persists in the `pricing-data`
volume. Continue using both Compose files for local restarts; plain root Compose
starts the application without Pricing integration.

Price suggestions need no expiry input and update automatically every 30 seconds
while the immediate-order editor is visible. The manual suggestion-refresh button
has been removed; expiry remains required only for actual order creation.

## CI/CD and EC2

Pricing's component checks already run in CI. The release publisher now includes
`foc/pricing-service`, using the `runtime` Docker target and the required repository
variable `PRICING_POLICY_PACKAGE` to choose a committed release directory. The
existing Pricing CI checks continue to validate the framework and policy packages.
The EC2 override adds private Pricing/PostgreSQL, persistent storage, credential initialization,
readiness checks and immutable-image verification. Local development still uses
the separate `local-demo` target through `compose.pricing.yaml`.

Before enabling the cloud release, provision the ECR repository/access and select
a policy package with validated campus routes. The current checked-in example
and the generated local UI fixtures are not a production campus map. See
[Pricing release wiring](../docs/aws-deployment-notes.md#pricing-release-wiring)
for prerequisites and verification. Training capture remains separate work.
