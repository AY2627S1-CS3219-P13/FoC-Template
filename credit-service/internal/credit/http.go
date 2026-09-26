package credit

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

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
				status, code := problemFor(err)
				message := err.Error()
				if status == http.StatusInternalServerError {
					// Database errors can include submitted values; log the route only.
					a.log.Error("request failed", "route", r.Pattern)
					message = "The request could not be completed."
				}
				e = &apiError{status, code, message}
			}
			writeJSON(w, e.status, map[string]any{"error": map[string]string{"code": e.code, "message": e.message}})
		}
	}
}

// PublicHandler serves the frontend on port 8080. Every /api route resolves
// the caller from their session cookie through User Service.
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
	m.HandleFunc("GET /api/v1/wallets/me", a.endpoint(a.myWallet))
	m.HandleFunc("GET /api/v1/wallets/me/transactions", a.endpoint(a.myHistory))
	m.HandleFunc("POST /api/v1/admin/wallets/{userId}/debits", a.endpoint(a.adminDebit))
	return a.middleware(m, true)
}

// InternalHandler serves other services on port 8081, authenticated by a
// shared service token. Never route it through the public proxy.
func (a *App) InternalHandler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /internal/v1/wallets/{userId}", a.endpoint(a.wallet))
	m.HandleFunc("GET /internal/v1/wallets/{userId}/transactions", a.endpoint(a.history))
	m.HandleFunc("POST /internal/v1/wallets/{userId}/initial-allocation", a.endpoint(a.allocate))
	// PUT because the caller chooses the ID (the order ID) and repeating it is safe.
	m.HandleFunc("PUT /internal/v1/escrows/{orderId}", a.endpoint(a.reserve))
	m.HandleFunc("POST /internal/v1/escrows/{orderId}/release", a.endpoint(a.release))
	m.HandleFunc("POST /internal/v1/escrows/{orderId}/payout", a.endpoint(a.transfer))
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
		reject := func(status int, code, message string) {
			a.endpoint(func(http.ResponseWriter, *http.Request) error { return problem(status, code, message) })(w, r)
		}
		if public {
			origin := r.Header.Get("Origin")
			w.Header().Add("Vary", "Origin")
			if origin == a.cfg.Origin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			}
			// Require the exact frontend Origin on every write (CSRF protection).
			if (origin != "" && origin != a.cfg.Origin) || (r.Method != "GET" && r.Method != "HEAD" && origin != a.cfg.Origin) {
				reject(403, "origin_rejected", "A trusted Origin header is required.")
				return
			}
			if r.Method == "OPTIONS" {
				w.WriteHeader(204)
				return
			}
		} else {
			provided, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || a.cfg.InternalToken == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(a.cfg.InternalToken)) != 1 {
				reject(401, "invalid_service_credentials", "Valid service credentials are required.")
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// caller resolves the signed-in user. It fails closed when User Service is unreachable.
func (a *App) caller(r *http.Request) (Caller, error) {
	cookie, err := r.Cookie(a.cfg.SessionCookie)
	if err != nil || cookie.Value == "" {
		return Caller{}, problem(401, "invalid_session", "The session is missing, expired or revoked.")
	}
	c, err := a.sessions.Validate(r.Context(), cookie.Value)
	if errors.Is(err, ErrInvalidSession) {
		return Caller{}, problem(401, "invalid_session", "The session is missing, expired or revoked.")
	}
	if err != nil {
		a.log.Error("session validation unavailable")
		return Caller{}, problem(503, "auth_unavailable", "Session validation is temporarily unavailable.")
	}
	return c, nil
}

func (a *App) myWallet(w http.ResponseWriter, r *http.Request) error {
	c, err := a.caller(r)
	if err != nil {
		return err
	}
	wallet, err := a.Balance(r.Context(), c.UserID)
	if err != nil {
		return err
	}
	writeJSON(w, 200, wallet)
	return nil
}

func (a *App) adminDebit(w http.ResponseWriter, r *http.Request) error {
	c, err := a.caller(r)
	if err != nil {
		return err
	}
	if !c.Admin {
		return problem(403, "admin_required", "Only admins can debit credits.")
	}
	var body struct {
		Amount    int64  `json:"amount"`
		Reason    string `json:"reason"`
		RequestID string `json:"requestId"`
	}
	if err = readJSON(w, r, &body); err != nil {
		return err
	}
	wallet, err := a.AdminDebit(r.Context(), c.UserID, r.PathValue("userId"), body.Amount, body.Reason, body.RequestID)
	if err != nil {
		return err
	}
	writeJSON(w, 200, wallet)
	return nil
}

func (a *App) myHistory(w http.ResponseWriter, r *http.Request) error {
	c, err := a.caller(r)
	if err != nil {
		return err
	}
	return a.writeHistory(w, r, c.UserID)
}

func (a *App) history(w http.ResponseWriter, r *http.Request) error {
	return a.writeHistory(w, r, r.PathValue("userId"))
}

// writeHistory answers one page: ?limit=1-100 (default 30) and ?before=<id>
// from the previous page's nextBefore. nextBefore is null on the last page.
func (a *App) writeHistory(w http.ResponseWriter, r *http.Request, userID string) error {
	limit, before := DefaultHistoryLimit, int64(0)
	query := r.URL.Query()
	var err error
	if v := query.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil {
			return ErrInvalidInput
		}
	}
	if v := query.Get("before"); v != "" {
		if before, err = strconv.ParseInt(v, 10, 64); err != nil || before < 1 {
			return ErrInvalidInput
		}
	}
	entries, err := a.History(r.Context(), userID, limit, before)
	if err != nil {
		return err
	}
	var next *int64
	if len(entries) == limit {
		next = &entries[len(entries)-1].ID
	}
	writeJSON(w, 200, map[string]any{"transactions": entries, "nextBefore": next})
	return nil
}

