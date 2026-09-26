package supplier

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL, Origin, UserServiceURL, InternalToken string
	SecureCookies                                      bool
}

func LoadConfig() (Config, error) {
	c := Config{
		DatabaseURL:    os.Getenv("SUPPLIER_DATABASE_URL"),
		Origin:         os.Getenv("USER_APP_ORIGIN"),
		UserServiceURL: os.Getenv("USER_SERVICE_INTERNAL_URL"),
		InternalToken:  os.Getenv("USER_INTERNAL_TOKEN"),
	}
	var err error
	if c.SecureCookies, err = strconv.ParseBool(os.Getenv("USER_COOKIE_SECURE")); err != nil {
		return c, errors.New("USER_COOKIE_SECURE must be true or false")
	}
	if c.DatabaseURL == "" || len(c.InternalToken) < 32 || strings.Contains(c.InternalToken, "REPLACE") {
		return c, errors.New("database URL and a random internal service credential are required")
	}
	o, err := url.Parse(c.Origin)
	if err != nil || o.Host == "" || o.User != nil || o.Path != "" || o.RawQuery != "" || o.Fragment != "" || (o.Scheme != "http" && o.Scheme != "https") {
		return c, errors.New("USER_APP_ORIGIN must be an exact HTTP(S) origin")
	}
	if (o.Scheme == "https" && !c.SecureCookies) || (o.Scheme == "http" && (c.SecureCookies || (o.Hostname() != "localhost" && o.Hostname() != "127.0.0.1"))) {
		return c, errors.New("use secure cookies with HTTPS; HTTP is local development only")
	}
	u, err := url.Parse(c.UserServiceURL)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return c, errors.New("USER_SERVICE_INTERNAL_URL must be an HTTP(S) service origin")
	}
	c.UserServiceURL = strings.TrimRight(c.UserServiceURL, "/")
	return c, nil
}

func (c Config) CookieName() string {
	if c.SecureCookies {
		return "__Host-foc_session"
	}
	return "foc_session"
}
