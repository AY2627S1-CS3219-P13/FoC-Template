package credit

import (
	"context"
	"testing"
)

func TestAbortMissingEscrowFencesLateReserveWithoutFunds(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	u := newID(t)
	e.seedWallet(t, u, 100)
	_, err := e.app.Reserve(ctx, u, "lost-refusal", 101)
	wantErr(t, err, ErrInsufficientCredits)
	wantStatus(t, e.service(t, "POST", "/internal/v1/escrows/lost-refusal/abort", nil), 204)
	mustOK(t, e.app.Abort(ctx, "lost-refusal"))
	_, err = e.app.Reserve(ctx, u, "lost-refusal", 101)
	wantErr(t, err, ErrReservationSettled)
	e.wantStored(t, u, 100, 0)
	e.wantLedger(t, u)
	// Even an as-yet absent wallet can be fenced.
	mustOK(t, e.app.Abort(ctx, "absent-wallet"))
	newUser := newID(t)
	_, err = e.app.AllocateInitial(ctx, newUser)
	mustOK(t, err)
	_, err = e.app.Reserve(ctx, newUser, "absent-wallet", 10)
	wantErr(t, err, ErrReservationSettled)
}
func TestAbortHeldEscrowRefundsOnceAndProtectsPayout(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	u, c := newID(t), newID(t)
	e.seedWallet(t, u, 100)
	e.seedWallet(t, c, 100)
	_, err := e.app.Reserve(ctx, u, "held", 30)
	mustOK(t, err)
	mustOK(t, e.app.Abort(ctx, "held"))
	mustOK(t, e.app.Abort(ctx, "held"))
	e.wantStored(t, u, 100, 0)
	e.wantLedger(t, u, "reserve 30", "release 30")
	_, err = e.app.Reserve(ctx, u, "paid", 20)
	mustOK(t, err)
	mustOK(t, e.app.Transfer(ctx, "paid", c))
	wantErr(t, e.app.Abort(ctx, "paid"), ErrReservationSettled)
	e.wantStored(t, u, 80, 0)
	e.wantStored(t, c, 120, 0)
}
func TestConcurrentAbortAndReserveCannotLeaveHeldCredits(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	u := newID(t)
	e.seedWallet(t, u, 100)
	errs := concurrently(20, func(i int) error {
		if i%2 == 0 {
			return e.app.Abort(ctx, "race")
		}
		_, err := e.app.Reserve(ctx, u, "race", 30)
		if err == ErrReservationSettled {
			return nil
		}
		return err
	})
	for _, err := range errs {
		mustOK(t, err)
	}
	e.wantStored(t, u, 100, 0)
	_, err := e.app.Reserve(ctx, u, "race", 30)
	wantErr(t, err, ErrReservationSettled)
}
