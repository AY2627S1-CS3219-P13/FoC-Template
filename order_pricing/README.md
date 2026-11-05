# Order pricing

Planning only: no service, training jobs or AWS resources are implemented yet.

## Context and boundaries

Provide model-backed order pricing through a stable interface, independently of
model architecture. Initial assumptions: customer price equals deliverer payout;
each delivery serves exactly one request. Model algorithms are intentionally
unspecified. Prediction and price selection are separate responsibilities; the
location of the pricing optimizer is still to be decided.

Order remains responsible for order lifecycle and Credit for balances and
settlement. This component must not write their databases. Cross-service access
uses authenticated internal HTTP, following the repository's service boundaries.

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

1. Define the caller, prediction contracts and pricing decision ownership.
2. Add containerized local MLflow and a reproducible training/evaluation job.
3. Add the serving adapter, API validation and compatibility checks.
4. Add a local release script: select, package, test, push, deploy and wait for
   ECS stability; support rollback to a previous release.
5. Provision ECR, ECS Fargate, an internal ALB, IAM and monitoring through
   infrastructure as code. Start with health-gated rolling deployments.

## Decisions for the next pass

- Is the API prediction-only, or does it also return an optimized price? Who
  owns the objective, fulfilment/earnings targets and infeasible-case policy?
- Which service calls it, and how are input features assembled and authenticated?
- What are the units (including credits versus currency), area boundaries,
  prediction horizons, required fields and output meanings?
- How are historical data, outcomes and any controlled price variation collected
  without accessing another service's database directly?
- Define latency/availability targets, timeout/fallback behaviour, promotion
  criteria and AWS budget/region. See component documents for narrower decisions.
