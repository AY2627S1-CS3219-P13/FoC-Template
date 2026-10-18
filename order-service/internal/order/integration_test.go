package order

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	testUserID         = "11111111-1111-4111-8111-111111111111"
	testSupplierID     = "22222222-2222-4222-8222-222222222222"
	inactiveSupplierID = "33333333-3333-4333-8333-333333333333"
	secondSupplierID   = "44444444-4444-4444-8444-444444444444"
)

type fakeDependencies struct {
	server    *httptest.Server
	mu        sync.Mutex
	available int64
	reserved  int64
	escrows   map[string]struct {
		user     string
		amount   int64
		released bool
	}
	reserveEffects, releases int
	payouts                  int
	paid                     map[string]string
}

func newFakeDependencies(t *testing.T) *fakeDependencies {
	t.Helper()
	f := &fakeDependencies{available: 100, escrows: make(map[string]struct {
		user     string
		amount   int64
		released bool
	})}
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeDependencies) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/internal/v1/sessions/validate":
		if r.Header.Get("Authorization") != "Bearer user-internal-token-for-tests-123456" {
			writeJSON(w, 401, map[string]any{"error": map[string]string{"code": "invalid_service_credentials"}})
			return
		}
		var request map[string]string
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request["sessionToken"] == "invalid" {
			writeJSON(w, 401, map[string]any{"error": map[string]string{"code": "invalid_session"}})
			return
		}
		writeJSON(w, 200, Identity{UserID: testUserID, Roles: []string{"user"}, ExpiresAt: time.Now().Add(time.Hour)})
	case r.URL.Path == "/api/v1/locations":
		if cookie, _ := r.Cookie("foc_session"); cookie == nil {
			w.WriteHeader(401)
			return
		}
		writeJSON(w, 200, map[string]any{"locations": []map[string]string{{"id": "com2", "name": "COM2"}, {"id": "utown", "name": "UTown"}}})
	case r.URL.Path == "/api/v1/suppliers":
		writeJSON(w, 200, map[string]any{"suppliers": []map[string]any{{"id": testSupplierID, "name": "Campus Cafe", "locationId": "com2", "description": "Level 1", "openingHours": "09:00-17:00", "active": true}}, "page": 1, "pageSize": 100, "total": 1, "totalPages": 1})
	case strings.HasPrefix(r.URL.Path, "/api/v1/suppliers/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/suppliers/")
		if id != testSupplierID && id != inactiveSupplierID && id != secondSupplierID {
			w.WriteHeader(404)
			return
		}
		writeJSON(w, 200, map[string]any{"supplier": map[string]any{"id": id, "name": "Campus Cafe", "locationId": "com2", "description": "Level 1", "openingHours": "09:00-17:00", "active": id != inactiveSupplierID}})
	case strings.HasPrefix(r.URL.Path, "/internal/v1/escrows/"):
		if r.Header.Get("Authorization") != "Bearer credit-internal-token-for-tests-1234" {
			w.WriteHeader(401)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/internal/v1/escrows/")
		if strings.HasSuffix(id, "/payout") {
			id = strings.TrimSuffix(id, "/payout")
			var body struct {
				CourierID string `json:"courierId"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			defer f.mu.Unlock()
			escrow, ok := f.escrows[id]
			if !ok {
				writeJSON(w, 404, map[string]any{"error": map[string]string{"code": "escrow_not_found"}})
				return
			}
			if escrow.released || (f.paid[id] != "" && f.paid[id] != body.CourierID) {
				writeJSON(w, 409, map[string]any{"error": map[string]string{"code": "escrow_settled"}})
				return
			}
			if body.CourierID == "" || body.CourierID == escrow.user {
				w.WriteHeader(400)
				return
			}
			if f.paid[id] == "" {
				if f.paid == nil {
					f.paid = map[string]string{}
				}
				f.paid[id] = body.CourierID
				f.reserved -= escrow.amount
				f.payouts++
			}
			w.WriteHeader(204)
			return
		}
		if strings.HasSuffix(id, "/release") {
			id = strings.TrimSuffix(id, "/release")
			f.mu.Lock()
			escrow, ok := f.escrows[id]
			if f.paid[id] != "" {
				f.mu.Unlock()
				writeJSON(w, 409, map[string]any{"error": map[string]string{"code": "escrow_settled"}})
				return
			}
			if ok && !escrow.released {
				escrow.released = true
				f.escrows[id] = escrow
				f.available += escrow.amount
				f.reserved -= escrow.amount
				f.releases++
			}
			available := f.available
			reserved := f.reserved
			f.mu.Unlock()
			if !ok {
				writeJSON(w, 404, map[string]any{"error": map[string]string{"code": "escrow_not_found"}})
				return
			}
			writeJSON(w, 200, map[string]any{"userId": testUserID, "available": available, "reserved": reserved})
			return
		}
		var request struct {
			UserID string `json:"userId"`
			Amount int64  `json:"amount"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Amount == 997 {
			w.WriteHeader(503)
			return
		}
		if request.Amount == 998 {
			writeJSON(w, 404, map[string]any{"error": map[string]string{"code": "wallet_not_found"}})
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if old, ok := f.escrows[id]; ok {
			if old.user != request.UserID || old.amount != request.Amount {
				writeJSON(w, 409, map[string]any{"error": map[string]string{"code": "idempotency_conflict"}})
				return
			}
			writeJSON(w, 200, map[string]any{"userId": testUserID, "available": f.available, "reserved": f.reserved})
			return
		}
		if request.Amount > f.available {
			writeJSON(w, 409, map[string]any{"error": map[string]string{"code": "insufficient_credits"}})
			return
		}
		f.available -= request.Amount
		f.reserved += request.Amount
		f.escrows[id] = struct {
			user     string
			amount   int64
			released bool
		}{request.UserID, request.Amount, false}
		f.reserveEffects++
		writeJSON(w, 200, map[string]any{"userId": testUserID, "available": f.available, "reserved": f.reserved})
	default:
		w.WriteHeader(404)
	}
}

func testApp(t *testing.T) (*App, *fakeDependencies) {
	t.Helper()
	databaseURL := os.Getenv("ORDER_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ORDER_TEST_DATABASE_URL for PostgreSQL integration tests")
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Database != "order_service_test" {
		t.Fatal("tests require the dedicated order_service_test database")
	}
	ctx := context.Background()
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + strings.NewReplacer("/", "_", "-", "_").Replace(strings.ToLower(t.Name()))
	if len(schema) > 40 {
		schema = schema[:40]
	}
	schema += fmt.Sprintf("_%x", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	})
	if err = Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, db); err != nil {
		t.Fatal("repeat migration:", err)
	}
	fakes := newFakeDependencies(t)
	appConfig := Config{Origin: "http://localhost:3000", SessionCookie: "foc_session", UserServiceURL: fakes.server.URL,
		UserServiceToken: "user-internal-token-for-tests-123456", SupplierServiceURL: fakes.server.URL,
		CreditServiceURL: fakes.server.URL, CreditServiceToken: "credit-internal-token-for-tests-1234",
		DependencyTimeout: 2 * time.Second, RecoveryInterval: time.Second, RecoveryStaleAfter: time.Millisecond}
	return New(db, appConfig, slog.New(slog.NewTextHandler(io.Discard, nil))), fakes
}

