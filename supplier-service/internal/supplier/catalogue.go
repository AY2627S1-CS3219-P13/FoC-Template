package supplier

import (
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

type pageResult struct {
	Suppliers  []Supplier `json:"suppliers"`
	Page       int        `json:"page"`
	PageSize   int        `json:"pageSize"`
	Total      int        `json:"total"`
	TotalPages int        `json:"totalPages"`
}

func queryInt(q url.Values, key string, fallback, max int) (int, error) {
	if !q.Has(key) {
		return fallback, nil
	}
	n, err := strconv.Atoi(q.Get(key))
	if err != nil || n < 1 || n > max {
		return 0, problem(400, "invalid_query", "Use valid positive page and pageSize values (pageSize at most 100).")
	}
	return n, nil
}

func (a *App) list(w http.ResponseWriter, r *http.Request) error {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return problem(400, "invalid_query", "Use a valid query string.")
	}
	for key, values := range q {
		if !slices.Contains([]string{"q", "category", "locationId", "status", "sort", "direction", "page", "pageSize"}, key) || len(values) != 1 {
			return problem(400, "invalid_query", "Use each documented query parameter at most once.")
		}
	}
	search, category, location := strings.TrimSpace(q.Get("q")), q.Get("category"), q.Get("locationId")
	if !validText(search, 0, 100, false) || !validText(location, 0, 100, false) || (category != "" && !slices.Contains(categories, category)) {
		return problem(400, "invalid_query", "Use a search up to 100 characters, an allowed category and a campus location ID.")
	}
	state := q.Get("status")
	if state == "" {
		state = "active"
	}
	if !slices.Contains([]string{"active", "inactive", "all"}, state) {
		return problem(400, "invalid_query", "Status must be active, inactive or all.")
	}
	sort := q.Get("sort")
	if sort == "" {
		sort = "name"
	}
	order, ok := map[string]string{"name": "lower(s.name)", "category": "s.category", "location": "lower(l.name)", "createdAt": "s.created_at"}[sort]
	if !ok {
		return problem(400, "invalid_query", "Sort must be name, category, location or createdAt.")
	}
	direction := q.Get("direction")
	if direction == "" {
		direction = "asc"
	}
	if direction != "asc" && direction != "desc" {
		return problem(400, "invalid_query", "Direction must be asc or desc.")
	}
	page, err := queryInt(q, "page", 1, 100000)
	if err != nil {
		return err
	}
	size, err := queryInt(q, "pageSize", 20, 100)
	if err != nil {
		return err
	}
	// Count and rows share one snapshot, including an out-of-range page.
	tx, err := a.db.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	if location != "" {
		var exists bool
		if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM locations WHERE id=$1)", location).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return problem(400, "invalid_location", "Choose a location from the campus list.")
		}
	}
	const from = ` FROM suppliers s JOIN locations l ON l.id=s.location_id WHERE s.deleted_at IS NULL
        AND ($1='' OR strpos(lower(s.name || ' ' || s.description || ' ' || s.opening_hours || ' ' || l.name),lower($1))>0)
        AND ($2='' OR s.category=$2) AND ($3='' OR s.location_id=$3)
        AND ($4='all' OR s.active=($4='active'))`
	args := []any{search, category, location, state}
	result := pageResult{Suppliers: []Supplier{}, Page: page, PageSize: size}
	if err = tx.QueryRow(r.Context(), "SELECT count(*)"+from, args...).Scan(&result.Total); err != nil {
		return err
	}
	result.TotalPages = (result.Total + size - 1) / size
	// Interpolate only allowlisted SQL; all user values remain parameters.
	columns := "s." + strings.ReplaceAll(supplierColumns, ",", ",s.")
	rows, err := tx.Query(r.Context(), "SELECT "+columns+from+" ORDER BY "+order+" "+direction+",s.id "+direction+" LIMIT $5 OFFSET $6", append(args, size, (page-1)*size)...)
	if err != nil {
		return err
	}
	for rows.Next() {
		s, scanErr := scanSupplier(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		result.Suppliers = append(result.Suppliers, s)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, 200, result)
	return nil
}
