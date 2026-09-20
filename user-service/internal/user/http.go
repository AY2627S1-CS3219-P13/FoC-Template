package user

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Mailer interface {
	SendVerification(context.Context, string, string) error
}

type App struct {
	db            *pgxpool.Pool
	cfg           Config
	mailer        Mailer
	log           *slog.Logger
	dummyHash     string
	passwordSlots chan struct{}
}

func New(db *pgxpool.Pool, cfg Config, mailer Mailer, log *slog.Logger) (*App, error) {
	dummy, err := hashPassword("unused dummy password")
	if err != nil {
		return nil, err
	}
	return &App{db: db, cfg: cfg, mailer: mailer, log: log, dummyHash: dummy, passwordSlots: make(chan struct{}, 4)}, nil
}

type apiError struct {
	status        int
	code, message string
}

func (e *apiError) Error() string                    { return e.code }
func problem(status int, code, message string) error { return &apiError{status, code, message} }

type endpoint func(http.ResponseWriter, *http.Request) error

func (a *App) endpoint(fn endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			var e *apiError
			if !errors.As(err, &e) {
				// Database/SMTP errors can include credentials or submitted values.
				// Log the operation, never the raw error or request body.
				a.log.Error("request failed", "route", r.Pattern)
				e = &apiError{500, "internal_error", "The request could not be completed."}
			}
			if e.status == 429 {
				w.Header().Set("Retry-After", "60")
			}
			writeJSON(w, e.status, map[string]any{"error": map[string]string{"code": e.code, "message": e.message}})
		}
	}
}

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
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err = d.Decode(dst); err != nil {
		return problem(400, "invalid_json", "Send a valid JSON object with the documented fields.")
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return problem(400, "invalid_json", "Send exactly one JSON object.")
	}
	return nil
}

func (a *App) PublicHandler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("GET /readyz", a.endpoint(func(w http.ResponseWriter, r *http.Request) error {
		if err := a.db.Ping(r.Context()); err != nil {
			return problem(503, "not_ready", "Database is unavailable.")
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
		return nil
	}))
	m.HandleFunc("POST /api/v1/auth/register", a.endpoint(a.authLimited(a.register)))
	m.HandleFunc("POST /api/v1/auth/verify-email", a.endpoint(a.authLimited(a.verifyEmail)))
	m.HandleFunc("POST /api/v1/auth/resend-verification", a.endpoint(a.authLimited(a.resend)))
	m.HandleFunc("POST /api/v1/auth/login", a.endpoint(a.authLimited(a.login)))
	m.HandleFunc("POST /api/v1/auth/logout", a.endpoint(a.logout))
	m.HandleFunc("GET /api/v1/users/me", a.endpoint(a.me))
	m.HandleFunc("PATCH /api/v1/users/me", a.endpoint(a.updateProfile))
	m.HandleFunc("PUT /api/v1/users/me/password", a.endpoint(a.authLimited(a.changePassword)))
	m.HandleFunc("PUT /api/v1/admin/users/{id}/role", a.endpoint(a.changeRole))
	return a.middleware(m, true)
}

func (a *App) InternalHandler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /internal/v1/sessions/validate", a.endpoint(func(w http.ResponseWriter, r *http.Request) error {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare([]byte(provided), []byte(a.cfg.InternalToken)) != 1 {
			return problem(401, "invalid_service_credentials", "Valid service credentials are required.")
		}
		var body struct {
			SessionToken string `json:"sessionToken"`
		}
		if err := readJSON(w, r, &body); err != nil {
			return err
		}
		u, expiry, err := a.session(r.Context(), body.SessionToken)
		if errors.Is(err, errInvalidSession) {
			return problem(401, "invalid_session", "The session is missing, expired or revoked.")
		}
		if err != nil {
			return problem(503, "auth_unavailable", "Session validation is temporarily unavailable.")
		}
		writeJSON(w, 200, map[string]any{"userId": u.ID, "roles": u.Roles, "expiresAt": expiry})
		return nil
	}))
	return a.middleware(m, false)
}

func (a *App) middleware(next http.Handler, public bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		defer func() {
			if recover() != nil {
				a.log.Error("request panic recovered")
				writeJSON(w, 500, map[string]any{"error": map[string]string{"code": "internal_error", "message": "The request could not be completed."}})
			}
		}()
		if public {
			origin := r.Header.Get("Origin")
			w.Header().Add("Vary", "Origin")
			if origin == a.cfg.Origin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, OPTIONS")
			}
			// Require an exact trusted Origin on every mutation, including login.
			// Non-browser API clients must supply the same header.
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
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *App) authLimited(next endpoint) endpoint {
	return func(w http.ResponseWriter, r *http.Request) error {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		// Deliberately ignore client-supplied X-Forwarded-For.
		if err = a.limit(r.Context(), "ip:"+ip, 60, time.Minute); err != nil {
			return err
		}
		select {
		case a.passwordSlots <- struct{}{}:
			defer func() { <-a.passwordSlots }()
		default:
			return problem(503, "busy", "Please retry shortly.")
		}
		return next(w, r)
	}
}

func (a *App) limit(ctx context.Context, key string, max int, window time.Duration) error {
	key = hex.EncodeToString(codeHash(a.cfg.CodeSecret, "rate-limit", key))
	var count int
	err := a.db.QueryRow(ctx, `INSERT INTO rate_limits(key,requests,expires_at) VALUES($1,1,now()+$2*interval '1 second')
		ON CONFLICT(key) DO UPDATE SET
		requests=CASE WHEN rate_limits.expires_at<=now() THEN 1 ELSE rate_limits.requests+1 END,
		expires_at=CASE WHEN rate_limits.expires_at<=now() THEN EXCLUDED.expires_at ELSE rate_limits.expires_at END
		RETURNING requests`, key, int(window.Seconds())).Scan(&count)
	if err != nil {
		return err
	}
	if count > max {
		return problem(429, "rate_limited", "Too many attempts. Please try again later.")
	}
	return nil
}

func (a *App) currentUser(r *http.Request) (User, error) {
	cookie, err := r.Cookie(a.cfg.CookieName())
	if err != nil {
		return User{}, problem(401, "invalid_session", "Please log in.")
	}
	u, _, err := a.session(r.Context(), cookie.Value)
	if errors.Is(err, errInvalidSession) {
		return u, problem(401, "invalid_session", "Please log in again.")
	}
	return u, err
}

func (a *App) setCookie(w http.ResponseWriter, token string, expiry time.Time) {
	maxAge := int(time.Until(expiry).Seconds())
	if token == "" {
		maxAge = -1
		expiry = time.Unix(1, 0)
	}
	http.SetCookie(w, &http.Cookie{Name: a.cfg.CookieName(), Value: token, Path: "/", HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteLaxMode, Expires: expiry, MaxAge: maxAge})
}
