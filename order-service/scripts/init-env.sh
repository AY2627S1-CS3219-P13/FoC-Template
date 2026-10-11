#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
file="$root/.env"
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
if [ -z "$user_token" ] || [ -z "$credit_token" ]; then
  echo 'Initialize User and Credit services first, or export their internal tokens.' >&2
  exit 1
fi

password=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
umask 077
{
  echo "ORDER_POSTGRES_PASSWORD=$password"
  echo "ORDER_USER_INTERNAL_TOKEN=$user_token"
  echo "ORDER_CREDIT_INTERNAL_TOKEN=$credit_token"
} > "$file"
echo "Created $file"
