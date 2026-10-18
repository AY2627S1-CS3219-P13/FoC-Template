package order

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReview03SupplierCancellationWithoutBrowserReads(t *testing.T) {
	for _, state := range []string{"open", "accepted", "picked up"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			a, f := testApp(t)
			a.auth = actorAuth{}
			o := createdForTest(t, a, "unattended-change")
			if state != "open" {
				assertStatus(t, actionForTest(t, a, o.ID, "courier", "accept"), 200)
			}
			if state == "picked up" {
				assertStatus(t, actionForTest(t, a, o.ID, "courier", "pick_up"), 200)
			}
			underlying := a.catalogue
			a.backgroundCatalogue = resolverFunc(func(ctx context.Context, ref PlaceRef, session string) (Place, error) {
				if ref.Type == "supplier" {
					return Place{}, invalidPlace() // Supplier now reports this selection inactive/deleted.
				}
				return underlying.Resolve(ctx, ref, "session")
			})
			// Only the real worker entry points run: no list, refresh, or action request.
			deadline := time.Now().Add(300 * time.Millisecond)
			for time.Now().Before(deadline) {
				if err := recoverForTest(a, context.Background()); err != nil {
					t.Fatal(err)
				}
				time.Sleep(100 * time.Millisecond)
			}
			// Cancellation and refund queues run concurrently; the next worker tick delivers the queued refund.
			if err := recoverForTest(a, context.Background()); err != nil {
				t.Fatal(err)
			}
			observed := readStored(t, a, o.ID)
			f.mu.Lock()
			reserved := f.reserved
			f.mu.Unlock()
			if reserved != 0 {
				t.Fatalf("cancelled order still holds %d credits", reserved)
			}
			// Control: repeated authenticated refresh preserves cancellation.
			assertStatus(t, request(t, a, "POST", "/api/v1/orders/"+o.ID+"/refresh", "requester", "", map[string]any{}), 200)
			if readStored(t, a, o.ID).Status != "cancelled" {
				t.Fatal("control refresh did not discover Supplier deactivation")
			}
			if observed.Status != "cancelled" {
				t.Fatalf("after background recovery of background recovery with no browser reads: status=%q reserved=%d; want cancelled", observed.Status, reserved)
			}
		})
	}
}

func TestSupplierWorkerOutageAndProtectedStates(t *testing.T) {
	for _, state := range []string{"open", "delivered", "dispute", "completed"} {
		t.Run(state, func(t *testing.T) {
			a, _ := testApp(t)
			a.auth = actorAuth{}
			o := createdForTest(t, a, "protected")
			if state != "open" {
				assertStatus(t, actionForTest(t, a, o.ID, "courier", "accept"), 200)
				assertStatus(t, actionForTest(t, a, o.ID, "courier", "pick_up"), 200)
				assertStatus(t, actionForTest(t, a, o.ID, "courier", "deliver"), 200)
			}
			if state == "dispute" {
				assertStatus(t, actionForTest(t, a, o.ID, "requester", "dispute"), 200)
			}
			if state == "completed" {
				assertStatus(t, actionForTest(t, a, o.ID, "requester", "confirm"), 202)
				if err := advanceForTest(a, context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			before := readStored(t, a, o.ID)
			a.backgroundCatalogue = resolverFunc(func(context.Context, PlaceRef, string) (Place, error) { return Place{}, placesUnavailable() })
			_ = recoverForTest(a, context.Background())
			if readStored(t, a, o.ID).Status != before.Status {
				t.Fatal("dependency outage changed status")
			}
			if state != "open" {
				a.backgroundCatalogue = resolverFunc(func(context.Context, PlaceRef, string) (Place, error) { return Place{}, invalidPlace() })
				_ = recoverForTest(a, context.Background())
				if readStored(t, a, o.ID).Status != before.Status {
					t.Fatal("supplier change cancelled protected state")
				}
			}
		})
	}
}
func TestPrivateCatalogueUsesServiceCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer worker-token" || r.Header.Get("Cookie") != "" || !strings.HasPrefix(r.URL.Path, "/internal/v1/") {
			t.Error("worker must use only its private service credential")
		}
		if strings.HasSuffix(r.URL.Path, "locations") {
			writeJSON(w, 200, map[string]any{"locations": []map[string]string{{"id": "com2", "name": "COM2"}}})
			return
		}
		writeJSON(w, 200, map[string]any{"supplier": map[string]any{"id": testSupplierID, "name": "Cafe", "locationId": "com2", "active": false}})
	}))
	defer server.Close()
	c := &CatalogueClient{BaseURL: server.URL, InternalToken: "worker-token", Client: server.Client()}
	_, err := c.Resolve(context.Background(), PlaceRef{Type: "supplier", ID: testSupplierID}, "")
	if err == nil {
		t.Fatal("inactive supplier accepted")
	}
}

func TestSupplierWorkerDefersToLiveAdmission(t *testing.T) {
	a, _ := testApp(t)
	o := createdForTest(t, a, "admission")
	a.backgroundCatalogue = resolverFunc(func(context.Context, PlaceRef, string) (Place, error) { return Place{}, invalidPlace() })
	if _, err := a.db.Exec(context.Background(), "INSERT INTO order_admissions(order_id,received_at) VALUES($1,clock_timestamp())", o.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.cancelInactiveSuppliers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if readStored(t, a, o.ID).Status != "open" {
		t.Fatal("worker overtook an admitted action")
	}
	if _, err := a.db.Exec(context.Background(), "DELETE FROM order_admissions WHERE order_id=$1", o.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.cancelInactiveSuppliers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if readStored(t, a, o.ID).Status != "cancelled" {
		t.Fatal("worker failed to resume after admission completed")
	}
}

func TestSupplierWorkerLookupFailureDoesNotBlockOtherOrders(t *testing.T) {
	a, _ := testApp(t)
	first := createdForTest(t, a, "unavailable")
	second := createdForTest(t, a, "inactive")
	if _, err := a.db.Exec(context.Background(), "UPDATE orders SET pickup=jsonb_set(pickup,'{id}',to_jsonb($1::text)) WHERE id=$2", secondSupplierID, second.ID); err != nil {
		t.Fatal(err)
	}
	underlying := a.catalogue
	a.backgroundCatalogue = resolverFunc(func(ctx context.Context, ref PlaceRef, _ string) (Place, error) {
		if ref.ID == testSupplierID {
			return Place{}, placesUnavailable()
		}
		if ref.ID == secondSupplierID {
			return Place{}, invalidPlace()
		}
		return underlying.Resolve(ctx, ref, "session")
	})
	if err := a.cancelInactiveSuppliers(context.Background()); err == nil {
		t.Fatal("failed lookup was not reported")
	}
	if readStored(t, a, first.ID).Status != "open" {
		t.Fatal("outage cancelled order")
	}
	if readStored(t, a, second.ID).Status != "cancelled" {
		t.Fatal("one failed supplier blocked unrelated cancellation")
	}
}
