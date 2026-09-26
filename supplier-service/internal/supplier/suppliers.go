package supplier

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func (a *App) list(w http.ResponseWriter, r *http.Request) error {
	rows, err := a.db.Query(r.Context(), "SELECT "+supplierColumns+" FROM suppliers WHERE active ORDER BY lower(name),id LIMIT 100")
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Supplier{}
	for rows.Next() {
		s, err := scanSupplier(rows)
		if err != nil {
			return err
		}
		items = append(items, s)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"suppliers": items})
	return nil
}

func supplierID(r *http.Request) (string, error) {
	id := r.PathValue("id")
	var parsed pgtype.UUID
	if parsed.Scan(id) != nil || !parsed.Valid {
		return "", problem(400, "invalid_supplier_id", "Use a valid supplier UUID.")
	}
	return id, nil
}

func (a *App) get(w http.ResponseWriter, r *http.Request) error {
	id, err := supplierID(r)
	if err != nil {
		return err
	}
	s, err := scanSupplier(a.db.QueryRow(r.Context(), "SELECT "+supplierColumns+" FROM suppliers WHERE id=$1 AND active", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return problem(404, "supplier_not_found", "No active supplier matches that ID.")
	}
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"supplier": s})
	return nil
}

func (a *App) create(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Name        string `json:"name"`
		Category    string `json:"category"`
		LocationID  string `json:"locationId"`
		Description string `json:"description"`
	}
	if err := readJSON(w, r, &body); err != nil {
		return err
	}
	body.Name = strings.TrimSpace(body.Name)
	body.Description = strings.TrimSpace(body.Description)
	if !validText(body.Name, 1, 100) || !validText(body.Description, 0, 500) || !slices.Contains([]string{"food", "printing", "retail", "services", "other"}, body.Category) {
		return problem(400, "invalid_supplier", "Use a name of 1–100 characters, an allowed category and a description of at most 500 characters.")
	}
	s, err := scanSupplier(a.db.QueryRow(r.Context(), "INSERT INTO suppliers(name,category,location_id,description) VALUES($1,$2,$3,$4) RETURNING "+supplierColumns, body.Name, body.Category, body.LocationID, body.Description))
	if err != nil {
		return storeError(err)
	}
	identity := r.Context().Value(identityKey{}).(Identity)
	a.log.Info("supplier created", "supplierId", s.ID, "actorId", identity.UserID)
	w.Header().Set("Location", "/api/v1/suppliers/"+s.ID)
	writeJSON(w, 201, map[string]any{"supplier": s})
	return nil
}

func validText(value string, min, max int) bool {
	n := utf8.RuneCountInString(value)
	if !utf8.ValidString(value) || n < min || n > max {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

func storeError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return problem(409, "supplier_name_taken", "An active supplier already has that name at this location.")
		case "23503":
			return problem(400, "invalid_location", "Choose a location from the campus list.")
		}
	}
	return err
}

func (a *App) setActive(w http.ResponseWriter, r *http.Request) error {
	id, err := supplierID(r)
	if err != nil {
		return err
	}
	var body struct {
		Active *bool `json:"active"`
	}
	if err = readJSON(w, r, &body); err != nil {
		return err
	}
	if body.Active == nil {
		return problem(400, "invalid_status", "Provide active as true or false.")
	}
	s, err := scanSupplier(a.db.QueryRow(r.Context(), "UPDATE suppliers SET active=$2 WHERE id=$1 RETURNING "+supplierColumns, id, *body.Active))
	if errors.Is(err, pgx.ErrNoRows) {
		return problem(404, "supplier_not_found", "No supplier matches that ID.")
	}
	if err != nil {
		return storeError(err)
	}
	identity := r.Context().Value(identityKey{}).(Identity)
	a.log.Info("supplier status changed", "supplierId", s.ID, "active", s.Active, "actorId", identity.UserID)
	writeJSON(w, 200, map[string]any{"supplier": s})
	return nil
}

func (a *App) locations(w http.ResponseWriter, r *http.Request) error {
	rows, err := a.db.Query(r.Context(), "SELECT id,name FROM locations ORDER BY name")
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Location{}
	for rows.Next() {
		var l Location
		if err = rows.Scan(&l.ID, &l.Name); err != nil {
			return err
		}
		items = append(items, l)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"locations": items})
	return nil
}
