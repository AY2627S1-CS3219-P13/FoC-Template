# Local training and MLflow

Planning only. This directory will own the local training environment and MLflow
setup; model algorithms and architectures remain behind the serving contract.
See the [overall plan](../README.md) and [serving plan](../serving/README.md).

With no historical customer or deliverer data, follow the
[synthetic data and transition plan](synthetic-data.md) for campus-grounded
simulation, real-data collection and gradual replacement.

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
together using MLflow `pyfunc`. Its `predict()` adapter and model signature expose
consistent inputs/outputs across implementations. Dependency requirements are
packaged too; architecture changes may require rebuilding the serving runtime.
An MLflow signature supplements the explicit API contract, rather than defining
business semantics by itself.

## Implementation plan

1. Add local Compose services, pinned images/dependencies and persistent storage;
   do not install training tools on the host.
2. Define a versioned dataset manifest and deterministic feature/label pipeline.
   Record snapshot identity, training configuration, code commit and seeds.
3. Train, evaluate and register candidates with signature and example inputs.
4. Gate release on agreed quality, compatibility and inference performance
   criteria. Select an exact registered version; never deploy merely because a
   training run completed or a candidate is newest.
5. Export that version for the serving build. Record its provenance in a release
   manifest and validate the resulting image before pushing.
6. Start with manually triggered retraining. Add scheduled batch retraining when
   useful; continuously arriving data does not require online learning.

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
