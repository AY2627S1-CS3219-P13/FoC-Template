# Agent task: first relative-effort policy

Historical implementation brief. The later user-approved traffic revision is
specified in `formula/FOOT_TRAFFIC.md`; it supersedes the no-footfall-adjustment
rule below and selects `formula/releases/effort-traffic-001` as the default.
The original `formula/package` remains an immutable effort-only release.

Implement the first formula policy and release builder in `order_pricing/formula/`
and focused tests in `order_pricing/tests/formula/`. Read root AGENTS.md,
order_pricing/README.md, API.md, contracts/README.md, Python DTOs/schemas and shared
policy/formula conformance suites first. Other agents implement serving and MLOps
concurrently. Preserve their work; do not edit shared contracts/tests, serving,
mlflow, root Compose or CI. Coordinate through the parent if interfaces must change.

Implement load_policy(Path), warm_up(PolicyInput), calculate(PolicyInput) and
idempotent close using the existing shared DTOs. No FastAPI objects, network/DB
calls, user/draft identity features, wall-clock business features or training.
Provide reproducible whole-credit recommendations bounded inclusively to 5–30.
Use walkable distance converted to travel time, explicit approach/pickup time
assumptions and a modest capped quantity adjustment. Do not charge distance and
derived travel time twice. Unknown quantity remains unknown with its warning;
stale/unavailable Order context gives baseline warnings. Initial footfall changes
must not change price. Missing supported route raises UnsupportedRoute. Validate
all coefficients/static assets at load; invalid configuration must fail startup.

Select and document conservative configurable initial coefficients consistent
with prior illustrative base=2 credits and 15 credits/hour, explicitly labelled
uncalibrated launch defaults. Document representative short/typical/difficult
examples, bounds, rounding, quantity cap and feature diagnostics/provenance.
No fulfilment, availability, conversion or earnings guarantees.

Export a complete immutable policy package under `order_pricing/formula/package/`
and provide a reproducible CLI builder (caller supplies code commit/release ID).
Package contains manifest.json, requirements.lock (empty if stdlib only),
policy/formula.py (entryPoint policy.formula:load_policy), artifacts/config.json,
artifacts/context.json (routes/footfall plus static source stamps) and
artifacts/warmup.json (shared fixture PolicyInput). Manifest hashes every payload
file, declares adapter/context major 1, supported required capabilities, feature
schema, bounds and truthful code/dataset provenance. Synthetic campus routes are
explicitly examples, not a production map. Never invent real observations.

MLOps needs a reusable baseline in its exported model package: keep formula loader
self-contained in policy/formula.py plus config.json, so its exporter can copy
these files into a model release and delegate stale-context fallback. Notify the
parent and MLOps agent when this interface is ready.

Add meaningful unit tests for coefficients, exact examples, bounds, rounding,
quantity handling, no-double-counting diagnostics and no footfall surcharge. Run
shared policy and formula conformance against the real package in containers.
Document how to build/test/export and report results, file paths and integration
requirements. Do not weaken existing tests. No host tooling installation; Docker
socket access may require escalation.
