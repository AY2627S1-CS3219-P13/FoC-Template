package order

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

func actor(r *http.Request) string { return r.Context().Value(identityKey{}).(Identity).UserID }

func visible(o Order, user string) bool {
	return o.Status == "open" || o.RequesterID == user || (o.CourierID != nil && *o.CourierID == user)
}

func (a *App) session(w http.ResponseWriter, r *http.Request) error {
	var busy bool
	if err := a.db.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM orders WHERE courier_id=$1 AND status IN ('accepted','picked up'))", actor(r)).Scan(&busy); err != nil {
		return err
	}
	writeJSON(w, 200, struct {
		Identity
		CourierBusy bool `json:"courierBusy"`
	}{r.Context().Value(identityKey{}).(Identity), busy})
	return nil
}

func (a *App) listOrders(w http.ResponseWriter, r *http.Request) error {
	scope := r.URL.Query().Get("scope")
	// Only hard-coded SQL predicates may be selected by browser input.
	predicate := "requester_id=$1"
	switch scope {
	case "", "mine":
	case "open":
		return a.openOrders(w, r)
	case "courier":
		predicate = "courier_id=$1"
	case "disputes":
		if !slices.Contains(r.Context().Value(identityKey{}).(Identity).Roles, "admin") {
			return problem(403, "admin_required", "An administrator is required.")
		}
		predicate = "status='dispute' AND $1::uuid IS NOT NULL"
	default:
		return problem(400, "invalid_scope", "Choose mine, open or courier.")
	}
	filter := r.URL.Query().Get("filter")
	if filter != "" && filter != "all" {
		if scope != "" && scope != "mine" {
			return problem(400, "invalid_filter", "Request filters apply only to My requests.")
		}
		switch filter {
		case "current":
			predicate += " AND status NOT IN ('completed','not completed','cancelled','expired')"
		case "past":
			predicate += " AND status IN ('completed','not completed','cancelled','expired')"
		default:
			return problem(400, "invalid_filter", "Choose all, current or past requests.")
		}
	}
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100000 {
			return problem(400, "invalid_page", "Choose a valid page.")
		}
		page = n
	}
	rows, err := a.db.Query(r.Context(), "SELECT "+orderColumns+" FROM orders WHERE "+predicate+" ORDER BY created_at DESC,id LIMIT 21 OFFSET $2", actor(r), (page-1)*20)
	if err != nil {
		return err
	}
	defer rows.Close()
	result := []Order{}
	for rows.Next() {
		value, err := scanOrder(rows)
		if err != nil {
			return err
		}
		result = append(result, value)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	more := len(result) > 20
	if more {
		result = result[:20]
	}
	// Release the query connection before reconciliation, including with a
	// one-connection pool. Refresh every displayed row, not only selected detail.
	rows.Close()
	cookie, _ := r.Cookie(a.cfg.SessionCookie)
	resolved, missing, err := a.snapshotPlaces(r.Context(), result, cookie.Value)
	if err != nil {
		return err
	}
	for i, observed := range result {
		result[i], err = a.refreshHistory(r.Context(), observed, cookie.Value, resolved, missing)
		if err != nil {
			return err
		}
	}
	if scope == "disputes" {
		result = slices.DeleteFunc(result, func(o Order) bool { return o.Status != "dispute" })
	}
	// Reconciliation can expire/cancel a selected current request.
	if filter == "current" {
		result = slices.DeleteFunc(result, func(o Order) bool {
			return o.Status == "completed" || o.Status == "not completed" || o.Status == "cancelled" || o.Status == "expired"
		})
	}
	if err := r.Context().Err(); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"orders": result, "page": page, "hasMore": more})
	return nil
}

func (a *App) refreshHistory(ctx context.Context, observed Order, session string, resolved map[PlaceRef]Place, missing map[PlaceRef]bool) (Order, error) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return Order{}, err
	}
	defer rollback(tx)
	if allowed, err := a.guardAdmission(ctx, tx, observed.ID); err != nil {
		return Order{}, err
	} else if !allowed {
		return Order{}, updatingOrder()
	}
	before, err := scanOrder(tx.QueryRow(ctx, "SELECT "+orderColumns+" FROM orders WHERE id=$1 FOR UPDATE", observed.ID))
	if err != nil {
		return Order{}, err
	}
	if before.Version != observed.Version {
		return Order{}, updatingOrder()
	}
	after := before
	for _, target := range []*Place{&after.Pickup, &after.Delivery} {
		ref := PlaceRef{Type: target.Type, ID: target.ID}
		fresh, known := resolved[ref]
		if !known && !missing[ref] {
			fresh, err = a.catalogue.Resolve(ctx, ref, session)
			var api *apiError
			if errors.As(err, &api) && api.code == "invalid_place" {
				missing[ref] = true
			} else if err != nil {
				return Order{}, err
			} else {
				resolved[ref] = fresh
			}
		}
		if missing[ref] {
			// Preserve a pickup that won while this snapshot waited for the
			// lock, just as explicit detail refresh does.
			if supplierCancellable(before.Status) && !(observed.Status == "accepted" && before.Status == "picked up") {
				after.Status = "cancelled"
				after.CancellationReason = "A selected place was deactivated or deleted. Credit refund is queued."
			}
			continue
		}
		*target = fresh
	}
	after, err = saveChange(ctx, tx, before, after)
	if err != nil {
		return Order{}, err
	}
	return after, tx.Commit(ctx)
}

func (a *App) findVisible(r *http.Request) (Order, error) {
	value, err := scanOrder(a.db.QueryRow(r.Context(), "SELECT "+orderColumns+" FROM orders WHERE id=$1", r.PathValue("id")))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !visible(value, actor(r)) && !(value.Status == "dispute" && slices.Contains(r.Context().Value(identityKey{}).(Identity).Roles, "admin"))) {
		return Order{}, problem(404, "order_not_found", "This errand is not available.")
	}
	return value, err
}

