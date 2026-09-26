package credit

import (
	"context"
	"testing"
	"time"
)

// Function-level tests: one Test per operation, one subtest per edge case.
// http_test.go repeats the same cases through the endpoints.

func TestBalance(t *testing.T) {
	ctx := context.Background()

	t.Run("reports available and reserved separately", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		e.seedEscrow(t, u, newID(t), 30)
		w, err := e.app.Balance(ctx, u)
		mustOK(t, err)
		wantWallet(t, w, u, 70, 30)
	})

	t.Run("unknown user", func(t *testing.T) {
		e := newTestEnv(t)
		_, err := e.app.Balance(ctx, newID(t))
		wantErr(t, err, ErrWalletNotFound)
	})

	t.Run("malformed user ID", func(t *testing.T) {
		e := newTestEnv(t)
		for _, id := range []string{"", "not-a-uuid", "1; DROP TABLE wallets"} {
			_, err := e.app.Balance(ctx, id)
			wantErr(t, err, ErrInvalidInput)
		}
	})
}

func TestAllocateInitial(t *testing.T) {
	ctx := context.Background()

	t.Run("creates a wallet with the initial credits", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		w, err := e.app.AllocateInitial(ctx, u)
		mustOK(t, err)
		wantWallet(t, w, u, initialCredits, 0)
		e.wantStored(t, u, initialCredits, 0)
		e.wantLedger(t, u, "allocation 100")
	})

	t.Run("repeat allocates nothing", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		_, err := e.app.AllocateInitial(ctx, u)
		mustOK(t, err)
		w, err := e.app.AllocateInitial(ctx, u)
		mustOK(t, err)
		wantWallet(t, w, u, initialCredits, 0)
		e.wantLedger(t, u, "allocation 100")
	})

	t.Run("repeat after spending does not top up", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		_, err := e.app.AllocateInitial(ctx, u)
		mustOK(t, err)
		e.seedEscrow(t, u, newID(t), 60)
		w, err := e.app.AllocateInitial(ctx, u)
		mustOK(t, err)
		wantWallet(t, w, u, 40, 60)
	})

	t.Run("concurrent calls allocate exactly once", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		errs := concurrently(10, func(int) error {
			_, err := e.app.AllocateInitial(ctx, u)
			return err
		})
		if count(errs, nil) != 10 {
			t.Fatalf("every call should succeed: %v", errs)
		}
		e.wantStored(t, u, initialCredits, 0)
		e.wantLedger(t, u, "allocation 100")
	})

	t.Run("malformed user ID", func(t *testing.T) {
		e := newTestEnv(t)
		_, err := e.app.AllocateInitial(ctx, "not-a-uuid")
		wantErr(t, err, ErrInvalidInput)
		if e.totalCredits(t) != 0 {
			t.Fatal("no wallet should be created")
		}
	})
}

