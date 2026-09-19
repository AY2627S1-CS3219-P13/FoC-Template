#!/bin/sh
# Run from the repository root. Never replace an existing environment.
set -eu
cd "$(dirname "$0")/../.."
if [ -e .env ]; then
  echo '.env already exists; kept unchanged.'
  exit 0
fi
umask 077
db_password=$(openssl rand -hex 24)
internal_token=$(openssl rand -hex 32)
code_secret=$(openssl rand -hex 32)
# noclobber also protects against simultaneous invocations.
set -C
sed -e "s/REPLACE_DATABASE_PASSWORD/$db_password/g" \
    -e "s/REPLACE_INTERNAL_TOKEN/$internal_token/g" \
    -e "s/REPLACE_CODE_SECRET/$code_secret/g" .env.example > .env
echo 'Created local .env with random secrets. Email stays in Mailpit.'
