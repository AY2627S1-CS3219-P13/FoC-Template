package supplier

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testUserID = "11111111-1111-4111-8111-111111111111"

func TestAuthContractAndRevocation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/internal/v1/sessions/validate" || r.Header.Get("Authorization") != "Bearer backend-secret" {
			t.Error("User Service request contract was not followed")
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["sessionToken"] != "browser-token" {
			t.Error("cookie not forwarded")
		}
		if calls.Add(1) == 1 {
			writeJSON(w, 200, Identity{UserID: testUserID, Roles: []string{"user"}, ExpiresAt: time.Now().Add(time.Hour)})
		} else {
			writeJSON(w, 401, map[string]any{"error": map[string]string{"code": "invalid_session"}})
		}
	}))
	defer server.Close()
	a := NewAuthClient(Config{UserServiceURL: server.URL, InternalToken: "backend-secret"})
	r := httptest.NewRequest("GET", "/api/v1/suppliers", nil)
	r.AddCookie(&http.Cookie{Name: "foc_session", Value: "browser-token"})
	identity, err := a.Validate(r)
	if err != nil || identity.UserID != testUserID {
		t.Fatalf("valid session rejected: %v", err)
	}
	_, err = a.Validate(r)
	if e, ok := err.(*apiError); !ok || e.status != 401 || calls.Load() != 2 {
		t.Fatal("revoked session was cached or accepted")
	}
}

func TestAuthFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"server failure", 503, `{}`},
		{"wrong backend credential", 401, `{"error":{"code":"invalid_service_credentials"}}`},
		{"malformed reply", 200, `not json`},
		{"missing identity", 200, `{}`},
		{"expired identity", 200, `{"userId":"` + testUserID + `","roles":["user"],"expiresAt":"2000-01-01T00:00:00Z"}`},
		{"redirect", 302, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			a := NewAuthClient(Config{UserServiceURL: server.URL})
			r := httptest.NewRequest("GET", "/", nil)
			r.AddCookie(&http.Cookie{Name: "foc_session", Value: "token"})
			_, err := a.Validate(r)
			if e, ok := err.(*apiError); !ok || e.status != 503 {
				t.Fatalf("expected unavailable, got %v", err)
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(100 * time.Millisecond):
			}
		}))
		defer server.Close()
		a := NewAuthClient(Config{UserServiceURL: server.URL})
		a.Client.Timeout = 30 * time.Millisecond
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: "foc_session", Value: "token"})
		_, err := a.Validate(r)
		if e, ok := err.(*apiError); !ok || e.status != 503 {
			t.Fatalf("expected timeout denial: %v", err)
		}
	})
}

func TestHTTPBoundaryProtection(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, 200, Identity{UserID: testUserID, Roles: []string{"user"}, ExpiresAt: time.Now().Add(time.Hour)})
	}))
	defer server.Close()
	app := New(nil, Config{Origin: "http://localhost:3000", UserServiceURL: server.URL}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, tc := range []struct {
		method, path, origin, cookie string
		want                         int
	}{
		{"GET", "/healthz", "", "", 200},
		{"GET", "/api/v1/suppliers", "", "", 401},
		{"GET", "/api/v1/locations", "", "", 401},
		{"POST", "/api/v1/suppliers", "", "token", 403},
		{"POST", "/api/v1/suppliers", "https://evil.example", "token", 403},
		{"POST", "/api/v1/suppliers", "http://localhost:3000", "token", 403},
		{"PATCH", "/api/v1/suppliers/" + testUserID, "http://localhost:3000", "token", 403},
		{"GET", "/internal/v1/sessions/validate", "", "", 404},
	} {
		t.Run(fmt.Sprintf("%s %s %s %s", tc.method, tc.path, tc.origin, tc.cookie), func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
			r.Header.Set("Origin", tc.origin)
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: "foc_session", Value: tc.cookie})
			}
			w := httptest.NewRecorder()
			app.Handler().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d, want %d", w.Code, tc.want)
			}
		})
	}
	if calls.Load() != 2 {
		t.Fatalf("unexpected auth calls: %d", calls.Load())
	}
}
