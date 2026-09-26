package credit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Every balance change runs in one transaction (inTx) that also writes its
// ledger entries, so balances and history always agree (NFR3.3, FR4.2).
//
// Lock order, to prevent deadlocks: an escrow row before wallet rows, and two
// wallets in ascending user ID order (lockWallets).

var (
	ErrInvalidAmount       = errors.New("amount must be a positive whole number")
	ErrInvalidInput        = errors.New("malformed ID or missing required field")
	ErrWalletNotFound      = errors.New("wallet not found")
	ErrInsufficientCredits = errors.New("insufficient available credits")
	ErrReservationNotFound = errors.New("escrow not found")
	ErrReservationSettled  = errors.New("escrow already released or paid out")
	ErrCourierIsRequester  = errors.New("a requester cannot be paid for their own errand")
	ErrConflict            = errors.New("request conflicts with an earlier request for the same key")
)

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

const (
	statusHeld        = "held"
	statusReleased    = "released"
	statusTransferred = "transferred"
)

// Wallet is a user's balance. Available can be spent on new errands; Reserved
// is held for the user's open errands (FR4.1.2).
type Wallet struct {
	UserID    string `json:"userId"`
	Available int64  `json:"available"`
	Reserved  int64  `json:"reserved"`
}

type App struct {
	db       *pgxpool.Pool
	cfg      Config
	sessions Sessions
	log      *slog.Logger
}

func New(db *pgxpool.Pool, cfg Config, sessions Sessions, log *slog.Logger) *App {
	return &App{db: db, cfg: cfg, sessions: sessions, log: log}
}

// Balance returns a user's available and reserved credits (FR4.1.2).
// Read-only, so it needs no transaction or row lock.
func (a *App) Balance(ctx context.Context, userID string) (Wallet, error) {
	userID, ok := normalizeUUID(userID)
	if !ok {
		return Wallet{}, ErrInvalidInput
	}
	w := Wallet{UserID: userID}
	err := a.db.QueryRow(ctx, "SELECT available, reserved FROM wallets WHERE user_id=$1", userID).Scan(&w.Available, &w.Reserved)
	if errors.Is(err, pgx.ErrNoRows) {
		return Wallet{}, ErrWalletNotFound
	}
	if err != nil {
		return Wallet{}, err
	}
	return w, nil
}

// AllocateInitial gives a newly verified user cfg.InitialCredits, exactly
// once (FR4.1.1). It is idempotent: calling it again for the same user returns
// the existing wallet unchanged, so User Service can retry safely.
func (a *App) AllocateInitial(ctx context.Context, userID string) (Wallet, error) {
	userID, ok := normalizeUUID(userID)
	if !ok {
		return Wallet{}, ErrInvalidInput
	}
	created := false
	w, err := inTx(ctx, a.db, func(tx pgx.Tx) (Wallet, error) {
		// A concurrent insert for the same user waits here, then does nothing.
		tag, err := tx.Exec(ctx, "INSERT INTO wallets(user_id, available) VALUES($1, $2) ON CONFLICT (user_id) DO NOTHING", userID, a.cfg.InitialCredits)
		if err != nil {
			return Wallet{}, err
		}
		if created = tag.RowsAffected() == 1; created {
			if err = recordEntry(ctx, tx, entry{userID: userID, kind: KindAllocation, amount: a.cfg.InitialCredits}); err != nil {
				return Wallet{}, err
			}
		}
		return lockWallet(ctx, tx, userID)
	})
	if err == nil && created {
		a.log.Info("initial credits allocated", "userId", userID, "amount", a.cfg.InitialCredits)
	}
	return w, err
}

