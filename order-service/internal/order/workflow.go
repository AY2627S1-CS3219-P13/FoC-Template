package order

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	timerConsumer       = "workflow-timers"
	settlementConsumer  = "workflow-settlement"
	publicationConsumer = "workflow-publication"
)

// ConsumeWorkflow has no HTTP dependencies. Each order/consumer has its own
// transaction lock, receipt and retry record; another consumer cannot ack it.
// Missing-receipt discovery is commit-safe even when older inserts commit late.
func (a *App) ConsumeWorkflow(ctx context.Context, consumer string) error {
	switch consumer {
	case timerConsumer, settlementConsumer, publicationConsumer:
	default:
		return errors.New("unknown workflow consumer")
	}
	var failures error
	for budget := 100; budget > 0; {
		rows, err := a.db.Query(ctx, `SELECT e.order_id FROM order_events e
 WHERE e.domain_type IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM workflow_receipts r WHERE r.consumer_name=$1 AND r.event_id=e.event_id)
 AND NOT EXISTS(SELECT 1 FROM order_events older WHERE older.order_id=e.order_id AND older.domain_type IS NOT NULL AND older.order_version<e.order_version
   AND NOT EXISTS(SELECT 1 FROM workflow_receipts r WHERE r.consumer_name=$1 AND r.event_id=older.event_id))
 AND NOT EXISTS(SELECT 1 FROM workflow_retries r WHERE r.consumer_name=$1 AND r.event_id=e.event_id AND r.next_attempt_at>clock_timestamp())
 ORDER BY e.created_at,e.event_id LIMIT $2`, consumer, budget)
		if err != nil {
			return errors.Join(failures, err)
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err != nil || rows.Err() != nil {
			return errors.Join(failures, err, rows.Err())
		}
		if len(ids) == 0 {
			break
		}
		progressed := false
		for _, id := range ids {
			done, err := a.consumeOrder(ctx, consumer, id)
			failures = errors.Join(failures, err)
			progressed = progressed || done
			budget--
		}
		if !progressed {
			break
		} // all selected orders are busy on another replica
	}
	return failures
}

func (a *App) consumeOrder(ctx context.Context, consumer, id string) (bool, error) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	var held bool
	if err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))", consumer+":"+id).Scan(&held); err != nil || !held {
		return false, err
	}
	// Foreign-key checks on newly materialized effects also touch the order.
	// Skip a busy command before those checks rather than waiting behind it.
	var unlocked string
	if err = tx.QueryRow(ctx, "SELECT id FROM orders WHERE id=$1 FOR KEY SHARE SKIP LOCKED", id).Scan(&unlocked); errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var e domainEvent
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT e.event_id,e.order_id,e.order_version,e.domain_type,e.domain_schema_version,e.domain_payload,e.created_at
 FROM order_events e WHERE e.order_id=$2 AND e.domain_type IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM workflow_receipts r WHERE r.consumer_name=$1 AND r.event_id=e.event_id)
 ORDER BY e.order_version LIMIT 1`, consumer, id).Scan(&e.ID, &e.OrderID, &e.Version, &e.Type, &e.Schema, &raw, &e.OccurredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var eligible bool
	if err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM workflow_retries WHERE consumer_name=$1 AND event_id=$2 AND next_attempt_at>clock_timestamp())`, consumer, e.ID).Scan(&eligible); err != nil || !eligible {
		return false, err
	}
	// Savepoint lets a failed SQL effect roll back with its receipt, then retain
	// sanitized retry metadata under the same order/consumer lock.
	effect, err := tx.Begin(ctx)
	if err != nil {
		return false, err
	}
	err = decodeDomain(raw, &e)
	if err == nil {
		switch consumer {
		case timerConsumer:
			err = projectTimers(ctx, effect, e.OrderID, e.Version, e.ID, e.Data, false)
		case settlementConsumer:
			err = settleEvent(ctx, effect, e)
		case publicationConsumer:
			err = publishEvent(ctx, effect, e.ID)
		}
	}
	if err == nil {
		_, err = effect.Exec(ctx, "INSERT INTO workflow_receipts(consumer_name,event_id) VALUES($1,$2)", consumer, e.ID)
	}
	if err == nil {
		_, err = effect.Exec(ctx, "DELETE FROM workflow_retries WHERE consumer_name=$1 AND event_id=$2", consumer, e.ID)
	}
	if err != nil {
		rollback(effect)
		category := "effect_failed"
		if errors.Is(err, errDomain) {
			category = "invalid_domain_event"
		}
		_, saveErr := tx.Exec(ctx, `INSERT INTO workflow_retries(consumer_name,event_id,attempts,next_attempt_at,error_category)
 VALUES($1,$2,1,clock_timestamp()+interval '1 second',$3)
 ON CONFLICT(consumer_name,event_id) DO UPDATE SET attempts=workflow_retries.attempts+1,
 next_attempt_at=clock_timestamp()+LEAST(60,power(2,LEAST(workflow_retries.attempts,6))) * interval '1 second',error_category=$3`, consumer, e.ID, category)
		if saveErr != nil {
			return false, saveErr
		}
		if saveErr = tx.Commit(ctx); saveErr != nil {
			return false, saveErr
		}
		return true, errors.New(category)
	}
	if err = effect.Commit(ctx); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func settleEvent(ctx context.Context, tx pgx.Tx, e domainEvent) error {
	switch e.Type {
	case "OrderCompleted":
		// A stale timer projection must never suppress a required financial effect.
		return insertSettlement(ctx, tx, e.OrderID, e.Data.Status, e.Data.CourierID)
	case "OrderCancelled", "OrderExpired", "OrderNotCompleted":
		return insertSettlement(ctx, tx, e.OrderID, e.Data.Status, e.Data.CourierID)
	}
	return nil
}

