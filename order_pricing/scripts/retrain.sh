#!/bin/sh
# Train, gate and export a new acceptance-gbm Pricing release, then point
# order_pricing/RELEASE_PACKAGE at it. All Python runs in the training container.
#
# Nothing is committed, pushed or deployed. Review the staged diff and open a PR:
# CI tests the package, and the main-branch release publishes and deploys it.
set -eu
cd "$(dirname "$0")/../.."

usage() {
  cat >&2 <<'EOF'
Usage: sh order_pricing/scripts/retrain.sh --release-id ID [options]

  --release-id ID            new package name, written to order_pricing/models/ID
  --orders CSV               observed orders (mlflow/sql/extract-orders.sql); omit for synthetic-only
  --pricing-events JSONL     Pricing training-export events used to link quotes (needs --orders)
  --observed-weight W        share of training weight for observed orders (default 0.2)
  --dataset DIR              reuse a simulated dataset instead of simulating one
  --seed N                   simulation and training seed (default 42)
  --quick                    smaller simulation, for demos (about a third of the default)
  --formula-package PATH     baseline release under order_pricing (default formula/releases/effort-traffic-002)
  --approved-by NAME         record production approval (needs --approval-reason)
  --approval-reason TEXT
  --no-switch                export the package but leave RELEASE_PACKAGE unchanged
  --check quick|full|none    quick (default): package adapter suite and runtime image;
                             full: also order_pricing/scripts/check.sh
  --allow-dirty              train from uncommitted code (lineage then names HEAD, not your edits)
EOF
  exit 2
}

release_id='' orders='' events='' weight=0.2 dataset='' seed=42 quick='' switch=1 check=quick dirty=''
formula=formula/releases/effort-traffic-002 approved_by='' approval_reason=''
while [ $# -gt 0 ]; do
  case $1 in
    --release-id) release_id=${2-}; shift 2 ;;
    --orders) orders=${2-}; shift 2 ;;
    --pricing-events) events=${2-}; shift 2 ;;
    --observed-weight) weight=${2-}; shift 2 ;;
    --dataset) dataset=${2-}; shift 2 ;;
    --seed) seed=${2-}; shift 2 ;;
    --quick) quick=1; shift ;;
    --formula-package) formula=${2-}; shift 2 ;;
    --approved-by) approved_by=${2-}; shift 2 ;;
    --approval-reason) approval_reason=${2-}; shift 2 ;;
    --no-switch) switch=; shift ;;
    --check) check=${2-}; shift 2 ;;
    --allow-dirty) dirty=1; shift ;;
    -h | --help) usage ;;
    *) echo "unknown option: $1" >&2; usage ;;
  esac
done

fail() { echo "retrain: $*" >&2; exit 1; }
case $release_id in
  "" | *[!A-Za-z0-9_.-]* | .*) fail "--release-id must be letters, digits, '.', '_' or '-'" ;;
esac
case $seed in "" | *[!0-9]*) fail "--seed must be a whole number" ;; esac
case $check in quick | full | none) ;; *) fail "--check must be quick, full or none" ;; esac
case $formula in "" | /* | *..*) fail "--formula-package must be relative to order_pricing" ;; esac
[ -f "order_pricing/$formula/manifest.json" ] || fail "no formula package at order_pricing/$formula"
[ -z "$events" ] || [ -n "$orders" ] || fail "--pricing-events needs --orders"
if { [ -n "$approved_by" ] && [ -z "$approval_reason" ]; } || { [ -z "$approved_by" ] && [ -n "$approval_reason" ]; }; then
  fail "--approved-by and --approval-reason go together"
fi
for file in "$orders" "$events"; do
  [ -z "$file" ] || [ -f "$file" ] || fail "no such file: $file"
done
[ -z "$dataset" ] || [ -f "$dataset/manifest.json" ] || fail "no dataset at $dataset"

package="models/$release_id"
[ ! -e "order_pricing/$package" ] || fail "order_pricing/$package already exists; releases are never overwritten"
if [ -n "$(git status --porcelain)" ]; then
  [ -n "$dirty" ] || fail "the working tree has uncommitted changes; commit them so the release names the code that trained it, or pass --allow-dirty"
  echo "retrain: warning: training from uncommitted code; the release records HEAD $(git rev-parse --short HEAD)" >&2
fi
commit=$(git rev-parse HEAD)

# Inputs are copied into the run directory, which keeps what this run trained on.
work="$PWD/order_pricing/.retrain/$release_id-$(date +%Y%m%d%H%M%S)"
mkdir -p "$work/input"
[ -z "$orders" ] || cp "$orders" "$work/input/orders.csv"
[ -z "$events" ] || cp "$events" "$work/input/pricing-events.jsonl"
[ -z "$dataset" ] || cp -R "$dataset" "$work/input/synthetic"

docker build -f order_pricing/mlflow/Dockerfile -t foc-pricing-mlops order_pricing
mlops() {
  docker run --rm --network none --read-only --tmpfs /tmp --user "$(id -u):$(id -g)" --cap-drop ALL \
    --security-opt no-new-privileges -v "$work:/data" foc-pricing-mlops python -m pricing_mlops "$@"
}
step() { printf '\n== %s\n' "$*" >&2; }
baseline="/app/$formula"

synthetic=/data/input/synthetic
if [ -z "$dataset" ]; then
  step "Simulating orders (seed $seed)"
  if [ -n "$quick" ]; then
    set -- --graph-rows 4000 --graph-batches 10 --agent-days 20 --agent-batches 10
  else
    set --
  fi
  mlops simulate --output /data/synthetic --formula-package "$baseline" --seed "$seed" "$@" > "$work/simulate.json"
  synthetic=/data/synthetic
fi
training_set=$synthetic
if [ -n "$orders" ]; then
  step "Adding observed orders"
  set -- --synthetic "$synthetic" --orders /data/input/orders.csv --output /data/dataset --observed-weight "$weight"
  [ -z "$events" ] || set -- "$@" --pricing-events /data/input/pricing-events.jsonl
  mlops import-observed "$@" > "$work/import.json"
  training_set=/data/dataset
fi

step "Training"
mlops train --kind acceptance-gbm --dataset "$training_set" --work-dir /data/work \
  --formula-package "$baseline" --code-commit "$commit" --seed "$seed" > "$work/train.json"
run_id=$(sed -n 's/.*"runId": "\([0-9a-f]\{32\}\)".*/\1/p' "$work/train.json")
[ -n "$run_id" ] || fail "training printed no run ID; see $work/train.json"

