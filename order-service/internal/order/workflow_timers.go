package order

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type dueTimer struct {
	id, kind string
	version  int64
}

// Advance executes durable schedules only. It never scans orders to synthesize
// everyday lifecycle work. Commands still enforce receipt-time expiry directly.
func (a *App) Advance(ctx context.Context) error {
	if _, err := a.db.Exec(ctx, "DELETE FROM order_admissions WHERE valid_until<=clock_timestamp()"); err != nil {
		return err
	}
	rows, err := a.db.Query(ctx, `SELECT order_id,timer_kind,source_order_version FROM workflow_timers t
 WHERE due_at<=$1 AND next_attempt_at<=clock_timestamp()
 AND NOT EXISTS(SELECT 1 FROM order_admissions a WHERE a.order_id=t.order_id AND a.valid_until>clock_timestamp())
 ORDER BY next_attempt_at,due_at,order_id LIMIT 100`, a.now())
	if err != nil {
		return err
	}
	var due []dueTimer
	for rows.Next() {
		var t dueTimer
		if err = rows.Scan(&t.id, &t.kind, &t.version); err != nil {
			break
		}
		due = append(due, t)
	}
	rows.Close()
	if err != nil || rows.Err() != nil {
		return errors.Join(err, rows.Err())
	}
	var failures error
	for _, t := range due {
		if err = a.executeTimer(ctx, t); err != nil {
			// Stable generation check prevents a failed/stale worker delaying new work.
			_, saveErr := a.db.Exec(ctx, `UPDATE workflow_timers SET attempts=attempts+1,error_category='timer_failed',
 next_attempt_at=clock_timestamp()+LEAST(60,power(2,LEAST(attempts,6))) * interval '1 second'
 WHERE order_id=$1 AND timer_kind=$2 AND source_order_version=$3`, t.id, t.kind, t.version)
			failures = errors.Join(failures, err, saveErr)
		}
	}
	return failures
}

func (a *App) executeTimer(ctx context.Context, t dueTimer) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	// Lock the authoritative order first, using SKIP LOCKED to leave admitted or
	// busy work retryable and let unrelated timers progress on this replica.
	before, err := scanOrder(tx.QueryRow(ctx, "SELECT "+orderColumns+" FROM orders WHERE id=$1 FOR UPDATE SKIP LOCKED", t.id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var due time.Time
	err = tx.QueryRow(ctx, `SELECT due_at FROM workflow_timers WHERE order_id=$1 AND timer_kind=$2 AND source_order_version=$3
 AND due_at<=$4 AND next_attempt_at<=clock_timestamp() FOR UPDATE SKIP LOCKED`, t.id, t.kind, t.version, a.now()).Scan(&due)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	after := before
	if t.kind == "expiry" {
		if supplierCancellable(before.Status) && !before.ExpiresAt.After(a.now()) {
			allowed, err := a.guardAdmission(ctx, tx, t.id)
			if err != nil || !allowed {
				return err
			}
			after.Status = "expired"
		}
	} else if before.Status == "delivered" {
		actual, err := completionDue(domainData{DeliveredAt: before.DeliveredAt, CompletionRequestedAt: before.CompletionRequestedAt})
		if err != nil {
			return err
		}
		if actual.After(a.now()) {
			return nil
		}
		var pending bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM order_admissions WHERE order_id=$1 AND valid_until>clock_timestamp())", t.id).Scan(&pending); err != nil || pending {
			return err
		}
		after.Status = "completed"
		after.CompletionRequestedAt = nil
	}
	if before.Status != after.Status {
		// Completion leaves admission open through speculative status/event writes.
		if after, err = saveChange(ctx, tx, before, after); err != nil {
			return err
		}
	}
	if before.Status != after.Status {
		// A timer transition is terminal. Retain its committed version even
		// before its event is consumed, so older delayed projections cannot
		// recreate work removed by this executor.
		if _, err = tx.Exec(ctx, "UPDATE workflow_projections SET order_version=GREATEST(order_version,$2) WHERE order_id=$1", t.id, after.Version); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "DELETE FROM workflow_timers WHERE order_id=$1 AND timer_kind=$2 AND source_order_version=$3", t.id, t.kind, t.version); err != nil {
		return err
	}
	// The final gate fences every speculative write and acknowledgement.
	// A veto rolls all of them back and retains the same retryable schedule.
	if allowed, err := a.guardAdmission(ctx, tx, t.id); err != nil || !allowed {
		return err
	}
	return tx.Commit(ctx)
}
