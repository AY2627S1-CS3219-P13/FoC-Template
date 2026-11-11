#!/bin/sh
# Deploy one immutable application version to the existing single EC2 host.
set -eu
cd "$(dirname "$0")/.."

if [ "$#" -ne 1 ] || [ "${#1}" -ne 40 ]; then
  echo 'Usage: ECR_REGISTRY=<registry-hostname> sh scripts/deploy-ec2.sh <full-commit-sha>' >&2
  exit 1
fi
case "$1" in
  *[!0-9a-f]*) echo 'Image tag must be a full lowercase Git commit SHA.' >&2; exit 1 ;;
esac
: "${ECR_REGISTRY:?Set ECR_REGISTRY to the private ECR registry hostname}"
case "$ECR_REGISTRY" in
  ''|*[!a-z0-9.-]*) echo 'ECR_REGISTRY must be a hostname without a scheme or path.' >&2; exit 1 ;;
esac
FOC_IMAGE_TAG=$1
export ECR_REGISTRY FOC_IMAGE_TAG

for tool in docker curl flock; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is required." >&2; exit 1; }
done
for file in .env credit-service/.env order-service/.env; do
  if [ ! -f "$file" ]; then
    echo "Missing $file; initialize the deployment environment first." >&2
    exit 1
  fi
done

# Prevent concurrent manual and automated deployments on this checkout.
exec 9>.ec2-deploy.lock
flock -n 9 || { echo 'Another deployment is already running.' >&2; exit 1; }

compose() {
  # Explicit files replace the server's earlier, untracked restart override.
  docker compose -f compose.yaml -f compose.ec2.yaml "$@"
}

ready() {
  service=$1
  address=$(compose port "$service" 8080)
  printf 'Checking %s readiness\n' "$service"
  curl --fail --silent --show-error --retry 10 --retry-delay 2 \
    --retry-all-errors --connect-timeout 3 --max-time 5 "http://$address/readyz"
}

sh order_pricing/scripts/init-env.sh
compose config --quiet
# The legacy runtime must be cut over offline; never start two schedule writers.
if compose ps --all --format '{{.Service}}' | grep -qx schedule-order; then
  echo 'Legacy Schedule container found. Complete schedule-order/README.md offline cutover and remove that application container before deploying.' >&2
  exit 1
fi
# Remember what is running so a failed release can be undone. Tags are immutable,
# so the previous images are still cached locally.
previous_tag=''
container=$(compose ps -q gateway 2>/dev/null || true)
if [ -n "$container" ]; then
  previous_tag=$(docker inspect --format '{{.Config.Image}}' "$container" 2>/dev/null | sed 's|.*:||')
  # Only a full commit SHA can be redeployed; anything else leaves this empty.
  [ "${#previous_tag}" -eq 40 ] || previous_tag=''
fi

# Restore the previous release if this one fails after a service was stopped.
# Images roll back; database migrations do not.
rollback() {
  status=$?
  if [ "$status" -eq 0 ] || [ -z "${release_started:-}" ] || [ -z "$previous_tag" ]; then
    exit "$status"
  fi
  echo "Deployment of $FOC_IMAGE_TAG failed; restoring $previous_tag" >&2
  FOC_IMAGE_TAG=$previous_tag
  export FOC_IMAGE_TAG
  if compose up --no-build --pull never -d --wait --wait-timeout 120 \
      user-service supplier-service credit-service order-service pricing-service frontend gateway; then
    echo "Restored $previous_tag; the failed release is not deployed." >&2
  else
    echo "Rollback to $previous_tag failed; the host needs manual attention." >&2
  fi
  exit "$status"
}
trap rollback EXIT

# Immutable tags cannot change; existing cached images can safely be reused.
# A missing image or failed download aborts before any running service is stopped.
compose pull --policy missing user-service supplier-service credit-service order-service pricing-service frontend gateway

# Catch origin/cookie/SMTP mismatches before changing any running service.
# This command validates settings only; it does not contact or migrate databases.
compose run --rm --no-deps user-service check-config

# Order upgrades prohibit old writers and require the new Credit endpoint first.
release_started=1
compose stop pricing-service order-service
compose up --no-build --pull never -d --wait --wait-timeout 120 user-service supplier-service credit-service
for service in user-service supplier-service credit-service; do
  ready "$service"
done
compose up --no-build --pull never -d --wait --wait-timeout 120 gateway
ready order-service
compose exec -T pricing-service python -c 'import urllib.request; urllib.request.urlopen("http://127.0.0.1:8080/readyz", timeout=3)'

address=$(compose port gateway 8080)
case "$address" in
  0.0.0.0:*) address="127.0.0.1:${address##*:}" ;;
esac
curl --fail --silent --show-error --output /dev/null --retry 10 --retry-delay 2 \
  --retry-all-errors --connect-timeout 3 --max-time 5 "http://$address/"

for service in user-service supplier-service credit-service order-service pricing-service frontend gateway; do
  container=$(compose ps -q "$service")
  actual=$(docker inspect --format '{{.Config.Image}}' "$container")
  expected="$ECR_REGISTRY/foc/$service:$FOC_IMAGE_TAG"
  if [ "$actual" != "$expected" ]; then
    echo "$service is not running the requested image." >&2
    exit 1
  fi
done
printf 'Deployment ready: %s\n' "$FOC_IMAGE_TAG"
compose ps -a
