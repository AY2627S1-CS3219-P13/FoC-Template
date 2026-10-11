package order

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

type Identity struct {
	UserID    string    `json:"userId"`
	Roles     []string  `json:"roles"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type identityKey struct{}

type AuthClient struct {
	URL, Token, CookieName string
	Client                 *http.Client
}

func (a *AuthClient) Validate(r *http.Request) (Identity, error) {
	var identity Identity
	cookie, err := r.Cookie(a.CookieName)
	if err != nil || cookie.Value == "" || len(cookie.Value) > 128 {
		return identity, problem(401, "invalid_session", "Please log in.")
	}
	body, _ := json.Marshal(map[string]string{"sessionToken": cookie.Value})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, a.URL+"/internal/v1/sessions/validate", bytes.NewReader(body))
	if err != nil {
		return identity, authUnavailable()
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	req.Header.Set("Content-Type", "application/json")
	response, err := a.Client.Do(req)
	if err != nil {
		return identity, authUnavailable()
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&failure) == nil && failure.Error.Code == "invalid_session" {
			return identity, problem(401, "invalid_session", "Please log in again.")
		}
		return identity, authUnavailable()
	}
	if response.StatusCode != http.StatusOK {
		return identity, authUnavailable()
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4096))
	if decoder.Decode(&identity) != nil || decoder.Decode(new(any)) != io.EOF {
		return Identity{}, authUnavailable()
	}
	var id pgtype.UUID
	if id.Scan(identity.UserID) != nil || !id.Valid || !slices.Contains(identity.Roles, "user") || !identity.ExpiresAt.After(time.Now()) {
		return Identity{}, authUnavailable()
	}
	identity.UserID = strings.ToLower(identity.UserID)
	return identity, nil
}

func authUnavailable() error {
	return problem(503, "auth_unavailable", "Authentication is temporarily unavailable. Please retry.")
}

func (a *App) protected(next endpoint) endpoint {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := a.auth.Validate(r)
		if err != nil {
			return err
		}
		return next(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, identity)))
	}
}
