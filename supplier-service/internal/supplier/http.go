package supplier

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

type App struct {
	db   *pgxpool.Pool
	cfg  Config
	auth *AuthClient
	log  *slog.Logger
}

func New(db *pgxpool.Pool, cfg Config, log *slog.Logger) *App {
	return &App{db: db, cfg: cfg, auth: NewAuthClient(cfg), log: log}
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

func (a *App) endpoint(fn endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			var e *apiError
			if !errors.As(err, &e) {
				a.log.Error("request failed", "route", r.Pattern)
				e = &apiError{500, "internal_error", "The request could not be completed."}
			}
			if e.status == 503 {
				a.log.Warn("dependency unavailable", "route", r.Pattern, "code", e.code)
			}
			writeJSON(w, e.status, map[string]any{"error": map[string]string{"code": e.code, "message": e.message}})
		}
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return problem(415, "json_required", "Send Content-Type: application/json.")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil || d.Decode(new(any)) != io.EOF {
		return problem(400, "invalid_json", "Send one JSON object with the documented fields.")
	}
	return nil
}

func (a *App) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("GET /readyz", a.endpoint(func(w http.ResponseWriter, r *http.Request) error {
		if a.db.Ping(r.Context()) != nil {
			return problem(503, "not_ready", "Database is unavailable.")
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
		return nil
	}))
	m.HandleFunc("GET /api/v1/suppliers", a.endpoint(a.protected(false, a.list)))
	m.HandleFunc("GET /api/v1/suppliers/{id}", a.endpoint(a.protected(false, a.get)))
	m.HandleFunc("POST /api/v1/suppliers", a.endpoint(a.protected(true, a.create)))
	m.HandleFunc("PATCH /api/v1/suppliers/{id}", a.endpoint(a.protected(true, a.update)))
	m.HandleFunc("DELETE /api/v1/suppliers/{id}", a.endpoint(a.protected(true, a.remove)))
	m.HandleFunc("GET /api/v1/locations", a.endpoint(a.protected(false, a.locations)))
	return a.middleware(m)
}

func (a *App) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Add("Vary", "Origin")
		origin := r.Header.Get("Origin")
		if origin == a.cfg.Origin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		}
		if (origin != "" && origin != a.cfg.Origin) || (r.Method != "GET" && r.Method != "HEAD" && origin != a.cfg.Origin) {
			a.endpoint(func(http.ResponseWriter, *http.Request) error {
				return problem(403, "origin_rejected", "A trusted Origin header is required.")
			})(w, r)
			return
		}
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		defer func() {
			if recover() != nil {
				a.log.Error("request panic recovered")
				writeJSON(w, 500, map[string]any{"error": map[string]string{"code": "internal_error", "message": "The request could not be completed."}})
			}
		}()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
