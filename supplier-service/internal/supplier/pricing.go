package supplier

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// A projection of the existing catalogue, not a separate price list. Both
// collections and their source timestamp come from one read-only snapshot.
func (a *App) pricingCatalogue(w http.ResponseWriter, r *http.Request) error {
	if r.URL.RawQuery != "" {
		return problem(400, "invalid_request", "Query parameters are not supported.")
	}
	tx, err := a.db.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var asOf time.Time
	if err = tx.QueryRow(r.Context(), "SELECT transaction_timestamp()").Scan(&asOf); err != nil {
		return err
	}
	type location struct {
		ID string `json:"id"`
	}
	type supplier struct {
		ID         string `json:"id"`
		LocationID string `json:"locationId"`
		Active     bool   `json:"active"`
	}
	locations := []location{}
	suppliers := []supplier{}
	rows, err := tx.Query(r.Context(), "SELECT id FROM locations ORDER BY id LIMIT 1001")
	if err != nil {
		return err
	}
	for rows.Next() {
		var item location
		if err = rows.Scan(&item.ID); err != nil {
			rows.Close()
			return err
		}
		locations = append(locations, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	rows, err = tx.Query(r.Context(), "SELECT id::text,location_id,active FROM suppliers WHERE deleted_at IS NULL ORDER BY id LIMIT 10001")
	if err != nil {
		return err
	}
	for rows.Next() {
		var item supplier
		if err = rows.Scan(&item.ID, &item.LocationID, &item.Active); err != nil {
			rows.Close()
			return err
		}
		suppliers = append(suppliers, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if len(locations) > 1000 || len(suppliers) > 10000 {
		return problem(503, "snapshot_too_large", "Pricing snapshot is too large.")
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"schemaVersion": 1, "snapshotId": "catalogue-" + asOf.UTC().Format(time.RFC3339Nano), "asOf": asOf.UTC(), "locations": locations, "suppliers": suppliers})
	return nil
}
