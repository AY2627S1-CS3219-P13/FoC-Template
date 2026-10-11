package order

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequiredDetailsAndNumericLimits(t *testing.T) {
	app, f := testApp(t)
	cases := []struct {
		field string
		value any
	}{
		{"pickup", nil}, {"delivery", nil}, {"items", nil}, {"expiresAt", nil}, {"offeredCredits", nil},
		{"items", []any{}},
		{"items", []any{map[string]any{"name": "Tea"}}},
		{"items", []any{map[string]any{"name": "Tea", "quantity": 0}}},
		{"items", []any{map[string]any{"name": "Tea", "quantity": -1}}},
		{"items", []any{map[string]any{"name": "Tea", "quantity": 1.5}}},
		{"items", []any{map[string]any{"name": " \t\n", "quantity": 1}}},
		{"items", []any{map[string]any{"name": "Tea", "quantity": json.Number("9223372036854775808")}}},
		{"offeredCredits", json.Number("9223372036854775808")},
		{"offeredCredits", "30"}, {"offeredCredits", 1.5},
		{"requesterId", testUserID},
		{"expiresAt", time.Now().Add(-time.Second).Format(time.RFC3339)},
		{"expiresAt", "2099-01-01T10:00:00"},
	}
	for i, tc := range cases {
		body := validCreateBody(1)
		body[tc.field] = tc.value
		assertStatus(t, request(t, app, "POST", "/api/v1/orders", "session", fmt.Sprintf("invalid-%d", i), body), 400)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reserveEffects != 0 {
		t.Fatal("invalid input reserved credits")
	}
}

func TestMoreThanFiveItemsAndLongTextArePreserved(t *testing.T) {
	app, _ := testApp(t)
	name, description := strings.Repeat("T", 10000), strings.Repeat("long text\r\n", 1000)
	body := validCreateBody(1)
	items := make([]Item, 6)
	for i := range items {
		items[i] = Item{Name: name, Description: description, Quantity: 9223372036854775807}
	}
	body["items"] = items
	response := request(t, app, "POST", "/api/v1/orders", "session", "six-items", body)
	assertStatus(t, response, 201)
	var result struct{ Order Order }
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Order.Items) != 6 || result.Order.Items[5] != items[5] {
		t.Fatal("text or quantity changed")
	}
}

func TestSameSupplierDifferentUUIDCaseIsNotDistinct(t *testing.T) {
	app, _ := testApp(t)
	body := validCreateBody(1)
	body["pickup"] = PlaceRef{Type: "supplier", ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
	body["delivery"] = PlaceRef{Type: "supplier", ID: "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"}
	response := request(t, app, "POST", "/api/v1/orders", "session", "uuid-case", body)
	assertStatus(t, response, 400)
	if !strings.Contains(response.Body.String(), "places_must_differ") {
		t.Fatal(response.Body.String())
	}
}

func TestDifferentSuppliersAtSameCampusAreDistinct(t *testing.T) {
	app, _ := testApp(t)
	body := validCreateBody(1)
	body["delivery"] = PlaceRef{Type: "supplier", ID: secondSupplierID}
	assertStatus(t, request(t, app, "POST", "/api/v1/orders", "session", "two-suppliers", body), 201)
}

func TestDeliveryPlaceValidationMatchesPickup(t *testing.T) {
	app, _ := testApp(t)
	for i, place := range []PlaceRef{
		{Type: "supplier", ID: inactiveSupplierID},
		{Type: "supplier", ID: "55555555-5555-4555-8555-555555555555"},
		{Type: "location", ID: "missing"},
	} {
		body := validCreateBody(1)
		body["delivery"] = place
		assertStatus(t, request(t, app, "POST", "/api/v1/orders", "session", fmt.Sprintf("bad-delivery-%d", i), body), 400)
	}
}

func TestAuthOutagesAndMalformedIdentityFailClosed(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{401, `{"error":{"code":"invalid_service_credentials"}}`},
		{503, `{"error":{"code":"unavailable"}}`},
		{200, `{}`},
		{200, `{"userId":"invalid","roles":["user"],"expiresAt":"2099-01-01T00:00:00Z"}`},
		{200, `{"userId":"11111111-1111-4111-8111-111111111111","roles":["user"],"expiresAt":"2000-01-01T00:00:00Z"}`},
		{200, `{"userId":"11111111-1111-4111-8111-111111111111","roles":[],"expiresAt":"2099-01-01T00:00:00Z"}`},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		auth := AuthClient{URL: server.URL, CookieName: "foc_session", Client: server.Client()}
		req := httptest.NewRequest("POST", "/", nil)
		req.AddCookie(&http.Cookie{Name: "foc_session", Value: "session"})
		_, err := auth.Validate(req)
		server.Close()
		if err == nil || err.Error() != "auth_unavailable" {
			t.Fatalf("%d/%s: %v", tc.status, tc.body, err)
		}
	}
}

