package schedule

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ImportLegacy is an offline cutover operation. Both application writers must be stopped.
// It preserves all identities and pending financial/acceptance state, without HTTP effects.
func ImportLegacy(ctx context.Context, target *pgxpool.Pool, sourceURL string) error {
	source, err := pgx.Connect(ctx, sourceURL)
	if err != nil {
		return errors.New("legacy schedule database unavailable")
	}
	defer source.Close(ctx)
	read, err := source.Begin(ctx)
	if err != nil {
		return err
	}
	defer read.Rollback(ctx)
	// Freeze source mutations for the complete copy, including late legacy workers.
	if _, err = read.Exec(ctx, "LOCK TABLE schedules IN SHARE MODE"); err != nil {
		return err
	}
	tx, err := target.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "LOCK TABLE schedules IN EXCLUSIVE MODE"); err != nil {
		return err
	}
	var populated bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schedules)").Scan(&populated); err != nil {
		return err
	}
	if populated {
		return errors.New("target schedules must be empty; import refused")
	}
	rows, err := read.Query(ctx, `SELECT id,requester_id,idempotency_key,fingerprint,request::text,order_data::text,state,courier_id,transfer_id,generation,refund_pending,updated_at FROM schedules ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schedules(id,requester_id,idempotency_key,fingerprint,request,order_data,state,courier_id,transfer_id,generation,refund_pending,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, values...); err != nil {
			return err
		}
	}
	if rows.Err() != nil {
		return rows.Err()
	}
	if _, err = tx.Exec(ctx, `INSERT INTO schedule_events(event_id,schedule_id,event_type) SELECT transfer_id,id,'ScheduledOrderAcceptanceRequested' FROM schedules WHERE state='transfer_pending'`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
