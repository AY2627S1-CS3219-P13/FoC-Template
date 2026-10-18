package order

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Use a session lock so the financial intent can commit before the remote call.
// Both HTTP handlers and recovery acquire it. Never return a locked connection
// to the pool, including after request cancellation or an uncertain SQL result.
func (a *App) lockAttempt(ctx context.Context, id string, try bool) (*pgxpool.Conn, func(), error) {
	conn, err := a.db.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	unlock := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanup, "SELECT pg_advisory_unlock(hashtextextended($1,0))", id); err != nil {
			_ = conn.Conn().Close(cleanup)
		}
		conn.Release()
	}
	if try {
		var acquired bool
		err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,0))", id).Scan(&acquired)
		if err == nil && !acquired {
			conn.Release()
			return nil, nil, nil
		}
	} else {
		_, err = conn.Exec(ctx, "SELECT pg_advisory_lock(hashtextextended($1,0))", id)
	}
	if err != nil {
		unlock()
		return nil, nil, err
	}
	return conn, unlock, nil
}

func (a *App) cleanupAttempt(conn *pgxpool.Conn, id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.reconcile(ctx, conn, id); err != nil {
		a.log.Warn("creation cleanup remains pending", "orderId", id)
		return err
	}
	return nil
}

// Caller holds the attempt lock. The absence of a Credit escrow is NOT proof
// that a timed-out reserve failed: it may still be running. Repeating that
// exact reservation creates/observes its durable idempotency record before
// release; a late original request then cannot reserve again.
func (a *App) reconcile(ctx context.Context, conn *pgxpool.Conn, id string) error {
	var state, userID string
	var amount *int64
	var saved bool
	err := conn.QueryRow(ctx, `SELECT state,requester_id::text,offered_credits,
        EXISTS(SELECT 1 FROM orders WHERE id=$1) FROM creation_attempts WHERE order_id=$1`, id).Scan(&state, &userID, &amount, &saved)
	if err != nil || saved || state == "completed" || state == "abandoned" {
		return err
	}
	if state == "pending" && amount != nil {
		// New-format pending attempts provably have no remote side effects.
		_, err = conn.Exec(ctx, "UPDATE creation_attempts SET state='abandoned',updated_at=clock_timestamp() WHERE order_id=$1", id)
		return err
	}
	if _, err = conn.Exec(ctx, "UPDATE creation_attempts SET state='release_pending',updated_at=clock_timestamp() WHERE order_id=$1", id); err != nil {
		return err
	}
	// First release if the reservation is already visible. This also supports
	// old attempts created before migration 002, which lack the reserve payload.
	err = a.credit.Release(ctx, id)
	if errors.Is(err, errEscrowMissing) && amount != nil {
		err = a.credit.Reserve(ctx, id, userID, *amount)
		if err == nil {
			err = a.credit.Release(ctx, id)
		}
	}
	// A missing legacy escrow cannot be reconciled safely without its payload.
	// Leave it visible for an operator rather than falsely declaring a refund.
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, "UPDATE creation_attempts SET state='abandoned',updated_at=clock_timestamp() WHERE order_id=$1", id)
	return err
}

func (a *App) Recover(ctx context.Context) error {
	if err := a.Advance(ctx); err != nil {
		return err
	}
	// Each queue gets the worker's deadline concurrently. A slow refund or
	// uncertain reservation must not consume the entire payout retry budget.
	jobs := []func(context.Context) error{a.recoverCreations, a.refundCancelled, a.payoutCompleted}
	failures := make([]error, len(jobs))
	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failures[i] = job(ctx)
		}()
	}
	wg.Wait()
	return errors.Join(failures...)
}

func (a *App) recoverCreations(ctx context.Context) error {
	rows, err := a.db.Query(ctx, `SELECT order_id FROM creation_attempts
        WHERE state IN ('pending','reserving','release_pending')
        AND updated_at < clock_timestamp()-($1 * interval '1 second')
        ORDER BY updated_at LIMIT 100`, a.cfg.RecoveryStaleAfter.Seconds())
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	var failures error
	for _, id := range ids {
		conn, unlock, err := a.lockAttempt(ctx, id, true)
		if err != nil {
			return err
		}
		if conn == nil {
			continue
		} // Never wait behind an active creation.
		err = a.reconcile(ctx, conn, id)
		unlock()
		failures = errors.Join(failures, err)
	}
	return failures
}
