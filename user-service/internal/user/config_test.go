package user

import (
	"strings"
	"testing"
)

func TestBootstrapEmailConfiguration(t *testing.T) {
	t.Setenv("USER_DATABASE_URL", "postgres://example:example@localhost/example")
	t.Setenv("USER_APP_ORIGIN", "http://localhost:3000")
	t.Setenv("USER_INTERNAL_TOKEN", strings.Repeat("i", 32))
	t.Setenv("USER_CODE_SECRET", strings.Repeat("c", 32))
	t.Setenv("USER_ALLOWED_EMAIL_DOMAINS", "u.nus.edu")
	t.Setenv("USER_COOKIE_SECURE", "false")
	t.Setenv("USER_SESSION_TTL", "24h")
	t.Setenv("USER_SMTP_ADDRESS", "localhost:1025")
	t.Setenv("USER_SMTP_FROM", "noreply@foc.local")
	t.Setenv("USER_SMTP_TLS", "false")
	t.Setenv("USER_SMTP_USERNAME", "")
	t.Setenv("USER_SMTP_PASSWORD", "")

	t.Setenv("USER_BOOTSTRAP_ADMIN_EMAIL", "")
	if _, err := LoadConfig(); err != nil {
		t.Fatal("optional bootstrap setting should allow empty value:", err)
	}
	t.Setenv("USER_BOOTSTRAP_ADMIN_EMAIL", " OWNER@U.NUS.EDU ")
	cfg, err := LoadConfig()
	if err != nil || cfg.BootstrapAdminEmail != "owner@u.nus.edu" {
		t.Fatalf("designated address should be normalized: %q, %v", cfg.BootstrapAdminEmail, err)
	}
	for _, value := range []string{"owner@example.com", "not-an-email", "owner@u.nus.edu\nmalicious"} {
		t.Setenv("USER_BOOTSTRAP_ADMIN_EMAIL", value)
		if _, err := LoadConfig(); err == nil {
			t.Fatalf("invalid bootstrap address was accepted: %q", value)
		}
	}
}
