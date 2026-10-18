package order

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func openForTest(t *testing.T, a *App, query, token string) []Order {
	t.Helper()
	got := request(t, a, "GET", "/api/v1/orders?scope=open"+query, token, "", nil)
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	var body struct {
		Orders  []Order
		HasMore bool
	}
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.HasMore {
		t.Fatal("open list was paginated")
	}
	return body.Orders
}

func TestOpenSnapshotAllRowsSortingAndSupplierChanges(t *testing.T) {
	a, f := testApp(t)
	f.mu.Lock()
	f.available = 1000
	f.mu.Unlock()
	a.auth = actorAuth{}
	for i := 0; i < 25; i++ {
		createdForTest(t, a, fmt.Sprintf("list-%02d", i))
	}
	ctx := context.Background()
	// Equal timestamps exercise the stable ID tie-breaker.
	if _, err := a.db.Exec(ctx, "UPDATE orders SET expires_at=$1,created_at=$2", time.Now().Add(time.Hour), time.Now()); err != nil {
		t.Fatal(err)
	}
	rows := openForTest(t, a, "", "courier")
	if len(rows) != 25 {
		t.Fatal("truncated list", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].ID >= rows[i].ID {
			t.Fatal("unstable ties")
		}
	}
	if len(openForTest(t, a, "", "requester")) != 0 {
		t.Fatal("self listing")
	}
	last := rows[24].ID
	if _, err := a.db.Exec(ctx, "UPDATE orders SET offered_credits=100 WHERE id=$1", last); err != nil {
		t.Fatal(err)
	}
	if openForTest(t, a, "&sort=credits", "courier")[0].ID != last {
		t.Fatal("credits ordering")
	}
	underlying := a.catalogue
	var calls atomic.Int64
	a.catalogue = resolverFunc(func(ctx context.Context, ref PlaceRef, session string) (Place, error) {
		calls.Add(1)
		p, err := underlying.Resolve(ctx, ref, session)
		if ref.Type == "supplier" {
			p.Name = "Renamed"
			p.LocationName = "Relocated"
		}
		return p, err
	})
	rows = openForTest(t, a, "", "courier")
	if calls.Load() != 2 {
		t.Fatal("expected one lookup per distinct place", calls.Load())
	}
	for _, o := range rows {
		if o.Pickup.Name != "Renamed" || o.Pickup.LocationName != "Relocated" || o.Status != "open" {
			t.Fatal("stale details", o)
		}
	}
	a.catalogue = resolverFunc(func(context.Context, PlaceRef, string) (Place, error) { return Place{}, placesUnavailable() })
	if got := request(t, a, "GET", "/api/v1/orders?scope=open", "courier", "", nil); got.Code != 503 {
		t.Fatal("outage reported as synchronized", got.Code)
	}
	a.catalogue = resolverFunc(func(context.Context, PlaceRef, string) (Place, error) { return Place{}, invalidPlace() })
	if len(openForTest(t, a, "", "courier")) != 0 {
		t.Fatal("deactivated entries remain")
	}
	assertSettlementIntents(t, a)
	if err := a.ConsumeWorkflow(ctx, settlementConsumer); err != nil {
		t.Fatal(err)
	}
	var refunds int
	if err := a.db.QueryRow(ctx, "SELECT count(*) FROM order_refunds").Scan(&refunds); err != nil || refunds != 25 {
		t.Fatal(refunds, err)
	}
}

type contenderAuth struct{}

