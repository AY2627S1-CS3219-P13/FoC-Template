# Pricing contract tests

These are shared fixtures, executable contract/adapter/export checks and component
and integration suites. See the [agreed contract decisions](../contracts/README.md).

The complete local check is `sh order_pricing/scripts/check.sh` from the repository
root. It runs the shared tests first, then real formula/MLOps/serving checks, then
HTTP/PostgreSQL integration and failure/restart probes. It trains and explicitly
selects a synthetic candidate before building a replacement policy image. Source
APIs are synthetic fixtures; actual Order/UI/Supplier integration is still pending.

## Run in containers

From the repository root:

```sh
docker build -f order_pricing/tests/Dockerfile -t foc-pricing-contracts order_pricing
docker run --rm --network none --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges foc-pricing-contracts
```

The same image/commands are part of the complete CI check. No host Python installation is required.
Network is needed at image build time for pinned test dependencies, not at test
runtime. The test image is not a production serving image.

## What runs now

- Schema and DTO tests: distinct HTTP/policy I/O, optional omission, invalid known
  fields, future unknown nested fields, major rejection and immutable input values.
- Compatibility fixtures: `policy-input.json` is the v1 full baseline;
  `compatibility-cases.json` defines named minimal/future/stale/unavailable/invalid
  variants via JSON pointer set/remove operations. These are synthetic examples.
- Output tests: integer/transport/release bounds, feature version agreement,
  extensible public mode/warnings and private diagnostic preservation.
- Export tests: complete checksum inventory, compatibility/capabilities, missing
  or corrupt assets, path traversal, symlinks and inverted bounds.
- Adapter tests: loading/warm-up, reproducibility, unknown quantity, baseline
  fallback, unknown route, draft-independent price, calculation budget, offline
  execution and idempotent close.

By default adapter tests use `fixtures/package`, a deliberately constant-price
test double. Its 8-credit result is not a proposed formula or expected real price.
The manifest uses a synthetic zero commit and test-only lineage. A passing suite
means scaffolding works; it is not evidence that serving or training works.
The full check additionally runs conformance against the actual formula and
exported trained model, instead of relying only on that double. The shared image
selects its three original suites explicitly; it does not discover integration
tests that require live services or component tests with extra dependencies.

## Apply the adapter suite to a real package

Build an image derived from the contract-test image that installs the package's
fully pinned runtime dependencies and copies its release directory to `/candidate`.
Run it with `PRICING_PACKAGE_DIR=/candidate` and command:

```sh
python -m unittest tests.test_policy_conformance -v
```

That command is the container command, not a host Python command. The loader is
read from the package's verified manifest. Never load an untrusted package:
checksum validation establishes integrity, not trust. Include the shared contracts
module in the candidate runtime. The testkit's decoder/freezer/validator are test
helpers; production code must implement the same behaviours and run its own tests.

The policy suite expects the fixture's campus route/location vocabulary and
reference-data shape. Supporting an additional domain or context major requires
new shared fixtures and capability negotiation, not silently changing v1 meaning.
Socket interception catches common accidental network calls; run with network
disabled as well. It does not prove absence of every possible IO side effect.

For the first real formula, also run `python -m unittest tests.formula_conformance -v`
in the candidate container. This explicitly requires `PRICING_PACKAGE_DIR` and
adds distance/quantity monotonicity, a nonconstant effort response, bounded
relative-traffic adjustments and isolation from legacy people-count overrides.
Calibration and no-double-counting gates still require the real
formula's diagnostics and agreed example prices. The default scaffolding command
does not discover this suite or pretend that the test double is a real formula.

## Gates awaiting components

An executable HTTP suite is ready in `framework_conformance.py`. Run
`python -m unittest tests.framework_conformance -v` from a test container with
`PRICING_BASE_URL` and `PRICING_SERVICE_TOKEN` set. Use a dedicated ready test app
with the fixture catalogue/routes, fresh source timestamps and the initial 5–30
release. The runner creates durable test quotes; do not point it at production.
It covers issuance/replay, unknown-field fingerprints, conflicts, omitted quantity,
transport/auth/input failures and health. It requires network access to that test
app, unlike the default offline suite. It does not simulate database failures or
concurrent replicas. Service credentials remain environment-only.

`acceptance.json` retains the complete gate inventory. Focused component suites
cover the implemented behaviours; `tests/integration` uses actual HTTP processes,
PostgreSQL, formula/model images, restart and recording-failure probes, and the
durable training consumer. The source projections are synthetic fixture servers.
Actual Order/UI wiring, production routing/calibration, archival/retention policy,
AWS promotion and full deployment rollback remain external gates. They are not
reported as passing tests merely because the isolated stack or test double passes.
