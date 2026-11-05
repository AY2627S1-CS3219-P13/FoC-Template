# Synthetic data and transition to real observations

Planning guidance for the campus-only service. Initially there is no historical
customer or deliverer training data. Synthetic data encodes assumptions; it does
not establish actual willingness to pay or deliverer acceptance. See the
[training plan](README.md) and [pricing objective and MVP](../README.md).

## Campus-grounded generation

Start with a lightweight probabilistic, discrete-event simulator using the real
campus map and activity schedule. Model implementations remain replaceable behind
the prediction interface.

| Method | Application |
| --- | --- |
| Spatial demand model | Divide campus into zones. Use residences, lecture halls, libraries, food outlets, opening hours and available aggregate footfall counts to assign baseline activity. |
| Non-homogeneous Poisson process | Generate pre-price quote requests with rates varying by zone, time, weekday, meal periods and academic calendar. Consider negative-binomial counts if observed variability exceeds Poisson assumptions. |
| Origin–destination / gravity model | Generate pickup and destination pairs from location attractiveness and travel distance. Foot traffic is a proxy, not delivery demand itself. |
| Agent-based, discrete-event simulation | Simulate deliverers going online, accepting jobs, travelling and becoming available again. Preserve capacity constraints: one active delivery per deliverer in the initial scope. |
| Behavioural choice models | Assume logistic booking and acceptance curves driven by price, wait, pickup distance and duration. Treat coefficients as uncertain hypotheses until calibrated against real behaviour. |

Generate complete scenarios rather than independent columns. Shared context,
such as rain, can affect requests, availability and travel time together. Respect
walkable routes, service boundaries, supplier hours and feasible task sequences.
Sample several plausible parameter settings rather than relying on one invented
configuration; record parameters, assumptions and random seeds.

Activity-based demand generation and synthetic populations are established in
transport simulation. [SUMO](https://sumo.dlr.de/docs/Demand/Activity-based_Demand_Generation.html)
and [MATSim](https://www.matsim.org/files/book/partOne-latest.pdf) provide reference
approaches; adopting either full framework is not required for this MVP.

## Collection and gradual replacement

1. Keep real and synthetic records distinguishable and append new observations
   without overwriting history. Record source, event time, schema version and
   simulator version/scenario where applicable. Train from versioned snapshots;
   correct or deduplicate records through a traceable ingestion process.
2. Collect the full funnel: quotes including unbooked quotes, offered prices,
   bookings, deliverer offers/rejections, availability and completion times.
   Completed deliveries alone create selection bias. Track model/policy versions
   and features available at decision time, without recording secrets.
3. Retrain periodically using cumulative real observations with mature outcome
   windows. Do not label a pending response or delivery as a rejection/failure.
   Recalibrate the simulator from observed evidence.
4. Compare synthetic-only, mixed and real-only candidates. Reduce synthetic
   influence through sample weights or sampling ratios, selected using held-out,
   later-period real-only validation. Normalize source contributions so a large
   synthetic dataset cannot overwhelm real evidence merely through row count.
5. Phase out by coverage across zone, time, price and order type. Busy lunch
   periods may become data-rich before quiet evenings. A fixed calendar schedule
   or total real row count alone does not establish adequate coverage.
6. Prevent leakage between training and evaluation windows and do not reuse the
   final real test set for repeated mixture tuning. Select and release a validated
   model through the normal MLflow/container pipeline.

An alternative is Bayesian updating: encode initial assumptions as weak priors
and let real observations progressively dominate, rather than manufacturing many
rows that repeat the same assumptions. Prior strength still requires validation.

## What to retain

Keep versioned synthetic scenarios for regression tests, rare events, capacity
stress tests and simulator-based policy comparisons. Include synthetic records
in production training only when they improve real-data validation performance;
they never count as evidence of real-world prediction accuracy.

For launch, use a transparent baseline pricing policy while collecting evidence.
Real price-response calibration needs observations across prices, ideally from
controlled variation within agreed bounds. Synthetic behaviour alone cannot
identify causal price sensitivity or guarantee fulfilment/earnings targets.

## Decisions for the next pass

- Choose zones, route source, population/activity proxies and calendar inputs;
  document uncertainty in all initial request and supply rates.
- Define simulator parameter ranges, behavioural assumptions and launch baseline.
- Define event contracts and permitted data collection/retention through owning
  services, without directly reading their databases.
- Agree on outcome windows, validation splits, coverage thresholds, mixture
  selection metrics and controlled price-variation design.
