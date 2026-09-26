package credit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
)

// ErrInvalidSession means the session is missing, expired or revoked (401).
// Any other error from Sessions means User Service is unreachable (503).
var ErrInvalidSession = errors.New("invalid session")

// Caller is the signed-in user behind a public request.
type Caller struct {
	UserID string
	Admin  bool
}

// Sessions resolves a browser's session cookie to its user. Tests use a fake.
type Sessions interface {
	Validate(ctx context.Context, sessionToken string) (Caller, error)
}

// UserServiceSessions validates sessions through User Service's
// POST /internal/v1/sessions/validate (see user-service/API.md).
type UserServiceSessions struct {
	URL    string
	Token  string
	Client *http.Client
}

func (s UserServiceSessions) Validate(ctx context.Context, sessionToken string) (Caller, error) {
	payload, err := json.Marshal(map[string]string{"sessionToken": sessionToken})
	if err != nil {
		return Caller{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL+"/internal/v1/sessions/validate", bytes.NewReader(payload))
	if err != nil {
		return Caller{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp, err := s.Client.Do(req)
	if err != nil {
		return Caller{}, err
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, 64*1024)
	if resp.StatusCode == http.StatusUnauthorized {
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		// invalid_service_credentials is a configuration fault, not a bad session.
		if json.NewDecoder(body).Decode(&e) == nil && e.Error.Code == "invalid_session" {
			return Caller{}, ErrInvalidSession
		}
		return Caller{}, errors.New("user service rejected the service credentials")
	}
	if resp.StatusCode != http.StatusOK {
		return Caller{}, fmt.Errorf("user service returned HTTP %d", resp.StatusCode)
	}
	var out struct {
		UserID string   `json:"userId"`
		Roles  []string `json:"roles"`
	}
	if err = json.NewDecoder(body).Decode(&out); err != nil || out.UserID == "" {
		return Caller{}, errors.New("unexpected user service response")
	}
	return Caller{UserID: out.UserID, Admin: slices.Contains(out.Roles, "admin")}, nil
}
