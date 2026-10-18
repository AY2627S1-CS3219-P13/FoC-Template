package supplier

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"
)

// Service credentials replace browser sessions only on internal catalogue reads.
func (a *App) internalCatalogue(next endpoint) endpoint {
	return func(w http.ResponseWriter, r *http.Request) error {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || a.cfg.CatalogueToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(a.cfg.CatalogueToken)) != 1 {
			return problem(401, "invalid_service_credentials", "Valid service credentials are required.")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		return next(w, r.WithContext(ctx))
	}
}