func (a *App) getOrder(w http.ResponseWriter, r *http.Request) error {
	value, err := a.findVisible(r)
	if err != nil {
		return err
	}
	writeOrder(w, 200, value)
	return nil
}

type orderEvent struct {
	ID            string    `json:"eventId"`
	OrderID       string    `json:"orderId"`
	Version       int64     `json:"orderVersion"`
	Type          string    `json:"type"`
	SchemaVersion int       `json:"schemaVersion"`
	CreatedAt     time.Time `json:"createdAt"`
}

func recordEvent(ctx context.Context, tx pgx.Tx, o Order, kind string) error {
	_, err := tx.Exec(ctx, `INSERT INTO order_events(event_id,order_id,order_version,event_type)
        VALUES($1,$2,$3,$4)`, o.ID+":"+strconv.FormatInt(o.Version, 10), o.ID, o.Version, kind)
	return err
}

func (a *App) events(w http.ResponseWriter, r *http.Request) error {
	// Authorize against current ownership, not a cursor or a historical event.
	value, err := a.findVisible(r)
	if err != nil {
		return err
	}
	after := int64(0)
	if raw := r.URL.Query().Get("afterVersion"); raw != "" {
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 {
			return problem(400, "invalid_version", "Version must be a non-negative integer.")
		}
	}
	rows, err := a.db.Query(r.Context(), `SELECT event_id,order_id,order_version,event_type,schema_version,created_at
        FROM order_events WHERE order_id=$1 AND order_version>$2 AND order_version<=$3 ORDER BY order_version LIMIT 100`, value.ID, after, value.Version)
	if err != nil {
		return err
	}
	defer rows.Close()
	events := []orderEvent{}
	for rows.Next() {
		var event orderEvent
		if err = rows.Scan(&event.ID, &event.OrderID, &event.Version, &event.Type, &event.SchemaVersion, &event.CreatedAt); err != nil {
			return err
		}
		events = append(events, event)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"events": events})
	return nil
}

// Refresh every eligible row, not just the selected card. Resolve each place once
// per snapshot using this request's session; no authentication is cached.
func (a *App) openOrders(w http.ResponseWriter, r *http.Request) error {
	ordering := r.URL.Query().Get("sort")
	if ordering != "" && ordering != "expiry" && ordering != "credits" {
		return problem(400, "invalid_sort", "Choose expiry or credits.")
	}
	rows, err := a.db.Query(r.Context(), "SELECT "+orderColumns+" FROM orders WHERE status='open' AND requester_id<>$1 AND expires_at>$2 ORDER BY id", actor(r), a.now())
	if err != nil {
		return err
	}
	var observed []Order
	for rows.Next() {
		o, scanErr := scanOrder(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		observed = append(observed, o)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	cookie, _ := r.Cookie(a.cfg.SessionCookie)
	resolved, missing, err := a.snapshotPlaces(r.Context(), observed, cookie.Value)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(observed))
	for _, o := range observed {
		ids = append(ids, o.ID)
		if err = a.refreshListed(r.Context(), o, cookie.Value, resolved, missing); err != nil {
			return err
		}
	}
	// Re-read after refresh so assignments made during catalogue I/O disappear.
	rows, err = a.db.Query(r.Context(), "SELECT "+orderColumns+" FROM orders WHERE status='open' AND requester_id<>$1 AND expires_at>$2 AND id=ANY($3::text[])", actor(r), a.now(), ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	result := []Order{}
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return err
		}
		result = append(result, o)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	sort.Slice(result, func(i, j int) bool {
		x, y := result[i], result[j]
		if ordering == "credits" && x.OfferedCredits != y.OfferedCredits {
			return x.OfferedCredits > y.OfferedCredits
		}
		if !x.ExpiresAt.Equal(y.ExpiresAt) {
			return x.ExpiresAt.Before(y.ExpiresAt)
		}
		if !x.CreatedAt.Equal(y.CreatedAt) {
			return x.CreatedAt.Before(y.CreatedAt)
		}
		return x.ID < y.ID
	})
	if err := r.Context().Err(); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"orders": result, "hasMore": false})
	return nil
}

func (a *App) refreshListed(ctx context.Context, observed Order, session string, resolved map[PlaceRef]Place, missing map[PlaceRef]bool) error {
	id := observed.ID
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if allowed, err := a.guardAdmission(ctx, tx, id); err != nil {
		return err
	} else if !allowed {
		return updatingOrder()
	}
	before, err := scanOrder(tx.QueryRow(ctx, "SELECT "+orderColumns+" FROM orders WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		return err
	}
	if before.Status != "open" {
		return nil
	}
	if before.Version != observed.Version {
		return updatingOrder()
	}
	after := before
	for _, target := range []*Place{&after.Pickup, &after.Delivery} {
		ref := PlaceRef{Type: target.Type, ID: target.ID}
		fresh, known := resolved[ref]
		if !known && !missing[ref] {
			fresh, err = a.catalogue.Resolve(ctx, ref, session)
			var api *apiError
			if errors.As(err, &api) && api.code == "invalid_place" {
				missing[ref] = true
			} else if err != nil {
				return err
			} else {
				resolved[ref] = fresh
			}
		}
		if missing[ref] {
			after.Status = "cancelled"
			after.CancellationReason = "A selected place was deactivated or deleted. Credit refund is queued."
			break
		}
		*target = fresh
	}
	if after.Status == "open" && !after.ExpiresAt.After(a.now()) {
		after.Status = "expired"
	}
	if _, err = saveChange(ctx, tx, before, after); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
