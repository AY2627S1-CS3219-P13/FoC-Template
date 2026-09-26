package credit

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL      string
	InitialCredits   int64
	Origin           string
	InternalToken    string
	SessionCookie    string
	UserServiceURL   string
	UserServiceToken string
}

func LoadConfig() (Config, error) {
	c := Config{
		DatabaseURL: os.Getenv("CREDIT_DATABASE_URL"), Origin: os.Getenv("CREDIT_APP_ORIGIN"),
		InternalToken: os.Getenv("CREDIT_INTERNAL_TOKEN"), SessionCookie: os.Getenv("CREDIT_SESSION_COOKIE"),
		UserServiceURL: os.Getenv("CREDIT_USER_SERVICE_URL"), UserServiceToken: os.Getenv("CREDIT_USER_INTERNAL_TOKEN"),
	}
	if c.DatabaseURL == "" {
		return c, errors.New("CREDIT_DATABASE_URL is required")
	}
	n, err := strconv.ParseInt(os.Getenv("CREDIT_INITIAL_CREDITS"), 10, 64)
	if err != nil || n <= 0 {
		return c, errors.New("CREDIT_INITIAL_CREDITS must be a positive whole number")
	}
	c.InitialCredits = n
	if !secret(c.InternalToken) || !secret(c.UserServiceToken) {
		return c, errors.New("CREDIT_INTERNAL_TOKEN and CREDIT_USER_INTERNAL_TOKEN must be random values of at least 32 characters")
	}
	if c.SessionCookie != "foc_session" && c.SessionCookie != "__Host-foc_session" {
		return c, errors.New("CREDIT_SESSION_COOKIE must match User Service: foc_session or __Host-foc_session")
	}
	for name, raw := range map[string]string{"CREDIT_APP_ORIGIN": c.Origin, "CREDIT_USER_SERVICE_URL": c.UserServiceURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.Path != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return c, errors.New(name + " must be an http(s) origin without a trailing slash")
		}
	}
	return c, nil
}

func secret(s string) bool {
	return len(s) >= 32 && !strings.Contains(s, "REPLACE")
}
