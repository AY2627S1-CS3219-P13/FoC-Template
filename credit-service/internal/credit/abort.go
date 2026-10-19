package credit

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

func lockEscrowID(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('escrow:' || $1,0))", id)
	return err
}

// Abort fences even an absent escrow. No wallet or available credits are needed
// to record absence. Existing held funds are released in this same transaction.
func (a *App) Abort(ctx context.Context, id string) error {
	if !validKey(id) {
		return ErrInvalidInput
	}
	_, err := inTx(ctx, a, func(tx *ledgerTx) (Wallet, error) {
		if err := lockEscrowID(ctx, tx, id); err != nil {
			return Wallet{}, err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO escrow_aborts(order_id) VALUES($1) ON CONFLICT DO NOTHING", id); err != nil {
			return Wallet{}, err
		}
		r, err := lockReservation(ctx, tx, id)
		if errors.Is(err, ErrReservationNotFound) {
			return Wallet{}, nil
		}
		if err != nil {
			return Wallet{}, err
		}
		if r.status == statusTransferred {
			return Wallet{}, ErrReservationSettled
		}
		if r.status == statusReleased {
			return Wallet{}, nil
		}
		if _, err = tx.Exec(ctx, "UPDATE reservations SET status=$2,settled_at=now() WHERE order_id=$1", id, statusReleased); err != nil {
			return Wallet{}, err
		}
		return tx.apply(ctx, entry{userID: r.userID, kind: KindRelease, amount: r.amount, orderID: id})
	})
	return err
}