func validCreateBody(offer int64) map[string]any {
	return map[string]any{
		"pickup":         map[string]string{"type": "supplier", "id": testSupplierID},
		"delivery":       map[string]string{"type": "location", "id": "utown"},
		"items":          []map[string]any{{"name": "Milk Tea", "description": "Less sugar", "quantity": 2}},
		"offeredCredits": offer, "expiresAt": time.Now().Add(time.Hour).In(time.FixedZone("SGT", 8*60*60)).Format(time.RFC3339),
	}
}

func request(t *testing.T, app *App, method, path, token, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return rawRequest(app, method, path, token, key, data)
}

func rawRequest(app *App, method, path, token, key string, data []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.Header.Set("Origin", "http://localhost:3000")
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	if token != "" {
		r.AddCookie(&http.Cookie{Name: "foc_session", Value: token})
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	return w
}

func assertStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("got %d, want %d: %s", response.Code, want, response.Body.String())
	}
}

func TestCreateAndIdempotentRetry(t *testing.T) {
	app, fakes := testApp(t)
	body := validCreateBody(30)
	first := request(t, app, http.MethodPost, "/api/v1/orders", "session", "attempt-1", body)
	assertStatus(t, first, 201)
	second := request(t, app, http.MethodPost, "/api/v1/orders", "session", "attempt-1", body)
	assertStatus(t, second, 200)
	var got struct {
		Order Order `json:"order"`
	}
	if json.Unmarshal(first.Body.Bytes(), &got) != nil {
		t.Fatal("invalid response")
	}
	if got.Order.Status != "open" || got.Order.Pickup.LocationName != "COM2" || got.Order.Delivery.Name != "UTown" || got.Order.OfferedCredits != 30 {
		t.Fatalf("unexpected order: %+v", got.Order)
	}
	if _, offset := got.Order.ExpiresAt.Zone(); offset != 8*60*60 {
		t.Fatal("expiry not returned in Singapore time")
	}
	fakes.mu.Lock()
	defer fakes.mu.Unlock()
	if fakes.reserveEffects != 1 {
		t.Fatalf("got %d reservation effects", fakes.reserveEffects)
	}
}