func TestNoRequesterCountLimitAndAvailableBalanceOnly(t *testing.T) {
	app, f := testApp(t)
	for i := 0; i < 10; i++ {
		assertStatus(t, request(t, app, "POST", "/api/v1/orders", "session", fmt.Sprintf("many-%d", i), validCreateBody(10)), 201)
	}
	assertStatus(t, request(t, app, "POST", "/api/v1/orders", "session", "over-budget", validCreateBody(1)), 409)
	assertFinancialResult(t, app, f, "many-9", "completed", 10, 0)
}

func TestMaximumRepresentableCreditOfferIsAccepted(t *testing.T) {
	app, f := testApp(t)
	f.mu.Lock()
	f.available = 9223372036854775807
	f.mu.Unlock()
	assertStatus(t, request(t, app, "POST", "/api/v1/orders", "session", "max-offer", validCreateBody(9223372036854775807)), 201)
	assertFinancialResult(t, app, f, "max-offer", "completed", 1, 0)
}

func TestEquivalentExpiryOffsetsRetrySameAttempt(t *testing.T) {
	app, _ := testApp(t)
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	body := validCreateBody(1)
	body["expiresAt"] = expiry.UTC().Format(time.RFC3339)
	assertStatus(t, request(t, app, "POST", "/api/v1/orders", "session", "offset", body), 201)
	body["expiresAt"] = expiry.In(time.FixedZone("SGT", 28800)).Format(time.RFC3339)
	assertStatus(t, request(t, app, "POST", "/api/v1/orders", "session", "offset", body), 200)
}

func TestPreMigrationSavedSuccessRemainsRetryable(t *testing.T) {
	app, _ := testApp(t)
	body := validCreateBody(1)
	assertStatus(t, request(t, app, "POST", "/api/v1/orders", "session", "legacy-success", body), 201)
	raw, _ := json.Marshal(body)
	var parsed createRequest
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	_, err := app.db.Exec(context.Background(), "UPDATE creation_attempts SET request_fingerprint=$1,offered_credits=NULL WHERE idempotency_key='legacy-success'", requestFingerprint(parsed))
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, request(t, app, "POST", "/api/v1/orders", "session", "legacy-success", body), 200)
}

func TestOriginAndTransportLimit(t *testing.T) {
	app, _ := testApp(t)
	for _, origin := range []string{"", "https://foreign.example"} {
		req := httptest.NewRequest("POST", "/api/v1/orders", strings.NewReader("{}"))
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		assertStatus(t, rec, 403)
	}
	body := validCreateBody(1)
	body["items"] = []Item{{Name: strings.Repeat("a", 1024*1024), Quantity: 1}}
	assertStatus(t, request(t, app, "POST", "/api/v1/orders", "session", "too-large", body), 413)
}

func TestUnicodeDuplicateWarnings(t *testing.T) {
	for _, names := range [][2]string{{"Milk\tTea", " milktea "}, {"ΟΣ", "ος"}, {"ſoup", "Soup"}, {"a\u2003b", "AB"}} {
		if duplicateKey(names[0]) != duplicateKey(names[1]) {
			t.Fatalf("not folded: %q", names)
		}
	}
}

