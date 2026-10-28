package main

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func TestCheckConfigBeforeDatabaseStartup(t *testing.T) {
	settings := map[string]string{
		"USER_DATABASE_URL":          "postgres://test:test@127.0.0.1:1/unreachable?connect_timeout=1",
		"USER_APP_ORIGIN":            "http://localhost:3000",
		"USER_INTERNAL_TOKEN":        strings.Repeat("i", 32),
		"USER_CODE_SECRET":           strings.Repeat("c", 32),
		"USER_CREDIT_SERVICE_URL":    "http://credit-service:8081",
		"USER_CREDIT_INTERNAL_TOKEN": strings.Repeat("k", 32),
		"USER_BOOTSTRAP_ADMIN_EMAIL": "",
		"USER_ALLOWED_EMAIL_DOMAINS": "u.nus.edu",
		"USER_COOKIE_SECURE":         "false",
		"USER_SESSION_TTL":           "24h",
		"USER_SMTP_ADDRESS":          "unreachable.invalid:587",
		"USER_SMTP_FROM":             "noreply@example.com",
		"USER_SMTP_TLS":              "false",
		"USER_SMTP_USERNAME":         "",
		"USER_SMTP_PASSWORD":         "",
	}
	for key, value := range settings {
		t.Setenv(key, value)
	}
	previous := os.Args
	os.Args = []string{"user-service", "check-config"}
	t.Cleanup(func() { os.Args = previous })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run(log); err != nil {
		t.Fatalf("local config check must not contact unavailable database or SMTP: %v", err)
	}
	t.Setenv("USER_APP_ORIGIN", "https://demo.cloudfront.net")
	t.Setenv("USER_COOKIE_SECURE", "true")
	if err := run(log); err == nil || err.Error() != "deployed email must use SMTP STARTTLS" {
		t.Fatalf("public HTTPS with plain Mailpit must fail before database startup, got %v", err)
	}
	t.Setenv("USER_SMTP_TLS", "true")
	if err := run(log); err != nil {
		t.Fatalf("public config with STARTTLS must pass without contacting dependencies: %v", err)
	}
	t.Setenv("USER_DATABASE_URL", "postgres://%")
	if err := run(log); err == nil || err.Error() != "invalid database configuration" {
		t.Fatalf("invalid database syntax must fail validation, got %v", err)
	}
}
