package order

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Adversarial verification of observable outcomes, separate from the state
// matrix: use actual commands, lose dependency replies, reconstruct the app,
// and check both committed order history and independent fake Credit effects.
type lostPayout struct{ escrowClient }

func (c lostPayout) Payout(ctx context.Context, id, courier string) error {
	if err := c.escrowClient.Payout(ctx, id, courier); err != nil {
		return err
	}
	return errors.New("response lost after payout")
}
func TestVerificationLostPayoutRestartAndOldOrderIsolation(t *testing.T) {
	a, f := testApp(t)
	a.auth = actorAuth{}
	ctx := context.Background()
	old := deliveredForTest(t, a, "verify-old")
	next := createdForTest(t, a, "verify-next")
	mustAction(t, a, next.ID, "courier", "accept", "accepted")
	mustAction(t, a, old.ID, "requester", "confirm", "delivered")
	a.now = func() time.Time { return time.Now().Add(2 * time.Second) }
	real := a.credit
	a.credit = lostPayout{real}
	if err := recoverForTest(a, ctx); err == nil {
		t.Fatal("lost payout not retained")
	}
	if readStored(t, a, old.ID).Status != "completed" {
		t.Fatal("completion gated on payout response")
	}
	var jobs int
	if err := a.db.QueryRow(ctx, "SELECT count(*) FROM order_payouts").Scan(&jobs); err != nil || jobs != 1 {
		t.Fatal("lost retry", jobs, err)
	}
	restarted := New(a.db, a.cfg, a.log)
	restarted.auth = actorAuth{}
	for range 2 {
		if err := recoverForTest(restarted, ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []string{"accept", "pick_up", "deliver"} {
		mustAction(t, restarted, old.ID, "courier", action, "completed")
	}
	mustAction(t, restarted, old.ID, "requester", "confirm", "completed")
	if readStored(t, a, next.ID).Status != "accepted" {
		t.Fatal("settling old errand changed active slot")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.payouts != 1 || f.releases != 0 || f.reserved != 10 {
		t.Fatal("duplicate financial effect", f.payouts, f.releases, f.reserved)
	}
}

func TestVerificationConcurrentDisputeAndConfirmation(t *testing.T) {
	a, f := testApp(t)
	a.auth = actorAuth{}
	o := deliveredForTest(t, a, "verify-dispute")
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, action := range []string{"confirm", "dispute"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got := actionForTest(t, a, o.ID, "requester", action)
			if action == "dispute" && got.Code != 200 {
				t.Errorf("dispute lost: %d", got.Code)
			}
			if action == "confirm" && got.Code != 202 && got.Code != 409 {
				t.Errorf("confirmation: %d", got.Code)
			}
		}()
	}
	close(start)
	wg.Wait()
	a.now = func() time.Time { return time.Now().Add(time.Hour) }
	if err := recoverForTest(a, context.Background()); err != nil {
		t.Fatal(err)
	}
	if readStored(t, a, o.ID).Status != "dispute" {
		t.Fatal("completion beat concurrent dispute")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.payouts != 0 || f.releases != 0 {
		t.Fatal("losing completion moved credits")
	}
}

func TestVerificationFinalExpiryCannotBeReopened(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	o := createdForTest(t, a, "verify-expiry")
	mustAction(t, a, o.ID, "courier", "accept", "accepted")
	mustAction(t, a, o.ID, "courier", "pick_up", "picked up")
	a.now = func() time.Time { return o.ExpiresAt }
	if err := advanceForTest(a, context.Background()); err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return o.ExpiresAt.Add(-time.Second) }
	if got := actionForTest(t, a, o.ID, "courier", "deliver"); got.Code != 409 {
		t.Fatal("earlier final expiry reopened", got.Code)
	}
}

func TestVerificationTransitionRollbackRetainsCapacity(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	o := createdForTest(t, a, "verify-rollback")
	mustAction(t, a, o.ID, "courier", "accept", "accepted")
	mustAction(t, a, o.ID, "courier", "pick_up", "picked up")
	_, err := a.db.Exec(context.Background(), `CREATE FUNCTION reject_delivery() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status='delivered' THEN RAISE EXCEPTION 'write failed'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_delivery BEFORE UPDATE ON orders FOR EACH ROW EXECUTE FUNCTION reject_delivery();`)
	if err != nil {
		t.Fatal(err)
	}
	if got := actionForTest(t, a, o.ID, "courier", "deliver"); got.Code != 500 {
		t.Fatal(got.Code)
	}
	if readStored(t, a, o.ID).Status != "picked up" {
		t.Fatal("failed delivery released slot")
	}
	var count int
	if err = a.db.QueryRow(context.Background(), "SELECT count(*) FROM order_actions WHERE order_id=$1 AND action='deliver'", o.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed action recorded as successful")
	}
	next := createdForTest(t, a, "verify-blocked")
	if got := actionForTest(t, a, next.ID, "courier", "accept"); got.Code != 409 {
		t.Fatal("failed delivery freed capacity", got.Code)
	}
}

func TestVerificationExpiryWorkerCannotOvertakeDelivery(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	o := createdForTest(t, a, "verify-worker")
	mustAction(t, a, o.ID, "courier", "accept", "accepted")
	mustAction(t, a, o.ID, "courier", "pick_up", "picked up")
	consume(t, a, timerConsumer)
	var mu sync.Mutex
	now := o.ExpiresAt.Add(-time.Second)
	a.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	entered, finish := make(chan struct{}), make(chan struct{})
	original := a.catalogue
	first := true
	a.catalogue = resolverFunc(func(ctx context.Context, ref PlaceRef, token string) (Place, error) {
		if first {
			first = false
			close(entered)
			<-finish
		}
		return original.Resolve(ctx, ref, token)
	})
	result := make(chan int, 1)
	go func() { result <- actionForTest(t, a, o.ID, "courier", "deliver").Code }()
	<-entered
	mu.Lock()
	now = o.ExpiresAt.Add(time.Second)
	mu.Unlock()
	err := advanceForTest(a, context.Background())
	close(finish)
	if code := <-result; code != 200 {
		t.Fatal("delivery lost", code)
	}
	if err != nil {
		t.Fatal(err)
	}
	if readStored(t, a, o.ID).Status != "delivered" {
		t.Fatal("worker overtook valid pre-deadline delivery")
	}
}
