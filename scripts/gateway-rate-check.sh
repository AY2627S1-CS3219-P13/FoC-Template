#!/bin/sh
# Real Nginx + User regressions. Use a disposable stack with all four services up.
set -eu
cd "$(dirname "$0")/.."
client_a=
client_b=
client_c=
fixture_email="smoke-$(date +%s)-$(openssl rand -hex 4)@u.nus.edu"
account_email="gateway-account-$(openssl rand -hex 8)@u.nus.edu"
cleanup() {
  for client in "$client_a" "$client_b" "$client_c"; do
    if [ -n "$client" ]; then docker rm -f "$client" >/dev/null 2>&1 || true; fi
  done
  if ! sh scripts/cleanup-test-data.sh "$fixture_email"; then
    echo 'Gateway fixture cleanup failed.' >&2
    exit 1
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' HUP TERM
# Keep all three clients alive so Docker allocates distinct socket IPs; there are
# no manually assigned client addresses or extra application networks/services.
client_a=$(docker compose --profile test run --rm -d --no-deps --entrypoint node smoke -e 'setInterval(() => {}, 60000)')
client_b=$(docker compose --profile test run --rm -d --no-deps --entrypoint node smoke -e 'setInterval(() => {}, 60000)')
client_c=$(docker compose --profile test run --rm -d --no-deps --entrypoint node smoke -e 'setInterval(() => {}, 60000)')
docker exec "$client_a" node /checks/gateway-rate.mjs quota
docker exec -e "GATEWAY_RATE_EMAIL=$fixture_email" "$client_b" node /checks/gateway-rate.mjs independent
docker exec -e "GATEWAY_RATE_ACCOUNT_EMAIL=$account_email" "$client_b" node /checks/gateway-rate.mjs account
docker exec -e "GATEWAY_RATE_ACCOUNT_EMAIL=$account_email" "$client_c" node /checks/gateway-rate.mjs account-other-ip
