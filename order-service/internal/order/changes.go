package order

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func supplierCancellable(status string) bool {
	return status == "open" || status == "accepted" || status == "picked up"
}

// Resolve only with the CURRENT authenticated request's cookie. Never persist
// credentials for a worker. The row lock serializes refresh with transitions;
// a slower catalogue response cannot overwrite a newer order revision.
func (a *App) refreshPlaces(ctx context.Context, o Order, session string) (Order, error) {
	for _, target := range []*Place{&o.Pickup, &o.Delivery} {
		fresh, err := a.catalogue.Resolve(ctx, PlaceRef{Type: target.Type, ID: target.ID}, session)
		if err != nil {
			var api *apiError
			if errors.As(err, &api) && api.code == "invalid_place" {
				if supplierCancellable(o.Status) {
					o.Status = "cancelled"
					o.CancellationReason = "A selected place was deactivated or deleted. Credit refund is pending."
					return o, nil
				}
				// Historical delivered/disputed/terminal orders cannot be
				// cancelled by this path; retain their last known details.
				continue
			}
			return o, err
		}
		*target = fresh
	}
	return o, nil
}

func saveChange(ctx context.Context, tx pgx.Tx, before, after Order) (Order, error) {
	if err := checkChange(before, after); err != nil {
		return Order{}, err
	}
	if before.Status == after.Status && before.Pickup == after.Pickup && before.Delivery == after.Delivery && sameCourier(before.CourierID, after.CourierID) && sameTime(before.CompletionRequestedAt, after.CompletionRequestedAt) {
		return before, nil
	}
	pickup, _ := json.Marshal(after.Pickup)
	delivery, _ := json.Marshal(after.Delivery)
	updated, err := scanOrder(tx.QueryRow(ctx, `UPDATE orders SET pickup=$2,delivery=$3,status=$4,courier_id=$5,
        version=version+1,cancellation_reason=$6,
 delivered_at=CASE WHEN $4='delivered' AND status<>'delivered' THEN clock_timestamp() ELSE delivered_at END,
 completion_requested_at=$7 WHERE id=$1 RETURNING `+orderColumns,
		after.ID, pickup, delivery, after.Status, after.CourierID, after.CancellationReason, after.CompletionRequestedAt))
	if err != nil {
		return Order{}, err
	}
	kind := "OrderDetailsChanged"
	if before.Status != updated.Status {
		kind = "OrderStatusChanged"
	}
	if err = recordDomainEvent(ctx, tx, &before, updated, kind); err != nil {
		return Order{}, err
	}
	return updated, nil
}

func sameCourier(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func lockedVisible(ctx context.Context, tx pgx.Tx, id, user string) (Order, error) {
	o, err := scanOrder(tx.QueryRow(ctx, "SELECT "+orderColumns+" FROM orders WHERE id=$1 FOR UPDATE", id))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !visible(o, user)) {
		return Order{}, problem(404, "order_not_found", "This errand is not available.")
	}
	return o, err
}

