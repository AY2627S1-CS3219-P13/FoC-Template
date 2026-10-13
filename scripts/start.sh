#!/bin/sh
# Initialize and start the complete local stack from any working directory.
set -eu
cd "$(dirname "$0")/.."

if [ "$#" -ne 0 ]; then
  echo 'Usage: sh scripts/start.sh' >&2
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo 'Docker is required. Install and start Docker with Compose before retrying.' >&2
  exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
  echo 'Docker Compose is required. Enable the Compose plugin before retrying.' >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo 'Cannot reach Docker. Start Docker and ensure your user can access it.' >&2
  exit 1
fi
if ! command -v openssl >/dev/null 2>&1; then
  echo 'OpenSSL is required by the local environment setup scripts.' >&2
  exit 1
fi

# User also initializes Supplier credentials; Credit must precede Order.
sh user-service/scripts/init-env.sh
sh credit-service/scripts/init-env.sh
sh order-service/scripts/init-env.sh

# Validate without printing the resolved configuration, which contains secrets.
docker compose config --quiet
# Gateway dependencies start the frontend, all four services, databases and Mailpit.
docker compose up --build -d --wait --wait-timeout 120 gateway

echo 'Local stack started. Application: http://localhost:3000'
echo 'Verification inbox: http://localhost:8025'