func TestReserve(t *testing.T) {
	ctx := context.Background()

	t.Run("moves credits from available to reserved", func(t *testing.T) {
		e := newTestEnv(t)
		u, order := newID(t), newID(t)
		e.seedWallet(t, u, 100)
		w, err := e.app.Reserve(ctx, u, order, 30)
		mustOK(t, err)
		wantWallet(t, w, u, 70, 30)
		e.wantStored(t, u, 70, 30)
		e.wantLedger(t, u, "reserve 30")
		if status, _ := e.escrow(t, order); status != "held" {
			t.Fatalf("escrow status %q, expected held", status)
		}
	})

	t.Run("whole available balance", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		w, err := e.app.Reserve(ctx, u, newID(t), 100)
		mustOK(t, err)
		wantWallet(t, w, u, 0, 100)
	})

	t.Run("more than available", func(t *testing.T) {
		e := newTestEnv(t)
		u, order := newID(t), newID(t)
		e.seedWallet(t, u, 100)
		_, err := e.app.Reserve(ctx, u, order, 101)
		wantErr(t, err, ErrInsufficientCredits)
		e.wantStored(t, u, 100, 0)
		e.wantLedger(t, u)
		if e.escrowExists(t, order) {
			t.Fatal("a rejected reservation must not create an escrow")
		}
	})

	t.Run("reserved credits cannot be reserved again", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		e.seedEscrow(t, u, newID(t), 70)
		_, err := e.app.Reserve(ctx, u, newID(t), 40)
		wantErr(t, err, ErrInsufficientCredits)
		e.wantStored(t, u, 30, 70)
	})

	t.Run("non-positive amount", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		for _, amount := range []int64{0, -1, -100} {
			_, err := e.app.Reserve(ctx, u, newID(t), amount)
			wantErr(t, err, ErrInvalidAmount)
		}
		e.wantStored(t, u, 100, 0)
	})

	t.Run("unknown wallet", func(t *testing.T) {
		e := newTestEnv(t)
		_, err := e.app.Reserve(ctx, newID(t), newID(t), 10)
		wantErr(t, err, ErrWalletNotFound)
	})

	t.Run("malformed user or missing order ID", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		_, err := e.app.Reserve(ctx, "not-a-uuid", newID(t), 10)
		wantErr(t, err, ErrInvalidInput)
		_, err = e.app.Reserve(ctx, u, "", 10)
		wantErr(t, err, ErrInvalidInput)
		e.wantStored(t, u, 100, 0)
	})

	t.Run("retry with the same order reserves once", func(t *testing.T) {
		e := newTestEnv(t)
		u, order := newID(t), newID(t)
		e.seedWallet(t, u, 100)
		_, err := e.app.Reserve(ctx, u, order, 30)
		mustOK(t, err)
		w, err := e.app.Reserve(ctx, u, order, 30)
		mustOK(t, err)
		wantWallet(t, w, u, 70, 30)
		e.wantLedger(t, u, "reserve 30")
	})

	t.Run("same order with a different amount or user", func(t *testing.T) {
		e := newTestEnv(t)
		u, other, order := newID(t), newID(t), newID(t)
		e.seedWallet(t, u, 100)
		e.seedWallet(t, other, 100)
		_, err := e.app.Reserve(ctx, u, order, 30)
		mustOK(t, err)
		_, err = e.app.Reserve(ctx, u, order, 40)
		wantErr(t, err, ErrConflict)
		_, err = e.app.Reserve(ctx, other, order, 30)
		wantErr(t, err, ErrConflict)
		e.wantStored(t, u, 70, 30)
		e.wantStored(t, other, 100, 0)
	})

	t.Run("concurrent reservations cannot overspend", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		orders := make([]string, 10)
		for i := range orders {
			orders[i] = newID(t)
		}
		errs := concurrently(10, func(i int) error {
			_, err := e.app.Reserve(ctx, u, orders[i], 20)
			return err
		})
		if count(errs, nil) != 5 || count(errs, ErrInsufficientCredits) != 5 {
			t.Fatalf("expected 5 successes and 5 insufficient: %v", errs)
		}
		e.wantStored(t, u, 0, 100)
	})

	t.Run("concurrent retries of one order reserve once", func(t *testing.T) {
		e := newTestEnv(t)
		u, order := newID(t), newID(t)
		e.seedWallet(t, u, 100)
		errs := concurrently(10, func(int) error {
			_, err := e.app.Reserve(ctx, u, order, 30)
			return err
		})
		if count(errs, nil) != 10 {
			t.Fatalf("every retry should succeed: %v", errs)
		}
		e.wantStored(t, u, 70, 30)
		e.wantLedger(t, u, "reserve 30")
	})
}

