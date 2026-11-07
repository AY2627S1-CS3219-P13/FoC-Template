package order

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	schedule "foc/schedule-order"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

type scheduleImportRequest struct {
	ID             string    `json:"id"`
	RequesterID    string    `json:"requesterId"`
	CourierID      string    `json:"courierId"`
	Pickup         PlaceRef  `json:"pickup"`
	Delivery       PlaceRef  `json:"delivery"`
	Items          []Item    `json:"items"`
	OfferedCredits int64     `json:"offeredCredits"`
	WindowStart    time.Time `json:"windowStart"`
	ExpiresAt      time.Time `json:"expiresAt"`
}

// InternalHandler is served only on the private listener, never the browser port.
func (a *App) InternalHandler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /internal/v1/pricing-context", a.endpoint(a.pricingContext))
	m.HandleFunc("PUT /internal/v1/scheduled-orders/{transfer}", a.endpoint(func(w http.ResponseWriter, r *http.Request) error {
		if !validSecret(a.cfg.ScheduleToken) || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+a.cfg.ScheduleToken)) != 1 {
			return problem(401, "invalid_service_credentials", "A valid service credential is required.")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		return a.importScheduled(w, r.WithContext(ctx))
	}))
	return m
}
func (a *App) importScheduled(w http.ResponseWriter, r *http.Request) error {
	var b scheduleImportRequest
	if e := readJSON(w, r, &b); e != nil {
		return e
	}
	tx, e := a.db.Begin(r.Context())
	if e != nil {
		return e
	}
	defer rollback(tx)
	outcome, e := a.importScheduledTx(r.Context(), tx, r.PathValue("transfer"), b)
	if e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	result := map[string]string{"outcome": outcome}
	if outcome == "rejected" {
		var reason string
		if e = a.db.QueryRow(r.Context(), "SELECT reason FROM scheduled_imports WHERE transfer_id=$1", r.PathValue("transfer")).Scan(&reason); e != nil {
			return e
		}
		result["reason"] = reason
	}
	writeJSON(w, 200, result)
	return nil
}

// PrepareScheduledEvent resolves HTTP dependencies before the consumer opens its transaction.
func (a *App) PrepareScheduledEvent(ctx context.Context, transfer string, raw []byte) (schedule.Effect, error) {
	var b scheduleImportRequest
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	places := preparedSchedulePlaces{results: make(map[PlaceRef]schedulePlaceResult)}
	for _, ref := range []PlaceRef{b.Pickup, b.Delivery} {
		var result schedulePlaceResult
		if a.backgroundCatalogue == nil {
			result.err = placesUnavailable()
		} else {
			result.place, result.err = a.backgroundCatalogue.Resolve(ctx, ref, "")
		}
		places.results[ref] = result
	}
	return func(ctx context.Context, tx pgx.Tx) (string, error) {
		return a.importScheduledWithCatalogue(ctx, tx, transfer, b, places)
	}, nil
}

type schedulePlaceResult struct {
	place Place
	err   error
}
type preparedSchedulePlaces struct {
	results map[PlaceRef]schedulePlaceResult
}

