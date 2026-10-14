package credit

import "testing"

func TestCookieNameMatchesSharedOrigin(t *testing.T) {
	t.Setenv("CREDIT_DATABASE_URL", "postgres://test:test@localhost/test")
	t.Setenv("CREDIT_APP_ORIGIN", "http://localhost:3000")
	t.Setenv("CREDIT_SESSION_COOKIE", "")
	t.Setenv("CREDIT_USER_SERVICE_URL", "http://user:8081")
	t.Setenv("CREDIT_USER_INTERNAL_TOKEN", "uuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuu")
	t.Setenv("CREDIT_INITIAL_CREDITS", "100")
	t.Setenv("CREDIT_INTERNAL_TOKEN", "cccccccccccccccccccccccccccccccc")
	for _, tc := range []struct{ origin, cookie string }{{"http://localhost:3000", "foc_session"}, {"https://review.example", "__Host-foc_session"}} {
		t.Setenv("CREDIT_APP_ORIGIN", tc.origin)
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.SessionCookie != tc.cookie {
			t.Fatalf("origin %s: got %s want %s", tc.origin, cfg.SessionCookie, tc.cookie)
		}
	}
	t.Setenv("CREDIT_SESSION_COOKIE", "invalid")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("invalid explicit cookie accepted")
	}
}