func TestRelease(t *testing.T) {
	ctx := context.Background()

	t.Run("returns reserved credits to available", func(t *testing.T) {
		e := newTestEnv(t)
		u, order := newID(t), newID(t)
		e.seedWallet(t, u, 100)
		e.seedEscrow(t, u, order, 30)
		w, err := e.app.Release(ctx, order)
		mustOK(t, err)
		wantWallet(t, w, u, 100, 0)
		e.wantStored(t, u, 100, 0)
		e.wantLedger(t, u, "release 30")
		if status, _ := e.escrow(t, order); status != "released" {
			t.Fatalf("escrow status %q, expected released", status)
		}
	})

	t.Run("releases only that order", func(t *testing.T) {
		e := newTestEnv(t)
		u, first, second := newID(t), newID(t), newID(t)
		e.seedWallet(t, u, 100)
		e.seedEscrow(t, u, first, 30)
		e.seedEscrow(t, u, second, 20)
		w, err := e.app.Release(ctx, first)
		mustOK(t, err)
		wantWallet(t, w, u, 80, 20)
		if status, _ := e.escrow(t, second); status != "held" {
			t.Fatalf("other escrow status %q, expected held", status)
		}
	})

	t.Run("repeat releases once", func(t *testing.T) {
		e := newTestEnv(t)
		u, order := newID(t), newID(t)
		e.seedWallet(t, u, 100)
		e.seedEscrow(t, u, order, 30)
		_, err := e.app.Release(ctx, order)
		mustOK(t, err)
		w, err := e.app.Release(ctx, order)
		mustOK(t, err)
		wantWallet(t, w, u, 100, 0)
		e.wantLedger(t, u, "release 30")
	})

	t.Run("after payout", func(t *testing.T) {
		e := newTestEnv(t)
		u, courier, order := newID(t), newID(t), newID(t)
		e.seedWallet(t, u, 100)
		e.seedWallet(t, courier, 100)
		e.seedEscrow(t, u, order, 30)
		e.seedPaidOut(t, order, courier)
		_, err := e.app.Release(ctx, order)
		wantErr(t, err, ErrReservationSettled)
		e.wantStored(t, u, 70, 0)
		e.wantStored(t, courier, 130, 0)
	})

	t.Run("unknown order", func(t *testing.T) {
		e := newTestEnv(t)
		_, err := e.app.Release(ctx, newID(t))
		wantErr(t, err, ErrReservationNotFound)
	})

	t.Run("missing order ID", func(t *testing.T) {
		e := newTestEnv(t)
		_, err := e.app.Release(ctx, "")
		wantErr(t, err, ErrInvalidInput)
	})

	t.Run("concurrent releases release once", func(t *testing.T) {
		e := newTestEnv(t)
		u, order := newID(t), newID(t)
		e.seedWallet(t, u, 100)
		e.seedEscrow(t, u, order, 30)
		errs := concurrently(10, func(int) error {
			_, err := e.app.Release(ctx, order)
			return err
		})
		if count(errs, nil) != 10 {
			t.Fatalf("every release should succeed: %v", errs)
		}
		e.wantStored(t, u, 100, 0)
		e.wantLedger(t, u, "release 30")
	})
}

