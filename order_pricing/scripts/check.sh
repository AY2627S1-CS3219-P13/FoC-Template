#!/bin/sh
# Checks only the isolated Pricing stack; no real FoC source-service fixtures.
set -eu
cd "$(dirname "$0")/../.."

docker build -f order_pricing/tests/Dockerfile -t foc-pricing-contracts order_pricing
docker run --rm --network none --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges foc-pricing-contracts

docker build -f order_pricing/formula/Dockerfile -t foc-pricing-formula-tests order_pricing
docker run --rm --network none --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges foc-pricing-formula-tests
# The package the next AWS release will publish must pass the adapter suite inside
# its own serving runtime, which installs the package's locked dependencies.
release_package=$(sh order_pricing/scripts/release-package.sh)
case $(grep -o '"entryPoint": *"[^"]*"' "order_pricing/$release_package/manifest.json") in
  *policy.formula:*) release_checks="tests.test_policy_conformance tests.formula_conformance" ;;
  *policy.acceptance:*) release_checks="tests.test_policy_conformance tests.acceptance_conformance" ;;
  *) echo "unknown entry point in $release_package/manifest.json" >&2; exit 1 ;;
esac
docker build -f order_pricing/serving/Dockerfile --target tests --build-arg "POLICY_PACKAGE=$release_package" \
  -t foc-pricing-release-tests order_pricing
# shellcheck disable=SC2086 # release_checks is a list of test modules.
docker run --rm --network none --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges \
  -e PRICING_PACKAGE_DIR=/opt/policy foc-pricing-release-tests python -m unittest $release_checks -v

docker build -f order_pricing/mlflow/Dockerfile -t foc-pricing-mlops order_pricing
docker run --rm --network none --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges foc-pricing-mlops python -m unittest discover -s tests/mlops -v

task_commit=$(git rev-parse HEAD)
task_run=$(date +%Y%m%d%H%M%S)-$$
task_work="$PWD/order_pricing/.integration"
mkdir -p "$task_work/runs/$task_run" "$task_work/state"
docker run --rm --network none --read-only --tmpfs /tmp --user "$(id -u):$(id -g)" --cap-drop ALL --security-opt no-new-privileges \
  -v "$task_work:/data" foc-pricing-mlops python -m pricing_mlops demo --kind acceptance-gbm \
  --work-dir "/data/runs/$task_run/work" --formula-package /app/formula/releases/effort-traffic-002 \
  --code-commit "$task_commit" --output "/data/runs/$task_run/model" \
  --release-id "model-integration-$task_run" --seed 42 \
  --graph-rows 4000 --graph-batches 10 --agent-days 20 --agent-batches 10

export PRICING_MODEL_PACKAGE=".integration/runs/$task_run/model"
# Bind the generated package into the training image, which has LightGBM:
# actual model adapter checks.
docker run --rm --network none --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges \
  -v "$task_work/runs/$task_run/model:/candidate:ro" -e PRICING_PACKAGE_DIR=/candidate \
  foc-pricing-mlops python -m unittest tests.test_policy_conformance tests.acceptance_conformance -v

compose() { docker compose -f order_pricing/compose.integration.yaml "$@"; }
cleanup() {
  task_status=$?
  trap - EXIT INT TERM
  if [ "$task_status" != 0 ]; then
    compose logs --no-color --tail=60 || true
  fi
  compose down --volumes --remove-orphans
  exit "$task_status"
}
trap cleanup EXIT INT TERM
compose up --build -d database sources pricing-a pricing-b pricing-legacy pricing-model pricing-fallback pricing-unready
# Run framework-focused tests in its image, with real PostgreSQL where required.
compose run --build --rm --no-deps serving-checks
compose run --build --rm --no-deps checks
compose run --build --rm --no-deps feed-checks

compose run --rm --no-deps checks python -m tests.integration.lifecycle_probe prepare > "$task_work/state/replay.json"
compose restart pricing-a pricing-b
compose run --rm --no-deps checks python -m tests.integration.lifecycle_probe verify-replay /state/replay.json

# Fault injection is restricted to the disposable Pricing database in this project.
compose exec -T database psql -U pricing_test -d pricing_test -v ON_ERROR_STOP=1 \
  -c 'ALTER TABLE pricing_events RENAME TO pricing_events_unavailable'
compose run --rm --no-deps checks python -m tests.integration.lifecycle_probe recording-failure > "$task_work/state/recording.json"
compose exec -T database psql -U pricing_test -d pricing_test -v ON_ERROR_STOP=1 \
  -c 'ALTER TABLE pricing_events_unavailable RENAME TO pricing_events'
compose run --rm --no-deps checks python -m tests.integration.lifecycle_probe verify-recovery /state/recording.json
printf '%s\n' 'Pricing contracts, components and integration passed.'
