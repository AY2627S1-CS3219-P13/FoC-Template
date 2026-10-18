package order

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func workflowSQL(t *testing.T, a *App, sql string, args ...any) {
	t.Helper()
	if _, err := a.db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func workflowCount(t *testing.T, a *App, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := a.db.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func consume(t *testing.T, a *App, consumer string) {
	t.Helper()
	if err := a.ConsumeWorkflow(context.Background(), consumer); err != nil {
		t.Fatal(err)
	}
}
func timerAt(t *testing.T, a *App, id, kind string) time.Time {
	t.Helper()
	var due time.Time
	if err := a.db.QueryRow(context.Background(), "SELECT due_at FROM workflow_timers WHERE order_id=$1 AND timer_kind=$2", id, kind).Scan(&due); err != nil {
		t.Fatal(err)
	}
	return due
}
func assertDomain(t *testing.T, a *App, o Order, kind string) domainEvent {
	t.Helper()
	var e domainEvent
	var raw []byte
	err := a.db.QueryRow(context.Background(), `SELECT event_id,order_id,order_version,domain_type,domain_schema_version,domain_payload,created_at FROM order_events WHERE order_id=$1 AND order_version=$2`, o.ID, o.Version).Scan(&e.ID, &e.OrderID, &e.Version, &e.Type, &e.Schema, &raw, &e.OccurredAt)
	if err != nil || decodeDomain(raw, &e) != nil || e.Type != kind || e.Data.Status != o.Status || !sameCourier(e.Data.CourierID, o.CourierID) || !sameTime(e.Data.DeliveredAt, o.DeliveredAt) || !sameTime(e.Data.CompletionRequestedAt, o.CompletionRequestedAt) || !e.Data.ExpiresAt.Equal(o.ExpiresAt) || e.OccurredAt.IsZero() {
		t.Fatal("wrong committed domain data", e, string(raw), err)
	}
	if strings.Contains(string(raw), "Milk Tea") || strings.Contains(string(raw), "token") {
		t.Fatal("unnecessary private data in event", string(raw))
	}
	return e
}

func TestWorkflowPausedConsumersAndRestartRecovery(t *testing.T) {
	a, f := testApp(t)
	a.auth = actorAuth{}
	ctx := context.Background()
	o := createdForTest(t, a, "paused-workflow")
	assertDomain(t, a, o, "OrderCreated")
	for _, step := range []struct{ action, kind string }{{"accept", "OrderAccepted"}, {"pick_up", "OrderPickedUp"}, {"deliver", "OrderDelivered"}} {
		mustAction(t, a, o.ID, "courier", step.action, map[string]string{"accept": "accepted", "pick_up": "picked up", "deliver": "delivered"}[step.action])
		o = readStored(t, a, o.ID)
		assertDomain(t, a, o, step.kind)
	}
	// Commands commit all events while consumers and financial workers are off.
	if n := workflowCount(t, a, "SELECT count(*) FROM workflow_receipts"); n != 0 {
		t.Fatal(n)
	}
	if err := a.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	if workflowCount(t, a, "SELECT count(*) FROM workflow_timers") != 0 {
		t.Fatal("executor synthesized schedules")
	}
	restarted := New(a.db, a.cfg, a.log)
	restarted.auth = actorAuth{}
	consume(t, restarted, timerConsumer)
	if due := timerAt(t, a, o.ID, "completion"); !due.Equal(o.DeliveredAt.Add(5*time.Minute + time.Second)) {
		t.Fatal("deadline restarted", due)
	}
	mustAction(t, restarted, o.ID, "requester", "confirm", "delivered")
	o = readStored(t, a, o.ID)
	assertDomain(t, a, o, "OrderCompletionRequested")
	consume(t, restarted, timerConsumer)
	if !timerAt(t, a, o.ID, "completion").Equal(o.CompletionRequestedAt.Add(time.Second)) {
		t.Fatal("confirmation did not advance original deadline")
	}
	restarted.now = func() time.Time { return o.CompletionRequestedAt.Add(time.Second) }
	if err := restarted.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	assertDomain(t, a, readStored(t, a, o.ID), "OrderCompleted")
	if workflowCount(t, a, "SELECT count(*) FROM order_payouts") != 0 {
		t.Fatal("completion directly inserted payout")
	}
	if err := restarted.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	paid := f.payouts
	f.mu.Unlock()
	if paid != 0 {
		t.Fatal("financial worker replayed command")
	}
	consume(t, restarted, settlementConsumer)
	if workflowCount(t, a, "SELECT count(*) FROM order_payouts") != 1 {
		t.Fatal("missing settlement job")
	}
	restarted.credit = lostPayout{restarted.credit}
	if err := restarted.Recover(ctx); err == nil {
		t.Fatal("lost response was acknowledged")
	}
	if workflowCount(t, a, "SELECT count(*) FROM order_payouts") != 1 {
		t.Fatal("lost reply dropped job")
	}
	resumed := New(a.db, a.cfg, a.log)
	for range 2 {
		if err := resumed.Recover(ctx); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.payouts != 1 || f.reserveEffects != 1 {
		t.Fatal("duplicate financial effect", f.payouts, f.reserveEffects)
	}
}

func TestWorkflowEveryEventAndOriginalDeadlines(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	ctx := context.Background()
	o := createdForTest(t, a, "deadline-workflow")
	consume(t, a, timerConsumer)
	original := timerAt(t, a, o.ID, "expiry")
	if !original.Equal(o.ExpiresAt) {
		t.Fatal(original, o.ExpiresAt)
	}
	for _, step := range []struct{ action, status string }{{"accept", "accepted"}, {"pick_up", "picked up"}} {
		mustAction(t, a, o.ID, "courier", step.action, step.status)
		consume(t, a, timerConsumer)
		if !timerAt(t, a, o.ID, "expiry").Equal(original) {
			t.Fatal("deadline extended")
		}
	}
	mustAction(t, a, o.ID, "courier", "deliver", "delivered")
	consume(t, a, timerConsumer)
	if workflowCount(t, a, "SELECT count(*) FROM workflow_timers WHERE timer_kind='expiry'") != 0 {
		t.Fatal("delivery kept expiry")
	}
	mustAction(t, a, o.ID, "requester", "confirm", "delivered")
	mustAction(t, a, o.ID, "requester", "dispute", "dispute")
	disputed := readStored(t, a, o.ID)
	assertDomain(t, a, disputed, "OrderDisputed")
	consume(t, a, timerConsumer)
	if workflowCount(t, a, "SELECT count(*) FROM workflow_timers") != 0 {
		t.Fatal("dispute recreated completion")
	}
	a.now = func() time.Time { return original.Add(time.Hour) }
	if err := a.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	mustAction(t, a, o.ID, "admin", "resolve_not_completed", "not completed")
	assertDomain(t, a, readStored(t, a, o.ID), "OrderNotCompleted")
	assertSettlementIntents(t, a)
	consume(t, a, settlementConsumer)
	if workflowCount(t, a, "SELECT count(*) FROM order_refunds") != 1 || workflowCount(t, a, "SELECT count(*) FROM order_payouts") != 0 {
		t.Fatal("wrong disputed settlement")
	}
	// Replaying older scheduling data cannot resurrect a deleted timer tombstone.
	tx, err := a.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	if err = projectTimers(ctx, tx, o.ID, 1, "", domainData{Status: "open", ExpiresAt: original}, false); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if workflowCount(t, a, "SELECT count(*) FROM workflow_timers") != 0 {
		t.Fatal("stale creation resurrected timer")
	}
}

func TestWorkflowEffectsAndReceiptsRollbackTogether(t *testing.T) {
	for _, consumer := range []string{timerConsumer, settlementConsumer, publicationConsumer} {
		t.Run(consumer, func(t *testing.T) {
			a, _ := testApp(t)
			a.auth = actorAuth{}
			ctx := context.Background()
			o := createdForTest(t, a, "receipt-atomicity")
			if consumer == settlementConsumer {
				mustAction(t, a, o.ID, "requester", "cancel", "cancelled")
			}
			workflowSQL(t, a, `CREATE FUNCTION fail_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'secret dependency body must not be retained'; END $$;
 CREATE TRIGGER fail_receipt BEFORE INSERT ON workflow_receipts FOR EACH ROW EXECUTE FUNCTION fail_receipt()`)
			if err := a.ConsumeWorkflow(ctx, consumer); err == nil {
				t.Fatal("receipt fault ignored")
			}
			for _, table := range []string{"workflow_receipts", "workflow_timers", "order_refunds", "order_publications"} {
				if workflowCount(t, a, "SELECT count(*) FROM "+table) != 0 {
					t.Fatal("partial effect survived", table)
				}
			}
			workflowSQL(t, a, "DROP TRIGGER fail_receipt ON workflow_receipts")
			workflowSQL(t, a, "UPDATE workflow_retries SET next_attempt_at='-infinity'")
			b := New(a.db, a.cfg, a.log)
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					if err := b.ConsumeWorkflow(ctx, consumer); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			consume(t, b, consumer)
			expected := 1
			table := "workflow_timers"
			if consumer == settlementConsumer {
				table = "order_refunds"
				expected = 2
			}
			if consumer == publicationConsumer {
				table = "order_publications"
			}
			if workflowCount(t, a, "SELECT count(*) FROM "+table) != 1 || workflowCount(t, a, "SELECT count(*) FROM workflow_receipts WHERE consumer_name=$1", consumer) != expected {
				t.Fatal("missing/duplicate effects or receipts")
			}
			if workflowCount(t, a, "SELECT count(*) FROM workflow_receipts WHERE consumer_name<>$1", consumer) != 0 {
				t.Fatal("cross-consumer acknowledgement")
			}
		})
	}
}

func TestWorkflowPoisonIsolationAndAdminDiagnostics(t *testing.T) {
	for _, poison := range []string{"type", "schema", "payload"} {
		t.Run(poison, func(t *testing.T) {
			a, _ := testApp(t)
			a.auth = actorAuth{}
			ctx := context.Background()
			bad := createdForTest(t, a, "poison")
			good := createdForTest(t, a, "healthy")
			switch poison {
			case "type":
				workflowSQL(t, a, "UPDATE order_events SET domain_type='FutureEvent' WHERE order_id=$1", bad.ID)
			case "schema":
				workflowSQL(t, a, "UPDATE order_events SET domain_schema_version=2 WHERE order_id=$1", bad.ID)
			case "payload":
				workflowSQL(t, a, `UPDATE order_events SET domain_payload='{"secret":"must not escape"}' WHERE order_id=$1`, bad.ID)
			}
			for _, consumer := range []string{timerConsumer, settlementConsumer, publicationConsumer} {
				if err := a.ConsumeWorkflow(ctx, consumer); err == nil {
					t.Fatal("poison acknowledged")
				}
				if workflowCount(t, a, "SELECT count(*) FROM workflow_receipts WHERE consumer_name=$1 AND event_id=$2", consumer, bad.ID+":1") != 0 {
					t.Fatal("poison receipt")
				}
				if err := a.ConsumeWorkflow(ctx, consumer); err != nil {
					t.Fatal("backoff ignored", err)
				}
			}
			a.now = func() time.Time { return good.ExpiresAt }
			if err := a.Advance(ctx); err != nil {
				t.Fatal(err)
			}
			if readStored(t, a, good.ID).Status != "expired" {
				t.Fatal("poison blocked unrelated timer")
			}
			got := request(t, a, "GET", "/api/v1/orders/publication-status", "admin", "", nil)
			if got.Code != 200 || strings.Contains(got.Body.String(), "secret") || !strings.Contains(got.Body.String(), `"failed":1`) {
				t.Fatal(got.Code, got.Body.String())
			}
			if request(t, a, "GET", "/api/v1/orders/publication-status", "courier", "", nil).Code != 403 {
				t.Fatal("diagnostics leaked")
			}
			workflowSQL(t, a, "UPDATE order_events SET domain_type='OrderCreated',domain_schema_version=1 WHERE order_id=$1", bad.ID)
			if poison != "payload" {
				workflowSQL(t, a, "UPDATE workflow_retries SET next_attempt_at='-infinity'")
				consume(t, a, timerConsumer)
				if workflowCount(t, a, "SELECT count(*) FROM workflow_timers WHERE order_id=$1", bad.ID) != 1 {
					t.Fatal("repair did not resume")
				}
			}
		})
	}
}

func TestWorkflowStaleTimerAndDelayedCreation(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	ctx := context.Background()
	o := createdForTest(t, a, "delayed-timer")
	a.now = func() time.Time { return o.ExpiresAt.Add(time.Minute) }
	consume(t, a, timerConsumer)
	if !timerAt(t, a, o.ID, "expiry").Equal(o.ExpiresAt) {
		t.Fatal("overdue creation received new timeout")
	}
	if err := a.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	assertDomain(t, a, readStored(t, a, o.ID), "OrderExpired")
	if workflowCount(t, a, "SELECT count(*) FROM workflow_projections WHERE order_id=$1 AND order_version=$2", o.ID, readStored(t, a, o.ID).Version) != 1 {
		t.Fatal("executor lost terminal projection tombstone")
	}

	if workflowCount(t, a, "SELECT count(*) FROM order_refunds") != 0 {
		t.Fatal("expiry inserted direct refund")
	}
	consume(t, a, settlementConsumer)
	if workflowCount(t, a, "SELECT count(*) FROM order_refunds") != 1 {
		t.Fatal("expired intent lost")
	}
	// A snapshot selected by an old executor must not acknowledge new-generation work.
	a.now = time.Now
	d := deliveredForTest(t, a, "stale-timer")
	consume(t, a, timerConsumer)
	stale := dueTimer{d.ID, "completion", d.Version}
	mustAction(t, a, d.ID, "requester", "confirm", "delivered")
	consume(t, a, timerConsumer)
	wanted := timerAt(t, a, d.ID, "completion")
	a.now = func() time.Time { return wanted.Add(time.Second) }
	if err := a.executeTimer(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if !timerAt(t, a, d.ID, "completion").Equal(wanted) || readStored(t, a, d.ID).Status != "delivered" {
		t.Fatal("stale claim consumed newer timer")
	}
	mustAction(t, a, d.ID, "requester", "dispute", "dispute")
	// Delayed projection has not caught up: state still protects against completion.
	if err := a.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	if readStored(t, a, d.ID).Status != "dispute" {
		t.Fatal("old timer undid dispute")
	}
	consume(t, a, timerConsumer)
	consume(t, a, settlementConsumer)
	if workflowCount(t, a, "SELECT count(*) FROM order_payouts") != 0 {
		t.Fatal("dispute paid")
	}
}

func TestWorkflowDomainEventWriteFailureRollsBackCommand(t *testing.T) {
	a, f := testApp(t)
	a.auth = actorAuth{}
	ctx := context.Background()
	workflowSQL(t, a, `CREATE FUNCTION fail_domain() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'failed event'; END $$;
 CREATE TRIGGER fail_domain BEFORE INSERT ON order_events FOR EACH ROW EXECUTE FUNCTION fail_domain()`)
	got := request(t, a, "POST", "/api/v1/orders", "requester", "fail-created-event", validCreateBody(10))
	if got.Code != 500 || workflowCount(t, a, "SELECT count(*) FROM orders") != 0 {
		t.Fatal(got.Code, got.Body.String())
	}
	f.mu.Lock()
	released := f.releases
	f.mu.Unlock()
	if released != 1 {
		t.Fatal("creation reservation leaked")
	}
	workflowSQL(t, a, "DROP TRIGGER fail_domain ON order_events")
	o := createdForTest(t, a, "fail-cancel-event")
	workflowSQL(t, a, "CREATE TRIGGER fail_domain BEFORE INSERT ON order_events FOR EACH ROW EXECUTE FUNCTION fail_domain()")
	if actionForTest(t, a, o.ID, "requester", "cancel").Code != 500 {
		t.Fatal("event failure not propagated")
	}
	if readStored(t, a, o.ID).Status != "open" || workflowCount(t, a, "SELECT count(*) FROM order_actions") != 0 || workflowCount(t, a, "SELECT count(*) FROM order_refunds") != 0 {
		t.Fatal("partial lifecycle commit")
	}
	if err := a.PublishEvents(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowLifecycleVisibilityUsesCurrentIdentityAndState(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	ctx := context.Background()
	o := deliveredForTest(t, a, "lifecycle-visibility")
	mustAction(t, a, o.ID, "requester", "confirm", "delivered")
	mustAction(t, a, o.ID, "requester", "dispute", "dispute")
	consume(t, a, publicationConsumer)
	for _, user := range []struct {
		id    string
		admin bool
		want  int
	}{{testUserID, false, 5}, {courierUser, false, 5}, {otherUser, false, 1}, {otherUser, true, 5}} {
		batch, err := a.publicationBatch(ctx, 0, user.id, user.admin)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, p := range batch {
			if p.eligible {
				count++
				raw, _ := json.Marshal(p.event)
				if strings.Contains(string(raw), "requesterId") || strings.Contains(string(raw), "domain") {
					t.Fatal("internal payload leaked")
				}
			}
		}
		if count != user.want {
			t.Fatal("wrong lifecycle audience", user.id, count, user.want)
		}
	}
	mustAction(t, a, o.ID, "admin", "resolve_completed", "completed")
	consume(t, a, publicationConsumer)
	batch, err := a.publicationBatch(ctx, 0, otherUser, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range batch {
		if p.eligible && p.event.Version > 2 {
			t.Fatal("former dispute admin retained private event access", p)
		}
	}
}

func TestWorkflowTimerFailureIsRetainedAndUnrelatedWorkProceeds(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	ctx := context.Background()
	broken := createdForTest(t, a, "broken-expiry")
	good := createdForTest(t, a, "healthy-expiry")
	consume(t, a, timerConsumer)
	workflowSQL(t, a, fmt.Sprintf(`CREATE FUNCTION fail_one_timer() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.order_id='%s' AND NEW.domain_type='OrderExpired' THEN RAISE EXCEPTION 'timer write failed'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER fail_one_timer BEFORE INSERT ON order_events FOR EACH ROW EXECUTE FUNCTION fail_one_timer()`, broken.ID))
	a.now = func() time.Time { return good.ExpiresAt.Add(time.Second) }
	if err := a.Advance(ctx); err == nil {
		t.Fatal("timer failure ignored")
	}
	if readStored(t, a, broken.ID).Status != "open" || readStored(t, a, good.ID).Status != "expired" {
		t.Fatal("timer failure crossed orders")
	}
	if workflowCount(t, a, "SELECT count(*) FROM workflow_timers WHERE error_category='timer_failed'") != 1 {
		t.Fatal("failed timer lost")
	}
	workflowSQL(t, a, "DROP TRIGGER fail_one_timer ON order_events")
	workflowSQL(t, a, "UPDATE workflow_timers SET next_attempt_at='-infinity'")
	if err := a.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	if readStored(t, a, broken.ID).Status != "expired" {
		t.Fatal("failed timer not recoverable")
	}
}

func TestWorkflowCreditConflictRemainsPendingAndVisible(t *testing.T) {
	for _, outcome := range []string{"refund", "payout"} {
		t.Run(outcome, func(t *testing.T) {
			a, _ := testApp(t)
			a.auth = actorAuth{}
			ctx := context.Background()
			var o Order
			table := "order_refunds"
			if outcome == "refund" {
				o = createdForTest(t, a, "conflicting-refund")
				mustAction(t, a, o.ID, "requester", "cancel", "cancelled")
				if err := a.credit.Payout(ctx, o.ID, courierUser); err != nil {
					t.Fatal(err)
				}
			} else {
				o = deliveredForTest(t, a, "conflicting-payout")
				mustAction(t, a, o.ID, "requester", "dispute", "dispute")
				mustAction(t, a, o.ID, "admin", "resolve_completed", "completed")
				if err := a.credit.Release(ctx, o.ID); err != nil {
					t.Fatal(err)
				}
				table = "order_payouts"
			}
			consume(t, a, settlementConsumer)
			if err := a.Recover(ctx); err == nil {
				t.Fatal("permanent conflict treated as success")
			}
			if workflowCount(t, a, "SELECT count(*) FROM "+table+" WHERE error_category='credit_conflict' AND attempts=1") != 1 {
				t.Fatal("conflict diagnosis/job lost")
			}
			response := request(t, a, "GET", "/api/v1/orders/publication-status", "admin", "", nil)
			key := `"failedRefunds":1`
			if outcome == "payout" {
				key = `"failedPayouts":1`
			}
			if response.Code != 200 || !strings.Contains(response.Body.String(), key) {
				t.Fatal("financial failure invisible", response.Body.String())
			}
		})
	}
}

func TestWorkflowSlowCreditDoesNotBlockPublicationOrTimers(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cancelled := createdForTest(t, a, "slow-finance")
	mustAction(t, a, cancelled.ID, "requester", "cancel", "cancelled")
	consume(t, a, settlementConsumer)
	entered := make(chan struct{})
	a.credit = escrowFaults{escrowClient: a.credit, release: func(ctx context.Context, id string) error { close(entered); <-ctx.Done(); return ctx.Err() }}
	done := make(chan error, 1)
	go func() { done <- a.Recover(ctx) }()
	awaitSignal(t, entered)
	due := createdForTest(t, a, "timer-during-credit")
	a.now = func() time.Time { return due.ExpiresAt }
	consume(t, a, timerConsumer)
	if err := a.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	consume(t, a, publicationConsumer)
	if readStored(t, a, due.ID).Status != "expired" || workflowCount(t, a, "SELECT count(*) FROM order_publications") != 4 {
		t.Fatal("slow Credit blocked lifecycle/publication")
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("slow financial failure not retained")
	}
	if workflowCount(t, a, "SELECT count(*) FROM order_refunds WHERE order_id=$1", cancelled.ID) != 1 {
		t.Fatal("failed financial work dropped")
	}
}

func TestWorkflowConcurrentTimerExecutorsAndFinancialWorkers(t *testing.T) {
	for _, kind := range []string{"completion", "expiry"} {
		t.Run(kind, func(t *testing.T) {
			a, f := testApp(t)
			a.auth = actorAuth{}
			ctx := context.Background()
			var o Order
			target := "completed"
			if kind == "completion" {
				o = deliveredForTest(t, a, "timer-race")
			} else {
				o = createdForTest(t, a, "timer-race")
				target = "expired"
			}
			var next Order
			if kind == "completion" {
				next = createdForTest(t, a, "newer-slot")
				mustAction(t, a, next.ID, "courier", "accept", "accepted")
			}
			consume(t, a, timerConsumer)
			due := timerAt(t, a, o.ID, kind)
			a.now = func() time.Time { return due }
			b := New(a.db, a.cfg, a.log)
			b.now = a.now
			var workers sync.WaitGroup
			for i := range 8 {
				workers.Go(func() {
					app := a
					if i%2 != 0 {
						app = b
					}
					if err := app.Advance(ctx); err != nil {
						t.Error(err)
					}
				})
			}
			workers.Wait()
			stored := readStored(t, a, o.ID)
			if stored.Status != target || stored.Version != o.Version+1 {
				t.Fatal("duplicate timer transition", stored)
			}
			if workflowCount(t, a, "SELECT count(*) FROM order_events WHERE order_id=$1", o.ID) != int(stored.Version) {
				t.Fatal("duplicate timer event")
			}
			if workflowCount(t, a, "SELECT count(*) FROM order_payouts")+workflowCount(t, a, "SELECT count(*) FROM order_refunds") != 0 {
				t.Fatal("timer directly settled")
			}
			// Completing this older order leaves the newer assignment untouched.
			if kind == "completion" && readStored(t, a, next.ID).Status != "accepted" {
				t.Fatal("old timer changed newer slot")
			}
			consume(t, a, settlementConsumer)
			for _, app := range []*App{a, b} {
				workers.Go(func() {
					if err := app.Recover(ctx); err != nil {
						t.Error(err)
					}
				})
			}
			workers.Wait()
			f.mu.Lock()
			defer f.mu.Unlock()
			if kind == "completion" && (f.payouts != 1 || f.releases != 0) {
				t.Fatal("duplicate payout effect", f.payouts, f.releases)
			}
			if kind == "expiry" && (f.releases != 1 || f.payouts != 0) {
				t.Fatal("duplicate release effect", f.releases, f.payouts)
			}
		})
	}
}

func TestWorkflowDetailsAndSuccessfulReplaysDoNotRestartTimers(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	o := createdForTest(t, a, "details-and-replays")
	consume(t, a, timerConsumer)
	original := timerAt(t, a, o.ID, "expiry")
	underlying := a.catalogue
	a.catalogue = resolverFunc(func(ctx context.Context, ref PlaceRef, token string) (Place, error) {
		p, err := underlying.Resolve(ctx, ref, token)
		p.Name = "Renamed"
		return p, err
	})
	assertStatus(t, request(t, a, "POST", "/api/v1/orders/"+o.ID+"/refresh", "requester", "", map[string]any{}), 200)
	changed := readStored(t, a, o.ID)
	assertDomain(t, a, changed, "OrderDetailsChanged")
	consume(t, a, timerConsumer)
	if !timerAt(t, a, o.ID, "expiry").Equal(original) {
		t.Fatal("details reset timer")
	}
	assertStatus(t, request(t, a, "POST", "/api/v1/orders/"+o.ID+"/refresh", "requester", "", map[string]any{}), 200)
	if readStored(t, a, o.ID).Version != changed.Version {
		t.Fatal("no-op refresh emitted event")
	}
	mustAction(t, a, o.ID, "courier", "accept", "accepted")
	version := readStored(t, a, o.ID).Version
	mustAction(t, a, o.ID, "courier", "accept", "accepted")
	assertStatus(t, actionForTest(t, a, o.ID, "requester", "deliver"), 403)
	if readStored(t, a, o.ID).Version != version || workflowCount(t, a, "SELECT count(*) FROM order_events WHERE order_id=$1", o.ID) != int(version) {
		t.Fatal("replay/rejection emitted event")
	}
}
