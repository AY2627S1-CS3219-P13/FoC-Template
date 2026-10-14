package order

import "testing"

func TestCookieNameMatchesSharedOrigin(t *testing.T) {
	t.Setenv("ORDER_DATABASE_URL", "postgres://test:test@localhost/test")
	t.Setenv("ORDER_APP_ORIGIN", "http://localhost:3000")
	t.Setenv("ORDER_SESSION_COOKIE", "")
	t.Setenv("ORDER_USER_SERVICE_URL", "http://user:8081")
	t.Setenv("ORDER_USER_INTERNAL_TOKEN", "uuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuu")
	t.Setenv("ORDER_SUPPLIER_SERVICE_URL", "http://supplier:8080")
	t.Setenv("ORDER_CREDIT_SERVICE_URL", "http://credit:8081")
	t.Setenv("ORDER_CREDIT_INTERNAL_TOKEN", "cccccccccccccccccccccccccccccccc")
	for _, tc := range []struct{ origin, cookie string }{{"http://localhost:3000", "foc_session"}, {"https://review.example", "__Host-foc_session"}} {
		t.Setenv("ORDER_APP_ORIGIN", tc.origin)
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.SessionCookie != tc.cookie {
			t.Fatalf("origin %s: got %s want %s", tc.origin, cfg.SessionCookie, tc.cookie)
		}
	}
	t.Setenv("ORDER_SESSION_COOKIE", "invalid")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("invalid explicit cookie accepted")
	}
}
