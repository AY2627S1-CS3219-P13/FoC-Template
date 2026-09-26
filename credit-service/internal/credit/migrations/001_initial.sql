-- User IDs are User Service UUIDs. Order IDs are text until Order Service fixes its ID format.

-- One wallet per user, created by the initial allocation (FR4.1.1).
-- The CHECKs are the last line of defence against negative balances (FR4.1.9).
CREATE TABLE wallets (
    user_id uuid PRIMARY KEY,
    available bigint NOT NULL CHECK (available >= 0),
    reserved bigint NOT NULL DEFAULT 0 CHECK (reserved >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- One escrow per errand. order_id makes Reserve, Release and Transfer idempotent (NFR3.2).
CREATE TABLE reservations (
    order_id text PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES wallets(user_id),
    amount bigint NOT NULL CHECK (amount > 0),
    status text NOT NULL DEFAULT 'held' CHECK (status IN ('held', 'released', 'transferred')),
    courier_id uuid REFERENCES wallets(user_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    settled_at timestamptz,
    CHECK ((status = 'transferred') = (courier_id IS NOT NULL)),
    CHECK (courier_id <> user_id)
);

-- The credit history (FR4.2): one row per balance change, written by ledgerTx.apply
-- in the same transaction as the change. Each row records its effect on both
-- balances and the balances after it.
CREATE TABLE transactions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES wallets(user_id),
    kind text NOT NULL,
    amount bigint NOT NULL CHECK (amount > 0),
    available_delta bigint NOT NULL,
    reserved_delta bigint NOT NULL,
    available_after bigint NOT NULL CHECK (available_after >= 0),
    reserved_after bigint NOT NULL CHECK (reserved_after >= 0),
    order_id text REFERENCES reservations(order_id),
    idempotency_key text UNIQUE,
    note text,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- The effect of each kind is fixed. Keep in sync with effects in ledger.go.
    CONSTRAINT transactions_kind_effect CHECK (
        (kind = 'allocation'  AND available_delta = amount  AND reserved_delta = 0) OR
        (kind = 'reserve'     AND available_delta = -amount AND reserved_delta = amount) OR
        (kind = 'release'     AND available_delta = amount  AND reserved_delta = -amount) OR
        (kind = 'spend'       AND available_delta = 0       AND reserved_delta = -amount) OR
        (kind = 'receive'     AND available_delta = amount  AND reserved_delta = 0) OR
        (kind = 'admin_debit' AND available_delta = -amount AND reserved_delta = 0))
);
-- History pages are read newest first by entry ID (FR4.2.1, NFR1.2).
CREATE INDEX transactions_user_history ON transactions (user_id, id DESC);

-- History is append-only: entries can never be changed or removed.
CREATE FUNCTION transactions_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'credit history is append-only';
END;
$$;
CREATE TRIGGER transactions_no_update_delete BEFORE UPDATE OR DELETE ON transactions
    FOR EACH ROW EXECUTE FUNCTION transactions_append_only();
CREATE TRIGGER transactions_no_truncate BEFORE TRUNCATE ON transactions
    FOR EACH STATEMENT EXECUTE FUNCTION transactions_append_only();
