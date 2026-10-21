package order

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHistorySnapshotsReconcileEveryDisplayedOrder(t *testing.T) {
	for _, scope := range []string{"mine", "courier", "disputes"} {
		t.Run(scope, func(t *testing.T) {
			a, _ := testApp(t)
			a.auth = actorAuth{}
			statuses := []string{"open", "accepted", "picked up", "delivered", "dispute", "completed", "cancelled", "expired", "not completed"}
			if scope == "disputes" {
				statuses = []string{"dispute", "dispute"}
			}
			for i, status := range statuses {
				o := createdForTest(t, a, status+strings.Repeat("x", i))
				// Distinct couriers respect the active-slot constraint.
				courier := courierUser
				if status == "picked up" {
					courier = otherUser
				}
				if _, err := a.db.Exec(context.Background(), "UPDATE orders SET status=$2,courier_id=$3 WHERE id=$1", o.ID, status, courier); err != nil {
					t.Fatal(err)
				}
			}
			actor := "requester"
			if scope == "courier" {
				actor = "courier"
			} else if scope == "disputes" {
				actor = "admin"
			}
			underlying := a.catalogue
			var calls atomic.Int64
			a.catalogue = resolverFunc(func(ctx context.Context, ref PlaceRef, token string) (Place, error) {
				calls.Add(1)
				if token != actor {
					t.Error("snapshot did not use the current session")
				}
				p, err := underlying.Resolve(ctx, ref, token)
				p.Name = "Renamed"
				return p, err
			})
			path := "/api/v1/orders?scope=" + scope
			got := request(t, a, "GET", path, actor, "", nil)
			var body struct{ Orders []Order }
			if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &body) != nil || len(body.Orders) < 2 || calls.Load() != 2 {
				t.Fatalf("snapshot: %d %s calls=%d", got.Code, got.Body.String(), calls.Load())
			}
			for _, o := range body.Orders {
				if o.Pickup.Name != "Renamed" || o.Delivery.Name != "Renamed" {
					t.Fatal("unselected order retained stale details")
				}
			}
			a.catalogue = resolverFunc(func(context.Context, PlaceRef, string) (Place, error) { return Place{}, placesUnavailable() })
			if got = request(t, a, "GET", path, actor, "", nil); got.Code != 503 {
				t.Fatal("outage reported a synchronized list", got.Code)
			}
			a.catalogue = resolverFunc(func(context.Context, PlaceRef, string) (Place, error) { return Place{}, invalidPlace() })
			for range 2 {
				if got = request(t, a, "GET", path, actor, "", nil); got.Code != 200 {
					t.Fatal(got.Body.String())
				}
			}
			assertSettlementIntents(t, a)
			if err := a.ConsumeWorkflow(context.Background(), settlementConsumer); err != nil {
				t.Fatal(err)
			}
			for _, before := range body.Orders {
				after := readStored(t, a, before.ID)
				want, refunds := before.Status, 0
				if before.Status == "open" || before.Status == "accepted" || before.Status == "picked up" {
					want, refunds = "cancelled", 1
				}
				var count int
				if err := a.db.QueryRow(context.Background(), "SELECT count(*) FROM order_refunds WHERE order_id=$1", before.ID).Scan(&count); err != nil || count != refunds || after.Status != want {
					t.Fatalf("status=%s want=%s refunds=%d want=%d error=%v", after.Status, want, count, refunds, err)
				}
			}
		})
	}
}

type blockedRefund struct {
	escrowClient
	paid chan struct{}
}

func (c blockedRefund) Release(ctx context.Context, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

func (c blockedRefund) Payout(ctx context.Context, id, courier string) error {
	if err := c.escrowClient.Payout(ctx, id, courier); err != nil {
		return err
	}
	close(c.paid)
	return nil
}

func TestSlowRefundCannotStarvePayoutRecovery(t *testing.T) {
	a, f := testApp(t)
	a.auth = actorAuth{}
	completed := deliveredForTest(t, a, "payout-fairness")
	mustAction(t, a, completed.ID, "requester", "confirm", "delivered")
	cancelled := createdForTest(t, a, "refund-fairness")
	mustAction(t, a, cancelled.ID, "requester", "cancel", "cancelled")
	a.now = func() time.Time { return time.Now().Add(2 * time.Second) }
	paid := make(chan struct{})
	a.credit = blockedRefund{a.credit, paid}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- recoverForTest(a, ctx) }()
	select {
	case <-paid:
		cancel()
	case <-ctx.Done():
		t.Error("refund consumed payout retry budget")
	}
	if err := <-done; err == nil {
		t.Fatal("blocked refund was not reported for retry")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.payouts != 1 || f.releases != 0 {
		t.Fatal("wrong financial effects", f.payouts, f.releases)
	}
}

func TestLifecycleTimestampsUseSingaporeOffset(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	o := createdForTest(t, a, "sgt-lifecycle")
	for _, action := range []string{"accept", "pick_up", "deliver"} {
		if got := actionForTest(t, a, o.ID, "courier", action); got.Code != 200 {
			t.Fatal(got.Body.String())
		}
	}
	got := actionForTest(t, a, o.ID, "requester", "confirm")
	if got.Code != 202 {
		t.Fatal(got.Body.String())
	}
	var body struct{ Order map[string]any }
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"createdAt", "expiresAt", "deliveredAt", "completionRequestedAt"} {
		value, ok := body.Order[key].(string)
		if !ok || !strings.HasSuffix(value, "+08:00") {
			t.Fatalf("%s is not in Singapore time: %v", key, body.Order[key])
		}
	}
}

func TestAdminListsOrdersByUser(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	asRequester := createdForTest(t, a, "user-scope-requester")
	asCourier := createdForTest(t, a, "user-scope-courier")
	if _, err := a.db.Exec(context.Background(), "UPDATE orders SET requester_id=$2 WHERE id=$1", asCourier.ID, otherUser); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(context.Background(), "UPDATE orders SET status='accepted',courier_id=$2 WHERE id=$1", asCourier.ID, testUserID); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/orders?scope=user&userId=" + strings.ToUpper(testUserID)
	if got := request(t, a, "GET", path, "requester", "", nil); got.Code != 403 {
		t.Fatalf("non-admin: %d %s", got.Code, got.Body.String())
	}
	if got := request(t, a, "GET", "/api/v1/orders?scope=user&userId=nope", "admin", "", nil); got.Code != 400 {
		t.Fatalf("bad id: %d %s", got.Code, got.Body.String())
	}
	got := request(t, a, "GET", path, "admin", "", nil)
	var body struct{ Orders []Order }
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &body) != nil {
		t.Fatalf("admin: %d %s", got.Code, got.Body.String())
	}
	found := map[string]bool{}
	for _, o := range body.Orders {
		found[o.ID] = true
	}
	if !found[asRequester.ID] || !found[asCourier.ID] {
		t.Fatalf("missing requester or courier order: %v", found)
	}
}
