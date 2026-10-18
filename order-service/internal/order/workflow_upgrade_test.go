package order

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestWorkflowUpgradeFrom008ResumesWithoutDataLoss(t *testing.T) {
	a, f := testApp(t)
	a.auth = actorAuth{}
	ctx := context.Background()
	f.mu.Lock()
	f.available = 1000
	f.mu.Unlock()
	states := []string{"open", "accepted", "picked up", "delivered", "dispute", "completed", "cancelled", "expired", "not completed"}
	byStatus := map[string]Order{}
	delivered := time.Now().Add(-10 * time.Minute).Truncate(time.Microsecond)
	for i, status := range states {
		o := createdForTest(t, a, "legacy-"+status)
		courier := fmt.Sprintf("aaaaaaaa-aaaa-4aaa-8aaa-%012d", i)
		workflowSQL(t, a, "UPDATE orders SET status=$2,courier_id=$3,version=7,delivered_at=$4 WHERE id=$1", o.ID, status, courier, delivered)
		byStatus[status] = readStored(t, a, o.ID)
	}
	// Existing jobs and old publication IDs must survive the additive migration.
	workflowSQL(t, a, "INSERT INTO order_refunds(order_id) VALUES($1)", byStatus["cancelled"].ID)
	consume(t, a, publicationConsumer)
	head := workflowCount(t, a, "SELECT position FROM order_publication_head")
	events := workflowCount(t, a, "SELECT count(*) FROM order_events")
	// This fixture reconstructs the exact schema that preceded migration 009,
	// preserving live snapshots, events, queues, attempts and publication positions.
	workflowSQL(t, a, `DROP TABLE workflow_retries,workflow_receipts,workflow_timers,workflow_projections;
 ALTER TABLE order_events DROP COLUMN domain_type,DROP COLUMN domain_schema_version,DROP COLUMN domain_payload;
 ALTER TABLE order_refunds DROP COLUMN attempts,DROP COLUMN error_category;
 ALTER TABLE order_payouts DROP COLUMN attempts,DROP COLUMN error_category;
 DELETE FROM schema_migrations WHERE name='009_workflow.sql'`)
	if err := Migrate(ctx, a.db); err != nil {
		t.Fatal(err)
	}
	// Interrupt bootstrap after its first committed order; its next invocation
	// must resume rather than reset already initialized schedules or jobs.
	workflowSQL(t, a, `CREATE FUNCTION interrupt_bootstrap() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF (SELECT count(*) FROM workflow_projections)>0 THEN RAISE EXCEPTION 'bootstrap crash'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER interrupt_bootstrap BEFORE INSERT ON workflow_projections FOR EACH ROW EXECUTE FUNCTION interrupt_bootstrap()`)
	if err := a.BootstrapWorkflow(ctx); err == nil {
		t.Fatal("bootstrap interruption was ignored")
	}
	if workflowCount(t, a, "SELECT count(*) FROM workflow_projections") != 1 {
		t.Fatal("bootstrap was not per-order resumable")
	}
	workflowSQL(t, a, "DROP TRIGGER interrupt_bootstrap ON workflow_projections")
	for range 2 {
		if err := a.BootstrapWorkflow(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if workflowCount(t, a, "SELECT count(*) FROM workflow_projections WHERE bootstrap_version=7") != len(states) {
		t.Fatal("bootstrap versions lost")
	}
	if workflowCount(t, a, "SELECT count(*) FROM workflow_timers") != 4 {
		t.Fatal("wrong legacy schedules")
	}
	for _, status := range []string{"open", "accepted", "picked up"} {
		if !timerAt(t, a, byStatus[status].ID, "expiry").Equal(byStatus[status].ExpiresAt) {
			t.Fatal("expiry reset", status)
		}
	}
	if !timerAt(t, a, byStatus["delivered"].ID, "completion").Equal(delivered.Add(5*time.Minute + time.Second)) {
		t.Fatal("legacy delivery countdown restarted")
	}
	if workflowCount(t, a, "SELECT count(*) FROM order_refunds") != 3 || workflowCount(t, a, "SELECT count(*) FROM order_payouts") != 1 {
		t.Fatal("missing/duplicate legacy settlement")
	}
	for _, consumer := range []string{timerConsumer, settlementConsumer, publicationConsumer} {
		consume(t, a, consumer)
	}
	if workflowCount(t, a, "SELECT position FROM order_publication_head") != head || workflowCount(t, a, "SELECT count(*) FROM order_events") != events {
		t.Fatal("bootstrap reemitted legacy creation")
	}
	if workflowCount(t, a, "SELECT count(*) FROM creation_attempts WHERE state='completed'") != len(states) {
		t.Fatal("legacy attempts lost")
	}
	if err := Migrate(ctx, a.db); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowBootstrapFencesNewerEventsAndRetainsSettledLegacyIntent(t *testing.T) {
	a, f := testApp(t)
	a.auth = actorAuth{}
	ctx := context.Background()
	o := deliveredForTest(t, a, "bootstrap-fence")
	mustAction(t, a, o.ID, "requester", "dispute", "dispute")
	// A resumable repair based on an old snapshot cannot reset a newer projection.
	consume(t, a, timerConsumer)
	tx, err := a.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	if err = projectTimers(ctx, tx, o.ID, o.Version, "", domainData{Status: "delivered", DeliveredAt: o.DeliveredAt}, true); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.bootstrapOrder(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	if workflowCount(t, a, "SELECT count(*) FROM workflow_timers") != 0 {
		t.Fatal("bootstrap overrode dispute")
	}
	// Legacy terminal absence of a job is not proof of nonpayment: reissue the
	// existing stable Credit operation and rely on its escrow idempotency.
	mustAction(t, a, o.ID, "admin", "resolve_completed", "completed")
	if err = a.credit.Payout(ctx, o.ID, courierUser); err != nil {
		t.Fatal(err)
	}
	workflowSQL(t, a, "UPDATE order_events SET domain_type=NULL,domain_schema_version=NULL,domain_payload=NULL WHERE order_id=$1", o.ID)
	workflowSQL(t, a, "DELETE FROM workflow_projections WHERE order_id=$1", o.ID)
	if err = a.BootstrapWorkflow(ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.payouts != 1 {
		t.Fatal("legacy retry paid twice")
	}
}

func TestWorkflowMalformedLegacyDeliveryRemainsDiagnosable(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	ctx := context.Background()
	o := createdForTest(t, a, "malformed-legacy")
	workflowSQL(t, a, "UPDATE orders SET status='delivered',courier_id=$2 WHERE id=$1", o.ID, courierUser)
	workflowSQL(t, a, "UPDATE order_events SET domain_type=NULL,domain_schema_version=NULL,domain_payload=NULL WHERE order_id=$1", o.ID)
	if err := a.BootstrapWorkflow(ctx); err == nil {
		t.Fatal("invented missing legacy delivery time")
	}
	if workflowCount(t, a, "SELECT count(*) FROM workflow_projections") != 0 || workflowCount(t, a, "SELECT count(*) FROM order_payouts") != 0 {
		t.Fatal("malformed repair committed")
	}
	status, err := a.workflowStatus(ctx)
	if err != nil || status["legacyUninitialized"] != int64(1) {
		t.Fatal("legacy failure invisible", status, err)
	}
}
