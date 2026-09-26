package supplier

import (
	"bytes"
	"context"
	"encoding/json"
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

func testApp(t *testing.T) *App {
	t.Helper()
	url := os.Getenv("SUPPLIER_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set SUPPLIER_TEST_DATABASE_URL for PostgreSQL integration tests")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	if cfg.ConnConfig.Database != "supplier_service_test" {
		t.Fatal("tests require the dedicated supplier_service_test database")
	}
	ctx := context.Background()
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer integration-credential" {
			t.Error("missing backend credential")
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid validation request")
		}
		roles := []string{"user"}
		if body["sessionToken"] == "admin" {
			roles = append(roles, "admin")
		}
		writeJSON(w, 200, Identity{UserID: testUserID, Roles: roles, ExpiresAt: time.Now().Add(time.Hour)})
	}))
	t.Cleanup(server.Close)
	return New(db, Config{Origin: "http://localhost:3000", UserServiceURL: server.URL, InternalToken: "integration-credential"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func request(t *testing.T, a *App, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.Header.Set("Origin", "http://localhost:3000")
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.AddCookie(&http.Cookie{Name: "foc_session", Value: token})
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func status(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("got %d, want %d: %s", w.Code, want, w.Body.String())
	}
}

func TestSupplierLifecycle(t *testing.T) {
	a := testApp(t)
	w := request(t, a, "GET", "/api/v1/suppliers", "user", nil)
	status(t, w, 200)
	var list struct {
		Suppliers []Supplier `json:"suppliers"`
	}
	if json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Suppliers) != 21 {
		t.Fatal("expected exactly 21 seeded suppliers after two migrations")
	}
	status(t, request(t, a, "GET", "/api/v1/locations", "user", nil), 200)
	body := map[string]any{"name": "Test Cafe", "category": "food", "locationId": "com2", "description": "At the entrance"}
	status(t, request(t, a, "POST", "/api/v1/suppliers", "user", body), 403)
	w = request(t, a, "POST", "/api/v1/suppliers", "admin", body)
	status(t, w, 201)
	var created struct {
		Supplier Supplier `json:"supplier"`
	}
	if json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatal("invalid create response")
	}
	id := created.Supplier.ID
	status(t, request(t, a, "GET", "/api/v1/suppliers/"+id, "user", nil), 200)
	body["name"] = " test cafe "
	status(t, request(t, a, "POST", "/api/v1/suppliers", "admin", body), 409)
	status(t, request(t, a, "PATCH", "/api/v1/suppliers/"+id, "user", map[string]bool{"active": false}), 403)
	status(t, request(t, a, "PATCH", "/api/v1/suppliers/"+id, "admin", map[string]bool{"active": false}), 200)
	status(t, request(t, a, "GET", "/api/v1/suppliers/"+id, "user", nil), 404)
	if err := Migrate(context.Background(), a.db); err != nil {
		t.Fatal(err)
	}
	status(t, request(t, a, "GET", "/api/v1/suppliers/"+id, "user", nil), 404)
	status(t, request(t, a, "POST", "/api/v1/suppliers", "admin", body), 201)
	status(t, request(t, a, "PATCH", "/api/v1/suppliers/"+id, "admin", map[string]bool{"active": true}), 409)
}

func TestConcurrentDuplicateCreate(t *testing.T) {
	a := testApp(t)
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- request(t, a, "POST", "/api/v1/suppliers", "admin", map[string]string{"name": "Concurrent", "category": "food", "locationId": "com2"}).Code
		}()
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for code := range results {
		counts[code]++
	}
	if counts[201] != 1 || counts[409] != 1 {
		t.Fatalf("expected one success and one conflict: %v", counts)
	}
}

func TestInputValidation(t *testing.T) {
	a := testApp(t)
	for _, body := range []any{
		map[string]string{"name": "", "category": "food", "locationId": "com2"},
		map[string]string{"name": "Cafe", "category": "invalid", "locationId": "com2"},
		map[string]string{"name": "Cafe", "category": "food", "locationId": "not-a-campus-location"},
		map[string]any{"name": "Cafe", "category": "food", "locationId": "com2", "isAdmin": true},
		nil,
	} {
		status(t, request(t, a, "POST", "/api/v1/suppliers", "admin", body), 400)
	}
	status(t, request(t, a, "GET", "/api/v1/suppliers/invalid", "user", nil), 400)
	status(t, request(t, a, "PATCH", "/api/v1/suppliers/"+testUserID, "admin", map[string]string{}), 400)
}