func insertSettlement(ctx context.Context, tx pgx.Tx, id, status string, courier *string) error {
	if status == "completed" {
		if courier == nil || !validUser(*courier) {
			return errDomain
		}
		_, err := tx.Exec(ctx, `INSERT INTO order_payouts(order_id,courier_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, courier)
		if err != nil {
			return err
		}
		var same bool
		if err = tx.QueryRow(ctx, "SELECT courier_id=$2::uuid FROM order_payouts WHERE order_id=$1", id, courier).Scan(&same); err != nil {
			return err
		}
		if !same {
			return errDomain
		}
	} else if status == "cancelled" || status == "expired" || status == "not completed" {
		_, err := tx.Exec(ctx, "INSERT INTO order_refunds(order_id) VALUES($1) ON CONFLICT DO NOTHING", id)
		return err
	}
	return nil
}

func completionDue(d domainData) (time.Time, error) {
	if d.DeliveredAt == nil || d.DeliveredAt.IsZero() {
		return time.Time{}, errDomain
	}
	due := d.DeliveredAt.Add(5 * time.Minute)
	if d.CompletionRequestedAt != nil && d.CompletionRequestedAt.Before(due) {
		due = *d.CompletionRequestedAt
	}
	return due.Add(completionArbitration), nil
}

func projectTimers(ctx context.Context, tx pgx.Tx, id string, version int64, event string, d domainData, bootstrap bool) error {
	var boot *int64
	if bootstrap {
		boot = &version
	}
	tag, err := tx.Exec(ctx, `INSERT INTO workflow_projections(order_id,order_version,bootstrap_version) VALUES($1,$2,$3)
 ON CONFLICT(order_id) DO UPDATE SET order_version=$2 WHERE workflow_projections.order_version<$2`, id, version, boot)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM workflow_timers WHERE order_id=$1", id); err != nil {
		return err
	}
	kind := ""
	due := d.ExpiresAt
	switch d.Status {
	case "open", "accepted", "picked up":
		kind = "expiry"
	case "delivered":
		kind = "completion"
		due, err = completionDue(d)
		if err != nil {
			return err
		}
	}
	if kind == "" {
		return nil
	}
	var source *string
	if event != "" {
		source = &event
	}
	_, err = tx.Exec(ctx, `INSERT INTO workflow_timers(order_id,timer_kind,due_at,source_event_id,source_order_version) VALUES($1,$2,$3,$4,$5)`, id, kind, due, source, version)
	return err
}

// BootstrapWorkflow repairs only legacy snapshots. New writers carry their own
// durable intent and must pass through consumers, even if their queues lag.
// One transaction per order makes crash/repeat execution resumable. Old writers
// must be stopped before cutover. No legacy announcements or payloads are invented.
func (a *App) BootstrapWorkflow(ctx context.Context) error {
	for {
		rows, err := a.db.Query(ctx, `SELECT id FROM orders o WHERE NOT EXISTS(SELECT 1 FROM workflow_projections p WHERE p.order_id=o.id)
 AND NOT EXISTS(SELECT 1 FROM order_events e WHERE e.order_id=o.id AND e.domain_type IS NOT NULL) ORDER BY id LIMIT 100`)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err != nil || rows.Err() != nil {
			return errors.Join(err, rows.Err())
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			if err = a.bootstrapOrder(ctx, id); err != nil {
				a.log.Warn("legacy workflow bootstrap incomplete", "orderId", id)
				return err
			}
		}
	}
}
func (a *App) bootstrapOrder(ctx context.Context, id string) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	o, err := scanOrder(tx.QueryRow(ctx, "SELECT "+orderColumns+" FROM orders WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		return err
	}
	// Recheck after the snapshot lock; a racing new event wins.
	var initialized bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_projections WHERE order_id=$1) OR EXISTS(SELECT 1 FROM order_events WHERE order_id=$1 AND domain_type IS NOT NULL)`, id).Scan(&initialized); err != nil || initialized {
		return err
	}
	d := domainData{Status: o.Status, ExpiresAt: o.ExpiresAt, DeliveredAt: o.DeliveredAt, CompletionRequestedAt: o.CompletionRequestedAt}
	if err = projectTimers(ctx, tx, id, o.Version, "", d, true); err != nil {
		return err
	}
	if err = insertSettlement(ctx, tx, id, o.Status, o.CourierID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
