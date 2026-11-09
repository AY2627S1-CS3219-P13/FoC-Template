package supplier

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPricingCatalogueUsesExistingRecords(t *testing.T) {
	app := testApp(t)
	app.cfg.CatalogueToken = "pricing-source-test-token"
	_, err := app.db.Exec(context.Background(), `INSERT INTO suppliers(id,name,category,location_id,active) VALUES('11111111-1111-4111-8111-111111111111','Inactive pricing test','other','com2',false),('22222222-2222-4222-8222-222222222222','Deleted pricing test','other','com2',false)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.db.Exec(context.Background(), "UPDATE suppliers SET deleted_at=now() WHERE id='22222222-2222-4222-8222-222222222222'")
	if err != nil {
		t.Fatal(err)
	}
	call := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/internal/v1/pricing-catalogue", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		return w
	}
	status(t, call("bad"), 401)
	response := call(app.cfg.CatalogueToken)
	status(t, response, 200)
	var snapshot struct {
		SchemaVersion int
		SnapshotID    string
		AsOf          time.Time
		Locations     []struct {
			ID                  string
			Latitude, Longitude *float64
		}
		Suppliers []struct {
			ID, LocationID string
			Active         bool
		}
	}
	if json.Unmarshal(response.Body.Bytes(), &snapshot) != nil || snapshot.SchemaVersion != 1 || snapshot.SnapshotID == "" || snapshot.AsOf.IsZero() || len(snapshot.Locations) == 0 {
		t.Fatalf("invalid snapshot: %s", response.Body.String())
	}
	for _, location := range snapshot.Locations {
		if location.Latitude == nil || location.Longitude == nil {
			t.Fatalf("location %s lacks coordinates", location.ID)
		}
		if location.ID == "com2" && (*location.Latitude != 1.293925 || *location.Longitude != 103.774153) {
			t.Fatal("com2 coordinates changed")
		}
	}
	found := false
	for _, supplier := range snapshot.Suppliers {
		if supplier.ID == "22222222-2222-4222-8222-222222222222" {
			t.Fatal("deleted supplier included")
		}
		if supplier.ID == "11111111-1111-4111-8111-111111111111" {
			found = true
			if supplier.Active || supplier.LocationID != "com2" {
				t.Fatal("inactive supplier changed")
			}
		}
	}
	if !found {
		t.Fatal("inactive supplier omitted")
	}
}
