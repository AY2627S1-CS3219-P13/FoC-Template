package order

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

func mustAction(t *testing.T, a *App, id, token, action, status string) {
	t.Helper()
	got := actionForTest(t, a, id, token, action)
	if got.Code != 200 && got.Code != 202 {
		t.Fatalf("%s: %d %s", action, got.Code, got.Body.String())
	}
	if stored := readStored(t, a, id); stored.Status != status {
		t.Fatalf("%s: want %s got %s", action, status, stored.Status)
	}
}
func deliveredForTest(t *testing.T, a *App, key string) Order {
	t.Helper()
	o := createdForTest(t, a, key)
	mustAction(t, a, o.ID, "courier", "accept", "accepted")
	mustAction(t, a, o.ID, "courier", "pick_up", "picked up")
	mustAction(t, a, o.ID, "courier", "deliver", "delivered")
	return readStored(t, a, o.ID)
}
func TestLifecycleEveryStateAndSettlement(t *testing.T) {
	for _, outcome := range []string{"confirm", "auto", "resolve_completed", "resolve_not_completed", "cancel", "expire"} {
		t.Run(outcome, func(t *testing.T) {
			a, f := testApp(t)
			a.auth = actorAuth{}
			ctx := context.Background()
			var o Order
			switch outcome {
			case "cancel", "expire":
				o = createdForTest(t, a, outcome)
			default:
				o = deliveredForTest(t, a, outcome)
			}
			switch outcome {
			case "confirm":
				mustAction(t, a, o.ID, "requester", "confirm", "delivered")
				a.now = func() time.Time { return time.Now().Add(2 * time.Second) }
			case "auto":
				a.now = func() time.Time { return o.DeliveredAt.Add(5*time.Minute + 2*time.Second) }
			case "cancel":
				mustAction(t, a, o.ID, "requester", "cancel", "cancelled")
			case "expire":
				mustAction(t, a, o.ID, "courier", "accept", "accepted")
				mustAction(t, a, o.ID, "courier", "pick_up", "picked up")
				a.now = func() time.Time { return o.ExpiresAt }
			default:
				mustAction(t, a, o.ID, "requester", "dispute", "dispute")
				want := "completed"
				if outcome == "resolve_not_completed" {
					want = "not completed"
				}
				mustAction(t, a, o.ID, "admin", outcome, want)
			}
			for range 2 {
				if err := a.Recover(ctx); err != nil {
					t.Fatal(err)
				}
			}
			want := "completed"
			switch outcome {
			case "cancel":
				want = "cancelled"
			case "expire":
				want = "expired"
			case "resolve_not_completed":
				want = "not completed"
			}
			stored := readStored(t, a, o.ID)
			if stored.Status != want {
				t.Fatal(stored)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if want == "completed" {
				if f.payouts != 1 || f.releases != 0 || f.paid[o.ID] != courierUser {
					t.Fatal("wrong payout", f.payouts, f.releases)
				}
			} else if f.releases != 1 || f.payouts != 0 {
				t.Fatal("wrong refund", f.releases, f.payouts)
			}
		})
	}
}

func TestLifecycleTerminalAndActorMatrix(t *testing.T) {
	for _, state := range []string{"open", "accepted", "picked up", "delivered", "dispute", "completed", "cancelled", "expired", "not completed"} {
		for _, action := range []string{"accept", "pick_up", "deliver", "cancel", "confirm", "dispute", "resolve_completed", "resolve_not_completed"} {
			t.Run(state+"/"+action, func(t *testing.T) {
				a, _ := testApp(t)
				a.auth = actorAuth{}
				o := createdForTest(t, a, "matrix")
				if _, err := a.db.Exec(context.Background(), "UPDATE orders SET status=$2,courier_id=$3 WHERE id=$1", o.ID, state, courierUser); err != nil {
					t.Fatal(err)
				}
				// An unrelated normal user has no authority to mutate any assigned state.
				before := readStored(t, a, o.ID)
				got := actionForTest(t, a, o.ID, "other", action)
				if got.Code < 400 {
					t.Fatal("unauthorized mutation", state, action, got.Code)
				}
				after := readStored(t, a, o.ID)
				if before.Status != after.Status || before.Version != after.Version {
					t.Fatal("rejection mutated order")
				}
			})
		}
	}
}

func TestLifecycleGuardAllEdges(t *testing.T) {
	states := []string{"open", "accepted", "picked up", "delivered", "dispute", "completed", "cancelled", "expired", "not completed"}
	allowed := map[string][]string{"open": {"accepted", "cancelled", "expired"}, "accepted": {"picked up", "cancelled", "expired"}, "picked up": {"delivered", "cancelled", "expired"}, "delivered": {"completed", "dispute"}, "dispute": {"completed", "not completed"}}
	for _, from := range states {
		for _, to := range states {
			want := false
			for _, s := range allowed[from] {
				if s == to {
					want = true
				}
			}
			if forwardTransition(from, to) != want {
				t.Fatal(from, to)
			}
		}
	}
}

func TestDisputeBeatsPendingConfirmationAndTimer(t *testing.T) {
	for _, manual := range []bool{true, false} {
		t.Run(fmt.Sprint(manual), func(t *testing.T) {
			a, f := testApp(t)
			a.auth = actorAuth{}
			o := deliveredForTest(t, a, "priority")
			if manual {
				mustAction(t, a, o.ID, "requester", "confirm", "delivered")
			} else {
				a.now = func() time.Time { return o.DeliveredAt.Add(5 * time.Minute) }
			}
			mustAction(t, a, o.ID, "requester", "dispute", "dispute")
			a.now = func() time.Time { return o.DeliveredAt.Add(time.Hour) }
			if err := a.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			if readStored(t, a, o.ID).Status != "dispute" {
				t.Fatal("dispute timer resumed")
			}
			if got := actionForTest(t, a, o.ID, "requester", "confirm"); got.Code != 409 {
				t.Fatal("confirmation bypassed dispute", got.Code)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.payouts != 0 || f.releases != 0 {
				t.Fatal("dispute moved credits")
			}
		})
	}
}

func TestCompetingAdminResolutionsOneSettlement(t *testing.T) {
	a, f := testApp(t)
	a.auth = actorAuth{}
	o := deliveredForTest(t, a, "admin-race")
	mustAction(t, a, o.ID, "requester", "dispute", "dispute")
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, action := range []string{"resolve_completed", "resolve_not_completed"} {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- actionForTest(t, a, o.ID, "admin", action).Code }()
	}
	wg.Wait()
	close(codes)
	ok, conflict := 0, 0
	for c := range codes {
		if c == 200 {
			ok++
		}
		if c == 409 {
			conflict++
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
	if err := a.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.payouts+f.releases != 1 {
		t.Fatal("conflicting financial effects")
	}
}

func TestPickupReceiptSurvivesCancellationAndRestart(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	o := createdForTest(t, a, "replay-pickup")
	mustAction(t, a, o.ID, "courier", "accept", "accepted")
	a.catalogue = resolverFunc(func(context.Context, PlaceRef, string) (Place, error) { return Place{}, invalidPlace() })
	// The pickup command wins against deactivation discovered by its own lookup.
	mustAction(t, a, o.ID, "courier", "pick_up", "picked up")
	got := request(t, a, "POST", "/api/v1/orders/"+o.ID+"/refresh", "requester", "", map[string]any{})
	if got.Code != 200 || readStored(t, a, o.ID).Status != "cancelled" {
		t.Fatal("later deactivation", got.Code)
	}
	restarted := New(a.db, a.cfg, a.log)
	restarted.auth = actorAuth{}
	before := readStored(t, a, o.ID)
	mustAction(t, restarted, o.ID, "courier", "pick_up", "cancelled")
	if readStored(t, a, o.ID).Version != before.Version {
		t.Fatal("replay mutated history")
	}
	if got := actionForTest(t, a, o.ID, "courier", "deliver"); got.Code != 409 {
		t.Fatal("cancelled order reopened", got.Code)
	}
}

func TestDeliveryReceiptProtectsAgainstExpiry(t *testing.T) {
	for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(offset.String(), func(t *testing.T) {
			a, _ := testApp(t)
			a.auth = actorAuth{}
			o := createdForTest(t, a, "delivery-deadline")
			mustAction(t, a, o.ID, "courier", "accept", "accepted")
			mustAction(t, a, o.ID, "courier", "pick_up", "picked up")
			a.now = func() time.Time { return o.ExpiresAt.Add(offset) }
			original := a.catalogue
			a.catalogue = resolverFunc(func(ctx context.Context, ref PlaceRef, session string) (Place, error) {
				a.now = func() time.Time { return o.ExpiresAt.Add(time.Second) }
				return original.Resolve(ctx, ref, session)
			})
			got := actionForTest(t, a, o.ID, "courier", "deliver")
			if offset < 0 {
				if got.Code != 200 || readStored(t, a, o.ID).Status != "delivered" {
					t.Fatal(got.Code)
				}
			} else if got.Code != 409 || readStored(t, a, o.ID).Status != "expired" {
				t.Fatal("late delivery", got.Code)
			}
		})
	}
}

type adminCourierAuth struct{}

func (adminCourierAuth) Validate(*http.Request) (Identity, error) {
	return Identity{UserID: courierUser, Roles: []string{"user", "admin"}}, nil
}
func TestAdministratorCanPerformAssignedCourierTransitions(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	o := createdForTest(t, a, "admin-pickup")
	mustAction(t, a, o.ID, "courier", "accept", "accepted")
	a.auth = adminCourierAuth{}
	mustAction(t, a, o.ID, "courier", "pick_up", "picked up")
	mustAction(t, a, o.ID, "courier", "deliver", "delivered")
	a.auth = actorAuth{}
	mustAction(t, a, o.ID, "requester", "dispute", "dispute")
	if got := actionForTest(t, a, o.ID, "requester", "resolve_completed"); got.Code != 403 {
		t.Fatal("requester resolved dispute", got.Code)
	}
	mustAction(t, a, o.ID, "admin", "resolve_completed", "completed")
}
