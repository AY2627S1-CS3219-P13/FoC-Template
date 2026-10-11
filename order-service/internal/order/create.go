package order

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (a *App) create(w http.ResponseWriter, r *http.Request) error {
	key := r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) != 1 || key == "" || utf8.RuneCountInString(key) > 128 || key != strings.TrimSpace(key) || !validPlainText(key) {
		return problem(400, "invalid_idempotency_key", "Send one Idempotency-Key of 1–128 characters without surrounding whitespace.")
	}
	var body createRequest
	if err := readJSON(w, r, &body); err != nil {
		return err
	}
	legacyFingerprint := requestFingerprint(body)
	// A saved success is retrievable even after its original expiry.
	if err := body.validate(time.Time{}); err != nil {
		return err
	}
	identity := r.Context().Value(identityKey{}).(Identity)
	id := orderID(identity.UserID, key)
	conn, unlock, err := a.lockAttempt(r.Context(), id, false)
	if err != nil {
		return err
	}
	defer unlock()
	_, err = conn.Exec(r.Context(), `INSERT INTO creation_attempts
        (requester_id,idempotency_key,order_id,request_fingerprint,state,offered_credits)
        VALUES($1,$2,$3,$4,'pending',$5) ON CONFLICT(requester_id,idempotency_key) DO NOTHING`,
		identity.UserID, key, id, body.fingerprint(), body.OfferedCredits)
	if err != nil {
		return err
	}
	var fingerprint, state string
	var storedAmount *int64
	if err = conn.QueryRow(r.Context(), "SELECT request_fingerprint,state,offered_credits FROM creation_attempts WHERE order_id=$1", id).Scan(&fingerprint, &state, &storedAmount); err != nil {
		return err
	}
	if fingerprint != body.fingerprint() && fingerprint != legacyFingerprint {
		return problem(409, "idempotency_conflict", "This idempotency key was already used with different creation details.")
	}
	switch state {
	case "completed":
		created, err := scanOrder(conn.QueryRow(r.Context(), "SELECT "+orderColumns+" FROM orders WHERE id=$1", id))
		if err != nil {
			return err
		}
		writeOrder(w, http.StatusOK, created)
		return nil
	case "abandoned":
		return abandoned()
	case "reserving", "release_pending":
		// A previous process lost its reservation/persistence result. Q10b:
		// never finish that attempt, even when the client explicitly retries.
		a.cleanupAttempt(conn, id)
		return cleanupPending()
	}
	if storedAmount == nil {
		// Legacy pending rows did not distinguish before/after reservation.
		// Never interpret their missing intent as proof that no credits moved.
		a.cleanupAttempt(conn, id)
		return cleanupPending()
	}
	if !body.ExpiresAt.After(a.now()) {
		return invalidExpiry()
	}
	cookie, _ := r.Cookie(a.cfg.SessionCookie)
	pickup, err := a.catalogue.Resolve(r.Context(), body.Pickup, cookie.Value)
	if err != nil {
		return err
	}
	delivery, err := a.catalogue.Resolve(r.Context(), body.Delivery, cookie.Value)
	if err != nil {
		return err
	}
	if !body.ExpiresAt.After(a.now()) {
		return invalidExpiry()
	}

	// Commit intent BEFORE sending any reservation. A session advisory lock
	// spans these short transactions and the HTTP calls, using one connection.
	if _, err = conn.Exec(r.Context(), "UPDATE creation_attempts SET state='reserving',updated_at=clock_timestamp() WHERE order_id=$1", id); err != nil {
		return err
	}
	if err = a.credit.Reserve(r.Context(), id, identity.UserID, body.OfferedCredits); err != nil {
		if reservationRejected(err) {
			// A definitive refusal made no reservation. Keep this key retryable.
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = conn.Exec(ctx, "UPDATE creation_attempts SET state='pending',updated_at=clock_timestamp() WHERE order_id=$1", id)
		} else {
			a.cleanupAttempt(conn, id)
		}
		return err
	}
	created, err := saveOrder(r.Context(), conn, id, identity.UserID, body, pickup, delivery)
	if err != nil {
		// Commit errors can be ambiguous. Recovery first checks for a completed
		// local transaction under the same lock and NEVER refunds a saved order.
		a.cleanupAttempt(conn, id)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.ConstraintName == "orders_expiry_after_creation" {
			return invalidExpiry()
		}
		return err
	}
	writeOrder(w, http.StatusCreated, created)
	return nil
}

