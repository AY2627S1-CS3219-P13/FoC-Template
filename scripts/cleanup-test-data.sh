#!/bin/sh
# Removes the accounts and suppliers that scripts/smoke.sh and scripts/browser-check.sh
# generate in LOCAL app data. Seeded and hand-created rows are never touched: only the
# generated `smoke-<timestamp>-<random>` and `browser-admin-<timestamp>-<random>` names.
# Usage: sh scripts/cleanup-test-data.sh [email]   (no argument removes every fixture)
set -eu
cd "$(dirname "$0")/.."
sh user-service/scripts/init-env.sh
sh credit-service/scripts/init-env.sh

email="${1:-}"
if [ -n "$email" ]; then
  user_filter="email = '$(printf %s "$email" | tr -d "'")'"
  supplier_filter="name LIKE '$(printf %s "${email%@*}" | tr -d "'")%'"
else
  user_filter="email LIKE 'smoke-%@u.nus.edu' OR email LIKE 'browser-admin-%@u.nus.edu'"
  supplier_filter="name LIKE 'smoke-%' OR name LIKE 'browser-admin-%'"
fi

docker compose up -d --wait user-db supplier-db >/dev/null

user_sql() {
  docker compose exec -T user-db psql -v ON_ERROR_STOP=1 -U foc_user -d foc_user "$@"
}

user_ids=""
users_removed=0
# A database that has never run its migrations has nothing to clean.
if [ "$(user_sql -At -c "SELECT to_regclass('public.users') IS NOT NULL")" = "t" ]; then
  # Wallets reference users by id, so collect the ids before the accounts disappear.
  user_ids="$(user_sql -At -c "SELECT id FROM users WHERE $user_filter")"
  # Sessions and verification challenges cascade with the account.
  users_removed="$(user_sql -At -c "WITH removed AS (DELETE FROM users WHERE $user_filter RETURNING 1) SELECT count(*) FROM removed")"
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
if [ -n "$user_ids" ] && [ -n "$(docker compose ps -q credit-db)" ] &&
  [ "$(credit_sql -At -c "SELECT to_regclass('public.wallets') IS NOT NULL")" = "t" ]; then
  values="$(printf "'%s'," $user_ids)"
  wallets_removed="$(credit_sql -At -c "
    DELETE FROM transactions WHERE user_id IN (${values%,});
    DELETE FROM reservations WHERE user_id IN (${values%,}) OR courier_id IN (${values%,});
    WITH removed AS (DELETE FROM wallets WHERE user_id IN (${values%,}) RETURNING 1) SELECT count(*) FROM removed")"
  wallets_removed="$(printf %s "$wallets_removed" | tail -n 1)"
fi

echo "Removed $users_removed test accounts, $suppliers_removed test suppliers and $wallets_removed test wallets."
