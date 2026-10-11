package order

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

// Paginate Supplier instead of imposing an undocumented catalogue-size cap.
func (c *CatalogueClient) List(ctx context.Context, session string, page int) ([]Place, bool, error) {
	locations, err := c.locations(ctx, session)
	if err != nil {
		return nil, false, err
	}
	names := map[string]string{}
	places := []Place{}
	for _, l := range locations.Locations {
		names[l.ID] = l.Name
		if page == 1 {
			places = append(places, Place{Type: "location", ID: l.ID, Name: l.Name})
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/suppliers?status=active&pageSize=100&page="+strconv.Itoa(page), nil)
	if err != nil {
		return nil, false, placesUnavailable()
	}
	req.AddCookie(&http.Cookie{Name: c.CookieName, Value: session})
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, false, placesUnavailable()
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return nil, false, problem(401, "invalid_session", "Please log in again.")
	}
	if resp.StatusCode != 200 {
		return nil, false, placesUnavailable()
	}
	var body struct {
		Suppliers []struct {
			ID, Name, LocationID, Description, OpeningHours string
			Active                                          *bool
		} `json:"suppliers"`
		Page, PageSize, Total, TotalPages *int
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024))
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || body.Suppliers == nil || body.Page == nil || *body.Page != page || body.PageSize == nil || *body.PageSize != 100 || body.TotalPages == nil || *body.TotalPages < 0 || body.Total == nil || *body.Total < 0 || len(body.Suppliers) > 100 {
		return nil, false, placesUnavailable()
	}
	seen := map[string]bool{}
	expectedPages := 0
	if *body.Total > 0 {
		expectedPages = (*body.Total-1)/100 + 1
	}
	if *body.TotalPages != expectedPages {
		return nil, false, placesUnavailable()
	}
	for _, s := range body.Suppliers {
		ref := PlaceRef{Type: "supplier", ID: s.ID}
		if ref.validate() != nil || seen[ref.ID] || s.Name == "" || s.Active == nil || !*s.Active || names[s.LocationID] == "" {
			return nil, false, placesUnavailable()
		}
		seen[ref.ID] = true
		places = append(places, Place{Type: "supplier", ID: ref.ID, Name: s.Name, LocationID: s.LocationID, LocationName: names[s.LocationID], Description: s.Description, OpeningHours: s.OpeningHours})
	}
	return places, page < *body.TotalPages, nil
}

func (a *App) listPlaces(w http.ResponseWriter, r *http.Request) error {
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100000 {
			return problem(400, "invalid_page", "Choose a valid catalogue page.")
		}
		page = n
	}
	cookie, _ := r.Cookie(a.cfg.SessionCookie)
	places, more, err := a.places.List(r.Context(), cookie.Value, page)
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"places": places, "hasMore": more})
	return nil
}
