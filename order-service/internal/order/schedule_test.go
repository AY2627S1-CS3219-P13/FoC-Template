package order

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestScheduleCombinedCapacityThree(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	orders := []Order{}
	for i := 0; i < 5; i++ {
		orders = append(orders, createdForTest(t, a, fmt.Sprintf("capacity-three-%d", i)))
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for _, o := range orders {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			got := actionForTest(t, a, id, "courier", "accept")
			mu.Lock()
			defer mu.Unlock()
			if got.Code == 200 {
				successes++
			} else if got.Code != 409 {
				t.Errorf("accept %d %s", got.Code, got.Body.String())
			}
		}(o.ID)
	}
	wg.Wait()
	if successes != 3 {
		t.Fatalf("want exactly three concurrent assignments, got %d", successes)
	}
}
func scheduleImport(t *testing.T, a *App, body map[string]any, token string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("PUT", "/internal/v1/scheduled-orders/transfer-one", strings.NewReader(string(raw)))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.InternalHandler().ServeHTTP(w, r)
	return w
}
func TestScheduleImportAuthenticatedIdempotentAndDeliveryWindow(t *testing.T) {
	a, f := testApp(t)
	a.auth = actorAuth{}
	// Configuration is loaded from the same dedicated token used by Schedule.
	a.cfg.ScheduleToken = "schedule-internal-token-for-tests-123456"
	start := time.Now().Add(time.Hour).Truncate(30 * time.Minute)
	start = time.Date(start.Year(), start.Month(), start.Day()+1, 9, 0, 0, 0, time.FixedZone("SGT", 28800))
	body := map[string]any{"id": "scheduled-test-one", "requesterId": testUserID, "courierId": courierUser, "pickup": PlaceRef{"location", "com2"}, "delivery": PlaceRef{"location", "utown"}, "items": []Item{{Name: "Tea", Quantity: 1}}, "offeredCredits": 10, "windowStart": start, "expiresAt": start.Add(30 * time.Minute)}
	a.backgroundCatalogue = resolverFunc(func(_ context.Context, ref PlaceRef, _ string) (Place, error) {
		return Place{Type: ref.Type, ID: ref.ID, Name: ref.ID}, nil
	})
	if w := scheduleImport(t, a, body, "wrong"); w.Code != 401 {
		t.Fatalf("auth: %d %s", w.Code, w.Body.String())
	}
	for i := 0; i < 2; i++ {
		w := scheduleImport(t, a, body, a.cfg.ScheduleToken)
		if w.Code != 200 {
			t.Fatalf("import %d %s", w.Code, w.Body.String())
		}
	}
	var count int
	if err := a.db.QueryRow(context.Background(), "SELECT count(*) FROM orders WHERE id='scheduled-test-one'").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	f.mu.Lock()
	reserved := f.reserveEffects
	f.mu.Unlock()
	if reserved != 0 {
		t.Fatal("import reserved credits again")
	}
	a.now = func() time.Time { return start.Add(-time.Minute) }
	mustAction(t, a, "scheduled-test-one", "courier", "pick_up", "picked up")
	if w := actionForTest(t, a, "scheduled-test-one", "courier", "deliver"); w.Code != 409 {
		t.Fatalf("early delivery %d %s", w.Code, w.Body.String())
	}
	a.now = func() time.Time { return start }
	mustAction(t, a, "scheduled-test-one", "courier", "deliver", "delivered")
	body["offeredCredits"] = 11
	if w := scheduleImport(t, a, body, a.cfg.ScheduleToken); w.Code != 409 {
		t.Fatal("conflicting import", w.Code)
	}
}
func TestScheduleChildPage(t *testing.T) {
	a, _ := testApp(t)
	for _, path := range []string{"/orders", "/orders/schedule"} {
		w := request(t, a, "GET", path, "requester", "", nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "Schedule order") {
			t.Fatalf("child navigation %s: %d", path, w.Code)
		}
	}
}

func TestScheduleRejectedImportRemainsRejectedAfterCapacityFrees(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	a.cfg.ScheduleToken = "schedule-internal-token-for-tests-123456"
	a.backgroundCatalogue = resolverFunc(func(_ context.Context, ref PlaceRef, _ string) (Place, error) {
		return Place{Type: ref.Type, ID: ref.ID, Name: ref.ID}, nil
	})
	first := createdForTest(t, a, "full-one")
	for _, o := range []Order{first, createdForTest(t, a, "full-two"), createdForTest(t, a, "full-three")} {
		mustAction(t, a, o.ID, "courier", "accept", "accepted")
	}
	start := time.Date(time.Now().Year()+1, 1, 1, 9, 0, 0, 0, Singapore)
	body := map[string]any{"id": "scheduled-capacity-rejected", "requesterId": testUserID, "courierId": courierUser, "pickup": PlaceRef{"location", "com2"}, "delivery": PlaceRef{"location", "utown"}, "items": []Item{{Name: "Tea", Quantity: 1}}, "offeredCredits": 10, "windowStart": start, "expiresAt": start.Add(30 * time.Minute)}
	for i := 0; i < 2; i++ {
		w := scheduleImport(t, a, body, a.cfg.ScheduleToken)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"outcome":"rejected"`) {
			t.Fatalf("rejection %d %s", w.Code, w.Body.String())
		}
		if i == 0 {
			mustAction(t, a, first.ID, "requester", "cancel", "cancelled")
		}
	}
	var count int
	if e := a.db.QueryRow(context.Background(), "SELECT count(*) FROM orders WHERE id='scheduled-capacity-rejected'").Scan(&count); e != nil || count != 0 {
		t.Fatal("late import after rejection", count, e)
	}
}