func TestTransfer(t *testing.T) {
	ctx := context.Background()

	// setup gives a requester with 30 of 100 credits in escrow, and a courier with 100.
	setup := func(t *testing.T) (e *testEnv, requester, courier, order string) {
		e = newTestEnv(t)
		requester, courier, order = newID(t), newID(t), newID(t)
		e.seedWallet(t, requester, 100)
		e.seedWallet(t, courier, 100)
		e.seedEscrow(t, requester, order, 30)
		return e, requester, courier, order
	}

	t.Run("pays the escrow to the courier", func(t *testing.T) {
		e, requester, courier, order := setup(t)
		mustOK(t, e.app.Transfer(ctx, order, courier))
		e.wantStored(t, requester, 70, 0)
		e.wantStored(t, courier, 130, 0)
		e.wantLedger(t, requester, "spend 30")
		e.wantLedger(t, courier, "receive 30")
		if status, paid := e.escrow(t, order); status != "transferred" || paid != courier {
			t.Fatalf("escrow %q to %q, expected transferred to %q", status, paid, courier)
		}
		if e.totalCredits(t) != 200 {
			t.Fatal("a transfer must not create or destroy credits")
		}
	})

	t.Run("repeat to the same courier pays once", func(t *testing.T) {
		e, requester, courier, order := setup(t)
		mustOK(t, e.app.Transfer(ctx, order, courier))
		mustOK(t, e.app.Transfer(ctx, order, courier))
		e.wantStored(t, requester, 70, 0)
		e.wantStored(t, courier, 130, 0)
		e.wantLedger(t, courier, "receive 30")
	})

	t.Run("repeat to a different courier", func(t *testing.T) {
		e, _, courier, order := setup(t)
		other := newID(t)
		e.seedWallet(t, other, 100)
		mustOK(t, e.app.Transfer(ctx, order, courier))
		wantErr(t, e.app.Transfer(ctx, order, other), ErrReservationSettled)
		e.wantStored(t, other, 100, 0)
	})

	t.Run("after release", func(t *testing.T) {
		e, requester, courier, order := setup(t)
		e.seedReleased(t, order)
		wantErr(t, e.app.Transfer(ctx, order, courier), ErrReservationSettled)
		e.wantStored(t, requester, 100, 0)
		e.wantStored(t, courier, 100, 0)
	})

	t.Run("courier is the requester", func(t *testing.T) {
		e, requester, _, order := setup(t)
		wantErr(t, e.app.Transfer(ctx, order, requester), ErrCourierIsRequester)
		e.wantStored(t, requester, 70, 30)
	})

	t.Run("courier has no wallet leaves everything unchanged", func(t *testing.T) {
		e, requester, _, order := setup(t)
		wantErr(t, e.app.Transfer(ctx, order, newID(t)), ErrWalletNotFound)
		e.wantStored(t, requester, 70, 30)
		e.wantLedger(t, requester)
		if status, _ := e.escrow(t, order); status != "held" {
			t.Fatalf("escrow status %q, expected held", status)
		}
	})

	t.Run("unknown order", func(t *testing.T) {
		e, _, courier, _ := setup(t)
		wantErr(t, e.app.Transfer(ctx, newID(t), courier), ErrReservationNotFound)
	})

	t.Run("malformed courier or missing order ID", func(t *testing.T) {
		e, _, courier, order := setup(t)
		wantErr(t, e.app.Transfer(ctx, order, "not-a-uuid"), ErrInvalidInput)
		wantErr(t, e.app.Transfer(ctx, "", courier), ErrInvalidInput)
	})

	t.Run("concurrent payout and release: exactly one wins", func(t *testing.T) {
		e, requester, courier, order := setup(t)
		errs := concurrently(2, func(i int) error {
			if i == 0 {
				return e.app.Transfer(ctx, order, courier)
			}
			_, err := e.app.Release(ctx, order)
			return err
		})
		if count(errs, nil) != 1 || count(errs, ErrReservationSettled) != 1 {
			t.Fatalf("expected one success and one escrow-settled: %v", errs)
		}
		if got := e.stored(t, requester); got.Reserved != 0 {
			t.Fatalf("requester still has %d reserved", got.Reserved)
		}
		if e.totalCredits(t) != 200 {
			t.Fatal("a race must not create or destroy credits")
		}
	})

	t.Run("concurrent repeats pay once", func(t *testing.T) {
		e, _, courier, order := setup(t)
		errs := concurrently(10, func(int) error { return e.app.Transfer(ctx, order, courier) })
		if count(errs, nil) != 10 {
			t.Fatalf("every repeat should succeed: %v", errs)
		}
		e.wantStored(t, courier, 130, 0)
	})

	t.Run("opposite transfers between two users do not deadlock", func(t *testing.T) {
		e := newTestEnv(t)
		a, b := newID(t), newID(t)
		e.seedWallet(t, a, 100)
		e.seedWallet(t, b, 100)
		type job struct{ order, courier string }
		jobs := make([]job, 20)
		for i := range jobs {
			from, to := a, b
			if i%2 == 1 {
				from, to = b, a
			}
			jobs[i] = job{newID(t), to}
			e.seedEscrow(t, from, jobs[i].order, 5)
		}
		deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		errs := concurrently(len(jobs), func(i int) error { return e.app.Transfer(deadline, jobs[i].order, jobs[i].courier) })
		if count(errs, nil) != len(jobs) {
			t.Fatalf("every transfer should succeed: %v", errs)
		}
		e.wantStored(t, a, 100, 0)
		e.wantStored(t, b, 100, 0)
	})
}

