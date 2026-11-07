package order

import (
	"context"
	"encoding/json"
	schedule "foc/schedule-order"
	"sync"
	"testing"
	"time"
)

func TestScheduleEventReceiptRollbackAndConcurrentReplay(t *testing.T) {
	a, f := testApp(t)
	ctx := context.Background()
	if err := schedule.Migrate(ctx, a.db); err != nil {
		t.Fatal(err)
	}
	a.backgroundCatalogue = resolverFunc(func(_ context.Context, r PlaceRef, _ string) (Place, error) {
		return Place{Type: r.Type, ID: r.ID, Name: r.ID}, nil
	})
	start := time.Date(time.Now().Year()+1, 1, 2, 9, 0, 0, 0, time.FixedZone("SGT", 28800))
	request, _ := json.Marshal(map[string]any{"pickup": PlaceRef{"location", "com2"}, "delivery": PlaceRef{"location", "utown"}, "items": []Item{{Name: "Tea", Quantity: 1}}, "offeredCredits": 10, "windowStart": start})
	data, _ := json.Marshal(map[string]any{"id": "scheduled-events-test", "requesterId": testUserID, "status": "scheduled", "expiresAt": start.Add(30 * time.Minute), "windowStart": start})
	if _, err := a.db.Exec(ctx, `INSERT INTO schedules(id,requester_id,idempotency_key,fingerprint,request,order_data,state,courier_id,transfer_id,generation) VALUES('scheduled-events-test',$1,'events','fingerprint',$2,$3,'transfer_pending',$4,'event-one',1)`, testUserID, request, data, courierUser); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(ctx, `INSERT INTO schedule_events(event_id,schedule_id,event_type) VALUES('event-one','scheduled-events-test','ScheduledOrderAcceptanceRequested')`); err != nil {
		t.Fatal(err)
	}
	app := schedule.New(a.db, schedule.Config{}, a.log, a.PrepareScheduledEvent)
	workflowSQL(t, a, `CREATE FUNCTION fail_schedule_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'receipt fault'; END $$;
 CREATE TRIGGER fail_schedule_receipt BEFORE INSERT ON schedule_receipts FOR EACH ROW EXECUTE FUNCTION fail_schedule_receipt()`)
	if err := app.ConsumeEvents(ctx); err == nil {
		t.Fatal("receipt failure ignored")
	}
	for _, table := range []string{"orders", "order_events", "scheduled_imports", "schedule_receipts"} {
		var n int
		if err := a.db.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s not rolled back: %d %v", table, n, err)
		}
	}
	var state string
	if err := a.db.QueryRow(ctx, "SELECT state FROM schedules").Scan(&state); err != nil || state != "transfer_pending" {
		t.Fatal(state, err)
	}
	workflowSQL(t, a, "DROP TRIGGER fail_schedule_receipt ON schedule_receipts; UPDATE schedule_retries SET next_attempt_at=clock_timestamp()")
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := app.ConsumeEvents(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := app.ConsumeEvents(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"orders", "scheduled_imports", "schedule_receipts"} {
		var n int
		if err := a.db.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s effects: %d %v", table, n, err)
		}
	}
	if err := a.db.QueryRow(ctx, "SELECT state FROM schedules").Scan(&state); err != nil || state != "transferred" {
		t.Fatal(state, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reserveEffects != 0 {
		t.Fatal("import reserved funds again")
	}
}
