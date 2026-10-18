package order

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type delayedAdmissionAuth struct {
	token   string
	entered chan struct{}
	release chan struct{}
	once    atomic.Bool
	invalid bool
}

func (a *delayedAdmissionAuth) Validate(r *http.Request) (Identity, error) {
	cookie, _ := r.Cookie("foc_session")
	if cookie != nil && cookie.Value == a.token && a.once.CompareAndSwap(false, true) {
		close(a.entered)
		select {
		case <-a.release:
		case <-r.Context().Done():
			return Identity{}, authUnavailable()
		}
		if a.invalid {
			return Identity{}, problem(401, "invalid_session", "Log in again.")
		}
	}
	return actorAuth{}.Validate(r)
}

func delayAdmissionAuth(t *testing.T, a *App, token string, invalid bool) (<-chan struct{}, func()) {
	t.Helper()
	gate := &delayedAdmissionAuth{token: token, entered: make(chan struct{}), release: make(chan struct{}), invalid: invalid}
	a.auth = gate
	var once sync.Once
	finish := func() { once.Do(func() { close(gate.release) }) }
	t.Cleanup(finish)
	return gate.entered, finish
}

func admissionReplica(a *App) *App {
	b := NewWithAdmissionPool(a.admissions, a.admissions, a.cfg, a.log)
	b.auth = actorAuth{}
	b.now = a.now
	return b
}

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach authentication")
	}
}