func TestAdminDebit(t *testing.T) {
	ctx := context.Background()

	t.Run("removes available credits", func(t *testing.T) {
		e := newTestEnv(t)
		admin, u := newID(t), newID(t)
		e.seedWallet(t, u, 100)
		w, err := e.app.AdminDebit(ctx, admin, u, 30, "duplicate account", newID(t))
		mustOK(t, err)
		wantWallet(t, w, u, 70, 0)
		e.wantStored(t, u, 70, 0)
		e.wantLedger(t, u, "admin_debit 30")
	})

	t.Run("whole available balance", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		w, err := e.app.AdminDebit(ctx, newID(t), u, 100, "abuse", newID(t))
		mustOK(t, err)
		wantWallet(t, w, u, 0, 0)
	})

	t.Run("never touches reserved credits", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		e.seedEscrow(t, u, newID(t), 60)
		_, err := e.app.AdminDebit(ctx, newID(t), u, 50, "abuse", newID(t))
		wantErr(t, err, ErrInsufficientCredits)
		e.wantStored(t, u, 40, 60)
		e.wantLedger(t, u)
	})

	t.Run("non-positive amount", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		for _, amount := range []int64{0, -1} {
			_, err := e.app.AdminDebit(ctx, newID(t), u, amount, "abuse", newID(t))
			wantErr(t, err, ErrInvalidAmount)
		}
		e.wantStored(t, u, 100, 0)
	})

	t.Run("missing reason or request ID, malformed user", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		_, err := e.app.AdminDebit(ctx, newID(t), u, 10, "", newID(t))
		wantErr(t, err, ErrInvalidInput)
		_, err = e.app.AdminDebit(ctx, newID(t), u, 10, "abuse", "")
		wantErr(t, err, ErrInvalidInput)
		_, err = e.app.AdminDebit(ctx, newID(t), "not-a-uuid", 10, "abuse", newID(t))
		wantErr(t, err, ErrInvalidInput)
		e.wantStored(t, u, 100, 0)
	})

	t.Run("retry with the same request ID debits once", func(t *testing.T) {
		e := newTestEnv(t)
		admin, u, request := newID(t), newID(t), newID(t)
		e.seedWallet(t, u, 100)
		_, err := e.app.AdminDebit(ctx, admin, u, 30, "abuse", request)
		mustOK(t, err)
		w, err := e.app.AdminDebit(ctx, admin, u, 30, "abuse", request)
		mustOK(t, err)
		wantWallet(t, w, u, 70, 0)
		e.wantLedger(t, u, "admin_debit 30")
	})

	t.Run("same request ID with a different amount", func(t *testing.T) {
		e := newTestEnv(t)
		admin, u, request := newID(t), newID(t), newID(t)
		e.seedWallet(t, u, 100)
		_, err := e.app.AdminDebit(ctx, admin, u, 30, "abuse", request)
		mustOK(t, err)
		_, err = e.app.AdminDebit(ctx, admin, u, 50, "abuse", request)
		wantErr(t, err, ErrConflict)
		e.wantStored(t, u, 70, 0)
	})

	t.Run("unknown wallet", func(t *testing.T) {
		e := newTestEnv(t)
		_, err := e.app.AdminDebit(ctx, newID(t), newID(t), 10, "abuse", newID(t))
		wantErr(t, err, ErrWalletNotFound)
	})

	t.Run("concurrent debits cannot go negative", func(t *testing.T) {
		e := newTestEnv(t)
		admin, u := newID(t), newID(t)
		e.seedWallet(t, u, 100)
		requests := make([]string, 10)
		for i := range requests {
			requests[i] = newID(t)
		}
		errs := concurrently(10, func(i int) error {
			_, err := e.app.AdminDebit(ctx, admin, u, 20, "abuse", requests[i])
			return err
		})
		if count(errs, nil) != 5 || count(errs, ErrInsufficientCredits) != 5 {
			t.Fatalf("expected 5 successes and 5 insufficient: %v", errs)
		}
		e.wantStored(t, u, 0, 0)
	})
}