// Reserve freezes amount for an errand: it moves from the requester's
// available balance to reserved (FR3.1.4, FR4.1.3). Order Service calls it
// before creating the errand. orderID is the idempotency key: retrying with the
// same orderID, user and amount succeeds without reserving twice.
func (a *App) Reserve(ctx context.Context, userID, orderID string, amount int64) (Wallet, error) {
	if amount <= 0 {
		return Wallet{}, ErrInvalidAmount
	}
	userID, ok := normalizeUUID(userID)
	if !ok || !validKey(orderID) {
		return Wallet{}, ErrInvalidInput
	}
	w, err := inTx(ctx, a.db, func(tx pgx.Tx) (Wallet, error) {
		// Lock the wallet first: a concurrent retry of this order then waits
		// here and sees the committed escrow below.
		w, err := lockWallet(ctx, tx, userID)
		if err != nil {
			return Wallet{}, err
		}
		var heldBy string
		var held int64
		err = tx.QueryRow(ctx, "SELECT user_id::text, amount FROM reservations WHERE order_id=$1", orderID).Scan(&heldBy, &held)
		if err == nil {
			if heldBy != userID || held != amount {
				return Wallet{}, ErrConflict
			}
			return w, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Wallet{}, err
		}
		if w.Available < amount {
			return Wallet{}, ErrInsufficientCredits
		}
		if _, err = tx.Exec(ctx, "UPDATE wallets SET available=available-$2, reserved=reserved+$2, updated_at=now() WHERE user_id=$1", userID, amount); err != nil {
			return Wallet{}, err
		}
		_, err = tx.Exec(ctx, "INSERT INTO reservations(order_id, user_id, amount) VALUES($1, $2, $3)", orderID, userID, amount)
		if uniqueViolation(err) {
			// Another user's request for this order committed first.
			return Wallet{}, ErrConflict
		}
		if err != nil {
			return Wallet{}, err
		}
		if err = recordEntry(ctx, tx, entry{userID: userID, kind: KindReserve, amount: amount, orderID: orderID}); err != nil {
			return Wallet{}, err
		}
		w.Available -= amount
		w.Reserved += amount
		return w, nil
	})
	if err == nil {
		a.log.Info("credits reserved", "userId", userID, "orderId", orderID, "amount", amount)
	}
	return w, err
}

// Release unfreezes an errand's credits: they return from reserved to the
// requester's available balance when the errand is cancelled or expires
// (FR4.1.5). Releasing an already released escrow is a successful no-op.
func (a *App) Release(ctx context.Context, orderID string) (Wallet, error) {
	if !validKey(orderID) {
		return Wallet{}, ErrInvalidInput
	}
	w, err := inTx(ctx, a.db, func(tx pgx.Tx) (Wallet, error) {
		r, err := lockReservation(ctx, tx, orderID)
		if err != nil {
			return Wallet{}, err
		}
		switch r.status {
		case statusReleased:
			return lockWallet(ctx, tx, r.userID)
		case statusTransferred:
			return Wallet{}, ErrReservationSettled
		}
		w, err := lockWallet(ctx, tx, r.userID)
		if err != nil {
			return Wallet{}, err
		}
		if _, err = tx.Exec(ctx, "UPDATE wallets SET reserved=reserved-$2, available=available+$2, updated_at=now() WHERE user_id=$1", r.userID, r.amount); err != nil {
			return Wallet{}, err
		}
		if _, err = tx.Exec(ctx, "UPDATE reservations SET status=$2, settled_at=now() WHERE order_id=$1", orderID, statusReleased); err != nil {
			return Wallet{}, err
		}
		if err = recordEntry(ctx, tx, entry{userID: r.userID, kind: KindRelease, amount: r.amount, orderID: orderID}); err != nil {
			return Wallet{}, err
		}
		// TODO(FR3.6.1): penalty for cancelling after pickup; rules not agreed yet.
		w.Available += r.amount
		w.Reserved -= r.amount
		return w, nil
	})
	if err == nil {
		a.log.Info("escrow released", "orderId", orderID)
	}
	return w, err
}

