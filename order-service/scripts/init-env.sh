#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
file="$root/.env"
# Root Compose shares this read-only Supplier credential with Order. Keep
# existing credentials intact, including when the Order environment exists.
shared_env="$root/../.env"
if [ -f "$shared_env" ] && ! grep -q '^SUPPLIER_INTERNAL_TOKEN=' "$shared_env"; then
  umask 077
  supplier_token=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
  printf '\nSUPPLIER_INTERNAL_TOKEN=%s\n' "$supplier_token" >> "$shared_env"
  echo 'Added Supplier catalogue credential; existing settings kept.'
fi
supplier_token=${ORDER_SUPPLIER_INTERNAL_TOKEN:-}
if [ -z "$supplier_token" ] && [ -f "$shared_env" ]; then
  supplier_token=$(sed -n 's/^SUPPLIER_INTERNAL_TOKEN=//p' "$shared_env" | head -n 1)
fi
if [ -e "$file" ] && ! grep -q '^ORDER_SUPPLIER_INTERNAL_TOKEN=' "$file"; then
  if [ -z "$supplier_token" ]; then
    echo 'Initialize the shared Supplier credential or export ORDER_SUPPLIER_INTERNAL_TOKEN.' >&2
    exit 1
  fi
  umask 077
  printf '\nORDER_SUPPLIER_INTERNAL_TOKEN=%s\n' "$supplier_token" >> "$file"
fi
if [ -e "$file" ]; then
  echo "$file already exists; left unchanged."
  exit 0
fi

user_token=${USER_INTERNAL_TOKEN:-}
credit_token=${CREDIT_INTERNAL_TOKEN:-}
if [ -f "$root/../.env" ]; then
  user_token=${user_token:-$(sed -n 's/^USER_INTERNAL_TOKEN=//p' "$root/../.env" | head -n 1)}
fi
if [ -f "$root/../credit-service/.env" ]; then
  credit_token=${credit_token:-$(sed -n 's/^CREDIT_INTERNAL_TOKEN=//p' "$root/../credit-service/.env" | head -n 1)}
fi
if [ -z "$user_token" ] || [ -z "$credit_token" ] || [ -z "$supplier_token" ]; then
  echo 'Initialize User, Credit and the shared Supplier credential first, or export their internal tokens.' >&2
  exit 1
fi

password=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
umask 077
{
  echo "ORDER_SUPPLIER_INTERNAL_TOKEN=$supplier_token"
  echo "ORDER_POSTGRES_PASSWORD=$password"
  echo "ORDER_USER_INTERNAL_TOKEN=$user_token"
  echo "ORDER_CREDIT_INTERNAL_TOKEN=$credit_token"
} > "$file"
echo "Created $file"
