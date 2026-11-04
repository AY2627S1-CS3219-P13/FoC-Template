package schedule

import (
	"errors"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL, Origin, SessionCookie                                         string
	UserServiceURL, UserServiceToken, SupplierServiceURL, SupplierServiceToken string
	CreditServiceURL, CreditServiceToken, OrderServiceURL, OrderServiceToken   string
	DependencyTimeout                                                          time.Duration
}

func LoadConfig() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("SCHEDULE_DATABASE_URL"), Origin: os.Getenv("SCHEDULE_APP_ORIGIN"), SessionCookie: os.Getenv("SCHEDULE_SESSION_COOKIE"), UserServiceURL: os.Getenv("SCHEDULE_USER_SERVICE_URL"), UserServiceToken: os.Getenv("SCHEDULE_USER_INTERNAL_TOKEN"), SupplierServiceURL: os.Getenv("SCHEDULE_SUPPLIER_SERVICE_URL"), SupplierServiceToken: os.Getenv("SCHEDULE_SUPPLIER_INTERNAL_TOKEN"), CreditServiceURL: os.Getenv("SCHEDULE_CREDIT_SERVICE_URL"), CreditServiceToken: os.Getenv("SCHEDULE_CREDIT_INTERNAL_TOKEN"), OrderServiceURL: os.Getenv("SCHEDULE_ORDER_SERVICE_URL"), OrderServiceToken: os.Getenv("SCHEDULE_ORDER_INTERNAL_TOKEN"), DependencyTimeout: 3 * time.Second}
	if c.DatabaseURL == "" {
		return c, errors.New("SCHEDULE_DATABASE_URL required")
	}
	for _, s := range []string{c.UserServiceToken, c.SupplierServiceToken, c.CreditServiceToken, c.OrderServiceToken} {
		if len(s) < 32 || strings.ContainsAny(s, " \r\n\t") {
			return c, errors.New("configure separate internal tokens of at least 32 characters")
		}
	}
	for _, s := range []string{c.Origin, c.UserServiceURL, c.SupplierServiceURL, c.CreditServiceURL, c.OrderServiceURL} {
		u, e := url.Parse(s)
		if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.User != nil || u.Fragment != "" || u.ForceQuery {
			return c, errors.New("configure http(s) origins without paths or credentials")
		}
	}
	if c.SessionCookie == "" {
		c.SessionCookie = "foc_session"
		if strings.HasPrefix(c.Origin, "https:") {
			c.SessionCookie = "__Host-foc_session"
		}
	}
	if c.SessionCookie != "foc_session" && c.SessionCookie != "__Host-foc_session" {
		return c, errors.New("invalid session cookie")
	}
	return c, nil
}
