-- Draft schema: edit freely until it has been applied to a shared database.
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

-- One reservation per errand. order_id makes Reserve, Release and Transfer idempotent (NFR3.2).
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

-- Append-only ledger: every balance change writes at least one row in the same transaction (FR4.2).
CREATE TABLE transactions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES wallets(user_id),
    kind text NOT NULL CHECK (kind IN ('allocation', 'reserve', 'release', 'spend', 'receive', 'admin_debit')),
    amount bigint NOT NULL CHECK (amount > 0),
    order_id text REFERENCES reservations(order_id),
    idempotency_key text UNIQUE,
    note text,
    created_at timestamptz NOT NULL DEFAULT now()
);
-- NFR1.2: a user's 30 most recent transactions.
CREATE INDEX transactions_user_recent ON transactions (user_id, created_at DESC);
