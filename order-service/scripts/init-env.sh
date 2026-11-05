#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
file="$root/.env"
# Root Compose shares this read-only Supplier credential with Order. Keep
# existing credentials intact, including when the Order environment exists.
shared_env="$root/../.env"
if [ -f "$shared_env" ]; then
  sh "$root/../schedule-order/scripts/init-env.sh"
fi
if [ -f "$shared_env" ] && ! grep -q '^SUPPLIER_INTERNAL_TOKEN=' "$shared_env"; then
  umask 077
  supplier_token=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
  printf '\nSUPPLIER_INTERNAL_TOKEN=%s\n' "$supplier_token" >> "$shared_env"
  echo 'Added Supplier catalogue credential; existing settings kept.'
fi
# Nearby alerts sign Web Push requests with a stable VAPID key. Add one to an
# existing environment too; never replace a key, as that drops every device.
add_vapid() {
  if grep -q '^ORDER_VAPID_PRIVATE_KEY=' "$file"; then
    return
  fi
  if ! command -v openssl > /dev/null 2>&1; then
    echo 'openssl not found; nearby alerts stay disabled until ORDER_VAPID_PRIVATE_KEY is set.' >&2
    return
  fi
  umask 077
  if [ -n "$(tail -c 1 "$file")" ]; then echo >> "$file"; fi
  vapid_key=$(openssl ecparam -name prime256v1 -genkey -noout \
    | openssl pkcs8 -topk8 -nocrypt -outform DER | base64 | tr -d '\n')
  {
    printf 'ORDER_VAPID_PRIVATE_KEY=%s\n' "$vapid_key"
    grep -q '^ORDER_VAPID_SUBJECT=' "$file" || printf 'ORDER_VAPID_SUBJECT=%s\n' "$vapid_subject"
  } >> "$file"
  echo 'Added the nearby-alerts VAPID key; existing settings kept.'
}
vapid_subject=https://github.com/AY2627S1-CS3219-P13/FoC-Template

if [ -e "$file" ]; then
  add_vapid
  echo "$file already exists; other settings left unchanged."
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
add_vapid
echo "Created $file"
