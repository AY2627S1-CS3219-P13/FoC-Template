package credit

import (
	"context"
	"testing"
	"time"
)

// withEntries gives userID a wallet with 1 allocation and n one-credit
// reservations: n+1 ledger entries, created through the real operations.
func withEntries(t *testing.T, e *testEnv, userID string, n int) {
	t.Helper()
	ctx := context.Background()
	_, err := e.app.AllocateInitial(ctx, userID)
	mustOK(t, err)
	for range n {
		_, err = e.app.Reserve(ctx, userID, newID(t), 1)
		mustOK(t, err)
	}
}

func TestHistory(t *testing.T) {
	ctx := context.Background()

	t.Run("records every movement, newest first, with balances after each", func(t *testing.T) {
		e := newTestEnv(t)
		requester, courier, first, second := newID(t), newID(t), newID(t), newID(t)
		for _, u := range []string{requester, courier} {
			_, err := e.app.AllocateInitial(ctx, u)
			mustOK(t, err)
		}
		_, err := e.app.Reserve(ctx, requester, first, 30)
		mustOK(t, err)
		_, err = e.app.Release(ctx, first)
		mustOK(t, err)
		_, err = e.app.Reserve(ctx, requester, second, 20)
		mustOK(t, err)
		mustOK(t, e.app.Transfer(ctx, second, courier))
		_, err = e.app.AdminDebit(ctx, newID(t), requester, 10, "abuse", newID(t))
		mustOK(t, err)

		type row struct {
			kind                                  Kind
			amount, availableDelta, reservedDelta int64
			availableAfter, reservedAfter         int64
			orderID, counterpartyID               string
		}
		check := func(userID string, want []row) {
			t.Helper()
			got, err := e.app.History(ctx, userID, DefaultHistoryLimit, 0)
			mustOK(t, err)
			if len(got) != len(want) {
				t.Fatalf("%d entries, expected %d: %+v", len(got), len(want), got)
			}
			for i, g := range got {
				r := row{g.Kind, g.Amount, g.AvailableDelta, g.ReservedDelta, g.AvailableAfter, g.ReservedAfter, g.OrderID, g.CounterpartyID}
				if r != want[i] {
					t.Fatalf("entry %d is %+v, expected %+v", i, r, want[i])
				}
				if i > 0 && g.ID >= got[i-1].ID {
					t.Fatal("entries are not newest first")
				}
				if g.CreatedAt.IsZero() || time.Since(g.CreatedAt) > time.Minute {
					t.Fatalf("implausible timestamp %v", g.CreatedAt)
				}
			}
		}
		check(requester, []row{
			{KindAdminDebit, 10, -10, 0, 70, 0, "", ""},
			{KindSpend, 20, 0, -20, 80, 0, second, courier},
			{KindReserve, 20, -20, 20, 80, 20, second, ""},
			{KindRelease, 30, 30, -30, 100, 0, first, ""},
			{KindReserve, 30, -30, 30, 70, 30, first, ""},
			{KindAllocation, 100, 100, 0, 100, 0, "", ""},
		})
		check(courier, []row{
			{KindReceive, 20, 20, 0, 120, 0, second, requester},
			{KindAllocation, 100, 100, 0, 100, 0, "", ""},
		})
	})

	t.Run("rejected and repeated operations add nothing", func(t *testing.T) {
		e := newTestEnv(t)
		u, order := newID(t), newID(t)
		_, err := e.app.AllocateInitial(ctx, u)
		mustOK(t, err)
		_, err = e.app.AllocateInitial(ctx, u)
		mustOK(t, err)
		_, err = e.app.Reserve(ctx, u, order, 30)
		mustOK(t, err)
		_, err = e.app.Reserve(ctx, u, order, 30)
		mustOK(t, err)
		_, err = e.app.Reserve(ctx, u, newID(t), 500)
		wantErr(t, err, ErrInsufficientCredits)
		got, err := e.app.History(ctx, u, DefaultHistoryLimit, 0)
		mustOK(t, err)
		if len(got) != 2 {
			t.Fatalf("expected allocation and one reserve, got %+v", got)
		}
	})

	t.Run("only the user's own entries", func(t *testing.T) {
		e := newTestEnv(t)
		u, other := newID(t), newID(t)
		withEntries(t, e, u, 2)
		withEntries(t, e, other, 5)
		got, err := e.app.History(ctx, u, DefaultHistoryLimit, 0)
		mustOK(t, err)
		if len(got) != 3 {
			t.Fatalf("expected 3 entries, got %d", len(got))
		}
	})

	t.Run("pages cover every entry exactly once", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		withEntries(t, e, u, 34)
		page, err := e.app.History(ctx, u, 30, 0)
		mustOK(t, err)
		if len(page) != 30 {
			t.Fatalf("first page has %d entries, expected 30", len(page))
		}
		rest, err := e.app.History(ctx, u, 30, page[len(page)-1].ID)
		mustOK(t, err)
		if len(rest) != 5 {
			t.Fatalf("second page has %d entries, expected 5", len(rest))
		}
		all := append(page, rest...)
		for i := 1; i < len(all); i++ {
			if all[i].ID >= all[i-1].ID {
				t.Fatal("pages overlap or are out of order")
			}
		}
		if all[len(all)-1].Kind != KindAllocation {
			t.Fatal("the oldest entry should be the allocation")
		}
	})

	t.Run("wallet without entries", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 0)
		got, err := e.app.History(ctx, u, DefaultHistoryLimit, 0)
		mustOK(t, err)
		if got == nil || len(got) != 0 {
			t.Fatalf("expected an empty, non-nil history, got %#v", got)
		}
	})

	t.Run("invalid limit, cursor or user ID", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		withEntries(t, e, u, 0)
		for _, limit := range []int{0, -1, MaxHistoryLimit + 1} {
			_, err := e.app.History(ctx, u, limit, 0)
			wantErr(t, err, ErrInvalidInput)
		}
		_, err := e.app.History(ctx, u, 10, -1)
		wantErr(t, err, ErrInvalidInput)
		_, err = e.app.History(ctx, "not-a-uuid", 10, 0)
		wantErr(t, err, ErrInvalidInput)
	})

	t.Run("unknown wallet", func(t *testing.T) {
		e := newTestEnv(t)
		_, err := e.app.History(ctx, newID(t), DefaultHistoryLimit, 0)
		wantErr(t, err, ErrWalletNotFound)
	})

	t.Run("NFR1.2: 100 concurrent reads of 30 entries within 5 seconds", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		withEntries(t, e, u, 40)
		start := time.Now()
		errs := concurrently(100, func(int) error {
			page, err := e.app.History(ctx, u, 30, 0)
			if err == nil && len(page) != 30 {
				t.Errorf("page has %d entries", len(page))
			}
			return err
		})
		if n := count(errs, nil); n != 100 {
			t.Fatalf("%d of 100 reads succeeded: %v", n, errs)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("100 concurrent reads took %v", elapsed)
		}
	})
}

