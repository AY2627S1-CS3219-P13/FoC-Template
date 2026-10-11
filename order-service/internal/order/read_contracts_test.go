package order

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCataloguePagesAndFailureContracts(t *testing.T) {
	for _, scenario := range []string{"pages", "inactive", "malformed", "outage", "unauthorized"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cookie, _ := r.Cookie("foc_session")
				if cookie == nil || cookie.Value != "current-session" {
					t.Error("missing current user cookie")
				}
				if r.Header.Get("Authorization") != "" {
					t.Error("leaked internal token to Supplier")
				}
				if r.URL.Path == "/api/v1/locations" {
					writeJSON(w, 200, map[string]any{"locations": []map[string]string{{"id": "com2", "name": "COM2"}}})
					return
				}
				if r.URL.Query().Get("status") != "active" || r.URL.Query().Get("pageSize") != "100" {
					t.Error("wrong catalogue query")
				}
				if scenario == "outage" {
					w.WriteHeader(503)
					return
				}
				if scenario == "unauthorized" {
					w.WriteHeader(401)
					return
				}
				if scenario == "malformed" {
					writeJSON(w, 200, map[string]any{"suppliers": []any{}})
					return
				}
				page := 1
				id := testSupplierID
				if r.URL.Query().Get("page") == "2" {
					page = 2
					id = secondSupplierID
				}
				writeJSON(w, 200, map[string]any{"page": page, "pageSize": 100, "total": 101, "totalPages": 2, "suppliers": []map[string]any{{"id": id, "name": "Cafe", "locationId": "com2", "active": scenario != "inactive"}}})
			}))
			defer server.Close()
			client := &CatalogueClient{BaseURL: server.URL, CookieName: "foc_session", Client: server.Client()}
			places, more, err := client.List(context.Background(), "current-session", 1)
			if scenario != "pages" {
				if err == nil {
					t.Fatal("invalid catalogue accepted")
				}
				return
			}
			if err != nil || !more || len(places) != 2 || places[0].Type != "location" || places[1].LocationName != "COM2" {
				t.Fatalf("first page %+v %v", places, err)
			}
			places, more, err = client.List(context.Background(), "current-session", 2)
			if err != nil || more || len(places) != 1 || places[0].ID != secondSupplierID {
				t.Fatalf("second page %+v %v", places, err)
			}
		})
	}
}

func TestCancellationCommitsBeforeCompetingDelivery(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	o := createdForTest(t, a, "race")
	if _, err := a.db.Exec(context.Background(), "UPDATE orders SET status='picked up',courier_id=$2 WHERE id=$1", o.ID, courierUser); err != nil {
		t.Fatal(err)
	}
	entered, finish := make(chan struct{}), make(chan struct{})
	a.catalogue = resolverFunc(func(context.Context, PlaceRef, string) (Place, error) {
		close(entered)
		<-finish
		return Place{}, invalidPlace()
	})
	refreshed := make(chan int, 1)
	delivered := make(chan int, 1)
	go func() {
		refreshed <- request(t, a, "POST", "/api/v1/orders/"+o.ID+"/refresh", "courier", "", map[string]any{}).Code
	}()
	<-entered
	go func() { delivered <- actionForTest(t, a, o.ID, "courier", "deliver").Code }()
	close(finish)
	if code := <-refreshed; code != 200 {
		t.Fatal("refresh failed", code)
	}
	if code := <-delivered; code != 409 {
		t.Fatal("delivery overwrote cancellation", code)
	}
	after := readStored(t, a, o.ID)
	if after.Status != "cancelled" || after.Version != 2 {
		t.Fatalf("unexpected result %+v", after)
	}
}

func TestEventsSurviveRestartAndRetryWithoutDuplicates(t *testing.T) {
	a, _ := testApp(t)
	body := validCreateBody(10)
	for _, want := range []int{201, 200} {
		got := request(t, a, "POST", "/api/v1/orders", "requester", "event-replay", body)
		if got.Code != want {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	restarted := New(a.db, a.cfg, a.log)
	id := orderID(testUserID, "event-replay")
	got := request(t, restarted, "GET", "/api/v1/orders/"+id+"/events", "requester", "", nil)
	if got.Code != 200 || strings.Count(got.Body.String(), "OrderCreated") != 1 || !strings.Contains(got.Body.String(), `"schemaVersion":1`) {
		t.Fatal(got.Code, got.Body.String())
	}
	got = request(t, restarted, "GET", "/api/v1/orders/"+id+"/events?afterVersion=1", "requester", "", nil)
	if got.Code != 200 || strings.Contains(got.Body.String(), "OrderCreated") {
		t.Fatal("cursor replay", got.Body.String())
	}
	// A broken event sink must roll back a status change AND its refund intent.
	_, err := a.db.Exec(context.Background(), `CREATE FUNCTION fail_change_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'outbox unavailable'; END $$;
        CREATE TRIGGER fail_change_event BEFORE INSERT ON order_events FOR EACH ROW EXECUTE FUNCTION fail_change_event();`)
	if err != nil {
		t.Fatal(err)
	}
	a.catalogue = resolverFunc(func(context.Context, PlaceRef, string) (Place, error) { return Place{}, invalidPlace() })
	got = request(t, a, "POST", "/api/v1/orders/"+id+"/refresh", "requester", "", map[string]any{})
	if got.Code != 500 {
		t.Fatal(got.Code)
	}
	if readStored(t, a, id).Status != "open" {
		t.Fatal("partial cancellation committed")
	}
	var count int
	if err = a.db.QueryRow(context.Background(), "SELECT count(*) FROM order_refunds").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial refund", count, err)
	}
}
