package order

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type admissionKey struct{}
type admission struct {
	ticket         int64
	orderID        string
	observedStatus string
}

// Runtime admission uses a separate small pool, so handlers waiting for ordinary
// transaction connections remain visible to workers and other service replicas.
// Tests can use New with their existing pool when not testing pool contention.
func NewWithAdmissionPool(db, admissions *pgxpool.Pool, cfg Config, log *slog.Logger) *App {
	a := New(db, cfg, log)
	a.admissions = admissions
	return a
}

// This lock establishes one database admission/finalization order per errand.
// It is separate from orders' row locks and shared across application replicas.
func admissionGate(ctx context.Context, tx pgx.Tx, id string, try bool) (bool, error) {
	if !try {
		_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('order-admission:'||$1,0))", id)
		return err == nil, err
	}
	var held bool
	err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtextextended('order-admission:'||$1,0))", id).Scan(&held)
	return held, err
}

func (a *App) admit(r *http.Request) (admission, error) {
	id := r.PathValue("id")
	entry := admission{orderID: id}
	if id == "" || utf8.RuneCountInString(id) > 128 || strings.TrimSpace(id) != id {
		return entry, problem(404, "order_not_found", "This errand is not available.")
	}
	// Reject absent/malformed cookies before reserving admission space. Identity
	// and action permissions are still checked through User and the real handler.
	cookie, err := r.Cookie(a.cfg.SessionCookie)
	if err != nil || cookie.Value == "" || len(cookie.Value) > 128 {
		return entry, problem(401, "invalid_session", "Please log in.")
	}
	tx, err := a.admissions.Begin(r.Context())
	if err != nil {
		return entry, err
	}
	defer rollback(tx)
	// An unlocked snapshot preserves pickup priority for a competing refresh.
	if err = tx.QueryRow(r.Context(), "SELECT COALESCE((SELECT status FROM orders WHERE id=$1),'')", id).Scan(&entry.observedStatus); err != nil {
		return entry, err
	}
	if _, err = admissionGate(r.Context(), tx, id, false); err != nil {
		return entry, err
	}
	err = tx.QueryRow(r.Context(), `INSERT INTO order_admissions(order_id,received_at)
 VALUES($1,$2) RETURNING ticket`, id, r.Context().Value(receivedKey{}).(time.Time)).Scan(&entry.ticket)
	if err != nil {
		return entry, err
	}
	return entry, tx.Commit(r.Context())
}

func (a *App) admitted(next endpoint) endpoint {
	return func(w http.ResponseWriter, r *http.Request) error {
		entry, err := a.admit(r)
		if err != nil {
			return err
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if _, err := a.admissions.Exec(ctx, "DELETE FROM order_admissions WHERE ticket=$1", entry.ticket); err != nil {
				a.log.Warn("action admission cleanup deferred until lease expiry")
			}
		}()
		r = r.WithContext(context.WithValue(r.Context(), admissionKey{}, entry))
		return a.protected(func(w http.ResponseWriter, r *http.Request) error {
			if err := a.awaitAdmission(r.Context(), entry); err != nil {
				return err
			}
			return next(w, r)
		})(w, r)
	}
}

func (a *App) awaitAdmission(ctx context.Context, entry admission) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var live, first bool
		err := a.admissions.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM order_admissions WHERE ticket=$1 AND valid_until>clock_timestamp()),
 NOT EXISTS(SELECT 1 FROM order_admissions WHERE order_id=$2 AND ticket<$1 AND valid_until>clock_timestamp())`, entry.ticket, entry.orderID).Scan(&live, &first)
		if err != nil {
			return err
		}
		if !live {
			return admissionExpired()
		}
		if first {
			return nil
		}
		select {
		case <-ctx.Done():
			return admissionExpired()
		case <-ticker.C:
		}
	}
}

func admissionExpired() error {
	return problem(409, "admission_expired", "This request could not finish in time. Retry the same action to reconcile its result.")
}

// Call before any lifecycle mutation. Workers keep a nonblocking gate through
// commit and defer if an admitted command is still authenticating,
// waiting for its turn or waiting for a transaction connection. Expired tickets
// fence off old handlers; they never grant permission to finish work later.
func (a *App) guardAdmission(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	entry, command := ctx.Value(admissionKey{}).(admission)
	command = command && entry.orderID == id
	if command {
		// Lock the receipt, not the admission gate: later requests must be
		// able to register while this handler waits for an order row or HTTP.
		var ticket int64
		err := tx.QueryRow(ctx, "SELECT ticket FROM order_admissions WHERE ticket=$1 AND order_id=$2 AND valid_until>clock_timestamp() FOR UPDATE", entry.ticket, id).Scan(&ticket)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, admissionExpired()
		}
		if err != nil {
			return false, err
		}
		var allowed bool
		err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM order_admissions WHERE order_id=$2 AND ticket<$1 AND valid_until>clock_timestamp())`, entry.ticket, id).Scan(&allowed)
		if err == nil && !allowed {
			err = admissionExpired()
		}
		return allowed, err
	}
	held, err := admissionGate(ctx, tx, id, true)
	if err != nil || !held {
		return false, err
	}
	var pending bool
	err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM order_admissions WHERE order_id=$1 AND valid_until>clock_timestamp())", id).Scan(&pending)
	return !pending, err
}

func updatingOrder() error {
	return problem(503, "order_updating", "An earlier errand action is still being processed. Refresh again shortly.")
}
