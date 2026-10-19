package order

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestReview04LostRefusalCanFinishCleanupWithoutFunds(t *testing.T) {
	a, f := testApp(t)
	real := a.credit
	var first atomic.Bool
	a.credit = escrowFaults{escrowClient: real, reserve: func(ctx context.Context, id, user string, amount int64) error {
		err := real.Reserve(ctx, id, user, amount)
		if !first.Swap(true) {
			if !reservationRejected(err) {
				t.Fatal("fixture must definitively refuse the original reservation")
			}
			return creditUnavailable() // The refusal completed, but its response was lost.
		}
		return err
	}}
	const key = "lost-insufficient-refusal"
	assertStatus(t, request(t, a, "POST", "/api/v1/orders", "session", key, validCreateBody(101)), 503)
	for range 3 {
		if _, err := a.db.Exec(context.Background(), "UPDATE creation_attempts SET updated_at=clock_timestamp()-interval '2 seconds'"); err != nil {
			t.Fatal(err)
		}
		_ = a.Recover(context.Background()) // Capture the durable result below, including failed retries.
	}
	var state string
	if err := a.db.QueryRow(context.Background(), "SELECT state FROM creation_attempts WHERE idempotency_key=$1", key).Scan(&state); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	available, reserved, escrows := f.available, f.reserved, len(f.escrows)
	f.mu.Unlock()
	if available != 100 || reserved != 0 || escrows != 0 {
		t.Fatal("fixture unexpectedly moved credits")
	}
	if state != "abandoned" {
		t.Fatalf("cleanup cannot terminate after three retries: state=%q, available=%d, reserved=%d, escrows=%d; want abandoned without acquiring funds", state, available, reserved, escrows)
	}
}
