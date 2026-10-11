package order

import (
	"context"
	"testing"
	"time"
)

func TestDisputeDuringCompletionWritesVetoesDecisionAcrossReplicas(t *testing.T) {
	for _, mode := range []string{"manual", "automatic"} {
		t.Run(mode, func(t *testing.T) {
			a, f := testApp(t)
			a.auth = actorAuth{}
			o := deliveredForTest(t, a, "overlap-"+mode)
			if mode == "manual" {
				mustAction(t, a, o.ID, "requester", "confirm", "delivered")
				a.now = func() time.Time { return time.Now().Add(2 * time.Second) }
			} else {
				a.now = func() time.Time { return time.Now().Add(6 * time.Minute) }
			}
			b := admissionReplica(a)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			blocker, err := a.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer rollback(blocker)
			// Pause after the speculative status/event write, before payout insert.
			if _, err = blocker.Exec(ctx, "LOCK TABLE order_payouts IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- a.Advance(ctx) }()
			for {
				var waiting bool
				if err = a.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='order_payouts'::regclass AND NOT granted)").Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("completion never reached payout write")
				case <-time.After(10 * time.Millisecond):
				}
			}
			entered, finish := delayAdmissionAuth(t, b, "requester", false)
			result := make(chan int, 1)
			go func() { result <- actionForTest(t, b, o.ID, "requester", "dispute").Code }()
			awaitSignal(t, entered) // registration must not be blocked by worker writes
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			if readStored(t, a, o.ID).Status != "delivered" {
				t.Fatal("speculative completion escaped rollback")
			}
			// Authentication may outlast the old one-second overlap interval.
			time.Sleep(1100 * time.Millisecond)
			if err = a.Advance(ctx); err != nil {
				t.Fatal(err)
			}
			finish()
			awaitCode(t, result, 200)
			if err = a.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			stored := readStored(t, a, o.ID)
			var events, payouts int
			if err = a.db.QueryRow(ctx, "SELECT count(*) FROM order_events WHERE order_id=$1", o.ID).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if err = a.db.QueryRow(ctx, "SELECT count(*) FROM order_payouts WHERE order_id=$1", o.ID).Scan(&payouts); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if stored.Status != "dispute" || int64(events) != stored.Version || payouts != 0 || f.payouts != 0 {
				t.Fatal("losing completion left a status, event or financial effect", stored.Status, events, payouts, f.payouts)
			}
		})
	}
}
