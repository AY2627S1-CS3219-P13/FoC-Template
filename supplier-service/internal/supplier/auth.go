package supplier

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

type Identity struct {
	UserID    string    `json:"userId"`
	Roles     []string  `json:"roles"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type identityKey struct{}

// AuthClient never caches identities: User owns session and role revocation.
type AuthClient struct {
	URL, Token, CookieName string
	Client                 *http.Client
}

func NewAuthClient(c Config) *AuthClient {
	return &AuthClient{
		URL:   c.UserServiceURL + "/internal/v1/sessions/validate",
		Token: c.InternalToken, CookieName: c.CookieName(),
		Client: &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func (a *AuthClient) Validate(r *http.Request) (Identity, error) {
	var identity Identity
	cookie, err := r.Cookie(a.CookieName)
	if err != nil || cookie.Value == "" || len(cookie.Value) > 128 {
		return identity, problem(401, "invalid_session", "Please log in.")
	}
	data, _ := json.Marshal(map[string]string{"sessionToken": cookie.Value})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, a.URL, bytes.NewReader(data))
	if err != nil {
		return identity, authUnavailable()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.Token)
	response, err := a.Client.Do(req)
	if err != nil {
		return identity, authUnavailable()
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&body) == nil && body.Error.Code == "invalid_session" {
			return identity, problem(401, "invalid_session", "Please log in again.")
		}
		// Bad backend credentials are an integration outage, not a user login error.
		return identity, authUnavailable()
	}
	if response.StatusCode != http.StatusOK {
		return identity, authUnavailable()
	}
	d := json.NewDecoder(io.LimitReader(response.Body, 4096))
	if d.Decode(&identity) != nil || d.Decode(new(any)) != io.EOF {
		return identity, authUnavailable()
	}
	var id pgtype.UUID
	if id.Scan(identity.UserID) != nil || !id.Valid || !slices.Contains(identity.Roles, "user") || !identity.ExpiresAt.After(time.Now()) {
		return Identity{}, authUnavailable()
	}
	return identity, nil
}

func authUnavailable() error {
	return problem(503, "auth_unavailable", "Authentication is temporarily unavailable. Please retry.")
}

func (a *App) protected(admin bool, next endpoint) endpoint {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := a.auth.Validate(r)
		if err != nil {
			return err
		}
		if admin && !slices.Contains(identity.Roles, "admin") {
			return problem(403, "admin_required", "An administrator is required.")
		}
		return next(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, identity)))
	}
}
