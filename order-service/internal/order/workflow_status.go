package order

import (
	"context"
	"errors"
	"time"
)

type consumerStatus struct {
	Consumer string     `json:"consumer"`
	Pending  int64      `json:"pending"`
	Failed   int64      `json:"failed"`
	Oldest   *time.Time `json:"oldestPendingAt"`
}

func (a *App) workflowStatus(ctx context.Context) (map[string]any, error) {
	consumers := []consumerStatus{}
	for _, name := range []string{timerConsumer, settlementConsumer, publicationConsumer} {
		s := consumerStatus{Consumer: name}
		err := a.db.QueryRow(ctx, `SELECT count(*),count(r.event_id),min(e.created_at) FROM order_events e
 LEFT JOIN workflow_retries r ON r.consumer_name=$1 AND r.event_id=e.event_id
 WHERE e.domain_type IS NOT NULL AND NOT EXISTS(SELECT 1 FROM workflow_receipts p WHERE p.consumer_name=$1 AND p.event_id=e.event_id)`, name).Scan(&s.Pending, &s.Failed, &s.Oldest)
		if err != nil {
			return nil, err
		}
		consumers = append(consumers, s)
	}
	var timers, due, failedTimers, refunds, payouts, failedRefunds, failedPayouts, legacy int64
	err := a.db.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE due_at<=$1),count(*) FILTER(WHERE error_category IS NOT NULL) FROM workflow_timers`, a.now()).Scan(&timers, &due, &failedTimers)
	if err != nil {
		return nil, err
	}
	err = a.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM order_refunds),(SELECT count(*) FROM order_payouts),
 (SELECT count(*) FROM order_refunds WHERE error_category IS NOT NULL),(SELECT count(*) FROM order_payouts WHERE error_category IS NOT NULL),
 (SELECT count(*) FROM orders o WHERE NOT EXISTS(SELECT 1 FROM workflow_projections p WHERE p.order_id=o.id)
 AND NOT EXISTS(SELECT 1 FROM order_events e WHERE e.order_id=o.id AND e.domain_type IS NOT NULL))`).Scan(&refunds, &payouts, &failedRefunds, &failedPayouts, &legacy)
	if err != nil {
		return nil, err
	}
	return map[string]any{"consumers": consumers, "timers": timers, "dueTimers": due, "overdueTimers": due, "failedTimers": failedTimers, "pendingRefunds": refunds, "pendingPayouts": payouts, "failedRefunds": failedRefunds, "failedPayouts": failedPayouts, "legacyUninitialized": legacy}, nil
}

func financialCategory(err error) string {
	var api *apiError
	if errors.As(err, &api) && api.status == 409 {
		return "credit_conflict"
	}
	return "credit_unavailable"
}
