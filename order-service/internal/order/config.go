package order

import (
	"errors"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL, Origin, SessionCookie                       string
	UserServiceURL, UserServiceToken                         string
	SupplierServiceURL, CreditServiceURL, CreditServiceToken string
	DependencyTimeout, RecoveryInterval, RecoveryStaleAfter  time.Duration
}

func LoadConfig() (Config, error) {
	c := Config{
		DatabaseURL: os.Getenv("ORDER_DATABASE_URL"), Origin: os.Getenv("ORDER_APP_ORIGIN"),
		SessionCookie: os.Getenv("ORDER_SESSION_COOKIE"), UserServiceURL: os.Getenv("ORDER_USER_SERVICE_URL"),
		UserServiceToken: os.Getenv("ORDER_USER_INTERNAL_TOKEN"), SupplierServiceURL: os.Getenv("ORDER_SUPPLIER_SERVICE_URL"),
		CreditServiceURL: os.Getenv("ORDER_CREDIT_SERVICE_URL"), CreditServiceToken: os.Getenv("ORDER_CREDIT_INTERNAL_TOKEN"),
		DependencyTimeout: 3 * time.Second, RecoveryInterval: 30 * time.Second, RecoveryStaleAfter: time.Minute,
	}
	if c.DatabaseURL == "" {
		return c, errors.New("ORDER_DATABASE_URL is required")
	}
	if c.SessionCookie != "foc_session" && c.SessionCookie != "__Host-foc_session" {
		return c, errors.New("ORDER_SESSION_COOKIE must be foc_session or __Host-foc_session")
	}
	if !validSecret(c.UserServiceToken) || !validSecret(c.CreditServiceToken) {
		return c, errors.New("Order dependency tokens must be random values of at least 32 characters")
	}
	for name, raw := range map[string]string{
		"ORDER_APP_ORIGIN": c.Origin, "ORDER_USER_SERVICE_URL": c.UserServiceURL,
		"ORDER_SUPPLIER_SERVICE_URL": c.SupplierServiceURL, "ORDER_CREDIT_SERVICE_URL": c.CreditServiceURL,
	} {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery ||
			u.Fragment != "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return c, errors.New(name + " must be an http(s) origin without a trailing slash")
		}
	}
	return c, nil
}

func validSecret(value string) bool {
	return len(value) >= 32 && !strings.Contains(value, "REPLACE") && !strings.ContainsAny(value, " \t\r\n")
}
