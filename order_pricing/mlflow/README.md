# Local training and MLflow

This directory implements reproducible experimental training, explicit candidate
selection, immutable model-package export and durable training-feed ingestion.
The initial model distils synthetic effort assumptions; it is not evidence of
real customer/courier price response or a production model recommendation.
See the [overall plan](../README.md) and [serving plan](../serving/README.md).

With no historical customer or deliverer data, follow the
[synthetic data and transition plan](synthetic-data.md) for campus-grounded
simulation, real-data collection and gradual replacement.
The [v1 integration contract](../API.md) defines quote/outcome linkage, immutable
context capture and authenticated training export. Training consumes these
records without reading sibling services' databases.

## Architecture

Run training and MLflow in containers with pinned dependencies. Keep experiment
metadata, registry state and artifacts in persistent local storage. MLflow tracks
parameters, metrics, artifacts and model versions; a separate job performs the
actual training. AWS serving does not contact this local tracking server.

```text
Versioned data snapshot -> train -> evaluate -> register candidate in MLflow
                                                    |
                                         explicit release selection
                                                    |
                           export exact model package to serving image build
```

Package preprocessing, feature ordering, model artifacts and inference code
together. MLflow `pyfunc` can provide the model-loading implementation behind the
shared pricing-policy adapter; it is not required for the initial formula policy.
Its model signature supplements the explicit adapter/API contracts, rather than
defining business semantics by itself. Dependency requirements are packaged too;
architecture changes may require rebuilding the serving runtime.

Training and export can start with versioned fixtures while FastAPI is being
built. Implement the agreed [adapter/context and release-package contract](../contracts/README.md)
and run the [shared adapter/export tests](../tests/README.md). Shared I/O/context
must remain compatible in both directions within a major version; model-specific
features remain release-owned.
Export an explicitly selected package for a new immutable serving image, rather
than updating model files in a running container.

## Implemented local workflow

Build from the repository root; all Python tooling remains inside containers:

```sh
docker build -f order_pricing/mlflow/Dockerfile -t foc-pricing-mlops order_pricing
docker run --rm --network none --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges foc-pricing-mlops python -m unittest discover -s tests/mlops -v
```

The default formula release must already exist in
`formula/releases/effort-traffic-001` before image build.
Use the formula builder documented in [formula/README.md](../formula/README.md).
The model's serving runtime is standard-library-only; MLflow client dependencies
exist only in this training image. `requirements.lock` pins transitive training
dependencies and the shared test lock supplies validation dependencies.

The CLI has separate `train`, `select`, `export` and `consume` commands. For
example, these are container commands, with a full real Git SHA replacing SHA:

```sh
python -m pricing_mlops train --work-dir /data/work --formula-package /app/formula/releases/effort-traffic-001 --code-commit SHA --seed 42
python -m pricing_mlops select --work-dir /data/work --run-id EXACT_RUN_ID --selection /data/selected.json
python -m pricing_mlops export --work-dir /data/work --selection /data/selected.json --formula-package /app/formula/releases/effort-traffic-001 --output /data/model-package --release-id synthetic-model-001
```

Train defaults to persistent file tracking at `/data/work/mlruns`; an explicit
`--tracking-uri` or `MLFLOW_TRACKING_URI` selects a server instead. Mount `/data`
as a named volume or an appropriately owned bind directory. Selection requires an
exact MLflow run ID and checks synthetic holdout MAE <= 0.05 credits and discrete
price agreement >= 0.98. It writes an exclusive selection file with
`productionApproved: false`. These thresholds measure fidelity to a chosen
synthetic teacher, not economic effectiveness. Selection/export never overwrite
an existing selection/release, and export rechecks candidate, dataset and baseline
integrity. No process silently selects the newest run.

For integration exercises only, `demo` runs training, selects that exact returned
run ID and exports an experimental package in one invocation:

```sh
docker run --rm --network none --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges -v foc-pricing-training:/data foc-pricing-mlops python -m pricing_mlops demo --work-dir /data/work --formula-package /app/formula/releases/effort-traffic-001 --code-commit SHA --output /data/model-package --release-id synthetic-model-001
```

Final stdout is JSON with `runId`, candidate path, metrics, selection path, output
and release ID. A second exercise needs a new output path/release ID. This helper
is not automatic production promotion. Build a new serving image with the exported
package to update inference; production serving never contacts MLflow.

The synthetic generator records scenario IDs, seed, teacher configuration, rows
checksum, generator version and dataset identity. Validation indices are excluded
from fitting. A real ordinary-least-squares fit learns coefficients from training
rows for travel minutes and handling credits; inference shares exact feature
functions with the packaged formula. Unknown quantity remains explicit in
diagnostics; omitting its handling allowance is the declared missing-value policy,
not an invented quantity. The exported package includes the copied baseline for
stale/unavailable Order context, context/warm-up examples, model JSON, selection
record and complete SHA-256 manifest. No pickle or runtime artifact downloads.

## Acceptance model (LightGBM)

