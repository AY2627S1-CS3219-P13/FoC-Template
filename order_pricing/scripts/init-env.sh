#!/bin/sh
# Add local Pricing credentials without replacing existing application settings.
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
file="$root/.env"
if [ ! -f "$file" ]; then
  echo 'Initialize User, Credit and Order environments first.' >&2
  exit 1
fi
umask 077
for name in PRICING_POSTGRES_PASSWORD PRICING_QUOTE_TOKEN PRICING_ORDER_WRITE_TOKEN PRICING_EXPORT_TOKEN PRICING_ORDER_SOURCE_TOKEN; do
  if ! grep -q "^$name=" "$file"; then
    value=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
    printf '\n%s=%s\n' "$name" "$value" >> "$file"
  fi
done
echo 'Local Pricing credentials initialized; existing settings kept.'