func TestIdempotencyConflict(t *testing.T) {
	app, _ := testApp(t)
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", "same-key", validCreateBody(20)), 201)
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", "same-key", validCreateBody(21)), 409)
}

func TestConcurrentDoubleClickCreatesOneOrder(t *testing.T) {
	app, fakes := testApp(t)
	body := validCreateBody(30)
	var wait sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- request(t, app, http.MethodPost, "/api/v1/orders", "session", "double-click", body).Code
		}()
	}
	wait.Wait()
	close(results)
	counts := map[int]int{}
	for status := range results {
		counts[status]++
	}
	if counts[201] != 1 || counts[200] != 1 {
		t.Fatalf("unexpected statuses: %v", counts)
	}
	fakes.mu.Lock()
	defer fakes.mu.Unlock()
	if fakes.reserveEffects != 1 {
		t.Fatalf("got %d reservation effects", fakes.reserveEffects)
	}
}

func TestLostResponseRetryReturnsCompletedOrderAfterExpiry(t *testing.T) {
	app, _ := testApp(t)
	now := time.Now()
	app.now = func() time.Time { return now }
	body := validCreateBody(10)
	body["expiresAt"] = now.Add(time.Minute).Format(time.RFC3339)
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", "lost-response", body), 201)
	app.now = func() time.Time { return now.Add(2 * time.Minute) }
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", "lost-response", body), 200)
}

func TestSupplierAndItsCampusLocationAreDistinct(t *testing.T) {
	app, _ := testApp(t)
	body := validCreateBody(10)
	body["delivery"] = map[string]string{"type": "location", "id": "com2"}
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", "same-campus", body), 201)
}

func TestCreateValidationAndDependencies(t *testing.T) {
	app, _ := testApp(t)
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "", "key", validCreateBody(1)), 401)
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "invalid", "key", validCreateBody(1)), 401)
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", "", validCreateBody(1)), 400)
	for _, offer := range []int64{0, -1} {
		assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", fmt.Sprintf("offer-%d", offer), validCreateBody(offer)), 400)
	}
	assertStatus(t, rawRequest(app, http.MethodPost, "/api/v1/orders", "session", "fraction", []byte(`{"pickup":{"type":"location","id":"com2"},"delivery":{"type":"location","id":"utown"},"items":[{"name":"x","quantity":1.5}],"offeredCredits":1,"expiresAt":"2099-01-01T00:00:00+08:00"}`)), 400)
	noItems := validCreateBody(1)
	noItems["items"] = []any{}
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", "no-items", noItems), 400)
	same := validCreateBody(1)
	same["pickup"] = map[string]string{"type": "location", "id": "utown"}
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", "same", same), 400)
	inactive := validCreateBody(1)
	inactive["pickup"] = map[string]string{"type": "supplier", "id": inactiveSupplierID}
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", "inactive", inactive), 400)
	missing := validCreateBody(1)
	missing["pickup"] = map[string]string{"type": "location", "id": "missing"}
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", "missing", missing), 400)
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", "pending-wallet", validCreateBody(998)), 409)
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", "credit-down", validCreateBody(997)), 503)
}

