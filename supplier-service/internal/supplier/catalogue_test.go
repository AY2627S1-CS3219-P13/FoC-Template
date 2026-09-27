package supplier

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func record(t *testing.T, w *httptest.ResponseRecorder) Supplier {
	t.Helper()
	var result struct {
		Supplier Supplier `json:"supplier"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Supplier
}

func cataloguePage(t *testing.T, a *App, query string) pageResult {
	t.Helper()
	w := request(t, a, "GET", "/api/v1/suppliers?"+query, "user", nil)
	status(t, w, 200)
	var result pageResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestEditingAndDeletion(t *testing.T) {
	a := testApp(t)
	body := map[string]any{"name": "D2 Original", "category": "food", "locationId": "com2", "openingHours": "Mon-Fri\n9-5"}
	w := request(t, a, "POST", "/api/v1/suppliers", "admin", body)
	status(t, w, 201)
	original := record(t, w)
	path := "/api/v1/suppliers/" + original.ID
	patch := map[string]any{"name": " D2 Edited ", "category": "printing", "locationId": "com3", "description": "Upstairs\nNear the lift", "openingHours": "", "active": false}
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		status(t, request(t, a, method, path, "", patch), 401)
	}
	for _, method := range []string{"PATCH", "DELETE"} {
		status(t, request(t, a, method, path, "user", patch), 403)
	}
	w = request(t, a, "PATCH", path, "admin", patch)
	status(t, w, 200)
	updated := record(t, w)
	if updated.Name != "D2 Edited" || updated.Category != "printing" || updated.LocationID != "com3" || updated.OpeningHours != "" || updated.Active || updated.Description != "Upstairs\nNear the lift" || !updated.CreatedAt.Equal(original.CreatedAt) || updated.UpdatedAt.Before(original.UpdatedAt) {
		t.Fatal("patch did not preserve/update expected fields")
	}
	status(t, request(t, a, "GET", path, "user", nil), 200)
	if cataloguePage(t, a, "q=D2").Total != 0 || cataloguePage(t, a, "q=D2&status=inactive").Total != 1 {
		t.Fatal("inactive visibility")
	}
	for _, bad := range []any{
		map[string]any{"name": ""}, map[string]any{"category": "unknown"}, map[string]any{"locationId": "outside-campus"},
		map[string]any{"openingHours": strings.Repeat("a", 201)}, map[string]any{"id": original.ID},
		map[string]any{"createdAt": "2020-01-01T00:00:00Z"}, map[string]any{"deletedAt": nil},
	} {
		status(t, request(t, a, "PATCH", path, "admin", bad), 400)
	}
	status(t, request(t, a, "PATCH", path, "admin", map[string]bool{"active": true}), 200)
	status(t, request(t, a, "DELETE", path, "admin", nil), 204)
	status(t, request(t, a, "DELETE", path, "admin", nil), 404)
	status(t, request(t, a, "GET", path, "user", nil), 404)
	status(t, request(t, a, "PATCH", path, "admin", map[string]bool{"active": true}), 404)
	if cataloguePage(t, a, "q=D2&status=all").Total != 0 {
		t.Fatal("deleted row exposed")
	}
	var retained bool
	if err := a.db.QueryRow(context.Background(), "SELECT deleted_at IS NOT NULL AND NOT active FROM suppliers WHERE id=$1", original.ID).Scan(&retained); err != nil || !retained {
		t.Fatal("soft deletion must retain inactive row", err)
	}
	status(t, request(t, a, "POST", "/api/v1/suppliers", "admin", patch), 201)
}

func TestCatalogueQueries(t *testing.T) {
	a := testApp(t)
	for _, b := range []map[string]any{
		{"name": "D2 Gamma", "category": "food", "locationId": "com2", "active": false},
		{"name": "D2 Alpha", "category": "printing", "locationId": "com3"},
		{"name": "D2 Beta", "category": "food", "locationId": "com2"},
	} {
		status(t, request(t, a, "POST", "/api/v1/suppliers", "admin", b), 201)
	}
	first := cataloguePage(t, a, "q=d2&status=all&pageSize=2")
	second := cataloguePage(t, a, "q=d2&status=all&pageSize=2&page=2")
	if first.Total != 3 || first.TotalPages != 2 || len(first.Suppliers) != 2 || first.Suppliers[0].Name != "D2 Alpha" || len(second.Suppliers) != 1 || second.Suppliers[0].Name != "D2 Gamma" {
		t.Fatal("pagination/sorting mismatch")
	}
	last := cataloguePage(t, a, "q=d2&status=all&pageSize=2&page=3")
	if len(last.Suppliers) != 0 || last.Total != 3 {
		t.Fatal("out of range page loses total")
	}
	desc := cataloguePage(t, a, "q=d2&status=all&direction=desc")
	if desc.Suppliers[0].Name != "D2 Gamma" {
		t.Fatal("descending order")
	}
	for query, count := range map[string]int{
		"q=D2": 2, "q=D2&status=inactive": 1, "q=D2&status=all&locationId=com2&category=food": 2,
		"q=D2&locationId=com3&category=food": 0, "q=%25": 0, "q=%27%20OR%201%3D1--": 0,
	} {
		if cataloguePage(t, a, query).Total != count {
			t.Fatalf("unexpected count for %s", query)
		}
	}
	for _, sort := range []string{"category", "location", "createdAt"} {
		cataloguePage(t, a, "q=D2&sort="+sort)
	}
	for _, query := range []string{"page=0", "page=-1", "page=100001", "pageSize=101", "pageSize=", "pageSize=no", "category=invalid", "status=deleted", "direction=sideways", "sort=name;DROP", "locationId=outside-campus", "q=" + strings.Repeat("a", 101), "page=1&page=2", "unsupported=true"} {
		status(t, request(t, a, "GET", "/api/v1/suppliers?"+query, "user", nil), 400)
	}
}

func TestDuplicateEditRollsBack(t *testing.T) {
	a := testApp(t)
	first := record(t, request(t, a, "POST", "/api/v1/suppliers", "admin", map[string]any{"name": "D2 Duplicate", "category": "food", "locationId": "com2"}))
	other := record(t, request(t, a, "POST", "/api/v1/suppliers", "admin", map[string]any{"name": "D2 Other", "category": "food", "locationId": "com2"}))
	path := "/api/v1/suppliers/" + other.ID
	status(t, request(t, a, "PATCH", path, "admin", map[string]any{"name": strings.ToUpper(first.Name), "description": "must not persist"}), 409)
	persisted := record(t, request(t, a, "GET", path, "user", nil))
	if persisted.Name != other.Name || persisted.Description != "" {
		t.Fatal("failed update must be atomic")
	}
}
