#!/bin/sh
# Creates credit-service/.env with random local secrets. Never replaces an existing file.
set -eu
cd "$(dirname "$0")/.."
if [ -e .env ]; then
  echo 'credit-service/.env already exists; kept unchanged.'
else
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
fi

# Keep the two service credentials equal without replacing existing credentials.
internal_token=$(sed -n 's/^CREDIT_INTERNAL_TOKEN=//p' .env)
if [ -z "$internal_token" ] || [ ! -e ../.env ]; then
  echo 'Credit token or root .env missing; run user-service/scripts/init-env.sh first.' >&2
  exit 1
fi
configured_token=$(sed -n 's/^USER_CREDIT_INTERNAL_TOKEN=//p' ../.env)
if [ -n "$configured_token" ] && [ "$configured_token" != "$internal_token" ]; then
  echo 'USER_CREDIT_INTERNAL_TOKEN differs from Credit Service; reconcile local .env files.' >&2
  exit 1
fi
if [ -z "$configured_token" ]; then
  if grep -q '^USER_CREDIT_INTERNAL_TOKEN=' ../.env; then
    echo 'USER_CREDIT_INTERNAL_TOKEN is empty; set it to the Credit Service token.' >&2
    exit 1
  fi
  printf '\nUSER_CREDIT_INTERNAL_TOKEN=%s\n' "$internal_token" >> ../.env
fi