func (p preparedSchedulePlaces) Resolve(_ context.Context, ref PlaceRef, _ string) (Place, error) {
	r := p.results[ref]
	return r.place, r.err
}
func (a *App) importScheduledTx(ctx context.Context, tx pgx.Tx, transfer string, b scheduleImportRequest) (string, error) {
	return a.importScheduledWithCatalogue(ctx, tx, transfer, b, a.backgroundCatalogue)
}
func (a *App) importScheduledWithCatalogue(ctx context.Context, tx pgx.Tx, transfer string, b scheduleImportRequest, catalogue placeResolver) (string, error) {

	if !strings.HasPrefix(b.ID, "scheduled-") || len(b.ID) > 128 || !validPlainText(b.ID) || !validUser(b.RequesterID) || !validUser(b.CourierID) || b.RequesterID == b.CourierID || !b.ExpiresAt.Equal(b.WindowStart.Add(30*time.Minute)) || ValidateWindow(b.WindowStart, time.Time{}) != nil {
		return "", problem(400, "invalid_import", "Invalid scheduled order data.")
	}
	base := createRequest{Pickup: b.Pickup, Delivery: b.Delivery, Items: b.Items, OfferedCredits: b.OfferedCredits, ExpiresAt: b.ExpiresAt}
	if e := base.validate(time.Time{}); e != nil {
		return "", e
	}
	b.Pickup, b.Delivery = base.Pickup, base.Delivery
	b.WindowStart = b.WindowStart.UTC()
	b.ExpiresAt = b.ExpiresAt.UTC()
	raw, _ := json.Marshal(b)
	fingerprint := importFingerprint(raw)
	var e error
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,321914))", b.ID); e != nil {
		return "", e
	}
	var previousHash, outcome string
	e = tx.QueryRow(ctx, "SELECT fingerprint,outcome FROM scheduled_imports WHERE transfer_id=$1", transfer).Scan(&previousHash, &outcome)
	if e == nil {
		if previousHash != fingerprint {
			return "", problem(409, "idempotency_conflict", "Transfer ID reused with changed data.")
		}
		return outcome, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return "", e
	}
	reject := func(reason string) (string, error) {
		_, e := tx.Exec(ctx, "INSERT INTO scheduled_imports(transfer_id,fingerprint,order_id,outcome,reason) VALUES($1,$2,$3,'rejected',$4)", transfer, fingerprint, b.ID, reason)
		return "rejected", e
	}
	var exists bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM orders WHERE id=$1)", b.ID).Scan(&exists); e != nil {
		return "", e
	}
	if exists {
		return reject("order_already_imported")
	}
	if !b.ExpiresAt.After(a.now()) {
		return reject("order_expired")
	}
	if catalogue == nil {
		return "", placesUnavailable()
	}
	p, e := catalogue.Resolve(ctx, b.Pickup, "")
	if e != nil {
		var api *apiError
		if errors.As(e, &api) && api.code == "invalid_place" {
			return reject("invalid_place")
		}
		return "", e
	}
	d, e := catalogue.Resolve(ctx, b.Delivery, "")
	if e != nil {
		var api *apiError
		if errors.As(e, &api) && api.code == "invalid_place" {
			return reject("invalid_place")
		}
		return "", e
	}
	probe := Order{WindowStart: &b.WindowStart, ExpiresAt: b.ExpiresAt, Pickup: p, Delivery: d, Status: "accepted"}
	probe, e = checkScheduledHours(probe)
	if e != nil {
		return "", e
	}
	if probe.Status == "cancelled" {
		return reject("place_closed")
	}
	if allowed, e := a.guardAdmission(ctx, tx, b.ID); e != nil {
		return "", e
	} else if !allowed {
		return "", updatingOrder()
	}
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,321913))", b.CourierID); e != nil {
		return "", e
	}
	var active int
	if e = tx.QueryRow(ctx, "SELECT count(*) FROM orders WHERE courier_id=$1 AND status IN ('accepted','picked up')", b.CourierID).Scan(&active); e != nil {
		return "", e
	}
	if active >= 3 {
		return reject("courier_busy")
	}
	if !b.ExpiresAt.After(a.now()) {
		return reject("order_expired")
	}
	if _, e = tx.Exec(ctx, "INSERT INTO creation_attempts(requester_id,idempotency_key,order_id,request_fingerprint,state,offered_credits) VALUES($1,$2,$3,$4,'completed',$5)", b.RequesterID, "schedule:"+b.ID, b.ID, fingerprint, b.OfferedCredits); e != nil {
		return "", e
	}
	pj, _ := json.Marshal(p)
	dj, _ := json.Marshal(d)
	ij, _ := json.Marshal(b.Items)
	before, e := scanOrder(tx.QueryRow(ctx, "INSERT INTO orders(id,requester_id,pickup,delivery,items,offered_credits,expires_at,window_start) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING "+orderColumns, b.ID, b.RequesterID, pj, dj, ij, b.OfferedCredits, b.ExpiresAt, b.WindowStart))
	if e != nil {
		return "", e
	}
	if e = recordEvent(ctx, tx, before, "OrderCreated"); e != nil {
		return "", e
	}
	after := before
	after.Status = "accepted"
	after.CourierID = &b.CourierID
	after, e = saveChange(ctx, tx, before, after)
	if e != nil {
		return "", e
	}
	if e = recordAction(ctx, tx, after, "accept", b.CourierID, a.now()); e != nil {
		return "", e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO scheduled_imports(transfer_id,fingerprint,order_id,outcome) VALUES($1,$2,$3,'accepted')", transfer, fingerprint, b.ID); e != nil {
		return "", e
	}
	return "accepted", nil
}
func checkScheduledHours(o Order) (Order, error) {
	if o.WindowStart == nil || !supplierCancellable(o.Status) {
		return o, nil
	}
	for _, p := range []Place{o.Pickup, o.Delivery} {
		ok, e := HoursCover(p.OpeningHours, *o.WindowStart, o.ExpiresAt)
		if e != nil {
			return o, problem(503, "invalid_hours", "Supplier opening hours require correction.")
		}
		if !ok {
			o.Status = "cancelled"
			o.CancellationReason = "Opening hours no longer cover the delivery window. Credit refund is pending."
		}
	}
	return o, nil
}