func (contenderAuth) Validate(r *http.Request) (Identity, error) {
	c, err := r.Cookie("foc_session")
	if err != nil {
		return Identity{}, problem(401, "invalid_session", "Log in.")
	}
	return Identity{UserID: c.Value, Roles: []string{"user"}, ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func TestManyCouriersOneWinner(t *testing.T) {
	a, _ := testApp(t)
	o := createdForTest(t, a, "contenders")
	a.auth = contenderAuth{}
	var wg sync.WaitGroup
	results := make(chan string, 8)
	for i := 0; i < 8; i++ {
		user := fmt.Sprintf("aaaaaaaa-aaaa-4aaa-8aaa-%012d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := actionForTest(t, a, o.ID, user, "accept")
			if got.Code == 200 {
				results <- user
			} else if got.Code != 404 && got.Code != 409 {
				t.Errorf("unexpected loser: %d", got.Code)
			}
		}()
	}
	wg.Wait()
	close(results)
	winners := []string{}
	for user := range results {
		winners = append(winners, user)
	}
	if len(winners) != 1 {
		t.Fatal(winners)
	}
	stored := readStored(t, a, o.ID)
	if stored.CourierID == nil || *stored.CourierID != winners[0] {
		t.Fatal("wrong assignment")
	}
	if got := actionForTest(t, a, o.ID, winners[0], "accept"); got.Code != 200 {
		t.Fatal("retry", got.Code)
	}
}

func TestAcceptanceReceiptDeadlineAndRecovery(t *testing.T) {
	for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(offset.String(), func(t *testing.T) {
			a, f := testApp(t)
			a.auth = actorAuth{}
			o := createdForTest(t, a, "deadline")
			deadline := o.ExpiresAt
			a.now = func() time.Time { return deadline.Add(offset) }
			underlying := a.catalogue
			a.catalogue = resolverFunc(func(ctx context.Context, ref PlaceRef, token string) (Place, error) {
				// Receipt precedes expiry but the successful write occurs afterward.
				a.now = func() time.Time { return deadline.Add(time.Second) }
				return underlying.Resolve(ctx, ref, token)
			})
			got := actionForTest(t, a, o.ID, "courier", "accept")
			if offset < 0 {
				if got.Code != 200 {
					t.Fatal(got.Code, got.Body.String())
				}
				stored := readStored(t, a, o.ID)
				if stored.Status != "expired" || stored.CourierID == nil || stored.Version != 3 {
					t.Fatal(stored)
				}
				if retry := actionForTest(t, a, o.ID, "courier", "accept"); retry.Code != 200 {
					t.Fatal("lost-response retry", retry.Code)
				}
			} else if got.Code != 409 || readStored(t, a, o.ID).CourierID != nil {
				t.Fatal("late acceptance assigned", got.Code)
			}
			if err := recoverForTest(a, context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := recoverForTest(a, context.Background()); err != nil {
				t.Fatal(err)
			}
			if readStored(t, a, o.ID).Status != "expired" {
				t.Fatal("deadline not recovered")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.releases != 1 {
				t.Fatal("refund effects", f.releases)
			}
		})
	}
}

func TestAcceptedDeadlineFreesCapacity(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	first := createdForTest(t, a, "deadline-first")
	second := createdForTest(t, a, "deadline-next")
	if got := actionForTest(t, a, first.ID, "courier", "accept"); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	if _, err := a.db.Exec(context.Background(), "UPDATE orders SET expires_at=$2,created_at=$2::timestamptz-interval '1 hour' WHERE id=$1", first.ID, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := actionForTest(t, a, second.ID, "courier", "accept"); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	if got := actionForTest(t, a, first.ID, "courier", "accept"); got.Code != 200 {
		t.Fatal("old retry", got.Body.String())
	}
	if readStored(t, a, second.ID).Status != "accepted" {
		t.Fatal("old expiry released newer work")
	}
}

func TestDeliveryCommitControlsNextAcceptance(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	first := createdForTest(t, a, "delivery-race-first")
	next := createdForTest(t, a, "delivery-race-next")
	for _, action := range []string{"accept", "pick_up"} {
		if got := actionForTest(t, a, first.ID, "courier", action); got.Code != 200 {
			t.Fatal(got.Body.String())
		}
	}
	underlying := a.catalogue
	entered, finish := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	firstCall := true
	a.catalogue = resolverFunc(func(ctx context.Context, ref PlaceRef, token string) (Place, error) {
		mu.Lock()
		hold := firstCall
		firstCall = false
		mu.Unlock()
		if hold {
			close(entered)
			<-finish
		}
		return underlying.Resolve(ctx, ref, token)
	})
	delivered := make(chan int, 1)
	go func() { delivered <- actionForTest(t, a, first.ID, "courier", "deliver").Code }()
	<-entered
	got := actionForTest(t, a, next.ID, "courier", "accept")
	close(finish)
	if code := <-delivered; code != 200 {
		t.Fatal("delivery failed", code)
	}
	if got.Code != 409 {
		t.Fatal("uncommitted delivery freed capacity", got.Code)
	}
	if got = actionForTest(t, a, next.ID, "courier", "accept"); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	if got = actionForTest(t, a, first.ID, "courier", "deliver"); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	if readStored(t, a, next.ID).Status != "accepted" {
		t.Fatal("delivery replay changed new assignment")
	}
}
