package order

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
)

type CatalogueClient struct {
	BaseURL, CookieName string
	Client              *http.Client
}

type supplierResponse struct {
	Supplier struct {
		ID, Name, LocationID, Description, OpeningHours string
		Active                                          *bool
	} `json:"supplier"`
}

type locationResponse struct {
	Locations []struct {
		ID, Name string
	} `json:"locations"`
}

func (c *CatalogueClient) Resolve(ctx context.Context, ref PlaceRef, session string) (Place, error) {
	locations, err := c.locations(ctx, session)
	if err != nil {
		return Place{}, err
	}
	if ref.Type == "location" {
		for _, location := range locations.Locations {
			if location.ID == ref.ID {
				return Place{Type: "location", ID: location.ID, Name: location.Name}, nil
			}
		}
		return Place{}, invalidPlace()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/suppliers/"+url.PathEscape(ref.ID), nil)
	if err != nil {
		return Place{}, placesUnavailable()
	}
	req.AddCookie(&http.Cookie{Name: c.CookieName, Value: session})
	response, err := c.Client.Do(req)
	if err != nil {
		return Place{}, placesUnavailable()
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return Place{}, invalidPlace()
	}
	if response.StatusCode == http.StatusUnauthorized {
		return Place{}, problem(401, "invalid_session", "Please log in again.")
	}
	if response.StatusCode != http.StatusOK {
		return Place{}, placesUnavailable()
	}
	var body supplierResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64*1024))
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || body.Supplier.ID != ref.ID || body.Supplier.Active == nil || body.Supplier.Name == "" {
		return Place{}, placesUnavailable()
	}
	if !*body.Supplier.Active {
		return Place{}, invalidPlace()
	}
	locationName := ""
	for _, location := range locations.Locations {
		if location.ID == body.Supplier.LocationID {
			locationName = location.Name
			break
		}
	}
	if locationName == "" {
		return Place{}, placesUnavailable()
	}
	return Place{Type: "supplier", ID: body.Supplier.ID, Name: body.Supplier.Name, LocationID: body.Supplier.LocationID,
		LocationName: locationName, Description: body.Supplier.Description, OpeningHours: body.Supplier.OpeningHours}, nil
}

func (c *CatalogueClient) locations(ctx context.Context, session string) (locationResponse, error) {
	var body locationResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/locations", nil)
	if err != nil {
		return body, placesUnavailable()
	}
	req.AddCookie(&http.Cookie{Name: c.CookieName, Value: session})
	response, err := c.Client.Do(req)
	if err != nil {
		return body, placesUnavailable()
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return body, problem(401, "invalid_session", "Please log in again.")
	}
	if response.StatusCode != http.StatusOK {
		return body, placesUnavailable()
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64*1024))
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || body.Locations == nil {
		return body, placesUnavailable()
	}
	seen := make(map[string]bool)
	for _, location := range body.Locations {
		if location.ID == "" || location.Name == "" || seen[location.ID] {
			return body, placesUnavailable()
		}
		seen[location.ID] = true
	}
	return body, nil
}

func invalidPlace() error {
	return problem(400, "invalid_place", "Choose a known campus location or active supplier.")
}

func placesUnavailable() error {
	return problem(503, "places_unavailable", "Places are temporarily unavailable. Please retry.")
}

type CreditClient struct {
	BaseURL, Token string
	Client         *http.Client
}

var errEscrowMissing = errors.New("escrow missing; reservation outcome may still be uncertain")

func (c *CreditClient) Reserve(ctx context.Context, id, userID string, amount int64) error {
	body, _ := json.Marshal(map[string]any{"userId": userID, "amount": amount})
	return c.call(ctx, http.MethodPut, "/internal/v1/escrows/"+url.PathEscape(id), body, true)
}

func (c *CreditClient) Release(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodPost, "/internal/v1/escrows/"+url.PathEscape(id)+"/release", nil, false)
}

func (c *CreditClient) call(ctx context.Context, method, path string, body []byte, reserve bool) error {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return creditUnavailable()
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.Client.Do(req)
	if err != nil {
		return creditUnavailable()
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK {
		var wallet struct {
			UserID    string `json:"userId"`
			Available *int64 `json:"available"`
			Reserved  *int64 `json:"reserved"`
		}
		decoder := json.NewDecoder(io.LimitReader(response.Body, 4096))
		if decoder.Decode(&wallet) != nil || decoder.Decode(new(any)) != io.EOF ||
			wallet.UserID == "" || wallet.Available == nil || wallet.Reserved == nil ||
			*wallet.Available < 0 || *wallet.Reserved < 0 {
			return creditUnavailable()
		}
		if reserve {
			var sent struct {
				UserID string `json:"userId"`
			}
			_ = json.Unmarshal(body, &sent)
			if wallet.UserID != sent.UserID {
				return creditUnavailable()
			}
		}
		return nil
	}
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4096))
	if decoder.Decode(&failure) != nil || decoder.Decode(new(any)) != io.EOF {
		return creditUnavailable()
	}
	if !reserve && response.StatusCode == 404 && failure.Error.Code == "escrow_not_found" {
		return errEscrowMissing
	}
	if reserve {
		switch failure.Error.Code {
		case "wallet_not_found":
			if response.StatusCode != 404 {
				return creditUnavailable()
			}
			return problem(409, "wallet_not_ready", "Your credit wallet is not ready. Please retry shortly.")
		case "insufficient_credits":
			if response.StatusCode != 409 {
				return creditUnavailable()
			}
			return problem(409, "insufficient_credits", "You do not have enough available credits.")
		case "idempotency_conflict":
			if response.StatusCode != 409 {
				return creditUnavailable()
			}
			return problem(409, "idempotency_conflict", "This creation key was already used with different details.")
		}
	}
	return creditUnavailable()
}

func creditUnavailable() error {
	return problem(503, "credit_unavailable", "Creation could not be confirmed. Credit cleanup may be pending. Retry with the same idempotency key to check the result.")
}

func (c *CreditClient) Payout(ctx context.Context, id, courier string) error {
	body, _ := json.Marshal(map[string]string{"courierId": courier})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/internal/v1/escrows/"+url.PathEscape(id)+"/payout", bytes.NewReader(body))
	if err != nil {
		return creditUnavailable()
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.Client.Do(req)
	if err != nil {
		return creditUnavailable()
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return problem(503, "payout_pending", "Credit payout remains pending; it will be retried.")
	}
	return nil
}
