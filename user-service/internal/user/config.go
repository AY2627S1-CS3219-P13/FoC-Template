package user

import (
	"errors"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL, Origin, InternalToken, CodeSecret    string
	AllowedDomains                                    []string
	SecureCookies                                     bool
	SessionTTL                                        time.Duration
	SMTPAddress, SMTPFrom, SMTPUsername, SMTPPassword string
	SMTPTLS                                           bool
}

func LoadConfig() (Config, error) {
	c := Config{
		DatabaseURL: os.Getenv("USER_DATABASE_URL"), Origin: os.Getenv("USER_APP_ORIGIN"),
		InternalToken: os.Getenv("USER_INTERNAL_TOKEN"), CodeSecret: os.Getenv("USER_CODE_SECRET"),
		AllowedDomains: strings.Split(strings.ToLower(os.Getenv("USER_ALLOWED_EMAIL_DOMAINS")), ","),
		SMTPAddress:    os.Getenv("USER_SMTP_ADDRESS"), SMTPFrom: os.Getenv("USER_SMTP_FROM"),
		SMTPUsername: os.Getenv("USER_SMTP_USERNAME"), SMTPPassword: os.Getenv("USER_SMTP_PASSWORD"),
	}
	var err error
	if c.SecureCookies, err = strconv.ParseBool(os.Getenv("USER_COOKIE_SECURE")); err != nil {
		return c, errors.New("USER_COOKIE_SECURE must be true or false")
	}
	if c.SMTPTLS, err = strconv.ParseBool(os.Getenv("USER_SMTP_TLS")); err != nil {
		return c, errors.New("USER_SMTP_TLS must be true or false")
	}
	if c.SessionTTL, err = time.ParseDuration(os.Getenv("USER_SESSION_TTL")); err != nil || c.SessionTTL < time.Minute || c.SessionTTL > 7*24*time.Hour {
		return c, errors.New("USER_SESSION_TTL must be between 1m and 168h")
	}
	if c.DatabaseURL == "" || c.SMTPAddress == "" {
		return c, errors.New("database and SMTP addresses are required")
	}
	if len(c.InternalToken) < 32 || len(c.CodeSecret) < 32 || strings.Contains(c.InternalToken, "REPLACE") || strings.Contains(c.CodeSecret, "REPLACE") {
		return c, errors.New("set separate random internal and verification secrets of at least 32 characters")
	}
	if c.InternalToken == c.CodeSecret {
		return c, errors.New("internal and verification secrets must differ")
	}
	u, err := url.Parse(c.Origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return c, errors.New("USER_APP_ORIGIN must be an exact HTTP(S) origin without a trailing slash")
	}
	if u.Scheme == "https" && !c.SecureCookies {
		return c, errors.New("HTTPS requires secure cookies")
	}
	if u.Scheme == "http" && (c.SecureCookies || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1")) {
		return c, errors.New("HTTP is only supported for local development with secure cookies disabled")
	}
	if c.SecureCookies && !c.SMTPTLS {
		return c, errors.New("deployed email must use SMTP STARTTLS")
	}
	if (c.SMTPUsername != "" || c.SMTPPassword != "") && !c.SMTPTLS {
		return c, errors.New("SMTP credentials require STARTTLS")
	}
	a, err := mail.ParseAddress(c.SMTPFrom)
	if err != nil || a.Address != c.SMTPFrom || strings.ContainsAny(c.SMTPFrom, "\r\n") {
		return c, errors.New("USER_SMTP_FROM must be a plain email address")
	}
	for i, d := range c.AllowedDomains {
		c.AllowedDomains[i] = strings.TrimSpace(d)
		if c.AllowedDomains[i] == "" || strings.ContainsAny(d, "@/ *") {
			return c, errors.New("set an explicit comma-separated school-domain allowlist")
		}
	}
	return c, nil
}

func (c Config) CookieName() string {
	if c.SecureCookies {
		return "__Host-foc_session"
	}
	return "foc_session"
}
