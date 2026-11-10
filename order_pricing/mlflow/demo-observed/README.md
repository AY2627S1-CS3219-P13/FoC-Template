# Fake observed data (demo only)

**Every value here is invented.** These files have exactly the shape of the real
one-off exports so the observed-data path can be demonstrated end to end; they are
not real orders and are not evidence of real acceptance or price response.

- `orders.csv`: 100 orders in the format of [../sql/extract-orders.sql](../sql/extract-orders.sql)
  (Postgres CSV timestamps, supplier and location pickups, accepted / expired /
  cancelled outcomes, one retired location that the converter skips).
- `pricing-events.jsonl`: `QuoteCalculated` envelopes in Pricing's training-export
  format: 1–3 draft revisions before about two thirds of the orders, one competing
  draft that makes a link ambiguous, and drafts that never became orders.

The invented acceptance truth rewards credits above a rough walking-effort
estimate and is busier in the evening. Regenerate deterministically with:

```sh
docker run --rm --network none --read-only --tmpfs /tmp --user "$(id -u):$(id -g)" --cap-drop ALL \
  -v "$PWD/order_pricing/mlflow/demo-observed:/out" foc-pricing-mlops python mlflow/demo-observed/generate.py /out
```

Demo retraining:

```sh
sh order_pricing/scripts/retrain.sh --release-id acceptance-gbm-demo --quick \
  --orders order_pricing/mlflow/demo-observed/orders.csv \
  --pricing-events order_pricing/mlflow/demo-observed/pricing-events.jsonl
```

Do not merge a release trained on this data: unstage `order_pricing/models/acceptance-gbm-demo`
and restore `order_pricing/RELEASE_PACKAGE` after the demo.