func (a *App) refreshOrder(w http.ResponseWriter, r *http.Request) error {
	var body struct{}
	if err := readJSON(w, r, &body); err != nil {
		return err
	}
	if slices.Contains(r.Context().Value(identityKey{}).(Identity).Roles, "admin") {
		// Admins may inspect disputes; this is not a general state override.
		o, err := a.findVisible(r)
		if err != nil {
			return err
		}
		if o.Status == "dispute" {
			writeOrder(w, 200, o)
			return nil
		}
	}
	observed, err := a.findVisible(r)
	if err != nil {
		return err
	}
	if entry, ok := r.Context().Value(admissionKey{}).(admission); ok {
		observed.Status = entry.observedStatus
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if allowed, err := a.guardAdmission(r.Context(), tx, r.PathValue("id")); err != nil {
		return err
	} else if !allowed {
		return updatingOrder()
	}
	before, err := lockedVisible(r.Context(), tx, r.PathValue("id"), actor(r))
	if err != nil {
		return err
	}
	cookie, _ := r.Cookie(a.cfg.SessionCookie)
	after, err := a.refreshPlaces(r.Context(), before, cookie.Value)
	if err != nil {
		return err
	}
	if observed.Status == "accepted" && before.Status == "picked up" && after.Status == "cancelled" {
		after.Status = before.Status
		after.CancellationReason = before.CancellationReason
	}
	after, err = saveChange(r.Context(), tx, before, after)
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeOrder(w, 200, after)
	return nil
}

// A narrow courier path makes the delivered capacity decision executable.
// Requester and administrator branches use the same transactional change guard.
// Retrying the same action on the same order is safe, even after a newer job.
func (a *App) transition(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Action string `json:"action"`
	}
	if err := readJSON(w, r, &body); err != nil {
		return err
	}
	switch body.Action {
	case "cancel", "confirm", "dispute", "resolve_completed", "resolve_not_completed":
		return a.settleAction(w, r, body.Action)
	}
	from, to := "", ""
	switch body.Action {
	case "accept":
		from, to = "open", "accepted"
	case "pick_up":
		from, to = "accepted", "picked up"
	case "deliver":
		from, to = "picked up", "delivered"
	default:
		return problem(400, "invalid_action", "Choose a documented errand action.")
	}
	if body.Action == "accept" {
		// Expired older assignments cannot occupy this courier's capacity.
		// Leave this command's target to its receipt-time check below.
		if err := a.expireUncollected(r.Context(), r.PathValue("id")); err != nil {
			return err
		}
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if allowed, err := a.guardAdmission(r.Context(), tx, r.PathValue("id")); err != nil {
		return err
	} else if !allowed {
		return updatingOrder()
	}
	before, err := lockedVisible(r.Context(), tx, r.PathValue("id"), actor(r))
	if err != nil {
		return err
	}
	user := actor(r)
	if before.RequesterID == user || (body.Action != "accept" && (before.CourierID == nil || *before.CourierID != user)) {
		return problem(403, "action_forbidden", "Only the assigned courier can perform this action; requesters cannot courier their own errands.")
	}
	recorded, err := actionRecorded(r.Context(), tx, before.ID, body.Action, user)
	if err != nil {
		return err
	}
	// Legacy successful actions can be inferred only in states proving they
	// occurred. New history also proves pickup before a later expiry/cancel.
	reached := before.Status == to
	if body.Action == "accept" {
		reached = before.CourierID != nil && *before.CourierID == user
	}
	if body.Action == "pick_up" && (before.Status == "delivered" || before.Status == "dispute" || before.Status == "completed" || before.Status == "not completed") {
		reached = true
	}
	if body.Action == "deliver" && (before.Status == "dispute" || before.Status == "completed" || before.Status == "not completed") {
		reached = true
	}
	if reached || recorded {
		writeOrder(w, 200, before)
		return nil
	}
	if before.Status != from {
		return problem(409, "status_conflict", "This errand has changed. Refresh its confirmed status.")
	}
	if !before.ExpiresAt.After(r.Context().Value(receivedKey{}).(time.Time)) {
		expired := before
		expired.Status = "expired"
		if _, err = saveChange(r.Context(), tx, before, expired); err != nil {
			return err
		}
		if err = tx.Commit(r.Context()); err != nil {
			return err
		}
		return problem(409, "order_expired", "This errand has reached its expiry and cannot be progressed.")
	}
	cookie, _ := r.Cookie(a.cfg.SessionCookie)
	after, err := a.refreshPlaces(r.Context(), before, cookie.Value)
	if err != nil {
		return err
	}
	// No earlier cancellation is final under our row lock: pickup takes
	// precedence over deactivation discovered by this competing lookup.
	if body.Action == "pick_up" && after.Status == "cancelled" {
		after.Status = before.Status
		after.CancellationReason = before.CancellationReason
	}
	if after.Status != "cancelled" {
		after.Status = to
		after.CourierID = &user
	}
	after, err = saveChange(r.Context(), tx, before, after)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.ConstraintName == "orders_one_courier_slot" {
			return problem(409, "courier_busy", "Finish delivering your current errand before accepting another.")
		}
		return err
	}
	if after.Status == to {
		if err = recordAction(r.Context(), tx, after, body.Action, user, r.Context().Value(receivedKey{}).(time.Time)); err != nil {
			return err
		}
	}
	if (after.Status == "accepted" || after.Status == "picked up") && !after.ExpiresAt.After(a.now()) {
		expired := after
		expired.Status = "expired"
		after, err = saveChange(r.Context(), tx, after, expired)
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeOrder(w, 200, after)
	return nil
}

func (a *App) refundCancelled(ctx context.Context) error {
	rows, err := a.db.Query(ctx, "SELECT order_id FROM order_refunds ORDER BY last_attempt_at,created_at LIMIT 100")
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
		// Rotate attempted work before remote I/O. A repeatedly failing escrow
		// must not consume every worker deadline and starve newer refunds.
		if _, err = a.db.Exec(ctx, "UPDATE order_refunds SET last_attempt_at=clock_timestamp(),attempts=attempts+1 WHERE order_id=$1", id); err != nil {
			return errors.Join(failures, err)
		}
		// Duplicate worker delivery is safe under Credit's release contract.
		if err = a.credit.Release(ctx, id); err != nil {
			_, diagnostic := a.db.Exec(ctx, "UPDATE order_refunds SET error_category=$2 WHERE order_id=$1", id, financialCategory(err))
			failures = errors.Join(failures, err, diagnostic)
			continue
		}
		_, err = a.db.Exec(ctx, "DELETE FROM order_refunds WHERE order_id=$1", id)
		failures = errors.Join(failures, err)
	}
	return failures
}

// Pre-delivery deadlines free capacity and durably queue refunds, including
// after downtime. Skip active transitions rather than overtaking their row lock.
func (a *App) expireUncollected(ctx context.Context, exclude string) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	rows, err := tx.Query(ctx, "SELECT "+orderColumns+" FROM orders WHERE status IN ('open','accepted','picked up') AND expires_at<=$1 AND id<>$2 ORDER BY id FOR UPDATE SKIP LOCKED", a.now(), exclude)
	if err != nil {
		return err
	}
	var due []Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			rows.Close()
			return err
		}
		due = append(due, o)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, before := range due {
		if allowed, err := a.guardAdmission(ctx, tx, before.ID); err != nil {
			return err
		} else if !allowed {
			continue
		}
		after := before
		after.Status = "expired"
		if _, err = saveChange(ctx, tx, before, after); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
