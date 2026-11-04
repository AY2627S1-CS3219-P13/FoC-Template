package order

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestRequestHistoryCreationPageBoundaries(t *testing.T) {
	a, f := testApp(t)
	f.available = 1000
	a.auth = actorAuth{}
	var orders []Order
	created := time.Now().Add(-time.Hour)
	for i := range 21 {
		o := createdForTest(t, a, fmt.Sprintf("history-%d", i))
		if _, err := a.db.Exec(context.Background(), "UPDATE orders SET created_at=$2 WHERE id=$1", o.ID, created.Add(-time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
		orders = append(orders, o)
	}
	for page := 1; page <= 2; page++ {
		got := request(t, a, "GET", fmt.Sprintf("/api/v1/orders?scope=mine&filter=all&page=%d", page), "requester", "", nil)
		var body struct {
			Orders                   []Order
			HasMore                  bool
			PageStart, NextPageStart *struct {
				ID        string
				CreatedAt time.Time
			}
		}
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &body) != nil {
			t.Fatal(got.Code, got.Body.String())
		}
		offset := (page - 1) * 20
		if body.PageStart == nil || body.PageStart.ID != orders[offset].ID || !body.PageStart.CreatedAt.Equal(created.Add(-time.Duration(offset)*time.Minute).Truncate(time.Microsecond)) {
			t.Fatal("incorrect page boundary", body.PageStart)
		}
		if page == 1 && (!body.HasMore || len(body.Orders) != 20 || body.NextPageStart == nil || body.NextPageStart.ID != orders[20].ID) {
			t.Fatal("incorrect next-page boundary")
		}
		if page == 2 && (body.HasMore || len(body.Orders) != 1 || body.NextPageStart != nil) {
			t.Fatal("incorrect final page")
		}
		for i, o := range body.Orders {
			if o.ID != orders[offset+i].ID {
				t.Fatal("history not newest first")
			}
		}
	}
}

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
	check(false)
	for _, key := range []string{"capacity-extra-one", "capacity-extra-two"} {
		extra := createdForTest(t, a, key)
		mustAction(t, a, extra.ID, "courier", "accept", "accepted")
	}
	check(true)
	mustAction(t, a, o.ID, "courier", "pick_up", "picked up")
	check(true)
	mustAction(t, a, o.ID, "courier", "deliver", "delivered")
	check(false)
}
