#!/bin/sh
# Creates one unique verified test account and one inactive supplier in LOCAL app data.
# The test account remains an admin for browser exploration; no shared default password.
set -eu
cd "$(dirname "$0")/.."
sh user-service/scripts/init-env.sh
sh credit-service/scripts/init-env.sh
docker compose up --build -d gateway
export SMOKE_EMAIL="smoke-$(date +%s)-$(openssl rand -hex 4)@u.nus.edu"
export SMOKE_PASSWORD="$(openssl rand -hex 24)"
docker compose --profile test run --rm --no-deps -e SMOKE_EMAIL -e SMOKE_PASSWORD smoke setup
docker compose exec -T user-service user-service promote-admin "$SMOKE_EMAIL"
docker compose --profile test run --rm --no-deps -e SMOKE_EMAIL -e SMOKE_PASSWORD smoke check
echo 'Smoke test passed. A generated test account and inactive supplier remain in local data.'
