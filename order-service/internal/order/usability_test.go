package order

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestRequestFiltersIncludeAllNineStates(t *testing.T) {
	a, f := testApp(t)
	f.available = 1000
	a.auth = actorAuth{}
	states := []string{"open", "accepted", "picked up", "delivered", "dispute", "completed", "not completed", "cancelled", "expired"}
	for i, state := range states {
		o := createdForTest(t, a, state)
		if _, err := a.db.Exec(context.Background(), "UPDATE orders SET status=$2,courier_id=$3 WHERE id=$1", o.ID, state, fmt.Sprintf("aaaaaaaa-aaaa-4aaa-8aaa-%012d", i)); err != nil {
			t.Fatal(err)
		}
	}
	for filter, want := range map[string]int{"all": 9, "current": 5, "past": 4} {
		got := request(t, a, "GET", "/api/v1/orders?scope=mine&filter="+filter, "requester", "", nil)
		var body struct{ Orders []Order }
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &body) != nil || len(body.Orders) != want {
			t.Fatalf("%s: %d %s", filter, got.Code, got.Body.String())
		}
		for _, o := range body.Orders {
			terminal := o.Status == "completed" || o.Status == "not completed" || o.Status == "cancelled" || o.Status == "expired"
			if filter == "current" && terminal || filter == "past" && !terminal {
				t.Fatalf("%s included %s", filter, o.Status)
			}
		}
	}
	if got := request(t, a, "GET", "/api/v1/orders?scope=mine&filter=invalid", "requester", "", nil); got.Code != 400 {
		t.Fatal("invalid filter accepted", got.Code)
	}
}

func TestSessionTracksCourierCapacity(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	check := func(want bool) {
		t.Helper()
		got := request(t, a, "GET", "/api/v1/orders/session", "courier", "", nil)
		var body struct{ CourierBusy bool }
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &body) != nil || body.CourierBusy != want {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	o := createdForTest(t, a, "capacity")
	check(false)
	mustAction(t, a, o.ID, "courier", "accept", "accepted")
	check(true)
	mustAction(t, a, o.ID, "courier", "pick_up", "picked up")
	check(true)
	mustAction(t, a, o.ID, "courier", "deliver", "delivered")
	check(false)
}
