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

compose config --quiet
# Immutable tags cannot change; existing cached images can safely be reused.
# A missing image or failed download aborts before any running service is stopped.
compose pull --policy missing user-service supplier-service credit-service order-service frontend gateway

# Order upgrades prohibit old writers and require the new Credit endpoint first.
compose stop order-service
compose up --no-build --pull never -d --wait --wait-timeout 120 user-service supplier-service credit-service
for service in user-service supplier-service credit-service; do
  ready "$service"
done
compose up --no-build --pull never -d --wait --wait-timeout 120 gateway
ready order-service

address=$(compose port gateway 8080)
curl --fail --silent --show-error --output /dev/null --retry 10 --retry-delay 2 \
  --retry-all-errors --connect-timeout 3 --max-time 5 "http://$address/"

for service in user-service supplier-service credit-service order-service frontend gateway; do
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
