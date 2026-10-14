package supplier

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInternalCatalogueAuthorizationAndDeletion(t *testing.T) {
	a := testApp(t)
	a.cfg.CatalogueToken = strings.Repeat("s", 32)
	var id string
	if err := a.db.QueryRow(context.Background(), "SELECT id::text FROM suppliers LIMIT 1").Scan(&id); err != nil {
		t.Fatal(err)
	}
	get := func(path, token string) int {
		r := httptest.NewRequest("GET", path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		return w.Code
	}
	for _, token := range []string{"", a.cfg.InternalToken, "invalid"} {
		if get("/internal/v1/suppliers/"+id, token) != 401 {
			t.Fatal("invalid service credential accepted")
		}
	}
	if get("/internal/v1/suppliers/"+id, a.cfg.CatalogueToken) != 200 {
		t.Fatal("valid worker lookup failed")
	}
	status(t, request(t, a, "PATCH", "/api/v1/suppliers/"+id, "admin", map[string]any{"active": false}), 200)
	if get("/internal/v1/suppliers/"+id, a.cfg.CatalogueToken) != 200 {
		t.Fatal("inactive record must remain readable")
	}
	status(t, request(t, a, "DELETE", "/api/v1/suppliers/"+id, "admin", nil), 204)
	if get("/internal/v1/suppliers/"+id, a.cfg.CatalogueToken) != 404 {
		t.Fatal("deleted supplier must be missing")
	}
	if get("/internal/v1/locations", a.cfg.CatalogueToken) != 200 {
		t.Fatal("internal location lookup failed")
	}
	// A browser session cannot replace the service credential, and the service
	// credential cannot replace browser authentication on the public catalogue.
	status(t, request(t, a, "GET", "/internal/v1/locations", "admin", nil), 401)
	if get("/api/v1/suppliers/"+id, a.cfg.CatalogueToken) != 401 {
		t.Fatal("service credential bypassed public session validation")
	}
	a.cfg.CatalogueToken = ""
	if get("/internal/v1/locations", strings.Repeat("s", 32)) != 401 {
		t.Fatal("unconfigured internal catalogue accepted a credential")
	}
}
