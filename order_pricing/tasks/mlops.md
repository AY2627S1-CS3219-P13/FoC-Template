# Agent task: reproducible training, selection and model export

Implement local MLOps in `order_pricing/mlflow/` and focused tests under
`order_pricing/tests/mlops/`. Read root AGENTS.md, Pricing API.md, contracts/README.md,
shared DTOs/schemas/tests, mlflow/README.md and synthetic-data.md first. Serving and
formula agents work concurrently. Preserve their work; do not edit serving,
formula, shared contracts/tests, root Compose or CI. Raise interface changes with
the parent and communicate training/export commands early.

Build a containerized, pinned local training/evaluation environment and persistent
local MLflow tracking/artifacts. Add reproducible synthetic dataset generation,
versioned manifests, deterministic seeds, train/validation separation and provenance.
Implement an actual small trained candidate, explicit evaluation/promotion criteria
and exact run/version selection; never auto-promote the newest run. With no real
behavioural data, a synthetic effort-price surrogate is acceptable to exercise the
pipeline, but label it synthetic and experimental, not evidence of optimal prices,
causal price response or real-world booking/completion performance.

Export preprocessing and inference/price-selection code behind the same
load_policy(Path), warm_up(PolicyInput), calculate(PolicyInput), close interface.
Return final 5–30 integer credits and feature/provenance diagnostics. Keep inference
offline/reproducible and independent of FastAPI/MLflow tracking server. Prefer a
small serializable model with stdlib inference if suitable; MLflow pyfunc is
optional. Preserve missing quantity, use the package's baseline for stale/missing
Order context with warnings, and raise UnsupportedRoute for unsupported routes.

The formula agent exports self-contained policy/formula.py and
artifacts/config.json; copy these into model packages and delegate fallback rather
than importing another live service. Export model entryPoint policy.model:load_policy.
Include manifest.json, fully pinned requirements.lock, policy/, artifacts/model.json
(or equivalent), artifacts/context.json and artifacts/warmup.json. Static context
contains routes, footfall and static source stamps. Hash every payload file;
declare adapter/context major 1, capabilities, feature schema, bounds and actual
code/dataset/MLflow run provenance. Package dependencies install at image build,
never runtime. Output package and selection manifest must be immutable after export.

Add an authenticated training-feed consumer with durable local cursor/dedup and
atomic ingestion (no cursor advancement before local commit; 410 requires explicit
reconciliation). Do not invent rejection/abandonment labels from absent events.
Document what real-data labels/evaluation remain unavailable. Model updates mean
export + a new serving image, not mutating files inside a running replica. Provide
a repeatable local train/evaluate/select/export command for the parent integration
test and focused tests for reproducibility, train/serve preprocessing parity,
invalid export/selection, compatibility and feed resume/dedup.

Run container checks, update mlflow README with exact commands and limitations,
and report changed paths, package build dependencies and results. Do not provision
AWS, modify sibling services or weaken shared tests. No host tooling installation;
Docker socket access may require escalation.
