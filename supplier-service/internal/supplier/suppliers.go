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

var categories = []string{"food", "printing", "retail", "services", "other"}

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
	s, err := scanSupplier(a.db.QueryRow(r.Context(), "SELECT "+supplierColumns+" FROM suppliers WHERE id=$1 AND deleted_at IS NULL", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return supplierNotFound()
	}
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"supplier": s})
	return nil
}

// Null is treated as omitted; an empty PATCH is rejected.
type supplierWrite struct {
	Name         *string `json:"name"`
	Category     *string `json:"category"`
	LocationID   *string `json:"locationId"`
	Description  *string `json:"description"`
	OpeningHours *string `json:"openingHours"`
	Active       *bool   `json:"active"`
}

func (b *supplierWrite) validate(create bool) error {
	if create && (b.Name == nil || b.Category == nil || b.LocationID == nil) {
		return problem(400, "invalid_supplier", "Name, category and location are required.")
	}
	if b.Name == nil && b.Category == nil && b.LocationID == nil && b.Description == nil && b.OpeningHours == nil && b.Active == nil {
		return problem(400, "invalid_supplier", "Provide at least one editable supplier field.")
	}
	for _, field := range []struct {
		value     *string
		min, max  int
		multiline bool
	}{
		{b.Name, 1, 100, false}, {b.LocationID, 1, 100, false},
		{b.Description, 0, 500, true}, {b.OpeningHours, 0, 200, true},
	} {
		if field.value == nil {
			continue
		}
		*field.value = strings.TrimSpace(strings.ReplaceAll(*field.value, "\r\n", "\n"))
		if !validText(*field.value, field.min, field.max, field.multiline) {
			return problem(400, "invalid_supplier", "Use a name of 1–100 characters, description up to 500 and opening hours up to 200. Choose a campus location.")
		}
	}
	if b.Category != nil && !slices.Contains(categories, *b.Category) {
		return problem(400, "invalid_category", "Choose food, printing, retail, services or other.")
	}
	return nil
}

func (a *App) create(w http.ResponseWriter, r *http.Request) error {
	var body supplierWrite
	if err := readJSON(w, r, &body); err != nil {
		return err
	}
	if err := body.validate(true); err != nil {
		return err
	}
	s, err := scanSupplier(a.db.QueryRow(r.Context(), `INSERT INTO suppliers(name,category,location_id,description,opening_hours,active)
        VALUES($1,$2,$3,COALESCE($4,''),COALESCE($5,''),COALESCE($6,true)) RETURNING `+supplierColumns,
		body.Name, body.Category, body.LocationID, body.Description, body.OpeningHours, body.Active))
	if err != nil {
		return storeError(err)
	}
	a.audit(r, "supplier created", s.ID)
	w.Header().Set("Location", "/api/v1/suppliers/"+s.ID)
	writeJSON(w, 201, map[string]any{"supplier": s})
	return nil
}

func (a *App) update(w http.ResponseWriter, r *http.Request) error {
	id, err := supplierID(r)
	if err != nil {
		return err
	}
	var body supplierWrite
	if err = readJSON(w, r, &body); err != nil {
		return err
	}
	if err = body.validate(false); err != nil {
		return err
	}
	// Apply the patch atomically; PostgreSQL enforces concurrent uniqueness.
	s, err := scanSupplier(a.db.QueryRow(r.Context(), `UPDATE suppliers SET
        name=COALESCE($2,name),category=COALESCE($3,category),location_id=COALESCE($4,location_id),
        description=COALESCE($5,description),opening_hours=COALESCE($6,opening_hours),
        active=COALESCE($7,active),updated_at=now()
        WHERE id=$1 AND deleted_at IS NULL RETURNING `+supplierColumns,
		id, body.Name, body.Category, body.LocationID, body.Description, body.OpeningHours, body.Active))
	if errors.Is(err, pgx.ErrNoRows) {
		return supplierNotFound()
	}
	if err != nil {
		return storeError(err)
	}
	a.audit(r, "supplier updated", s.ID)
	writeJSON(w, 200, map[string]any{"supplier": s})
	return nil
}

func (a *App) remove(w http.ResponseWriter, r *http.Request) error {
	id, err := supplierID(r)
	if err != nil {
		return err
	}
	result, err := a.db.Exec(r.Context(), `UPDATE suppliers SET deleted_at=now(),active=false,updated_at=now()
        WHERE id=$1 AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return supplierNotFound()
	}
	a.audit(r, "supplier deleted", id)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func supplierNotFound() error {
	return problem(404, "supplier_not_found", "No supplier matches that ID.")
}

func (a *App) audit(r *http.Request, message, id string) {
	identity := r.Context().Value(identityKey{}).(Identity)
	a.log.Info(message, "supplierId", id, "actorId", identity.UserID)
}

func validText(value string, min, max int, multiline bool) bool {
	n := utf8.RuneCountInString(value)
	if !utf8.ValidString(value) || n < min || n > max {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) && !(multiline && c == '\n') {
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
