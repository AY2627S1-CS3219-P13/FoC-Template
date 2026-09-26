#!/bin/sh
# Creates credit-service/.env with random local secrets. Never replaces an existing file.
set -eu
cd "$(dirname "$0")/.."
if [ -e .env ]; then
  echo 'credit-service/.env already exists; kept unchanged.'
  exit 0
fi
umask 077
db_password=$(openssl rand -hex 24)
internal_token=$(openssl rand -hex 32)
# Reuse User Service's internal token when the root .env exists.
user_token=$(sed -n 's/^USER_INTERNAL_TOKEN=//p' ../.env 2>/dev/null || true)
if [ -z "$user_token" ]; then
  user_token=$(openssl rand -hex 32)
fi
set -C
sed -e "s/REPLACE_DATABASE_PASSWORD/$db_password/g" \
    -e "s/REPLACE_INTERNAL_TOKEN/$internal_token/g" \
    -e "s/REPLACE_USER_INTERNAL_TOKEN/$user_token/g" .env.example > .env
echo 'Created credit-service/.env with random local secrets.'
