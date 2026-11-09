package supplier

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
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
	w := request(t, a, "GET", "/api/v1/suppliers?pageSize=100", "user", nil)
	status(t, w, 200)
	var list struct {
		Suppliers []Supplier `json:"suppliers"`
	}
	if json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Suppliers) != 21 {
		t.Fatal("expected exactly 21 seeded suppliers after two migrations")
	}
	w = request(t, a, "GET", "/api/v1/locations", "user", nil)
	status(t, w, 200)
	var places struct {
		Locations []Location `json:"locations"`
	}
	if json.Unmarshal(w.Body.Bytes(), &places) != nil || len(places.Locations) != 15 {
		t.Fatal("expected the 15 seeded campus locations")
	}
	for _, l := range places.Locations {
		// Every seeded location lies on the NUS Kent Ridge campus.
		if l.Latitude < 1.28 || l.Latitude > 1.31 || l.Longitude < 103.76 || l.Longitude > 103.79 {
			t.Fatalf("location %s has off-campus coordinates %v,%v", l.ID, l.Latitude, l.Longitude)
		}
	}
	if _, err := a.db.Exec(context.Background(), "INSERT INTO locations(id,name) VALUES('nowhere','Nowhere')"); err == nil {
		t.Fatal("a location without coordinates was accepted")
	}
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
	status(t, request(t, a, "GET", "/api/v1/suppliers/"+id, "user", nil), 200)
	if err := Migrate(context.Background(), a.db); err != nil {
		t.Fatal(err)
	}
	status(t, request(t, a, "GET", "/api/v1/suppliers/"+id, "user", nil), 200)
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

func TestSupplierImages(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	var seeded int
	if err := a.db.QueryRow(ctx, "SELECT count(*) FROM suppliers WHERE image LIKE 'data:image/jpeg;base64,%'").Scan(&seeded); err != nil || seeded != 6 {
		t.Fatalf("expected the 6 template photos to be seeded, got %d (%v)", seeded, err)
	}
	// Lists never carry photos: Order reads 100 suppliers within 1 MiB.
	w := request(t, a, "GET", "/api/v1/suppliers?pageSize=100", "user", nil)
	status(t, w, 200)
	if strings.Contains(w.Body.String(), `"image"`) {
		t.Fatal("the supplier list returned photos")
	}
	w = request(t, a, "GET", "/api/v1/suppliers?q=Anna", "user", nil)
	var list struct {
		Suppliers []Supplier `json:"suppliers"`
	}
	if json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Suppliers) != 1 {
		t.Fatal("expected one seeded supplier named Anna")
	}
	anna := "/api/v1/suppliers/" + list.Suppliers[0].ID
	read := func(path string) Supplier {
		t.Helper()
		w := request(t, a, "GET", path, "user", nil)
		status(t, w, 200)
		var body struct {
			Supplier Supplier `json:"supplier"`
		}
		if json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatal("invalid supplier response")
		}
		return body.Supplier
	}
	// Order resolves pickups without include and reads at most 64 KiB.
	if w = request(t, a, "GET", anna, "user", nil); strings.Contains(w.Body.String(), `"image"`) || w.Body.Len() > 64*1024 {
		t.Fatal("a plain supplier read returned the photo")
	}
	if s := read(anna + "?include=image"); !strings.HasPrefix(s.Image, "data:image/jpeg;base64,") {
		t.Fatal("the seeded photo was not returned on request")
	}
	status(t, request(t, a, "GET", anna+"?include=photos", "user", nil), 400)

	photo := dataURL("png", encodePNG(t, image.NewRGBA(image.Rect(0, 0, 3, 2))))
	body := map[string]any{"name": "Photo Kiosk", "category": "retail", "locationId": "com2", "image": photo}
	status(t, request(t, a, "POST", "/api/v1/suppliers", "user", body), 403)
	body["image"] = "data:image/png;base64,bad"
	if w = request(t, a, "POST", "/api/v1/suppliers", "admin", body); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_image") {
		t.Fatalf("an invalid photo was not rejected: %d %s", w.Code, w.Body.String())
	}
	body["image"] = photo
	w = request(t, a, "POST", "/api/v1/suppliers", "admin", body)
	status(t, w, 201)
	var created struct {
		Supplier Supplier `json:"supplier"`
	}
	if json.Unmarshal(w.Body.Bytes(), &created) != nil || !strings.HasPrefix(created.Supplier.Image, "data:image/jpeg;base64,") {
		t.Fatal("the created supplier's photo was not stored as JPEG")
	}
	path := "/api/v1/suppliers/" + created.Supplier.ID
	stored := read(path + "?include=image").Image
	if stored != created.Supplier.Image {
		t.Fatal("the read photo differs from the saved one")
	}
	// Editing other fields keeps the photo; an empty string removes it.
	status(t, request(t, a, "PATCH", path, "admin", map[string]string{"description": "Next to LT19"}), 200)
	if read(path+"?include=image").Image != stored {
		t.Fatal("a text edit changed the photo")
	}
	status(t, request(t, a, "PATCH", path, "admin", map[string]string{"image": ""}), 200)
	if read(path+"?include=image").Image != "" {
		t.Fatal("the photo was not removed")
	}
	status(t, request(t, a, "PATCH", path, "admin", map[string]string{"image": photo}), 200)
	// A supplier without a photo is created normally.
	status(t, request(t, a, "POST", "/api/v1/suppliers", "admin", map[string]string{"name": "No Photo", "category": "food", "locationId": "com2"}), 201)
	// Deleting clears the photo, and the database refuses one on a deleted row.
	status(t, request(t, a, "DELETE", path, "admin", nil), 204)
	var remaining string
	if err := a.db.QueryRow(ctx, "SELECT image FROM suppliers WHERE id=$1", created.Supplier.ID).Scan(&remaining); err != nil || remaining != "" {
		t.Fatal("deleting a supplier kept its photo")
	}
	if _, err := a.db.Exec(ctx, "UPDATE suppliers SET image='x' WHERE id=$1", created.Supplier.ID); err == nil {
		t.Fatal("a deleted supplier accepted a photo")
	}
	// A request body over 1 MiB is refused before decoding.
	body["name"], body["image"] = "Huge", "data:image/png;base64,"+strings.Repeat("A", 1024*1024)
	status(t, request(t, a, "POST", "/api/v1/suppliers", "admin", body), 400)
}
