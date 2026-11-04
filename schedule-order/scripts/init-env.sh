#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
shared="$root/../.env"
if [ ! -f "$shared" ]; then
  echo 'Initialize User and Credit environments first.' >&2
  exit 1
fi
umask 077
if ! grep -q '^SCHEDULE_INTERNAL_TOKEN=' "$shared"; then
  token=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
  printf '\nSCHEDULE_INTERNAL_TOKEN=%s\n' "$token" >> "$shared"
fi
if [ ! -f "$root/.env" ]; then
  password=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
  printf 'SCHEDULE_POSTGRES_PASSWORD=%s\n' "$password" > "$root/.env"
  echo 'Created Schedule Order environment.'
fi