// Transfer pays an errand's escrow from the requester to the courier after the
// requester confirms delivery (FR4.1.4). The requester is taken from the
// escrow, so callers cannot move arbitrary credits (FR4.1.8). Both balances
// change in one transaction: both update or neither does (NFR3.3). Repeating a
// completed payout to the same courier is a successful no-op (NFR3.2).
func (a *App) Transfer(ctx context.Context, orderID, courierID string) error {
	courierID, ok := normalizeUUID(courierID)
	if !ok || !validKey(orderID) {
		return ErrInvalidInput
	}
	_, err := inTx(ctx, a.db, func(tx pgx.Tx) (struct{}, error) {
		r, err := lockReservation(ctx, tx, orderID)
		if err != nil {
			return struct{}{}, err
		}
		switch {
		case r.status == statusTransferred && r.courierID == courierID:
			return struct{}{}, nil
		case r.status != statusHeld:
			return struct{}{}, ErrReservationSettled
		case r.userID == courierID:
			return struct{}{}, ErrCourierIsRequester
		}
		if _, _, err = lockWallets(ctx, tx, r.userID, courierID); err != nil {
			return struct{}{}, err
		}
		if _, err = tx.Exec(ctx, "UPDATE wallets SET reserved=reserved-$2, updated_at=now() WHERE user_id=$1", r.userID, r.amount); err != nil {
			return struct{}{}, err
		}
		if _, err = tx.Exec(ctx, "UPDATE wallets SET available=available+$2, updated_at=now() WHERE user_id=$1", courierID, r.amount); err != nil {
			return struct{}{}, err
		}
		if _, err = tx.Exec(ctx, "UPDATE reservations SET status=$2, courier_id=$3, settled_at=now() WHERE order_id=$1", orderID, statusTransferred, courierID); err != nil {
			return struct{}{}, err
		}
		if err = recordEntry(ctx, tx, entry{userID: r.userID, kind: KindSpend, amount: r.amount, orderID: orderID}); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, recordEntry(ctx, tx, entry{userID: courierID, kind: KindReceive, amount: r.amount, orderID: orderID})
	})
	if err == nil {
		a.log.Info("escrow paid out", "orderId", orderID, "courierId", courierID)
	}
	return err
}

// AdminDebit removes credits from a user's available balance.
//
// Not in the D1 backlog: FR4.1.7 lists the only permitted balance changes and
// this is not one of them. Agree it with the team and add it to the backlog.
// It never touches reserved credits, so open errands can still be paid or
// released. requestID makes retries safe.
func (a *App) AdminDebit(ctx context.Context, adminID, userID string, amount int64, reason, requestID string) (Wallet, error) {
	if amount <= 0 {
		return Wallet{}, ErrInvalidAmount
	}
	adminID, adminOK := normalizeUUID(adminID)
	userID, userOK := normalizeUUID(userID)
	reason = strings.TrimSpace(reason)
	if !adminOK || !userOK || reason == "" || len(reason) > 500 || !validKey(requestID) {
		return Wallet{}, ErrInvalidInput
	}
	key := "admin_debit:" + requestID
	w, err := inTx(ctx, a.db, func(tx pgx.Tx) (Wallet, error) {
		w, err := lockWallet(ctx, tx, userID)
		if err != nil {
			return Wallet{}, err
		}
		var debited string
		var debit int64
		err = tx.QueryRow(ctx, "SELECT user_id::text, amount FROM transactions WHERE idempotency_key=$1", key).Scan(&debited, &debit)
		if err == nil {
			if debited != userID || debit != amount {
				return Wallet{}, ErrConflict
			}
			return w, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Wallet{}, err
		}
		if w.Available < amount {
			return Wallet{}, ErrInsufficientCredits
		}
		if _, err = tx.Exec(ctx, "UPDATE wallets SET available=available-$2, updated_at=now() WHERE user_id=$1", userID, amount); err != nil {
			return Wallet{}, err
		}
		err = recordEntry(ctx, tx, entry{userID: userID, kind: KindAdminDebit, amount: amount, key: key, note: fmt.Sprintf("admin %s: %s", adminID, reason)})
		if uniqueViolation(err) {
			// The same request ID was used for another user's debit concurrently.
			return Wallet{}, ErrConflict
		}
		if err != nil {
			return Wallet{}, err
		}
		w.Available -= amount
		return w, nil
	})
	if err == nil {
		a.log.Info("admin debited credits", "adminId", adminID, "userId", userID, "amount", amount)
	}
	return w, err
}