func TestCreditAdapterRejectsMalformedAndMisclassifiedResponses(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{200, "{}"}, {200, "null"}, {200, "not json"},
		{200, `{"userId":"wrong-user","available":10,"reserved":30}`},
		{200, `{"userId":"11111111-1111-4111-8111-111111111111","available":-1,"reserved":30}`},
		{503, `{"error":{"code":"insufficient_credits"}}`},
		{401, `{"error":{"code":"wallet_not_found"}}`},
		{409, `{"error":{"code":"insufficient_credits"}}{}`},
	} {
		t.Run(fmt.Sprintf("%d-%s", tc.status, tc.body), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := CreditClient{BaseURL: server.URL, Client: server.Client()}
			err := client.Reserve(context.Background(), "id", testUserID, 30)
			if err == nil || err.Error() != "credit_unavailable" {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestCatalogueDistinguishesMissingFromOutage(t *testing.T) {
	for _, tc := range []struct {
		status     int
		body, code string
	}{
		{404, "", "invalid_place"},
		{503, "", "places_unavailable"},
		{200, "{}", "places_unavailable"},
		{200, `{"supplier":{"id":"22222222-2222-4222-8222-222222222222","name":"Cafe","locationId":"com2"}}`, "places_unavailable"},
		{200, `{"supplier":{"id":"22222222-2222-4222-8222-222222222222","name":"Cafe","locationId":"com2","active":false}}`, "invalid_place"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/locations" {
				_, _ = w.Write([]byte(`{"locations":[{"id":"com2","name":"COM2"}]}`))
				return
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		client := CatalogueClient{BaseURL: server.URL, CookieName: "foc_session", Client: server.Client()}
		_, err := client.Resolve(context.Background(), PlaceRef{Type: "supplier", ID: testSupplierID}, "session")
		server.Close()
		if err == nil || err.Error() != tc.code {
			t.Fatalf("%d/%s: got %v", tc.status, tc.body, err)
		}
	}
}

func TestAuthFailClosedAndNoCrossRequestCache(t *testing.T) {
	var responses int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responses++
		if responses == 1 {
			writeJSON(w, 200, Identity{UserID: testUserID, Roles: []string{"user"}, ExpiresAt: time.Now().Add(time.Hour)})
		} else {
			writeJSON(w, 401, map[string]any{"error": map[string]string{"code": "invalid_session"}})
		}
	}))
	defer server.Close()
	auth := AuthClient{URL: server.URL, CookieName: "foc_session", Client: server.Client()}
	req := httptest.NewRequest("POST", "/", nil)
	req.AddCookie(&http.Cookie{Name: "foc_session", Value: "session"})
	if _, err := auth.Validate(req); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Validate(req); err == nil || err.Error() != "invalid_session" {
		t.Fatalf("revocation ignored: %v", err)
	}
}

func TestConfigurationRejectsCredentialBearingAndMalformedURLs(t *testing.T) {
	for key, value := range map[string]string{
		"ORDER_DATABASE_URL": "postgres://localhost/order", "ORDER_APP_ORIGIN": "http://localhost:3000",
		"ORDER_USER_SERVICE_URL": "http://user-service:8081", "ORDER_SUPPLIER_SERVICE_URL": "http://supplier-service:8080",
		"ORDER_CREDIT_SERVICE_URL": "http://credit-service:8081", "ORDER_SESSION_COOKIE": "foc_session",
		"ORDER_USER_INTERNAL_TOKEN": strings.Repeat("u", 32), "ORDER_CREDIT_INTERNAL_TOKEN": strings.Repeat("c", 32),
	} {
		t.Setenv(key, value)
	}
	if _, err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"http://user:secret@localhost", "http://localhost?query", "http://localhost#fragment", "http://localhost/", "ftp://localhost"} {
		t.Setenv("ORDER_USER_SERVICE_URL", value)
		if _, err := LoadConfig(); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
