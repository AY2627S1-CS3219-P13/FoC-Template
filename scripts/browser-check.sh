#!/bin/sh
# Local fixtures only. Never prints passwords, sessions or email codes.
set -eu
cd "$(dirname "$0")/.."
sh user-service/scripts/init-env.sh
sh credit-service/scripts/init-env.sh
docker compose up --build -d gateway
export SMOKE_EMAIL="browser-admin-$(date +%s)-$(openssl rand -hex 3)@u.nus.edu"
export SMOKE_PASSWORD="$(openssl rand -hex 24)"
docker compose --profile test run --rm --no-deps -e SMOKE_EMAIL -e SMOKE_PASSWORD smoke setup
docker compose exec -T user-service user-service promote-admin "$SMOKE_EMAIL"
mkdir -p .agent/tmp/d2-browser
docker compose --profile test run --build --rm --no-deps -e SMOKE_EMAIL -e SMOKE_PASSWORD browser-checks
