#!/bin/sh
# Removes the accounts and suppliers that scripts/smoke.sh and scripts/browser-check.sh
# generate in LOCAL app data. Seeded and hand-created rows are never touched: only the
# generated `smoke-<timestamp>-<random>` and `browser-admin-<timestamp>-<random>` names.
# The browser check passes its unique d2-student address explicitly.
# Usage: sh scripts/cleanup-test-data.sh [email]   (no argument removes every fixture)
set -eu
cd "$(dirname "$0")/.."
email="${1:-}"
if [ "$#" -gt 1 ] || { [ "$#" -eq 1 ] && [ -z "$email" ]; } || { [ -n "$email" ] && ! printf '%s\n' "$email" | LC_ALL=C grep -Eq '^(smoke-[0-9]+-[0-9a-f]{8}|browser-admin-[0-9]+-[0-9a-f]{6}|d2-student-[0-9]+-[0-9a-f]{6})@u[.]nus[.]edu$'; }; then
  echo 'Refusing cleanup: specify a generated smoke, browser-admin or d2-student email.' >&2
  exit 2
fi

sh user-service/scripts/init-env.sh
sh credit-service/scripts/init-env.sh

if [ -n "$email" ]; then
  user_filter="email = '$email'"
  supplier_name="${email%@*}"
  supplier_filter="name = '$supplier_name' OR name = '$supplier_name edited'"
else
  user_filter="email ~ '^(smoke-[0-9]+-[0-9a-f]{8}|browser-admin-[0-9]+-[0-9a-f]{6})@u[.]nus[.]edu$'"
  supplier_filter="name ~ '^smoke-[0-9]+-[0-9a-f]{8}( edited)?$'"
fi

# Fail closed if Credit's database is unavailable. Otherwise deleting the User
# first could leave an orphan wallet that a later cleanup cannot discover.
docker compose up -d --wait user-db supplier-db credit-db >/dev/null

user_sql() {
  docker compose exec -T user-db psql -v ON_ERROR_STOP=1 -U foc_user -d foc_user "$@"
}

user_ids=""
users_removed=0
# A database that has never run its migrations has nothing to clean.
if [ "$(user_sql -At -c "SELECT to_regclass('public.users') IS NOT NULL")" = "t" ]; then
  # Wallets reference users by id, so collect the ids before the accounts disappear.
  user_ids="$(user_sql -At -c "SELECT id FROM users WHERE $user_filter")"
fi

supplier_sql() {
  docker compose exec -T supplier-db psql -v ON_ERROR_STOP=1 -U foc_supplier -d foc_supplier "$@"
}

suppliers_removed=0
if [ "$(supplier_sql -At -c "SELECT to_regclass('public.suppliers') IS NOT NULL")" = "t" ]; then
  suppliers_removed="$(supplier_sql -At -c "WITH removed AS (DELETE FROM suppliers WHERE $supplier_filter RETURNING 1) SELECT count(*) FROM removed")"
fi

credit_sql() {
  docker compose exec -T credit-db psql -v ON_ERROR_STOP=1 -U foc_credit -d foc_credit "$@"
}

wallets_removed=0
if [ -n "$user_ids" ] && [ "$(credit_sql -At -c "SELECT to_regclass('public.wallets') IS NOT NULL")" = "t" ]; then
  values="$(printf "'%s'," $user_ids)"
  # Only this local fixture cleanup bypasses the append-only ledger trigger.
  # Keep it inside one transaction so a failure restores the trigger and rows.
  wallets_removed="$(credit_sql -Atq -c "
    BEGIN;
    ALTER TABLE transactions DISABLE TRIGGER USER;
    DELETE FROM transactions WHERE user_id IN (${values%,});
    DELETE FROM reservations WHERE user_id IN (${values%,}) OR courier_id IN (${values%,});
    WITH removed AS (DELETE FROM wallets WHERE user_id IN (${values%,}) RETURNING 1) SELECT count(*) FROM removed;
    ALTER TABLE transactions ENABLE TRIGGER USER;
    COMMIT;")"
  wallets_removed="$(printf '%s\n' "$wallets_removed" | grep -E '^[0-9]+$' | tail -n 1)"
fi

if [ -n "$user_ids" ]; then
  # Sessions, challenges and allocation jobs cascade with the account.
  users_removed="$(user_sql -At -c "WITH removed AS (DELETE FROM users WHERE $user_filter RETURNING 1) SELECT count(*) FROM removed")"
fi

echo "Removed $users_removed test accounts, $suppliers_removed test suppliers and $wallets_removed test wallets."