func awaitCode(t *testing.T, result <-chan int, want int) {
	t.Helper()
	select {
	case code := <-result:
		if code != want {
			t.Fatalf("response=%d want=%d", code, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("admitted request did not finish")
	}
}

func waitAdmissions(t *testing.T, a *App, id string, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		var count int
		if err := a.admissions.QueryRow(ctx, "SELECT count(*) FROM order_admissions WHERE order_id=$1", id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("admissions=%d want=%d", count, want)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestAdmissionProtectsPreDeadlineCommandsDuringAuthentication(t *testing.T) {
	for _, action := range []string{"accept", "deliver"} {
		t.Run(action, func(t *testing.T) {
			a, f := testApp(t)
			a.auth = actorAuth{}
			o := createdForTest(t, a, "admission-deadline", time.Now().Add(time.Minute))
			if action == "deliver" {
				mustAction(t, a, o.ID, "courier", "accept", "accepted")
				mustAction(t, a, o.ID, "courier", "pick_up", "picked up")
			}
			// The deadline is part of the committed event, not a later test-only
			// SQL edit. Materialize it before admission so this exercises due work.
			consume(t, a, timerConsumer)
			if !timerAt(t, a, o.ID, "expiry").Equal(o.ExpiresAt) {
				t.Fatal("wrong original expiry schedule")
			}

			var clock atomic.Int64
			clock.Store(o.ExpiresAt.Add(-time.Second).UnixNano())
			a.now = func() time.Time { return time.Unix(0, clock.Load()) }
			b := admissionReplica(a)
			entered, finish := delayAdmissionAuth(t, a, "courier", false)
			result := make(chan int, 1)
			go func() { result <- actionForTest(t, a, o.ID, "courier", action).Code }()
			awaitSignal(t, entered)
			clock.Store(o.ExpiresAt.Add(time.Second).UnixNano())
			if err := advanceForTest(b, context.Background()); err != nil {
				t.Fatal(err)
			}
			if readStored(t, b, o.ID).Status == "expired" {
				t.Fatal("worker overtook admitted request during authentication")
			}
			finish()
			awaitCode(t, result, 200)
			want := "delivered"
			if action == "accept" {
				want = "expired"
			}
			after := readStored(t, b, o.ID)
			if after.Status != want || after.CourierID == nil {
				t.Fatal("wrong receipt deadline outcome", after)
			}
			if action == "accept" {
				var types []string
				rows, err := a.db.Query(context.Background(), "SELECT domain_type FROM order_events WHERE order_id=$1 ORDER BY order_version", o.ID)
				if err != nil {
					t.Fatal(err)
				}
				for rows.Next() {
					var kind string
					if err = rows.Scan(&kind); err != nil {
						t.Fatal(err)
					}
					types = append(types, kind)
				}
				rows.Close()
				if len(types) != 3 || types[0] != "OrderCreated" || types[1] != "OrderAccepted" || types[2] != "OrderExpired" {
					t.Fatal("acceptance-then-expiry lost ordered facts", types)
				}
			}
			if err := recoverForTest(b, context.Background()); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			refunds := 0
			if action == "accept" {
				refunds = 1
			}
			if f.releases != refunds || f.payouts != 0 {
				t.Fatal("wrong financial effects", f.releases, f.payouts)
			}
		})
	}
}

func TestAdmissionOrdersCancellationAndPickupAcrossReplicas(t *testing.T) {
	for _, first := range []string{"cancel", "pick_up"} {
		t.Run(first, func(t *testing.T) {
			a, _ := testApp(t)
			a.auth = actorAuth{}
			o := createdForTest(t, a, "admission-order")
			mustAction(t, a, o.ID, "courier", "accept", "accepted")
			b := admissionReplica(a)
			token, other, second := "requester", "courier", "pick_up"
			want := "cancelled"
			if first == "pick_up" {
				token, other, second = "courier", "requester", "cancel"
				want = "picked up"
			}
			entered, finish := delayAdmissionAuth(t, a, token, false)
			one, two := make(chan int, 1), make(chan int, 1)
			go func() { one <- actionForTest(t, a, o.ID, token, first).Code }()
			awaitSignal(t, entered)
			go func() { two <- actionForTest(t, b, o.ID, other, second).Code }()
			waitAdmissions(t, a, o.ID, 2)
			select {
			case code := <-two:
				t.Fatalf("later action overtook earlier authentication: %d", code)
			default:
			}
			finish()
			awaitCode(t, one, 200)
			awaitCode(t, two, 409)
			if readStored(t, a, o.ID).Status != want {
				t.Fatal("receipt ordering was lost")
			}
			waitAdmissions(t, a, o.ID, 0)
		})
	}
}

func TestAdmissionSurvivesTransactionPoolContention(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	o := createdForTest(t, a, "admission-pool")
	mustAction(t, a, o.ID, "courier", "accept", "accepted")
	mustAction(t, a, o.ID, "courier", "pick_up", "picked up")
	var clock atomic.Int64
	clock.Store(o.ExpiresAt.Add(-time.Second).UnixNano())
	a.now = func() time.Time { return time.Unix(0, clock.Load()) }
	b := admissionReplica(a)
	config := a.db.Config()
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	a.db = pool // a.admissions remains the independent admission pool.
	held, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(held.Release) }
	defer release()
	entered, finish := delayAdmissionAuth(t, a, "courier", false)
	result := make(chan int, 1)
	go func() { result <- actionForTest(t, a, o.ID, "courier", "deliver").Code }()
	awaitSignal(t, entered)
	finish()
	clock.Store(o.ExpiresAt.Add(time.Second).UnixNano())
	if err = advanceForTest(b, context.Background()); err != nil {
		t.Fatal(err)
	}
	if readStored(t, b, o.ID).Status != "picked up" {
		t.Fatal("pool wait was invisible to expiry")
	}
	release()
	awaitCode(t, result, 200)
}

func TestAbandonedAdmissionsExpireAndFenceOldHandlers(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	o := createdForTest(t, a, "admission-abandoned")
	mustAction(t, a, o.ID, "courier", "accept", "accepted")
	mustAction(t, a, o.ID, "courier", "pick_up", "picked up")
	var clock atomic.Int64
	clock.Store(o.ExpiresAt.Add(-time.Second).UnixNano())
	a.now = func() time.Time { return time.Unix(0, clock.Load()) }
	b := admissionReplica(a)
	entered, finish := delayAdmissionAuth(t, a, "courier", false)
	result := make(chan int, 1)
	go func() { result <- actionForTest(t, a, o.ID, "courier", "deliver").Code }()
	awaitSignal(t, entered)
	// Simulate the lease elapsing after a stalled/crashed process. No handler is
	// allowed to use that old admission, even if it later resumes.
	if _, err := a.admissions.Exec(context.Background(), "UPDATE order_admissions SET valid_until=clock_timestamp()-interval '1 second' WHERE order_id=$1", o.ID); err != nil {
		t.Fatal(err)
	}
	clock.Store(o.ExpiresAt.Add(time.Second).UnixNano())
	if err := advanceForTest(b, context.Background()); err != nil {
		t.Fatal(err)
	}
	finish()
	awaitCode(t, result, 409)
	if readStored(t, b, o.ID).Status != "expired" {
		t.Fatal("old handler revived final expiry")
	}
	waitAdmissions(t, b, o.ID, 0)
}

func TestInvalidAdmissionCannotProtectOrderAfterRejection(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	o := createdForTest(t, a, "admission-invalid")
	mustAction(t, a, o.ID, "courier", "accept", "accepted")
	mustAction(t, a, o.ID, "courier", "pick_up", "picked up")
	var clock atomic.Int64
	clock.Store(o.ExpiresAt.Add(-time.Second).UnixNano())
	a.now = func() time.Time { return time.Unix(0, clock.Load()) }
	b := admissionReplica(a)
	entered, finish := delayAdmissionAuth(t, a, "courier", true)
	result := make(chan int, 1)
	go func() { result <- actionForTest(t, a, o.ID, "courier", "deliver").Code }()
	awaitSignal(t, entered)
	clock.Store(o.ExpiresAt.Add(time.Second).UnixNano())
	finish()
	awaitCode(t, result, 401)
	if err := advanceForTest(b, context.Background()); err != nil {
		t.Fatal(err)
	}
	if readStored(t, b, o.ID).Status != "expired" {
		t.Fatal("rejected session protected delivery")
	}
	waitAdmissions(t, b, o.ID, 0)
}

func TestAdmittedDisputeBlocksCompletionWhileAuthenticating(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	o := deliveredForTest(t, a, "admission-dispute")
	mustAction(t, a, o.ID, "requester", "confirm", "delivered")
	a.now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	b := admissionReplica(a)
	entered, finish := delayAdmissionAuth(t, a, "requester", false)
	result := make(chan int, 1)
	go func() { result <- actionForTest(t, a, o.ID, "requester", "dispute").Code }()
	awaitSignal(t, entered)
	if err := advanceForTest(b, context.Background()); err != nil {
		t.Fatal(err)
	}
	if readStored(t, b, o.ID).Status != "delivered" {
		t.Fatal("completion overtook admitted dispute")
	}
	finish()
	awaitCode(t, result, 200)
	if err := recoverForTest(b, context.Background()); err != nil {
		t.Fatal(err)
	}
	if readStored(t, b, o.ID).Status != "dispute" {
		t.Fatal("dispute was lost")
	}
	var payouts int
	if err := b.db.QueryRow(context.Background(), "SELECT count(*) FROM order_payouts").Scan(&payouts); err != nil || payouts != 0 {
		t.Fatal("losing completion queued payout", payouts, err)
	}
}

func TestAdmissionRemainsVisibleWhileWaitingForOrderRow(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	o := createdForTest(t, a, "admission-row")
	mustAction(t, a, o.ID, "courier", "accept", "accepted")
	mustAction(t, a, o.ID, "courier", "pick_up", "picked up")
	var clock atomic.Int64
	clock.Store(o.ExpiresAt.Add(-time.Second).UnixNano())
	a.now = func() time.Time { return time.Unix(0, clock.Load()) }
	b := admissionReplica(a)
	tx, err := a.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	if _, err = tx.Exec(context.Background(), "SELECT id FROM orders WHERE id=$1 FOR UPDATE", o.ID); err != nil {
		t.Fatal(err)
	}
	entered, finish := delayAdmissionAuth(t, a, "courier", false)
	result := make(chan int, 1)
	go func() { result <- actionForTest(t, a, o.ID, "courier", "deliver").Code }()
	awaitSignal(t, entered)
	finish()
	clock.Store(o.ExpiresAt.Add(time.Second).UnixNano())
	if err = advanceForTest(b, context.Background()); err != nil {
		t.Fatal(err)
	}
	// Release the external blocker, then let another worker compete for the row.
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = advanceForTest(b, context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitCode(t, result, 200)
	if readStored(t, b, o.ID).Status != "delivered" {
		t.Fatal("expiry overtook an admitted row-lock waiter")
	}
}

func TestAdmissionPreventsSupplierRefreshOvertakingPickup(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	o := createdForTest(t, a, "admission-supplier")
	mustAction(t, a, o.ID, "courier", "accept", "accepted")
	b := admissionReplica(a)
	b.catalogue = resolverFunc(func(context.Context, PlaceRef, string) (Place, error) { return Place{}, invalidPlace() })
	entered, finish := delayAdmissionAuth(t, a, "courier", false)
	result := make(chan int, 1)
	go func() { result <- actionForTest(t, a, o.ID, "courier", "pick_up").Code }()
	awaitSignal(t, entered)
	if got := request(t, b, "GET", "/api/v1/orders?scope=mine", "requester", "", nil); got.Code != 503 {
		t.Fatal("refresh overtook earlier pickup", got.Code, got.Body.String())
	}
	refresh := make(chan int, 1)
	go func() {
		refresh <- request(t, b, "POST", "/api/v1/orders/"+o.ID+"/refresh", "requester", "", map[string]any{}).Code
	}()
	waitAdmissions(t, a, o.ID, 2)
	finish()
	awaitCode(t, result, 200)
	awaitCode(t, refresh, 200)
	if readStored(t, b, o.ID).Status != "picked up" {
		t.Fatal("competing refresh undid pickup")
	}
	// A new independent Supplier cancellation still applies after pickup.
	if got := request(t, b, "POST", "/api/v1/orders/"+o.ID+"/refresh", "requester", "", map[string]any{}); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	if readStored(t, b, o.ID).Status != "cancelled" {
		t.Fatal("later independent cancellation was lost")
	}
}

func TestCancelledRequestReleasesAdmissionWithoutMutation(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	o := createdForTest(t, a, "admission-disconnect")
	mustAction(t, a, o.ID, "courier", "accept", "accepted")
	entered, _ := delayAdmissionAuth(t, a, "requester", false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("POST", "/api/v1/orders/"+o.ID+"/actions", bytes.NewBufferString(`{"action":"cancel"}`)).WithContext(ctx)
	req.Header.Set("Origin", a.cfg.Origin)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: a.cfg.SessionCookie, Value: "requester"})
	result := make(chan int, 1)
	go func() { rec := httptest.NewRecorder(); a.Handler().ServeHTTP(rec, req); result <- rec.Code }()
	awaitSignal(t, entered)
	cancel()
	awaitCode(t, result, 503)
	waitAdmissions(t, a, o.ID, 0)
	b := admissionReplica(a)
	mustAction(t, b, o.ID, "courier", "pick_up", "picked up")
}