The first learned pricing model predicts P(accepted before expiry | context, price)
for created orders and chooses the price. Scope and assumptions are in
[synthetic-data.md](synthetic-data.md#implemented-generators-acceptance-gbm-1).

- Data: `simulate` writes a versioned dataset (records, shared context, manifest)
  from the causal-graph and agent-based generators. Cancelled-before-acceptance
  orders are censored and excluded from training, not counted as rejections.
- Features (`acceptance_policy.py`, exported as `policy/acceptance.py`): price,
  price minus effort credits, route/effort/quantity, Singapore hour and weekday,
  relative foot traffic and pickup open/created order counts. Training builds rows
  through the exported functions, so serving uses identical preprocessing.
- Model: LightGBM binary classifier, monotone increasing in both price features,
  deterministic single-threaded training, each generator weighted to half the total.
- Price choice: every whole credit 5..30 is scored as
  `P(accept) - lambda * (price - 5) / 25`; the best wins, ties go to the lower
  price. Lambda is calibrated on held-out parameter batches so the mean predicted
  acceptance at the chosen prices is 0.8.
- Metrics (MLflow experiment `foc-acceptance-gbm`): generator-balanced holdout log
  loss/Brier/AUC, leave-one-generator-out log loss/AUC, monotonicity violations,
  mean chosen/baseline credits and the lambda sweep.
- Selection gates: no monotonicity violations, holdout log loss below the base
  rate, and mean chosen-price acceptance within 0.03 of 0.8.

```sh
python -m pricing_mlops simulate --output /data/datasets/seed-42 --formula-package /app/formula/releases/effort-traffic-002 --seed 42
python -m pricing_mlops train --kind acceptance-gbm --dataset /data/datasets/seed-42 --work-dir /data/work --formula-package /app/formula/releases/effort-traffic-002 --code-commit SHA --seed 42
python -m pricing_mlops select --work-dir /data/work --run-id EXACT_RUN_ID --selection /data/acceptance.selection.json
python -m pricing_mlops export --work-dir /data/work --selection /data/acceptance.selection.json --formula-package /app/formula/releases/effort-traffic-002 --output /data/acceptance-gbm-001 --release-id acceptance-gbm-001
```

`select` and `export` dispatch on the candidate/selection `kind`; candidates without
one are the effort-distillation model. The package's `requirements.lock` is
`release-requirements.lock` (LightGBM, NumPy, SciPy); serving and training images
install `libgomp1` for LightGBM. The model is the native LightGBM text format, not
a pickle. Stale or unavailable Order context falls back to the copied baseline.

## Local MLflow server

```sh
docker compose -f order_pricing/mlflow/compose.yaml up --build -d mlflow
```

The pinned MLflow server is available on `http://127.0.0.1:5000`, using a persistent
SQLite tracking database and artifact volume. The training Compose service uses
`MLFLOW_TRACKING_URI=http://mlflow:5000`; its `/data` work volume is separate.
Keep this unauthenticated local server on its loopback/private network. Configure
authentication/TLS before remote use. Back up both metadata and artifacts;
persistent volumes alone are not backups. Offline file-tracked runs are separate
from server-tracked runs; they are not automatically imported into the server.

## Training-feed archive

`consume` polls the read-only Pricing export using `PRICING_EXPORT_TOKEN` from the
container environment; credentials are not written into the archive. Example
container command:

```sh
python -m pricing_mlops consume --database /data/events.sqlite --base-url https://pricing.example
```

For a private local HTTP test network, explicitly add `--allow-insecure-local`.
The SQLite archive commits all events and its opaque cursor atomically with FULL
synchronous durability. It deduplicates normalized known event fields, preserves
original payloads and model-owned feature snapshots, rejects immutable conflicts
and quarantines unknown schemas/types/statuses. Additive unknown fields do not
turn a replay into a conflict. The consumer creates no rejection/abandonment labels.
HTTP 410 stops consumption and requires explicit reconciliation from archived
history; it never resets the cursor silently.

Default page size is 1 because quote feature records can be large; configure
`--page-size 1..100` and `--max-response-bytes` (default 64 MiB) to match actual
records. An oversized/failed page never advances the cursor; retry with a smaller
page or a larger justified bound. Poll again when `hasMore` is false: later records
may arrive. Schedule polling externally; the command performs one bounded page.

## Remaining implementation work

The acceptance model is trained only on synthetic orders. Real records should
reuse the synthetic record shape (quote input + confirmed offer + outcome) so
the same `prepare` path trains on them. Real-data label joins, mature outcome
windows, real-only evaluation, causal price
response calibration, scheduled retraining and AWS release automation remain
follow-ups. The current synthetic generator exercises effort-price distillation;
it is not the complete campus behavioural simulator described in synthetic-data.md.

The current model learns the effort core only. A deterministic policy then applies
the same versioned [relative-traffic multiplier](../formula/FOOT_TRAFFIC.md) as the
baseline; synthetic core MAE/agreement is not validation of this multiplier.
Model feature schema is `synthetic-effort-model-2`. Selection/export pins the
entire baseline manifest, including traffic assets, against changes after training.

## Decisions for the next pass

- Define source APIs/exports/events, labels, outcome delays, retention and handling
  of missing data. Establish historical availability of any new input fields.
- Choose evaluation windows, leakage safeguards, acceptance thresholds and who
  selects releases. Price-response predictions require a causal validation plan.
- Choose local tracking/registry backend, artifact paths and backup/restore policy;
  persistent volumes alone are not backups.
- Define retraining cadence/triggers and hardware requirements. Shared MLflow
  hosting is a later option if multiple developers need access.
- Agree on the exact export layout and dependency-lock strategy shared with
  serving. Keep raw datasets, credentials and generated artifacts out of Git.
