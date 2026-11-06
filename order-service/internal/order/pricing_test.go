package order

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testQuoteKey = "55555555-5555-4555-8555-555555555555"

func quoteBody() map[string]any {
	return map[string]any{"draft": map[string]any{"id": testUserID, "revision": 2}, "pickup": PlaceRef{"supplier", testSupplierID}, "delivery": PlaceRef{"location", "utown"}, "orderExpiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "totalItemQuantity": 2}
}
func TestPricingProxyScopeValidationAndFallback(t *testing.T) {
	app, _ := testApp(t)
	token := strings.Repeat("p", 64)
	var receivedKey string
	var received quoteRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/price-quotes" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Cookie") != "" {
			t.Error("invalid private quote transport")
		}
		receivedKey = r.Header.Get("Idempotency-Key")
		var raw map[string]any
		if json.NewDecoder(r.Body).Decode(&raw) != nil || raw["userId"] != nil || raw["unknown"] != nil {
			t.Error("unfiltered quote inputs")
		}
		data, _ := json.Marshal(raw)
		received = quoteRequest{}
		_ = json.Unmarshal(data, &received)
		now := time.Now().UTC().Truncate(time.Second)
		writeJSON(w, 200, quoteResponse{QuoteID: testQuoteKey, Draft: received.Draft, RecommendedCredits: 12, PricingMode: "baseline", QuotedAt: now, ValidUntil: now.Add(120 * time.Second), ReleaseID: "test", ContextSnapshotID: "context", Warnings: []string{}})
	}))
	defer server.Close()
	app.cfg.PricingURL = server.URL
	app.cfg.PricingToken = token
	body := quoteBody()
	body["userId"] = "forged"
	body["unknown"] = map[string]any{"anything": true}
	response := request(t, app, "POST", "/api/v1/orders/price-quotes", "valid", testQuoteKey, body)
	assertStatus(t, response, 200)
	firstKey := receivedKey
	if firstKey == testQuoteKey || !canonicalUUID(firstKey) || firstKey != scopedQuoteKey(token, testUserID, testQuoteKey) || firstKey == scopedQuoteKey(token, secondSupplierID, testQuoteKey) {
		t.Fatal("keys must be private, stable and user scoped")
	}
	assertStatus(t, request(t, app, "POST", "/api/v1/orders/price-quotes", "valid", testQuoteKey, body), 200)
	if receivedKey != firstKey {
		t.Fatal("retry key changed")
	}
	assertStatus(t, request(t, app, "POST", "/api/v1/orders/price-quotes", "", testQuoteKey, body), 401)
	for _, raw := range []string{
		`{"draft":{"id":"` + testUserID + `","revision":0,"revision":1}}`,
		`{"draft":null}`, `{"draft":{"id":"` + testUserID + `","revision":null}}`,
	} {
		assertStatus(t, rawRequest(app, "POST", "/api/v1/orders/price-quotes", "valid", testQuoteKey, []byte(raw)), 400)
	}
	body["totalItemQuantity"] = pricingSafeInteger + 1
	assertStatus(t, request(t, app, "POST", "/api/v1/orders/price-quotes", "valid", testQuoteKey, body), 400)
	withoutExpiry := quoteBody()
	delete(withoutExpiry, "orderExpiresAt")
	assertStatus(t, request(t, app, "POST", "/api/v1/orders/price-quotes", "valid", secondSupplierID, withoutExpiry), 200)
	if received.OrderExpiresAt != nil {
		t.Fatal("Order invented a quote expiry")
	}
	withoutExpiry["orderExpiresAt"] = nil
	assertStatus(t, request(t, app, "POST", "/api/v1/orders/price-quotes", "valid", secondSupplierID, withoutExpiry), 400)
	server.Close()
	assertStatus(t, request(t, app, "POST", "/api/v1/orders/price-quotes", "valid", testQuoteKey, quoteBody()), 503)
	// Advisory failure never affects creation or the user's selected offer.
	assertStatus(t, request(t, app, "POST", "/api/v1/orders", "valid", "manual-after-pricing", validCreateBody(9)), 201)
}

func TestPricingProxyRejectsMalformedOutputAndMapsErrors(t *testing.T) {
	app, _ := testApp(t)
	status := 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == 429 {
			w.Header().Set("Retry-After", "7")
			writeJSON(w, 429, map[string]any{"error": map[string]string{"code": "rate_limited", "message": "secret must not be forwarded"}})
			return
		}
		writeJSON(w, 200, map[string]any{"recommendedCredits": -1})
	}))
	defer server.Close()
	app.cfg.PricingURL = server.URL
	app.cfg.PricingToken = strings.Repeat("p", 64)
	assertStatus(t, request(t, app, "POST", "/api/v1/orders/price-quotes", "valid", testQuoteKey, quoteBody()), 503)
	status = 429
	response := request(t, app, "POST", "/api/v1/orders/price-quotes", "valid", testQuoteKey, quoteBody())
	assertStatus(t, response, 429)
	if response.Header().Get("Retry-After") != "7" || strings.Contains(response.Body.String(), "secret") {
		t.Fatal("unsafe error forwarding")
	}
	_, err := app.db.Exec(context.Background(), "UPDATE pricing_quote_limits SET requests=60")
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, request(t, app, "POST", "/api/v1/orders/price-quotes", "valid", testQuoteKey, quoteBody()), 429)
}

func TestPricingContextPrivateAndImmediateOnly(t *testing.T) {
	app, _ := testApp(t)
	app.cfg.PricingSourceToken = strings.Repeat("s", 64)
	assertStatus(t, request(t, app, "POST", "/api/v1/orders", "valid", "context-immediate", validCreateBody(8)), 201)
	// An imported scheduled commitment must not contribute to immediate demand.
	assertStatus(t, request(t, app, "POST", "/api/v1/orders", "valid", "context-scheduled", validCreateBody(8)), 201)
	_, err := app.db.Exec(context.Background(), `UPDATE orders SET window_start=expires_at-interval '30 minutes' WHERE id=$1`, orderID(testUserID, "context-scheduled"))
	if err != nil {
		t.Fatal(err)
	}
	call := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/internal/v1/pricing-context", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		app.InternalHandler().ServeHTTP(w, r)
		return w
	}
	assertStatus(t, call("invalid"), 401)
	response := call(app.cfg.PricingSourceToken)
	assertStatus(t, response, 200)
	var snapshot struct {
		AsOf, WindowStart, WindowEnd time.Time
		Areas                        []struct {
			LocationID                          string
			OpenRequestCount, CreatedOrderCount int
		}
	}
	if json.Unmarshal(response.Body.Bytes(), &snapshot) != nil || len(snapshot.Areas) != 1 || snapshot.Areas[0].LocationID != "com2" || snapshot.Areas[0].OpenRequestCount != 1 || snapshot.Areas[0].CreatedOrderCount != 1 || !snapshot.WindowEnd.Equal(snapshot.AsOf) || snapshot.AsOf.Sub(snapshot.WindowStart) != 15*time.Minute {
		t.Fatalf("invalid snapshot: %s", response.Body.String())
	}
	assertStatus(t, request(t, app, "GET", "/internal/v1/pricing-context", "valid", "", nil), 404)
}
