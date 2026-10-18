package order

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// Completion is first an intent, then a committed fact. A one-second arbitration
// interval admits overlapping disputes before finalization or financial I/O.
// A final completed decision is never reopened by a later dispute.
const completionArbitration = time.Second

func (a *App) settleAction(w http.ResponseWriter, r *http.Request, action string) error {
	ctx := r.Context()
	identity := ctx.Value(identityKey{}).(Identity)
	admin := slices.Contains(identity.Roles, "admin")
	resolving := action == "resolve_completed" || action == "resolve_not_completed"
	if resolving && !admin {
		return problem(403, "admin_required", "Only an administrator can resolve a dispute.")
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if allowed, err := a.guardAdmission(ctx, tx, r.PathValue("id")); err != nil {
		return err
	} else if !allowed {
		return updatingOrder()
	}
	before, err := scanOrder(tx.QueryRow(ctx, "SELECT "+orderColumns+" FROM orders WHERE id=$1 FOR UPDATE", r.PathValue("id")))
	if errors.Is(err, pgx.ErrNoRows) {
		return problem(404, "order_not_found", "This errand is not available.")
	}
	if err != nil {
		return err
	}
	if !resolving && identity.UserID != before.RequesterID {
		return problem(403, "action_forbidden", "Only the requester can perform this action.")
	}
	recorded, err := actionRecorded(ctx, tx, before.ID, action, identity.UserID)
	if err != nil {
		return err
	}
	if recorded {
		if action == "confirm" && (before.Status == "dispute" || before.Status == "not completed") {
			return problem(409, "status_conflict", "The dispute must be resolved by an administrator.")
		}
		status := 200
		if action == "confirm" && before.Status == "delivered" {
			status = 202
		}
		writeOrder(w, status, before)
		return nil
	}
	after := before
	switch action {
	case "cancel":
		if before.Status != "open" && before.Status != "accepted" {
			return statusConflict()
		}
		after.Status = "cancelled"
		after.CancellationReason = "Cancelled by the requester. Credit refund is pending."
	case "confirm":
		if before.Status != "delivered" {
			return statusConflict()
		}
		now := a.now()
		after.CompletionRequestedAt = &now
	case "dispute":
		if before.Status != "delivered" {
			return statusConflict()
		}
		after.Status = "dispute"
		after.CompletionRequestedAt = nil
	case "resolve_completed", "resolve_not_completed":
		if before.Status != "dispute" {
			return statusConflict()
		}
		after.Status = "completed"
		if action == "resolve_not_completed" {
			after.Status = "not completed"
		}
		after.CompletionRequestedAt = nil
	}
	after, err = saveChange(ctx, tx, before, after)
	if err != nil {
		return err
	}
	if err = recordAction(ctx, tx, after, action, identity.UserID, ctx.Value(receivedKey{}).(time.Time)); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	status := 200
	if action == "confirm" {
		status = 202
	}
	writeOrder(w, status, after)
	return nil
}

func statusConflict() error {
	return problem(409, "status_conflict", "This action is not allowed in the errand's confirmed state.")
}

func (a *App) payoutCompleted(ctx context.Context) error {
	rows, err := a.db.Query(ctx, "SELECT order_id,courier_id::text FROM order_payouts ORDER BY last_attempt_at,created_at LIMIT 100")
	if err != nil {
		return err
	}
	type payout struct{ id, courier string }
	var jobs []payout
	for rows.Next() {
		var job payout
		if err = rows.Scan(&job.id, &job.courier); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, job)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	var failures error
	for _, job := range jobs {
		if _, err = a.db.Exec(ctx, "UPDATE order_payouts SET last_attempt_at=clock_timestamp(),attempts=attempts+1 WHERE order_id=$1", job.id); err != nil {
			return errors.Join(failures, err)
		}
		if err = a.credit.Payout(ctx, job.id, job.courier); err != nil {
			_, diagnostic := a.db.Exec(ctx, "UPDATE order_payouts SET error_category=$2 WHERE order_id=$1", job.id, financialCategory(err))
			failures = errors.Join(failures, err, diagnostic)
			continue
		}
		_, err = a.db.Exec(ctx, "DELETE FROM order_payouts WHERE order_id=$1", job.id)
		failures = errors.Join(failures, err)
	}
	return failures
}