step "Selecting run $run_id (gates are checked here)"
set -- --work-dir /data/work --run-id "$run_id" --selection /data/selection.json
[ -z "$approved_by" ] || set -- "$@" --approved-by "$approved_by" --approval-reason "$approval_reason"
mlops select "$@" > "$work/select.json"

step "Exporting $package"
mlops export --work-dir /data/work --selection /data/selection.json --formula-package "$baseline" \
  --output /data/package --release-id "$release_id" > "$work/export.json"
mv "$work/package" "order_pricing/$package"
# release-package.sh only accepts tracked packages, so stage (not commit) it.
git add -- "order_pricing/$package"
if [ -n "$switch" ]; then
  printf '%s\n' "$package" > order_pricing/RELEASE_PACKAGE
  git add -- order_pricing/RELEASE_PACKAGE
fi

if [ "$check" != none ]; then
  step "Checking $package in its serving runtime"
  docker build -f order_pricing/serving/Dockerfile --target tests --build-arg "POLICY_PACKAGE=$package" \
    -t foc-pricing-release-tests order_pricing
  docker run --rm --network none --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges \
    -e PRICING_PACKAGE_DIR=/opt/policy foc-pricing-release-tests \
    python -m unittest tests.test_policy_conformance tests.acceptance_conformance
  step "Building the runtime image CI will publish"
  docker build -f order_pricing/serving/Dockerfile --target runtime --build-arg "POLICY_PACKAGE=$package" \
    -t "foc/pricing-service:$release_id" order_pricing
fi
if [ "$check" = full ]; then
  step "Running order_pricing/scripts/check.sh"
  sh order_pricing/scripts/check.sh
fi

metrics=
for key in log_loss base_rate_log_loss auc mean_acceptance_at_chosen mean_chosen_credits \
  observed_rows observed_log_loss observed_base_rate_log_loss observed_auc; do
  value=$(grep -o "\"$key\": [^,}]*" "$work/train.json" | head -n 1 | sed 's/.*: //')
  [ -z "$value" ] || metrics="$metrics
  $key: $value"
done

step "Done"
cat >&2 <<EOF
Run ID:      $run_id
Package:     order_pricing/$package (staged)
Release:     $( [ -n "$switch" ] && echo "RELEASE_PACKAGE now names $package (staged)" || echo "RELEASE_PACKAGE unchanged" )
Image:       $( [ "$check" != none ] && echo "foc/pricing-service:$release_id (local only)" || echo "not built (--check none)" )
Run files:   $work (metrics in train.json; MLflow store in work/mlruns)

Holdout metrics:$metrics

Browse the run:
  docker run --rm -p 127.0.0.1:5000:5000 --user "$(id -u):$(id -g)" -v "$work:/data" foc-pricing-mlops \\
    mlflow ui --backend-store-uri /data/work/mlruns --host 0.0.0.0
Ship it: commit, open a PR and merge to main; CI tests the package and the
release workflow publishes and deploys it. Roll back by restoring RELEASE_PACKAGE.
EOF
