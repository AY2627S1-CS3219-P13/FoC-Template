#!/bin/sh
# Disposable Nginx + echo upstream; no app accounts or database access.
set -eu
cd "$(dirname "$0")/.."
work=$(mktemp -d)
name="foc-ingress-$(openssl rand -hex 6)"
trusted="$name-trusted"
untrusted="$name-untrusted"
gateway="$name-gateway"
backend="$name-backend"
cleanup() {
  docker rm -f "$gateway" "$backend" >/dev/null 2>&1 || true
  docker network rm "$trusted" "$untrusted" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' HUP TERM
image=${GATEWAY_TEST_IMAGE:-$(docker compose images -q gateway)}
: "${image:?Build the gateway image first}"
docker network create "$trusted" >/dev/null
docker network create "$untrusted" >/dev/null
cidr=$(docker network inspect --format '{{(index .IPAM.Config 0).Subnet}}' "$trusted")
# Substitute only the fixture's trusted subnet; run the actual production policy.
sed "s@10.20.16.0/24@$cidr@g" gateway/cloudfront-ingress.conf > "$work/ingress.conf"
# Network endpoints are aliases, not fixed container addresses.
docker run --rm -d --name "$backend" --network "$trusted" \
  --network-alias user-service --network-alias supplier-service \
  --network-alias credit-service --network-alias order-service --network-alias frontend \
  -v "$PWD/scripts:/checks:ro" node:22-bookworm-slim node /checks/cloudfront-ingress.mjs server >/dev/null
docker run --rm -d --name "$gateway" --network "$trusted" --network-alias gateway \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true --tmpfs /tmp \
  -v "$work/ingress.conf:/etc/nginx/foc/ingress.conf:ro" "$image" >/dev/null
docker network connect --alias gateway "$untrusted" "$gateway"
docker run --rm --network "$trusted" -v "$PWD/scripts:/checks:ro" \
  node:22-bookworm-slim node /checks/cloudfront-ingress.mjs trusted
docker run --rm --network "$untrusted" -v "$PWD/scripts:/checks:ro" \
  node:22-bookworm-slim node /checks/cloudfront-ingress.mjs untrusted
