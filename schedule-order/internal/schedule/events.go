package schedule

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Importer prepares dependencies outside SQL transactions and returns a database-only effect.
// Only Order implements it; scheduling never writes Order's tables itself.
type Effect func(context.Context, pgx.Tx) (string, error)
type Importer func(context.Context, string, []byte) (Effect, error)

func (a *App) SetImporter(importer Importer) { a.importer = importer }

// ConsumeEvents discovers missing receipts rather than relying on a sequence watermark.
// Schedule's advisory lock fences browser cancellation, recovery and other replicas.
func (a *App) ConsumeEvents(ctx context.Context) error {
	rows, err := a.db.Query(ctx, `SELECT e.event_id,e.schedule_id FROM schedule_events e
 WHERE NOT EXISTS(SELECT 1 FROM schedule_receipts r WHERE r.event_id=e.event_id)
 AND NOT EXISTS(SELECT 1 FROM schedule_retries r WHERE r.event_id=e.event_id AND r.next_attempt_at>clock_timestamp())
 ORDER BY e.created_at,e.event_id LIMIT 100`)
	if err != nil {
		return err
	}
	type work struct{ event, id string }
	var pending []work
	for rows.Next() {
		var w work
		if err = rows.Scan(&w.event, &w.id); err != nil {
			break
		}
		pending = append(pending, w)
	}
	rows.Close()
	if err != nil {
		return err
	}
	if rows.Err() != nil {
		return rows.Err()
	}
	var failures error
	for _, w := range pending {
		failures = errors.Join(failures, a.consumeEvent(ctx, w.event, w.id))
	}
	return failures
}
func (a *App) consumeEvent(ctx context.Context, event, id string) error {
	if a.importer == nil {
		return errors.New("schedule importer unavailable")
	}
	c, unlock, err := a.lock(ctx, id, true)
	if err != nil {
		return nil
	}
	defer unlock()
	var deferred bool
	if err = c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schedule_receipts WHERE event_id=$1) OR EXISTS(SELECT 1 FROM schedule_retries WHERE event_id=$1 AND next_attempt_at>clock_timestamp())`, event).Scan(&deferred); err != nil || deferred {
		return err
	}
	s, err := load(ctx, c, id)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]any{"id": s.id, "requesterId": s.user, "courierId": s.courier, "pickup": s.request.Pickup, "delivery": s.request.Delivery, "items": s.request.Items, "offeredCredits": s.request.OfferedCredits, "windowStart": s.request.WindowStart, "expiresAt": s.order.ExpiresAt})
	if err != nil {
		return err
	}
	apply, prepareErr := a.importer(ctx, event, raw)
	tx, err := c.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var done bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schedule_receipts WHERE event_id=$1)", event).Scan(&done); err != nil || done {
		return err
	}
	if s.state != "transfer_pending" || s.transfer != event {
		return errors.New("invalid schedule event state")
	}
	effect, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	var outcome string
	err = prepareErr
	if err == nil {
		outcome, err = apply(ctx, effect)
	}
	if err == nil && outcome != "accepted" && outcome != "rejected" {
		err = errors.New("invalid import outcome")
	}
	if err == nil {
		if outcome == "accepted" {
			_, err = effect.Exec(ctx, "UPDATE schedules SET state='transferred',refund_pending=false,updated_at=clock_timestamp() WHERE id=$1", id)
		} else {
			_, err = effect.Exec(ctx, "UPDATE schedules SET state='scheduled',courier_id=NULL,transfer_id=NULL,updated_at=clock_timestamp() WHERE id=$1", id)
		}
	}
	if err == nil {
		_, err = effect.Exec(ctx, "INSERT INTO schedule_receipts(consumer_name,event_id,outcome) VALUES('workflow-schedule-acceptance',$1,$2)", event, outcome)
	}
	if err == nil {
		_, err = effect.Exec(ctx, "DELETE FROM schedule_retries WHERE event_id=$1", event)
	}
	if err != nil {
		_ = effect.Rollback(ctx)
		if _, saveErr := tx.Exec(ctx, `INSERT INTO schedule_retries(event_id,attempts,next_attempt_at,error_category) VALUES($1,1,clock_timestamp()+interval '1 second','import_failed')
 ON CONFLICT(event_id) DO UPDATE SET attempts=schedule_retries.attempts+1,next_attempt_at=clock_timestamp()+LEAST(60,power(2,LEAST(schedule_retries.attempts,6)))*interval '1 second'`, event); saveErr != nil {
			return saveErr
		}
		if saveErr := tx.Commit(ctx); saveErr != nil {
			return saveErr
		}
		return errors.New("schedule import pending")
	}
	if err = effect.Commit(ctx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
