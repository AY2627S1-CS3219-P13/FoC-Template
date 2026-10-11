package order

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const courierUser = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
const otherUser = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

type actorAuth struct{}

func (actorAuth) Validate(r *http.Request) (Identity, error) {
	cookie, err := r.Cookie("foc_session")
	if err != nil {
		return Identity{}, problem(401, "invalid_session", "Log in.")
	}
	user := testUserID
	if cookie.Value == "courier" {
		user = courierUser
	}
	if cookie.Value == "other" {
		user = otherUser
	}
	roles := []string{"user"}
	if cookie.Value == "admin" {
		user = otherUser
		roles = append(roles, "admin")
	}
	return Identity{UserID: user, Roles: roles, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

type resolverFunc func(context.Context, PlaceRef, string) (Place, error)

func (f resolverFunc) Resolve(ctx context.Context, ref PlaceRef, token string) (Place, error) {
	return f(ctx, ref, token)
}

func createdForTest(t *testing.T, a *App, key string) Order {
	t.Helper()
	response := request(t, a, "POST", "/api/v1/orders", "requester", key, validCreateBody(10))
	if response.Code != 201 {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Order Order `json:"order"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Order
}
func actionForTest(t *testing.T, a *App, id, token, action string) *httptest.ResponseRecorder {
	t.Helper()
	return request(t, a, "POST", "/api/v1/orders/"+id+"/actions", token, "", map[string]string{"action": action})
}
func readStored(t *testing.T, a *App, id string) Order {
	t.Helper()
	o, err := scanOrder(a.db.QueryRow(context.Background(), "SELECT "+orderColumns+" FROM orders WHERE id=$1", id))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestReadsPlacesAndEventAuthorization(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	o := createdForTest(t, a, "read")
	for _, path := range []string{"/api/v1/orders/session", "/api/v1/orders/places", "/api/v1/orders", "/api/v1/orders/" + o.ID, "/api/v1/orders/" + o.ID + "/events"} {
		if got := request(t, a, "GET", path, "", "", nil); got.Code != 401 {
			t.Fatalf("unauth %s: %d", path, got.Code)
		}
		if got := request(t, a, "GET", path, "courier", "", nil); got.Code != 200 {
			t.Fatalf("read %s: %d %s", path, got.Code, got.Body.String())
		}
	}
	events := request(t, a, "GET", "/api/v1/orders/"+o.ID+"/events?afterVersion=0", "courier", "", nil)
	if !strings.Contains(events.Body.String(), "OrderCreated") || strings.Contains(events.Body.String(), "sessionToken") {
		t.Fatal(events.Body.String())
	}
	if got := actionForTest(t, a, o.ID, "courier", "accept"); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	for _, suffix := range []string{"", "/events"} {
		if got := request(t, a, "GET", "/api/v1/orders/"+o.ID+suffix, "other", "", nil); got.Code != 404 {
			t.Fatal("private accepted order leaked", got.Code)
		}
	}
	if got := request(t, a, "POST", "/api/v1/orders/"+o.ID+"/refresh", "other", "", map[string]any{}); got.Code != 404 {
		t.Fatal("unauthorized refresh", got.Code)
	}
	list := request(t, a, "GET", "/api/v1/orders?scope=courier", "courier", "", nil)
	if !strings.Contains(list.Body.String(), o.ID) {
		t.Fatal("courier listing empty")
	}
	list = request(t, a, "GET", "/api/v1/orders?scope=mine", "other", "", nil)
	if strings.Contains(list.Body.String(), o.ID) {
		t.Fatal("private listing leaked")
	}
	for _, path := range []string{"?scope=all", "?page=-1", "?page=999999999999999999999", "/places?page=0", "/" + o.ID + "/events?afterVersion=-1"} {
		if got := request(t, a, "GET", "/api/v1/orders"+path, "requester", "", nil); got.Code != 400 {
			t.Fatal("invalid query", path, got.Code)
		}
	}
}

func TestSupplierChangeCancellationAndRefund(t *testing.T) {
	for _, status := range []string{"open", "accepted", "picked up", "delivered", "dispute", "completed", "cancelled", "expired", "not completed"} {
		t.Run(strings.ReplaceAll(status, " ", "_"), func(t *testing.T) {
			a, f := testApp(t)
			o := createdForTest(t, a, "supplier-change")
			_, err := a.db.Exec(context.Background(), "UPDATE orders SET status=$2,courier_id=$3 WHERE id=$1", o.ID, status, courierUser)
			if err != nil {
				t.Fatal(err)
			}
			underlying := a.catalogue
			a.catalogue = resolverFunc(func(ctx context.Context, ref PlaceRef, token string) (Place, error) {
				if ref.Type == "supplier" {
					return Place{}, invalidPlace()
				}
				return underlying.Resolve(ctx, ref, token)
			})
			for range 2 {
				got := request(t, a, "POST", "/api/v1/orders/"+o.ID+"/refresh", "requester", "", map[string]any{})
				if got.Code != 200 {
					t.Fatal(got.Body.String())
				}
			}
			updated := readStored(t, a, o.ID)
			want := status
			if supplierCancellable(status) {
				want = "cancelled"
			}
			if updated.Status != want {
				t.Fatalf("%s became %s", status, updated.Status)
			}
			wantVersion := int64(1)
			if supplierCancellable(status) {
				wantVersion = 2
			}
			if updated.Version != wantVersion {
				t.Fatalf("version %d", updated.Version)
			}
			if updated.CourierID == nil || *updated.CourierID != courierUser {
				t.Fatal("lost courier history")
			}
			if err = a.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err = a.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			releases := f.releases
			f.mu.Unlock()
			wantReleases := 0
			if supplierCancellable(status) {
				wantReleases = 1
			}
			if releases != wantReleases {
				t.Fatalf("refund effects: %d", releases)
			}
		})
	}
}

func TestRenameRelocationOutageAndRefundRetry(t *testing.T) {
	a, _ := testApp(t)
	o := createdForTest(t, a, "rename")
	underlying := a.catalogue
	a.catalogue = resolverFunc(func(ctx context.Context, ref PlaceRef, token string) (Place, error) {
		p, err := underlying.Resolve(ctx, ref, token)
		if ref.Type == "supplier" {
			p.Name = "New cafe"
			p.LocationID = "utown"
			p.LocationName = "UTown"
			p.Description = "Level 2"
		}
		return p, err
	})
	got := request(t, a, "POST", "/api/v1/orders/"+o.ID+"/refresh", "requester", "", map[string]any{})
	if got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	updated := readStored(t, a, o.ID)
	if updated.Status != "open" || updated.Pickup.Name != "New cafe" || updated.Pickup.LocationName != "UTown" || updated.Version != 2 {
		t.Fatalf("bad rename: %+v", updated)
	}
	a.catalogue = resolverFunc(func(context.Context, PlaceRef, string) (Place, error) { return Place{}, placesUnavailable() })
	got = request(t, a, "POST", "/api/v1/orders/"+o.ID+"/refresh", "requester", "", map[string]any{})
	if got.Code != 503 || readStored(t, a, o.ID).Version != 2 {
		t.Fatal("outage mutated order", got.Code)
	}
	a.catalogue = resolverFunc(func(context.Context, PlaceRef, string) (Place, error) { return Place{}, invalidPlace() })
	got = request(t, a, "POST", "/api/v1/orders/"+o.ID+"/refresh", "requester", "", map[string]any{})
	if got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	credit := a.credit
	a.credit = unavailableRelease{credit}
	if err := a.Recover(context.Background()); err == nil {
		t.Fatal("expected pending refund")
	}
	var count int
	_ = a.db.QueryRow(context.Background(), "SELECT count(*) FROM order_refunds").Scan(&count)
	if count != 1 {
		t.Fatal("lost durable refund")
	}
	a.credit = credit
	if err := a.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = a.db.QueryRow(context.Background(), "SELECT count(*) FROM order_refunds").Scan(&count)
	if count != 0 {
		t.Fatal("refund not retried")
	}
}

type unavailableRelease struct{ escrowClient }

func (unavailableRelease) Release(context.Context, string) error { return creditUnavailable() }

func TestDeliveredReleasesOnlyItsOwnCourierSlot(t *testing.T) {
	a, f := testApp(t)
	a.auth = actorAuth{}
	first := createdForTest(t, a, "first")
	second := createdForTest(t, a, "second")
	third := createdForTest(t, a, "third")
	if got := actionForTest(t, a, first.ID, "requester", "accept"); got.Code != 403 {
		t.Fatal("self assignment allowed")
	}
	if got := actionForTest(t, a, first.ID, "courier", "accept"); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	if got := actionForTest(t, a, first.ID, "courier", "deliver"); got.Code != 409 {
		t.Fatal("skipped pickup")
	}
	if got := actionForTest(t, a, second.ID, "courier", "accept"); got.Code != 409 {
		t.Fatal("second assignment allowed")
	}
	for _, action := range []string{"pick_up", "deliver"} {
		if got := actionForTest(t, a, first.ID, "courier", action); got.Code != 200 {
			t.Fatal(got.Body.String())
		}
	}
	if got := actionForTest(t, a, second.ID, "courier", "accept"); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	for _, action := range []string{"deliver", "accept", "pick_up", "deliver"} {
		if got := actionForTest(t, a, first.ID, "courier", action); got.Code != 200 {
			t.Fatal("old retry", got.Body.String())
		}
	}
	if got := actionForTest(t, a, third.ID, "courier", "accept"); got.Code != 409 {
		t.Fatal("old retry freed new slot")
	}
	before := readStored(t, a, first.ID)
	if before.Version != 4 || before.Status != "delivered" || before.CourierID == nil || *before.CourierID != courierUser {
		t.Fatalf("invalid delivery %+v", before)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.releases != 0 || f.reserved != 30 {
		t.Fatal("delivery moved credits")
	}
}

func TestConcurrentCourierAssignments(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	orders := []Order{createdForTest(t, a, "one"), createdForTest(t, a, "two")}
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for _, o := range orders {
		wg.Add(1)
		go func() { defer wg.Done(); results <- actionForTest(t, a, o.ID, "courier", "accept").Code }()
	}
	wg.Wait()
	close(results)
	ok, busy := 0, 0
	for code := range results {
		if code == 200 {
			ok++
		}
		if code == 409 {
			busy++
		}
	}
	if ok != 1 || busy != 1 {
		t.Fatalf("success=%d busy=%d", ok, busy)
	}
}

func TestOutboxFailureRollsBackOrderAndRefunds(t *testing.T) {
	a, f := testApp(t)
	_, err := a.db.Exec(context.Background(), `CREATE FUNCTION fail_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'outbox unavailable'; END $$;
        CREATE TRIGGER fail_event BEFORE INSERT ON order_events FOR EACH ROW EXECUTE FUNCTION fail_event();`)
	if err != nil {
		t.Fatal(err)
	}
	got := request(t, a, "POST", "/api/v1/orders", "requester", "event-fail", validCreateBody(10))
	if got.Code != 500 {
		t.Fatal(got.Code, got.Body.String())
	}
	var count int
	_ = a.db.QueryRow(context.Background(), "SELECT count(*) FROM orders").Scan(&count)
	if count != 0 {
		t.Fatal("order without event committed")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.releases != 1 || f.reserved != 0 {
		t.Fatal("failed outbox left reserved credits")
	}
}

func TestEmbeddedUI(t *testing.T) {
	a, _ := testApp(t)
	for _, path := range []string{"/orders", "/orders/", "/orders/app.js", "/orders/core.mjs", "/orders/style.css"} {
		got := request(t, a, "GET", path, "", "", nil)
		if got.Code != 200 || got.Body.Len() == 0 || !strings.Contains(got.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatal("UI asset", path, got.Code)
		}
	}
	if got := request(t, a, "GET", "/orders/missing", "", "", nil); got.Code != 404 {
		t.Fatal("unexpected fallback")
	}
}
