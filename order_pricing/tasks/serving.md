# Agent task: reusable serving framework

Implement the Pricing framework in `order_pricing/serving/` and its focused tests
in `order_pricing/tests/serving/`. Read root AGENTS.md, order_pricing/API.md,
contracts/README.md, shared Python DTOs/schemas and all shared test suites first.
Other agents implement formula and MLOps concurrently. Preserve existing work;
do not edit their directories, shared contracts/tests, root Compose or CI. Ask the
parent about any contract change; communicate integration interfaces early.

Deliver a real FastAPI application and non-root/read-only-compatible Dockerfile,
with pinned runtime dependencies. Use the existing PolicyInput/PolicyResult
protocol and implement production normalization/freezing/validation; never import
test helpers into runtime. HTTP QuoteRequest/QuoteResponse stay distinct. Ignore
unknown fields recursively, reject invalid known fields/duplicate JSON keys/null,
normalize expiry instants and preserve omitted quantity. Authenticate each private
request with service credentials, sanitize errors and enforce transport limits.

Implement asynchronous Supplier/Order collectors with atomic validated snapshots,
size/time/freshness/regression checks, source bearer credentials and jitter. No
request-time source fan-out. Resolve places, pin immutable context/release, invoke
offline synchronous policy outside event loop with bounded capacity and deadlines.
Timed-out work retains its slot until it finishes. Validate outputs against release
bounds and feature schema. Implement health/readiness and load/warm-up/drain/close.

Use Pricing-owned PostgreSQL for durable issuance/idempotency and training events.
Number migrations; never edit an applied migration. Atomically record quote plus
QuoteCalculated before success. Shared scoped key serialization/replay must survive
expiry, process/release change and concurrent replicas. Implement API section 7
event batch ingestion, dedup/conflicts, separate write/export credentials and an
export feed with committed-prefix positions (serialize allocation through commit).
No process-local durability, sibling DB access or fake successful recording.

Package integration: `PRICING_PACKAGE_DIR=/opt/policy`; each trusted package has
manifest.json, requirements.lock, policy/, artifacts/context.json and
artifacts/warmup.json. context.json contains routes, footfall and their static
source stamps; warmup.json is serialized PolicyInput. Verify manifest paths,
checksums/major versions/capabilities before loading entryPoint. One release per
process. Docker build context is order_pricing; support build argument
POLICY_PACKAGE selecting a release directory to COPY, install its locked runtime
dependencies at build time, and run without runtime downloads. Communicate all
configuration names/entry point/test commands to parent promptly.

Use contract-shaped HTTP source mocks in your focused tests; parent supplies a
PostgreSQL/two-replica integration Compose stack. Do not implement sibling service
or UI changes or provision AWS. Update serving README with truthful implemented
behaviour, configuration and limitations. Run your focused container tests and
report paths, commands, results and outstanding integration risks. Do not weaken
shared tests. Docker commands may require sandbox escalation for socket access.