func saveOrder(ctx context.Context, conn *pgxpool.Conn, id, userID string, body createRequest, pickup, delivery Place) (Order, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return Order{}, err
	}
	defer func() {
		rollback, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = tx.Rollback(rollback)
	}()
	pickupJSON, _ := json.Marshal(pickup)
	deliveryJSON, _ := json.Marshal(delivery)
	itemsJSON, _ := json.Marshal(body.Items)
	created, err := scanOrder(tx.QueryRow(ctx, `INSERT INTO orders
        (id,requester_id,pickup,delivery,items,offered_credits,expires_at)
        VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+orderColumns,
		id, userID, pickupJSON, deliveryJSON, itemsJSON, body.OfferedCredits, body.ExpiresAt))
	if err != nil {
		return Order{}, err
	}
	if _, err = tx.Exec(ctx, "UPDATE creation_attempts SET state='completed',updated_at=clock_timestamp() WHERE order_id=$1", id); err != nil {
		return Order{}, err
	}
	if err = recordEvent(ctx, tx, created, "OrderCreated"); err != nil {
		return Order{}, err
	}
	return created, tx.Commit(ctx)
}

func invalidExpiry() error {
	return problem(400, "invalid_expiry", "Expiry must still be in the future when the errand is created.")
}

func abandoned() error {
	return problem(409, "creation_abandoned", "This creation attempt was abandoned. Start a new attempt with a new idempotency key.")
}

func cleanupPending() error {
	return problem(503, "creation_cleanup_pending", "Creation did not finish. Credit cleanup may still be pending. Retry this same attempt to check its result.")
}

func reservationRejected(err error) bool {
	var api *apiError
	return errors.As(err, &api) && (api.code == "wallet_not_ready" || api.code == "insufficient_credits")
}

func writeOrder(w http.ResponseWriter, status int, value Order) {
	writeJSON(w, status, map[string]any{"order": value})
}

const orderColumns = "id,requester_id,pickup,delivery,items,offered_credits,expires_at,status,created_at,courier_id,version,cancellation_reason,delivered_at,completion_requested_at"

type rowScanner interface{ Scan(...any) error }

func scanOrder(row rowScanner) (Order, error) {
	var value Order
	var pickup, delivery, items []byte
	err := row.Scan(&value.ID, &value.RequesterID, &pickup, &delivery, &items, &value.OfferedCredits, &value.ExpiresAt, &value.Status, &value.CreatedAt, &value.CourierID, &value.Version, &value.CancellationReason, &value.DeliveredAt, &value.CompletionRequestedAt)
	if err != nil {
		return value, err
	}
	if json.Unmarshal(pickup, &value.Pickup) != nil || json.Unmarshal(delivery, &value.Delivery) != nil || json.Unmarshal(items, &value.Items) != nil {
		return Order{}, errors.New("invalid stored order JSON")
	}
	singapore := time.FixedZone("Asia/Singapore", 8*60*60)
	value.ExpiresAt = value.ExpiresAt.In(singapore)
	value.CreatedAt = value.CreatedAt.In(singapore)
	if value.DeliveredAt != nil {
		sgt := value.DeliveredAt.In(singapore)
		value.DeliveredAt = &sgt
	}
	if value.CompletionRequestedAt != nil {
		sgt := value.CompletionRequestedAt.In(singapore)
		value.CompletionRequestedAt = &sgt
	}
	return value, nil
}
