package order

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// This server exists only in a test binary, gated by a dedicated test DB and
// explicit environment flag. It never imports or starts a sibling service.
func TestBrowserFixture(t *testing.T) {
	if os.Getenv("ORDER_UI_FIXTURE") != "1" {
		t.Skip("browser fixture is opt-in")
	}
	a, f := testApp(t)
	a.auth = actorAuth{}
	a.cfg.Origin = "http://order-ui-fixture:8080"
	handler := a.Handler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = advanceForTest(a, ctx)
				_ = a.PublishEvents(ctx)
			}
		}
	}()
	server := &http.Server{Addr: ":8080", ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__test/session" {
			token := r.URL.Query().Get("actor")
			if token != "requester" && token != "courier" && token != "other" && token != "admin" {
				w.WriteHeader(400)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "foc_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
			writeJSON(w, 200, map[string]string{"actor": token})
			return
		}
		if r.Method == "POST" && r.URL.Path == "/__test/reset" {
			if _, err := a.db.Exec(r.Context(), "TRUNCATE workflow_receipts,workflow_retries,workflow_timers,workflow_projections,order_admissions,order_publications,order_payouts,order_actions,order_refunds,order_events,orders,creation_attempts"); err != nil {
				w.WriteHeader(500)
				return
			}
			f.mu.Lock()
			f.available = 10000
			f.reserved = 0
			f.reserveEffects = 0
			f.releases = 0
			clear(f.escrows)
			clear(f.paid)
			f.payouts = 0
			f.mu.Unlock()
			w.WriteHeader(204)
			return
		}
		if r.Header.Get("Origin") == "http://localhost:8085" {
			r.Header.Set("Origin", a.cfg.Origin)
		}
		handler.ServeHTTP(w, r)
	})}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
