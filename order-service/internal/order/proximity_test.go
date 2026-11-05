package order

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDistanceMetersMatchesHaversine(t *testing.T) {
	com2 := Coordinate{1.293925, 103.774153}
	library := Coordinate{1.296558, 103.773111}
	// Reference values from an independent Haversine calculation (R = 6371 km).
	for _, c := range []struct {
		a, b Coordinate
		want int64
	}{{com2, library, 315}, {library, com2, 315}, {library, Coordinate{1.304, 103.7735}, 829}, {com2, com2, 0}} {
		if got := distanceMeters(c.a, c.b); got != c.want {
			t.Errorf("distance %v→%v = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCoordinatesRejectIncompleteOrOutOfRangeValues(t *testing.T) {
	for _, body := range []string{
		`{"locations":[{"id":"com2","name":"COM2","latitude":1.29}]}`,
		`{"locations":[{"id":"com2","name":"COM2","latitude":91,"longitude":103.77}]}`,
		`{"locations":[{"id":"com2","name":"COM2","latitude":1.29,"longitude":181}]}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		client := &CatalogueClient{BaseURL: server.URL, CookieName: "foc_session", Client: server.Client()}
		_, err := client.Coordinates(context.Background(), "session")
		server.Close()
		var api *apiError
		if !errors.As(err, &api) || api.status != 503 {
			t.Fatalf("%s: got %v, want 503", body, err)
		}
	}
}

type openWithDistance struct {
	ID                   string `json:"id"`
	PickupDistanceMeters *int64 `json:"pickupDistanceMeters"`
}

func openDistances(t *testing.T, a *App, query string) []openWithDistance {
	t.Helper()
	got := request(t, a, "GET", "/api/v1/orders?scope=open"+query, "courier", "", nil)
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	var body struct{ Orders []openWithDistance }
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Orders
}

func TestOpenOrdersSortByNearestPickup(t *testing.T) {
	a, f := testApp(t)
	f.mu.Lock()
	f.available = 1000
	f.mu.Unlock()
	a.auth = actorAuth{}
	create := func(key string, pickup map[string]string, expiry time.Duration) string {
		body := validCreateBody(10)
		body["pickup"] = pickup
		body["expiresAt"] = time.Now().Add(expiry).Format(time.RFC3339)
		response := request(t, a, "POST", "/api/v1/orders", "requester", key, body)
		if response.Code != 201 {
			t.Fatalf("create %s: %d %s", key, response.Code, response.Body.String())
		}
		var created struct{ Order Order }
		if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		return created.Order.ID
	}
	location := func(id string) map[string]string { return map[string]string{"type": "location", "id": id} }
	// The supplier pickup is at COM2, 315 m from Central Library.
	supplier := create("supplier", map[string]string{"type": "supplier", "id": testSupplierID}, time.Hour)
	unplaced := create("unplaced", location("annex"), 30*time.Minute)
	librarySoon := create("library-soon", location("central-library"), 2*time.Hour)
	libraryLater := create("library-later", location("central-library"), 3*time.Hour)

	rows := openDistances(t, a, "&sort=distance&from=central-library")
	ids := []string{}
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	// Nearest first; equal distances by expiry; unknown distances last.
	want := []string{librarySoon, libraryLater, supplier, unplaced}
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("got %v, want %v", ids, want)
		}
	}
	distances := map[string]*int64{}
	for _, row := range rows {
		distances[row.ID] = row.PickupDistanceMeters
	}
	if d := distances[librarySoon]; d == nil || *d != 0 {
		t.Fatal("same-location pickup should be 0 m", d)
	}
	if d := distances[supplier]; d == nil || *d != 315 {
		t.Fatal("supplier pickup should use its registered location", d)
	}
	if distances[unplaced] != nil {
		t.Fatal("a pickup without coordinates must not report a distance")
	}

	// A starting location adds distances without changing other sorts.
	rows = openDistances(t, a, "&sort=expiry&from=com2")
	if rows[0].ID != unplaced || rows[1].ID != supplier || rows[1].PickupDistanceMeters == nil || *rows[1].PickupDistanceMeters != 0 {
		t.Fatalf("expiry sort with a start changed order or distances: %+v", rows)
	}
	for _, row := range openDistances(t, a, "") {
		if row.PickupDistanceMeters != nil {
			t.Fatal("distances appeared without a starting location")
		}
	}

	for query, code := range map[string]string{
		"&sort=distance":                 "starting_location_required",
		"&sort=distance&from=atlantis":   "invalid_location",
		"&sort=distance&from=annex":      "invalid_location",
		"&sort=nearest&from=com2":        "invalid_sort",
		"&sort=credits&from=nonexistent": "invalid_location",
	} {
		got := request(t, a, "GET", "/api/v1/orders?scope=open"+query, "courier", "", nil)
		var body struct{ Error struct{ Code string } }
		_ = json.Unmarshal(got.Body.Bytes(), &body)
		if got.Code != 400 || body.Error.Code != code {
			t.Fatalf("%s: got %d %s, want 400 %s", query, got.Code, body.Error.Code, code)
		}
	}
}