// inTx runs fn in one database transaction, so a balance change and its ledger
// entries commit together or not at all (NFR3.3).
func inTx[T any](ctx context.Context, db *pgxpool.Pool, fn func(pgx.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := db.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	result, err := fn(tx)
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return result, nil
}

// lockWallet reads a wallet and locks its row until the transaction ends, so
// concurrent operations on one user's credits run one after another.
func lockWallet(ctx context.Context, tx pgx.Tx, userID string) (Wallet, error) {
	w := Wallet{UserID: userID}
	err := tx.QueryRow(ctx, "SELECT available, reserved FROM wallets WHERE user_id=$1 FOR UPDATE", userID).Scan(&w.Available, &w.Reserved)
	if errors.Is(err, pgx.ErrNoRows) {
		return Wallet{}, ErrWalletNotFound
	}
	if err != nil {
		return Wallet{}, err
	}
	return w, nil
}

// lockWallets locks two wallets in ascending user ID order, whatever order
// they are passed in, so two concurrent transfers cannot deadlock.
func lockWallets(ctx context.Context, tx pgx.Tx, first, second string) (Wallet, Wallet, error) {
	low, high := first, second
	if high < low {
		low, high = high, low
	}
	lw, err := lockWallet(ctx, tx, low)
	if err != nil {
		return Wallet{}, Wallet{}, err
	}
	hw, err := lockWallet(ctx, tx, high)
	if err != nil {
		return Wallet{}, Wallet{}, err
	}
	if low == first {
		return lw, hw, nil
	}
	return hw, lw, nil
}

type reservation struct {
	orderID   string
	userID    string
	amount    int64
	status    string
	courierID string
}

// lockReservation reads an errand's escrow with SELECT ... FOR UPDATE, so a
// concurrent Release and Transfer for the same errand cannot both succeed.
func lockReservation(ctx context.Context, tx pgx.Tx, orderID string) (reservation, error) {
	r := reservation{orderID: orderID}
	err := tx.QueryRow(ctx, `SELECT user_id::text, amount, status, coalesce(courier_id::text, '')
		FROM reservations WHERE order_id=$1 FOR UPDATE`, orderID).Scan(&r.userID, &r.amount, &r.status, &r.courierID)
	if errors.Is(err, pgx.ErrNoRows) {
		return reservation{}, ErrReservationNotFound
	}
	if err != nil {
		return reservation{}, err
	}
	return r, nil
}

// entry is one ledger row. orderID, key and note are empty when not relevant.
type entry struct {
	userID  string
	kind    Kind
	amount  int64
	orderID string
	key     string
	note    string
}

// recordEntry appends to the ledger (FR4.2). Call it in the same transaction as
// the balance change it describes, so history always matches balances.
func recordEntry(ctx context.Context, tx pgx.Tx, e entry) error {
	_, err := tx.Exec(ctx, `INSERT INTO transactions(user_id, kind, amount, order_id, idempotency_key, note)
		VALUES($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''))`,
		e.userID, string(e.kind), e.amount, e.orderID, e.key, e.note)
	return err
}

// normalizeUUID accepts the canonical 36-character UUID form and lowercases it,
// matching how PostgreSQL prints UUIDs.
func normalizeUUID(s string) (string, bool) {
	if len(s) != 36 {
		return "", false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return "", false
			}
		case !strings.ContainsRune("0123456789abcdefABCDEF", c):
			return "", false
		}
	}
	return strings.ToLower(s), true
}

// validKey checks caller-chosen identifiers: order IDs and request IDs.
func validKey(s string) bool {
	return s != "" && len(s) <= 128 && strings.TrimSpace(s) == s
}

func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
