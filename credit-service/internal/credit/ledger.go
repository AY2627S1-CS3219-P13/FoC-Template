package credit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The ledger (transactions table) is the credit history (FR4.2). Balances
// change only through ledgerTx.apply, which updates the wallet and appends the
// matching entry in one statement pair, and inTx logs each change once it
// commits. Operations never write wallets or the ledger themselves, so
// balances, history and logs cannot drift apart. The database backs this up:
// each kind's effect is a constraint, and the ledger is append-only.

// Kind labels each ledger entry so users can see why their balance changed (FR4.2.1).
type Kind string

const (
	KindAllocation Kind = "allocation"
	KindReserve    Kind = "reserve"
	KindRelease    Kind = "release"
	KindSpend      Kind = "spend"
	KindReceive    Kind = "receive"
	KindAdminDebit Kind = "admin_debit"
)

// effects fixes how each kind changes the two balances, per credit moved.
// The transactions_kind_effect constraint enforces the same table.
var effects = map[Kind]struct{ available, reserved int64 }{
	KindAllocation: {1, 0},
	KindReserve:    {-1, 1},
	KindRelease:    {1, -1},
	KindSpend:      {0, -1},
	KindReceive:    {1, 0},
	KindAdminDebit: {-1, 0},
}

// entry is one balance change. orderID, key and note are empty when not relevant.
type entry struct {
	userID  string
	kind    Kind
	amount  int64
	orderID string
	key     string
	note    string
}

// ledgerTx is a database transaction that records every balance change it makes.
type ledgerTx struct {
	pgx.Tx
	applied []applied
}

type applied struct {
	entry
	after Wallet
}

// apply changes one wallet by e and appends e to the ledger. A change that would
// make either balance negative fails with ErrInsufficientCredits (FR4.1.9).
func (tx *ledgerTx) apply(ctx context.Context, e entry) (Wallet, error) {
	effect, ok := effects[e.kind]
	if !ok || e.amount <= 0 {
		return Wallet{}, fmt.Errorf("invalid ledger entry: %s %d", e.kind, e.amount)
	}
	availableDelta, reservedDelta := effect.available*e.amount, effect.reserved*e.amount
	w := Wallet{UserID: e.userID}
	err := tx.QueryRow(ctx, `UPDATE wallets SET available=available+$2, reserved=reserved+$3, updated_at=now()
		WHERE user_id=$1 RETURNING available, reserved`, e.userID, availableDelta, reservedDelta).Scan(&w.Available, &w.Reserved)
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Wallet{}, ErrWalletNotFound
	case errors.As(err, &pgErr) && pgErr.Code == "23514":
		// A wallet CHECK constraint: the balance would go negative.
		return Wallet{}, ErrInsufficientCredits
	case err != nil:
		return Wallet{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO transactions(user_id, kind, amount, order_id, idempotency_key, note,
		available_delta, reserved_delta, available_after, reserved_after)
		VALUES($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''), $7, $8, $9, $10)`,
		e.userID, string(e.kind), e.amount, e.orderID, e.key, e.note, availableDelta, reservedDelta, w.Available, w.Reserved)
	if err != nil {
		return Wallet{}, err
	}
	tx.applied = append(tx.applied, applied{entry: e, after: w})
	return w, nil
}

// inTx runs fn in one database transaction, so its balance changes commit
// together or not at all (NFR3.3). After commit, each change is logged here,
// once, as a domain event (NFR6); rolled-back work is never logged.
func inTx[T any](ctx context.Context, a *App, fn func(*ledgerTx) (T, error)) (T, error) {
	var zero T
	pgTx, err := a.db.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer pgTx.Rollback(ctx)
	tx := &ledgerTx{Tx: pgTx}
	result, err := fn(tx)
	if err != nil {
		return zero, err
	}
	if err = pgTx.Commit(ctx); err != nil {
		return zero, err
	}
	for _, m := range tx.applied {
		a.log.Info("credits moved", "kind", string(m.kind), "userId", m.userID, "amount", m.amount,
			"orderId", m.orderID, "available", m.after.Available, "reserved", m.after.Reserved)
	}
	return result, nil
}

// Transaction is one ledger entry as shown in a user's history (FR4.2.1).
// CounterpartyID is the courier on a spend and the requester on a receive.
type Transaction struct {
	ID             int64     `json:"id"`
	Kind           Kind      `json:"kind"`
	Amount         int64     `json:"amount"`
	AvailableDelta int64     `json:"availableDelta"`
	ReservedDelta  int64     `json:"reservedDelta"`
	AvailableAfter int64     `json:"availableAfter"`
	ReservedAfter  int64     `json:"reservedAfter"`
	OrderID        string    `json:"orderId,omitempty"`
	CounterpartyID string    `json:"counterpartyId,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

const (
	DefaultHistoryLimit = 30
	MaxHistoryLimit     = 100
)

// History returns up to limit of a user's ledger entries, newest first
// (FR4.2.1). before is the ID of the last entry on the previous page, or 0 for
// the newest entries.
func (a *App) History(ctx context.Context, userID string, limit int, before int64) ([]Transaction, error) {
	userID, ok := normalizeUUID(userID)
	if !ok || limit < 1 || limit > MaxHistoryLimit || before < 0 {
		return nil, ErrInvalidInput
	}
	var exists bool
	if err := a.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM wallets WHERE user_id=$1)", userID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrWalletNotFound
	}
	rows, err := a.db.Query(ctx, `SELECT t.id, t.kind, t.amount, t.available_delta, t.reserved_delta,
		t.available_after, t.reserved_after, coalesce(t.order_id, ''),
		coalesce(CASE t.kind WHEN 'spend' THEN r.courier_id WHEN 'receive' THEN r.user_id END::text, ''), t.created_at
		FROM transactions t LEFT JOIN reservations r ON r.order_id = t.order_id
		WHERE t.user_id=$1 AND ($2::bigint = 0 OR t.id < $2::bigint)
		ORDER BY t.id DESC LIMIT $3`, userID, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := []Transaction{}
	for rows.Next() {
		var t Transaction
		var kind string
		if err = rows.Scan(&t.ID, &kind, &t.Amount, &t.AvailableDelta, &t.ReservedDelta,
			&t.AvailableAfter, &t.ReservedAfter, &t.OrderID, &t.CounterpartyID, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Kind = Kind(kind)
		history = append(history, t)
	}
	return history, rows.Err()
}
