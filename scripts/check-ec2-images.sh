#!/bin/sh
# Exercise fresh database startup and image-only deployment in the disposable CI stack.
set -eu
cd "$(dirname "$0")/.."
if [ "${CI:-}" != true ]; then
  echo 'Run this check only against the disposable CI stack (CI=true).' >&2
  exit 1
fi

ECR_REGISTRY=foc-ci.invalid
FOC_IMAGE_TAG=${GITHUB_SHA:-0000000000000000000000000000000000000000}
export ECR_REGISTRY FOC_IMAGE_TAG

# The normal smoke step already built these runtime images. Give them the same
# references the EC2 configuration expects, without any private-registry access.
for service in user-service supplier-service credit-service order-service frontend gateway; do
  image=$(docker compose -f compose.yaml images -q "$service")
  docker image tag "$image" "$ECR_REGISTRY/foc/$service:$FOC_IMAGE_TAG"
done

# CI created these volumes; fresh ones exercise the PostgreSQL initialization race.
docker compose -f compose.yaml down --volumes --remove-orphans
sh scripts/deploy-ec2.sh "$FOC_IMAGE_TAG"

COMPOSE_FILE=compose.yaml:compose.ec2.yaml
export COMPOSE_FILE
sh scripts/smoke.sh
docker compose --profile test run --rm --no-deps --entrypoint node smoke order-lifecycle.mjs
