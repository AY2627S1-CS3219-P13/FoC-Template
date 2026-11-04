package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"
)

type receivedKey struct{}
type App struct {
	db        *pgxpool.Pool
	cfg       Config
	log       *slog.Logger
	now       func() time.Time
	client    *http.Client
	auth      *AuthClient
	catalogue *CatalogueClient
	credit    *CreditClient
}

func New(db *pgxpool.Pool, cfg Config, log *slog.Logger) *App {
	client := &http.Client{Timeout: cfg.DependencyTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &App{db: db, cfg: cfg, log: log, now: time.Now, client: client, auth: &AuthClient{URL: cfg.UserServiceURL, Token: cfg.UserServiceToken, CookieName: cfg.SessionCookie, Client: client}, catalogue: &CatalogueClient{BaseURL: cfg.SupplierServiceURL, InternalToken: cfg.SupplierServiceToken, Client: client}, credit: &CreditClient{BaseURL: cfg.CreditServiceURL, Token: cfg.CreditServiceToken, Client: client}}
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
	if status != 204 {
		_ = json.NewEncoder(w).Encode(body)
	}
}
func readJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		return problem(415, "json_required", "Send JSON.")
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil || d.Decode(new(any)) != io.EOF {
		return problem(400, "invalid_json", "Send one object with documented fields.")
	}
	return nil
}
func (a *App) endpoint(fn endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if e := fn(w, r); e != nil {
			var api *apiError
			if !errors.As(e, &api) {
				a.log.Error("request failed", "route", r.Pattern)
				api = &apiError{500, "internal_error", "Request failed. Retry the same attempt."}
			}
			writeJSON(w, api.status, map[string]any{"error": map[string]string{"code": api.code, "message": api.message}})
		}
	}
}
func (a *App) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("GET /readyz", a.endpoint(func(w http.ResponseWriter, r *http.Request) error {
		if a.db.Ping(r.Context()) != nil {
			return problem(503, "not_ready", "Database unavailable.")
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
		return nil
	}))
	for pattern, fn := range map[string]endpoint{"POST /api/v1/scheduled-orders": a.create, "GET /api/v1/scheduled-orders": a.list, "GET /api/v1/scheduled-orders/slots": a.slots, "GET /api/v1/scheduled-orders/{id}": a.detail, "POST /api/v1/scheduled-orders/{id}/actions": a.action} {
		m.HandleFunc(pattern, a.endpoint(a.protected(fn)))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != a.cfg.Origin {
			a.endpoint(func(http.ResponseWriter, *http.Request) error {
				return problem(403, "origin_rejected", "A trusted Origin is required.")
			})(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), receivedKey{}, a.now()), 15*time.Second)
		defer cancel()
		m.ServeHTTP(w, r.WithContext(ctx))
	})
}
