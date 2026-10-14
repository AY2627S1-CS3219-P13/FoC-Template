package user

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"testing"
)

// Review 1: distinct clients of one reverse proxy must have independent IP limits.
func TestReview01ProxyClientsHaveIndependentRateLimits(t *testing.T) {
	a, _ := testApp(t)
	a.cfg.TrustedProxyCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	backend := httptest.NewServer(a.PublicHandler())
	defer backend.Close()
	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Header.Del("X-Forwarded-For")
	}
	throughProxy := func(client string, body any) int {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(data))
		r.RemoteAddr = client
		r.Header.Set("Origin", a.cfg.Origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, r)
		return w.Code
	}
	// Invalid input still consumes the IP quota, without hashing 60 passwords.
	for i := range 60 {
		if got := throughProxy("203.0.113.10:10000", map[string]string{}); got != 400 {
			t.Fatalf("attacker request %d: got %d, want input rejection 400", i+1, got)
		}
	}
	got := throughProxy("203.0.113.20:20000", map[string]string{
		"email": "independent@u.nus.edu", "displayName": "Independent client", "password": testPassword,
	})
	if got != 202 {
		t.Fatalf("a second client was blocked after the first sent 60 requests: got HTTP %d, want 202", got)
	}
}

func TestDirectClientCannotSpoofForwardedQuota(t *testing.T) {
	a, _ := testApp(t)
	a.cfg.TrustedProxyCIDRs = []netip.Prefix{netip.MustParsePrefix("192.0.2.2/32")}
	for i := 0; i < 61; i++ {
		r := httptest.NewRequest("POST", "/api/v1/auth/register", bytes.NewBufferString("{}"))
		r.RemoteAddr = "203.0.113.10:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", a.cfg.Origin)
		r.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i))
		w := httptest.NewRecorder()
		a.PublicHandler().ServeHTTP(w, r)
		want := 400
		if i == 60 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("request %d got %d want %d", i, w.Code, want)
		}
	}
}
