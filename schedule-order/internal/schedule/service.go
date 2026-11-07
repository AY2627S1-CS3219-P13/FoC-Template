package schedule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

type Request struct {
	Pickup         PlaceRef  `json:"pickup"`
	Delivery       PlaceRef  `json:"delivery"`
	Items          []Item    `json:"items"`
	OfferedCredits int64     `json:"offeredCredits"`
	WindowStart    time.Time `json:"windowStart"`
}
type Scheduled struct {
	Order
	WindowStart     time.Time `json:"windowStart"`
	TransferPending bool      `json:"transferPending"`
	RefundPending   bool      `json:"refundPending"`
}
type stored struct {
	id, user, state, courier, transfer string
	request                            Request
	order                              Scheduled
	fingerprint                        string
	generation                         int64
	refund                             bool
}

func hash(v []byte) string { s := sha256.Sum256(v); return hex.EncodeToString(s[:]) }
func (a *App) lock(ctx context.Context, id string, try bool) (*pgxpool.Conn, func(), error) {
	c, e := a.db.Acquire(ctx)
	if e != nil {
		return nil, nil, e
	}
	ok := true
	if try {
		e = c.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,7219))", id).Scan(&ok)
	} else {
		_, e = c.Exec(ctx, "SELECT pg_advisory_lock(hashtextextended($1,7219))", id)
	}
	if e != nil || !ok {
		c.Release()
		if e == nil {
			e = errors.New("schedule busy")
		}
		return nil, nil, e
	}
	return c, func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, e := c.Exec(cleanup, "SELECT pg_advisory_unlock(hashtextextended($1,7219))", id); e != nil {
			_ = c.Conn().Close(cleanup)
		}
		c.Release()
	}, nil
}
func load(ctx context.Context, c *pgxpool.Conn, id string) (stored, error) {
	var s stored
	var req, order []byte
	var courier, transfer *string
	e := c.QueryRow(ctx, "SELECT id,requester_id::text,state,request,order_data,fingerprint,courier_id::text,transfer_id,generation,refund_pending FROM schedules WHERE id=$1", id).Scan(&s.id, &s.user, &s.state, &req, &order, &s.fingerprint, &courier, &transfer, &s.generation, &s.refund)
	if e != nil {
		return s, e
	}
	if e = json.Unmarshal(req, &s.request); e != nil {
		return s, e
	}
	if order != nil {
		if e = json.Unmarshal(order, &s.order); e != nil {
			return s, e
		}
	}
	if courier != nil {
		s.courier = *courier
	}
	if transfer != nil {
		s.transfer = *transfer
	}
	s.order.TransferPending = s.state == "transfer_pending"
	s.order.RefundPending = s.refund
	return s, nil
}
func (a *App) resolve(ctx context.Context, b Request) (Place, Place, error) {
	p, e := a.catalogue.Resolve(ctx, b.Pickup, "")
	if e != nil {
		return p, Place{}, e
	}
	d, e := a.catalogue.Resolve(ctx, b.Delivery, "")
	if e != nil {
		return p, d, e
	}
	for _, place := range []Place{p, d} {
		ok, e := HoursCover(place.OpeningHours, b.WindowStart, b.WindowStart.Add(30*time.Minute))
		if e != nil {
			return p, d, problem(400, "invalid_hours", "A selected supplier has invalid opening hours. Ask for them to be corrected.")
		}
		if !ok {
			return p, d, problem(409, "place_closed", "Both places must be open for the full delivery window.")
		}
	}
	return p, d, nil
}
func (a *App) create(w http.ResponseWriter, r *http.Request) error {
	key := r.Header.Get("Idempotency-Key")
	if key == "" || utf8.RuneCountInString(key) > 128 || key != strings.TrimSpace(key) || !validPlainText(key) || len(r.Header.Values("Idempotency-Key")) != 1 {
		return problem(400, "invalid_idempotency_key", "Supply one idempotency key.")
	}
	var b Request
	if e := readJSON(w, r, &b); e != nil {
		return e
	}
	base := createRequest{Pickup: b.Pickup, Delivery: b.Delivery, Items: b.Items, OfferedCredits: b.OfferedCredits, ExpiresAt: b.WindowStart.Add(30 * time.Minute)}
	if e := base.validate(time.Time{}); e != nil {
		return e
	}
	b.Pickup, b.Delivery = base.Pickup, base.Delivery
	b.WindowStart = b.WindowStart.UTC()
	raw, _ := json.Marshal(b)
	fingerprint := hash(raw)
	user := r.Context().Value(identityKey{}).(Identity).UserID
	id := "scheduled-" + hash([]byte(user+"\x00"+key))
	c, unlock, e := a.lock(r.Context(), id, false)
	if e != nil {
		return e
	}
	defer unlock()
	s, e := load(r.Context(), c, id)
	if e == nil {
		if s.fingerprint != fingerprint {
			return problem(409, "idempotency_conflict", "Key already used for different details.")
		}
		if s.order.ID != "" {
			if s.state == "transferred" {
				http.Redirect(w, r, "/api/v1/orders/"+url.PathEscape(id), http.StatusSeeOther)
				return nil
			}
			writeJSON(w, 200, map[string]any{"order": s.order})
			return nil
		}
		if s.state != "pending" {
			if s.state == "reserving" {
				return problem(503, "creation_cleanup_pending", "Creation did not finish. Credit cleanup is unresolved. Retry this same attempt to check its outcome.")
			}
			return problem(409, "creation_abandoned", "This creation was abandoned. Use a new key after cleanup.")
		}
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if e = ValidateWindow(b.WindowStart, r.Context().Value(receivedKey{}).(time.Time)); e != nil {
		return problem(400, "invalid_window", e.Error())
	}
	p, d, e := a.resolve(r.Context(), b)
	if e != nil {
		return e
	}
	_, e = c.Exec(r.Context(), "INSERT INTO schedules(id,requester_id,idempotency_key,fingerprint,request,state) VALUES($1,$2,$3,$4,$5,'pending') ON CONFLICT DO NOTHING", id, user, key, fingerprint, raw)
	if e != nil {
		return e
	}
	if _, e = c.Exec(r.Context(), "UPDATE schedules SET state='reserving',updated_at=clock_timestamp() WHERE id=$1", id); e != nil {
		return e
	}
	if e = a.credit.Reserve(r.Context(), id, user, b.OfferedCredits); e != nil {
		var api *apiError
		if errors.As(e, &api) && (api.code == "insufficient_credits" || api.code == "wallet_not_ready") {
			_, _ = c.Exec(r.Context(), "UPDATE schedules SET state='pending' WHERE id=$1", id)
		}
		return e
	}
	o := Scheduled{Order: Order{ID: id, RequesterID: user, Pickup: p, Delivery: d, Items: b.Items, OfferedCredits: b.OfferedCredits, ExpiresAt: b.WindowStart.Add(30 * time.Minute), Status: "scheduled", CreatedAt: a.now(), Version: 1}, WindowStart: b.WindowStart.In(Singapore)}
	data, _ := json.Marshal(o)
	if _, e = c.Exec(r.Context(), "UPDATE schedules SET state='scheduled',order_data=$2,updated_at=clock_timestamp() WHERE id=$1", id, data); e != nil {
		return e
	}
	writeJSON(w, 201, map[string]any{"order": o})
	return nil
}
func (a *App) terminal(ctx context.Context, c *pgxpool.Conn, s stored, status string) error {
	s.order.Status = status
	s.order.Version++
	raw, _ := json.Marshal(s.order)
	_, e := c.Exec(ctx, "UPDATE schedules SET state=$2,order_data=$3,refund_pending=true,updated_at=clock_timestamp() WHERE id=$1", s.id, status, raw)
	return e
}
func (a *App) action(w http.ResponseWriter, r *http.Request) error {
	var b struct {
		Action string `json:"action"`
	}
	if e := readJSON(w, r, &b); e != nil {
		return e
	}
	if b.Action != "accept" && b.Action != "cancel" {
		return problem(400, "invalid_action", "Choose accept or cancel.")
	}
	id := r.PathValue("id")
	c, unlock, e := a.lock(r.Context(), id, false)
	if e != nil {
		return e
	}
	defer unlock()
	s, e := load(r.Context(), c, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return problem(404, "order_not_found", "Order unavailable.")
	}
	if e != nil {
		return e
	}
	user := r.Context().Value(identityKey{}).(Identity).UserID
	if b.Action == "cancel" && user != s.user || b.Action == "accept" && user == s.user {
		return problem(403, "action_forbidden", "This action is not available to you.")
	}
	if s.state == "transfer_pending" {
		if b.Action == "accept" && user == s.courier {
			writeJSON(w, 202, map[string]any{"order": s.order})
			return nil
		}
		return problem(409, "transfer_pending", "Acceptance is pending. Wait for its confirmed outcome.")
	}
	if s.state != "scheduled" {
		if b.Action == "cancel" && s.state == "cancelled" {
			writeJSON(w, 200, map[string]any{"order": s.order})
			return nil
		}
		return problem(409, "status_conflict", "Order has changed. Refresh.")
	}
	if !s.order.ExpiresAt.After(a.now()) {
		if e = a.terminal(r.Context(), c, s, "expired"); e != nil {
			return e
		}
		return problem(409, "order_expired", "Delivery window has ended.")
	}
	if b.Action == "cancel" {
		e = a.terminal(r.Context(), c, s, "cancelled")
		if e != nil {
			return e
		}
		s.order.Status = "cancelled"
		s.order.RefundPending = true
		writeJSON(w, 200, map[string]any{"order": s.order})
		return nil
	}
	if _, _, e = a.resolve(r.Context(), s.request); e != nil {
		return e
	}
	transfer := fmt.Sprintf("%s-%d", id, s.generation+1)
	tx, e := c.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if _, e = tx.Exec(r.Context(), "UPDATE schedules SET state='transfer_pending',courier_id=$2,transfer_id=$3,generation=generation+1,updated_at=clock_timestamp() WHERE id=$1", id, user, transfer); e != nil {
		return e
	}
	if _, e = tx.Exec(r.Context(), "INSERT INTO schedule_events(event_id,schedule_id,event_type) VALUES($1,$2,'ScheduledOrderAcceptanceRequested')", transfer, id); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	s.order.TransferPending = true
	writeJSON(w, 202, map[string]any{"order": s.order})
	return nil
}
func (a *App) detail(w http.ResponseWriter, r *http.Request) error {
	var data []byte
	var state string
	var pending bool
	e := a.db.QueryRow(r.Context(), "SELECT order_data,state,refund_pending FROM schedules WHERE id=$1 AND order_data IS NOT NULL", r.PathValue("id")).Scan(&data, &state, &pending)
	if errors.Is(e, pgx.ErrNoRows) {
		return problem(404, "order_not_found", "Order unavailable.")
	}
	if e != nil {
		return e
	}
	if state == "transferred" {
		http.Redirect(w, r, "/api/v1/orders/"+url.PathEscape(r.PathValue("id")), http.StatusTemporaryRedirect)
		return nil
	}
	var o Scheduled
	if e = json.Unmarshal(data, &o); e != nil {
		return e
	}
	user := r.Context().Value(identityKey{}).(Identity).UserID
	if o.RequesterID != user && state != "scheduled" && state != "transfer_pending" {
		return problem(404, "order_not_found", "Order unavailable.")
	}
	o.TransferPending = state == "transfer_pending"
	o.RefundPending = pending
	writeJSON(w, 200, map[string]any{"order": o})
	return nil
}
func (a *App) list(w http.ResponseWriter, r *http.Request) error {
	user := r.Context().Value(identityKey{}).(Identity).UserID
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "mine"
	}
	if scope != "mine" && scope != "open" {
		return problem(400, "invalid_scope", "Choose mine or open.")
	}
	rows, e := a.db.Query(r.Context(), "SELECT order_data,state,refund_pending FROM schedules WHERE order_data IS NOT NULL AND state<>'transferred' AND (($1='mine' AND requester_id=$2) OR ($1='open' AND requester_id<>$2 AND state='scheduled' AND (order_data->>'expiresAt')::timestamptz>$3)) ORDER BY updated_at DESC,id", scope, user, a.now())
	if e != nil {
		return e
	}
	defer rows.Close()
	orders := []Scheduled{}
	for rows.Next() {
		var raw []byte
		var state string
		var refund bool
		if e = rows.Scan(&raw, &state, &refund); e != nil {
			return e
		}
		var o Scheduled
		if e = json.Unmarshal(raw, &o); e != nil {
			return e
		}
		o.TransferPending = state == "transfer_pending"
		o.RefundPending = refund
		orders = append(orders, o)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	writeJSON(w, 200, map[string]any{"orders": orders, "hasMore": false})
	return nil
}
func (a *App) slots(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	p, e := a.catalogue.Resolve(r.Context(), PlaceRef{Type: q.Get("pickupType"), ID: q.Get("pickupId")}, "")
	if e != nil {
		return e
	}
	d, e := a.catalogue.Resolve(r.Context(), PlaceRef{Type: q.Get("deliveryType"), ID: q.Get("deliveryId")}, "")
	if e != nil {
		return e
	}
	slots, e := AvailableSlots(q.Get("date"), a.now(), p.OpeningHours, d.OpeningHours)
	if e != nil {
		return problem(400, "invalid_availability", "Check the date and selected supplier hours.")
	}
	writeJSON(w, 200, map[string]any{"slots": slots})
	return nil
}
func (a *App) Recover(ctx context.Context) error {
	rows, e := a.db.Query(ctx, "SELECT id FROM schedules WHERE state<>'transferred' AND (state IN ('reserving','scheduled','transfer_pending') OR refund_pending) ORDER BY updated_at,id LIMIT 100")
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	var failures error
	for _, id := range ids {
		c, unlock, e := a.lock(ctx, id, true)
		if e != nil {
			continue
		}
		s, e := load(ctx, c, id)
		if e == nil {
			e = a.recoverOne(ctx, c, s)
		}
		unlock()
		failures = errors.Join(failures, e)
	}
	return failures
}
func (a *App) recoverOne(ctx context.Context, c *pgxpool.Conn, s stored) error {
	// Rotate before I/O so repeated dependency failures cannot starve later work.
	if _, e := c.Exec(ctx, "UPDATE schedules SET updated_at=clock_timestamp() WHERE id=$1", s.id); e != nil {
		return e
	}
	if s.state == "reserving" {
		if e := a.credit.Abort(ctx, s.id); e != nil {
			return e
		}
		_, e := c.Exec(ctx, "UPDATE schedules SET state='abandoned' WHERE id=$1", s.id)
		return e
	}
	if s.state == "transfer_pending" {
		return nil
	}
	if s.state == "scheduled" {
		status := ""
		if !s.order.ExpiresAt.After(a.now()) {
			status = "expired"
		} else if _, _, e := a.resolve(ctx, s.request); e != nil {
			var api *apiError
			if errors.As(e, &api) && (api.code == "invalid_place" || api.code == "place_closed") {
				status = "cancelled"
			} else {
				return e
			}
		}
		if status != "" {
			if e := a.terminal(ctx, c, s, status); e != nil {
				return e
			}
			s.refund = true
		}
	}
	if s.refund {
		if e := a.credit.Release(ctx, s.id); e != nil {
			return e
		}
		_, e := c.Exec(ctx, "UPDATE schedules SET refund_pending=false WHERE id=$1", s.id)
		return e
	}
	return nil
}
