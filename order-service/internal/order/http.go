package order

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type receivedKey struct{}

type App struct {
	backgroundCatalogue placeResolver
	db                  *pgxpool.Pool
	admissions          *pgxpool.Pool
	cfg                 Config
	auth                sessionValidator
	catalogue           placeResolver
	places              placeLister
	credit              escrowClient
	log                 *slog.Logger
	now                 func() time.Time
}

func New(db *pgxpool.Pool, cfg Config, log *slog.Logger) *App {
	client := &http.Client{Timeout: cfg.DependencyTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var background placeResolver
	if cfg.SupplierInternalURL != "" {
		background = &CatalogueClient{BaseURL: cfg.SupplierInternalURL, InternalToken: cfg.SupplierInternalToken, Client: client}
	}
	return &App{
		backgroundCatalogue: background,
		db:                  db, admissions: db, cfg: cfg, log: log, now: time.Now,
		auth:      &AuthClient{URL: cfg.UserServiceURL, Token: cfg.UserServiceToken, CookieName: cfg.SessionCookie, Client: client},
		catalogue: &CatalogueClient{BaseURL: cfg.SupplierServiceURL, CookieName: cfg.SessionCookie, Client: client},
		places:    &CatalogueClient{BaseURL: cfg.SupplierServiceURL, CookieName: cfg.SessionCookie, Client: client},
		credit:    &CreditClient{BaseURL: cfg.CreditServiceURL, Token: cfg.CreditServiceToken, Client: client},
	}
}

type apiError struct {
	status        int
	code, message string
}

func (e *apiError) Error() string                    { return e.code }
func problem(status int, code, message string) error { return &apiError{status, code, message} }

type endpoint func(http.ResponseWriter, *http.Request) error

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status != http.StatusNoContent {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return problem(415, "json_required", "Send Content-Type: application/json.")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(dst)
	if err == nil {
		err = decoder.Decode(new(any))
		if err == io.EOF {
			return nil
		}
	}
	var sizeError *http.MaxBytesError
	if errors.As(err, &sizeError) {
		return problem(413, "request_too_large", "The request exceeds the 1 MiB transport limit. Reduce its size; there is no item-count limit.")
	}
	return problem(400, "invalid_json", "Send one JSON object with the documented fields and whole-number quantities and credits.")
}

func (a *App) endpoint(fn endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			var api *apiError
			if !errors.As(err, &api) {
				a.log.Error("request failed", "route", r.Pattern)
				api = &apiError{500, "internal_error", "The request could not be completed. Retry with the same idempotency key."}
			}
			if api.status == 503 {
				a.log.Warn("dependency unavailable", "route", r.Pattern, "code", api.code)
			}
			writeJSON(w, api.status, map[string]any{"error": map[string]string{"code": api.code, "message": api.message}})
		}
	}
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", a.endpoint(func(w http.ResponseWriter, r *http.Request) error {
		if a.db.Ping(r.Context()) != nil {
			return problem(503, "not_ready", "Database is unavailable.")
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
		return nil
	}))
	mux.HandleFunc("POST /api/v1/orders", a.endpoint(a.protected(a.create)))
	mux.HandleFunc("POST /api/v1/orders/items/add", a.endpoint(a.protected(a.addItem)))
	mux.HandleFunc("GET /api/v1/orders/session", a.endpoint(a.protected(a.session)))
	mux.HandleFunc("GET /api/v1/orders/stream", a.endpoint(a.protected(a.streamOrders)))
	mux.HandleFunc("GET /api/v1/orders/publication-status", a.endpoint(a.protected(a.publicationStatus)))
	mux.HandleFunc("GET /api/v1/orders/places", a.endpoint(a.protected(a.listPlaces)))
	mux.HandleFunc("GET /api/v1/orders", a.endpoint(a.snapshot(a.protected(a.listOrders))))
	mux.HandleFunc("GET /api/v1/orders/{id}", a.endpoint(a.protected(a.getOrder)))
	mux.HandleFunc("GET /api/v1/orders/{id}/events", a.endpoint(a.protected(a.events)))
	mux.HandleFunc("POST /api/v1/orders/{id}/refresh", a.endpoint(a.snapshot(a.admitted(a.refreshOrder))))
	mux.HandleFunc("POST /api/v1/orders/{id}/actions", a.endpoint(a.admitted(a.transition)))
	registerUI(mux)
	return a.middleware(mux)
}

func (a *App) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), receivedKey{}, a.now()))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Add("Vary", "Origin")
		origin := r.Header.Get("Origin")
		if origin == a.cfg.Origin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}
		if (origin != "" && origin != a.cfg.Origin) || (r.Method != http.MethodGet && r.Method != http.MethodHead && origin != a.cfg.Origin) {
			a.endpoint(func(http.ResponseWriter, *http.Request) error {
				return problem(403, "origin_rejected", "A trusted Origin header is required.")
			})(w, r)
			return
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		defer func() {
			if recover() != nil {
				a.log.Error("request panic recovered")
				writeJSON(w, 500, map[string]any{"error": map[string]string{"code": "internal_error", "message": "The request could not be completed."}})
			}
		}()
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type addItemRequest struct {
	ExistingItems []Item `json:"existingItems"`
	Item          Item   `json:"item"`
	Decision      string `json:"decision"`
}

func (a *App) addItem(w http.ResponseWriter, r *http.Request) error {
	var body addItemRequest
	if err := readJSON(w, r, &body); err != nil {
		return err
	}
	for i := range body.ExistingItems {
		if err := body.ExistingItems[i].validate(); err != nil {
			return err
		}
	}
	if err := body.Item.validate(); err != nil {
		return err
	}
	duplicate := false
	key := duplicateKey(body.Item.Name)
	for _, existing := range body.ExistingItems {
		if duplicateKey(existing.Name) == key {
			duplicate = true
			break
		}
	}
	if body.Decision != "" && body.Decision != "proceed" && body.Decision != "do_not_proceed" {
		return problem(400, "invalid_duplicate_decision", "Choose proceed or do_not_proceed.")
	}
	items := body.ExistingItems
	added := false
	requiresDecision := duplicate && body.Decision == ""
	if !requiresDecision && body.Decision != "do_not_proceed" {
		items = append(items, body.Item)
		added = true
	}
	writeJSON(w, 200, map[string]any{"duplicate": duplicate, "requiresDecision": requiresDecision, "added": added, "items": items})
	return nil
}