func TestLedgerGuarantees(t *testing.T) {
	ctx := context.Background()

	t.Run("history is append-only", func(t *testing.T) {
		e := newTestEnv(t)
		withEntries(t, e, newID(t), 1)
		for _, sql := range []string{"UPDATE transactions SET amount=amount+1", "DELETE FROM transactions", "TRUNCATE transactions"} {
			if _, err := e.app.db.Exec(ctx, sql); err == nil {
				t.Fatalf("%q should be rejected", sql)
			}
		}
	})

	t.Run("an entry must match its kind's effect", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		_, err := e.app.db.Exec(ctx, `INSERT INTO transactions(user_id, kind, amount, available_delta, reserved_delta, available_after, reserved_after)
			VALUES($1, 'reserve', 10, 10, 0, 110, 0)`, u)
		if err == nil {
			t.Fatal("a reserve that adds available credits should be rejected")
		}
	})

	t.Run("apply refuses unknown kinds and non-positive amounts", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		for _, bad := range []entry{{userID: u, kind: "gift", amount: 10}, {userID: u, kind: KindAllocation, amount: 0}} {
			_, err := inTx(ctx, e.app, func(tx *ledgerTx) (Wallet, error) { return tx.apply(ctx, bad) })
			if err == nil {
				t.Fatalf("entry %+v should be refused", bad)
			}
		}
		e.wantStored(t, u, 100, 0)
	})
}
