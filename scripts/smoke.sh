#!/bin/sh
# Creates one unique verified test account and one supplier in LOCAL app data, then removes
# them again. Set SMOKE_KEEP=1 to keep the admin account for browser exploration; no shared
# default password. `sh scripts/cleanup-test-data.sh` removes fixtures left by earlier runs.
set -eu
cd "$(dirname "$0")/.."
sh user-service/scripts/init-env.sh
sh credit-service/scripts/init-env.sh
export SMOKE_API_ONLY="${SMOKE_API_ONLY:-0}"
if [ "$SMOKE_API_ONLY" = "1" ]; then
  docker compose up --build -d user-service supplier-service
  export SMOKE_BASE_URL=http://user-service:8080
  export SMOKE_SUPPLIER_URL=http://supplier-service:8080
else
  docker compose up --build -d gateway
  export SMOKE_BASE_URL=http://gateway:8080
  export SMOKE_SUPPLIER_URL=http://gateway:8080
fi
export SMOKE_EMAIL="smoke-$(date +%s)-$(openssl rand -hex 4)@u.nus.edu"
export SMOKE_PASSWORD="$(openssl rand -hex 24)"
docker compose --profile test run --rm --no-deps -e SMOKE_EMAIL -e SMOKE_PASSWORD -e SMOKE_BASE_URL -e SMOKE_SUPPLIER_URL -e SMOKE_API_ONLY smoke setup
docker compose exec -T user-service user-service promote-admin "$SMOKE_EMAIL"
docker compose --profile test run --rm --no-deps -e SMOKE_EMAIL -e SMOKE_PASSWORD -e SMOKE_BASE_URL -e SMOKE_SUPPLIER_URL -e SMOKE_API_ONLY smoke check
if [ "${SMOKE_KEEP:-0}" = "1" ]; then
  echo 'Smoke test passed. The generated admin account remains in local data.'
else
  sh scripts/cleanup-test-data.sh "$SMOKE_EMAIL"
  echo 'Smoke test passed.'
fi