func (a *App) wallet(w http.ResponseWriter, r *http.Request) error {
	wallet, err := a.Balance(r.Context(), r.PathValue("userId"))
	if err != nil {
		return err
	}
	writeJSON(w, 200, wallet)
	return nil
}

func (a *App) allocate(w http.ResponseWriter, r *http.Request) error {
	wallet, err := a.AllocateInitial(r.Context(), r.PathValue("userId"))
	if err != nil {
		return err
	}
	writeJSON(w, 200, wallet)
	return nil
}

func (a *App) reserve(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		UserID string `json:"userId"`
		Amount int64  `json:"amount"`
	}
	if err := readJSON(w, r, &body); err != nil {
		return err
	}
	wallet, err := a.Reserve(r.Context(), body.UserID, r.PathValue("orderId"), body.Amount)
	if err != nil {
		return err
	}
	writeJSON(w, 200, wallet)
	return nil
}

func (a *App) release(w http.ResponseWriter, r *http.Request) error {
	wallet, err := a.Release(r.Context(), r.PathValue("orderId"))
	if err != nil {
		return err
	}
	writeJSON(w, 200, wallet)
	return nil
}

func (a *App) transfer(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		CourierID string `json:"courierId"`
	}
	if err := readJSON(w, r, &body); err != nil {
		return err
	}
	if err := a.Transfer(r.Context(), r.PathValue("orderId"), body.CourierID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// problemFor maps domain errors to the HTTP status and error code in API.md.
func problemFor(err error) (int, string) {
	switch {
	case errors.Is(err, ErrInvalidAmount):
		return http.StatusBadRequest, "invalid_amount"
	case errors.Is(err, ErrInvalidInput):
		return http.StatusBadRequest, "invalid_input"
	case errors.Is(err, ErrWalletNotFound):
		return http.StatusNotFound, "wallet_not_found"
	case errors.Is(err, ErrReservationNotFound):
		return http.StatusNotFound, "escrow_not_found"
	case errors.Is(err, ErrInsufficientCredits):
		return http.StatusConflict, "insufficient_credits"
	case errors.Is(err, ErrReservationSettled):
		return http.StatusConflict, "escrow_settled"
	case errors.Is(err, ErrCourierIsRequester):
		return http.StatusConflict, "courier_is_requester"
	case errors.Is(err, ErrConflict):
		return http.StatusConflict, "idempotency_conflict"
	default:
		return http.StatusInternalServerError, "internal_error"
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
