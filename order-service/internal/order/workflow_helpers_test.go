package order

import (
	"context"
	"testing"
)

// Test harness only: explicitly run the independent event, timer and financial
// workers. Production Recover deliberately contains no event consumption.
func advanceForTest(a *App, ctx context.Context) error {
	if err := a.ConsumeWorkflow(ctx, timerConsumer); err != nil {
		return err
	}
	if err := a.Advance(ctx); err != nil {
		return err
	}
	return a.ConsumeWorkflow(ctx, settlementConsumer)
}
func recoverForTest(a *App, ctx context.Context) error {
	if err := advanceForTest(a, ctx); err != nil {
		return err
	}
	return a.Recover(ctx)
}
func assertSettlementIntents(t *testing.T, a *App) {
	t.Helper()
	var jobs, missing int
	err := a.db.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM order_refunds)+(SELECT count(*) FROM order_payouts),
 (SELECT count(*) FROM orders o WHERE status IN ('cancelled','expired','not completed','completed')
 AND EXISTS(SELECT 1 FROM order_events e WHERE e.order_id=o.id AND e.order_version=o.version AND e.event_type='OrderStatusChanged') AND NOT EXISTS
 (SELECT 1 FROM order_events e WHERE e.order_id=o.id AND e.order_version=o.version AND e.domain_type IN ('OrderCancelled','OrderExpired','OrderNotCompleted','OrderCompleted')))`).Scan(&jobs, &missing)
	if err != nil || jobs != 0 || missing != 0 {
		t.Fatal("terminal intent must commit before job materialization", jobs, missing, err)
	}
}
