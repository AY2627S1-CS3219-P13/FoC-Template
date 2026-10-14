package supplier

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"
)

// A service credential replaces browser sessions only on the private listener.
func (a *App) InternalHandler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /internal/v1/suppliers/{id}", a.endpoint(a.get))
	m.HandleFunc("GET /internal/v1/locations", a.endpoint(a.locations))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || a.cfg.CatalogueToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(a.cfg.CatalogueToken)) != 1 {
			a.endpoint(func(http.ResponseWriter, *http.Request) error {
				return problem(401, "invalid_service_credentials", "Valid service credentials are required.")
			})(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		m.ServeHTTP(w, r.WithContext(ctx))
	})
}