func TestDuplicateItemDecision(t *testing.T) {
	app, _ := testApp(t)
	body := map[string]any{"existingItems": []map[string]any{{"name": "Milk Tea", "quantity": 1}}, "item": map[string]any{"name": " milktea ", "description": "Other", "quantity": 3}}
	response := request(t, app, http.MethodPost, "/api/v1/orders/items/add", "session", "", body)
	assertStatus(t, response, 200)
	var result struct {
		Duplicate, RequiresDecision, Added bool
		Items                              []Item `json:"items"`
	}
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || !result.Duplicate || !result.RequiresDecision || result.Added || len(result.Items) != 1 {
		t.Fatalf("unexpected warning: %s", response.Body.String())
	}
	body["decision"] = "proceed"
	response = request(t, app, http.MethodPost, "/api/v1/orders/items/add", "session", "", body)
	assertStatus(t, response, 200)
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || !result.Added || len(result.Items) != 2 {
		t.Fatalf("unexpected proceed: %s", response.Body.String())
	}
	body["decision"] = "do_not_proceed"
	response = request(t, app, http.MethodPost, "/api/v1/orders/items/add", "session", "", body)
	assertStatus(t, response, 200)
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Added || len(result.Items) != 1 {
		t.Fatalf("unexpected rejection: %s", response.Body.String())
	}
}

func TestConcurrentCreationsCannotOverspend(t *testing.T) {
	app, fakes := testApp(t)
	var wait sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func(n int) {
			defer wait.Done()
			results <- request(t, app, http.MethodPost, "/api/v1/orders", "session", fmt.Sprintf("parallel-%d", n), validCreateBody(60)).Code
		}(i)
	}
	wait.Wait()
	close(results)
	counts := map[int]int{}
	for status := range results {
		counts[status]++
	}
	if counts[201] != 1 || counts[409] != 1 {
		t.Fatalf("unexpected statuses: %v", counts)
	}
	fakes.mu.Lock()
	defer fakes.mu.Unlock()
	if fakes.available != 40 || fakes.reserveEffects != 1 {
		t.Fatalf("unexpected credit state: available=%d effects=%d", fakes.available, fakes.reserveEffects)
	}
}

func TestFailedOrderWriteReleasesReservation(t *testing.T) {
	app, fakes := testApp(t)
	_, err := app.db.Exec(context.Background(), `CREATE FUNCTION reject_order() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test rejection'; END $$`)
	if err == nil {
		_, err = app.db.Exec(context.Background(), `CREATE TRIGGER reject_order BEFORE INSERT ON orders FOR EACH ROW EXECUTE FUNCTION reject_order()`)
	}
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, app, http.MethodPost, "/api/v1/orders", "session", "failed-write", validCreateBody(30))
	assertStatus(t, response, 500)
	fakes.mu.Lock()
	defer fakes.mu.Unlock()
	if fakes.available != 100 || fakes.releases != 1 {
		t.Fatalf("reservation not compensated: available=%d releases=%d", fakes.available, fakes.releases)
	}
	var state string
	if err = app.db.QueryRow(context.Background(), "SELECT state FROM creation_attempts WHERE idempotency_key='failed-write'").Scan(&state); err != nil || state != "abandoned" {
		t.Fatalf("state=%q err=%v", state, err)
	}
}

func TestRecoveryReleasesStaleAttempt(t *testing.T) {
	app, fakes := testApp(t)
	id := orderID(testUserID, "crashed")
	if err := app.credit.Reserve(context.Background(), id, testUserID, 25); err != nil {
		t.Fatal(err)
	}
	_, err := app.db.Exec(context.Background(), `INSERT INTO creation_attempts(requester_id,idempotency_key,order_id,request_fingerprint,state,updated_at)
		VALUES($1,'crashed',$2,'fingerprint','pending',now()-interval '1 minute')`, testUserID, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = recoverForTest(app, context.Background()); err != nil {
		t.Fatal(err)
	}
	fakes.mu.Lock()
	defer fakes.mu.Unlock()
	if fakes.available != 100 || fakes.releases != 1 {
		t.Fatal("stale reservation was not released")
	}
}
