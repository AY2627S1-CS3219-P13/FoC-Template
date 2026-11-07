package order

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	PricingURL, PricingToken, PricingSourceToken             string
	ScheduleToken                                            string
	SupplierInternalURL, SupplierInternalToken               string
	DatabaseURL, Origin, SessionCookie                       string
	UserServiceURL, UserServiceToken                         string
	SupplierServiceURL, CreditServiceURL, CreditServiceToken string
	DependencyTimeout, RecoveryInterval, RecoveryStaleAfter  time.Duration
	// Nearby alerts are enabled only when both VAPID values are configured.
	VAPIDKey     *ecdsa.PrivateKey
	VAPIDSubject string
}

// loadVAPID accepts a base64 PKCS#8 DER P-256 key and a mailto:/https: contact.
func loadVAPID(rawKey, subject string) (*ecdsa.PrivateKey, error) {
	if rawKey == "" && subject == "" {
		return nil, nil
	}
	invalid := errors.New("set ORDER_VAPID_PRIVATE_KEY to a base64 PKCS#8 P-256 key and ORDER_VAPID_SUBJECT to a mailto: or https: contact")
	der, err := base64.StdEncoding.DecodeString(rawKey)
	if err != nil {
		return nil, invalid
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	key, ok := parsed.(*ecdsa.PrivateKey)
	if err != nil || !ok || key.Curve != elliptic.P256() {
		return nil, invalid
	}
	if !validVAPIDSubject(subject) {
		return nil, invalid
	}
	return key, nil
}

func validVAPIDSubject(subject string) bool {
	if len(subject) > 256 || strings.ContainsAny(subject, " \t\r\n") {
		return false
	}
	if address, ok := strings.CutPrefix(subject, "mailto:"); ok {
		local, domain, found := strings.Cut(address, "@")
		return found && local != "" && strings.Contains(domain, ".") && !strings.ContainsAny(domain, "@/?#")
	}
	u, err := url.Parse(subject)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}

func LoadConfig() (Config, error) {
	c := Config{
		PricingURL: os.Getenv("ORDER_PRICING_URL"), PricingToken: os.Getenv("ORDER_PRICING_TOKEN"), PricingSourceToken: os.Getenv("ORDER_PRICING_SOURCE_TOKEN"),
		ScheduleToken:       os.Getenv("ORDER_SCHEDULE_INTERNAL_TOKEN"),
		SupplierInternalURL: os.Getenv("ORDER_SUPPLIER_INTERNAL_URL"), SupplierInternalToken: os.Getenv("ORDER_SUPPLIER_INTERNAL_TOKEN"),
		DatabaseURL: os.Getenv("ORDER_DATABASE_URL"), Origin: os.Getenv("ORDER_APP_ORIGIN"),
		SessionCookie: os.Getenv("ORDER_SESSION_COOKIE"), UserServiceURL: os.Getenv("ORDER_USER_SERVICE_URL"),
		UserServiceToken: os.Getenv("ORDER_USER_INTERNAL_TOKEN"), SupplierServiceURL: os.Getenv("ORDER_SUPPLIER_SERVICE_URL"),
		CreditServiceURL: os.Getenv("ORDER_CREDIT_SERVICE_URL"), CreditServiceToken: os.Getenv("ORDER_CREDIT_INTERNAL_TOKEN"),
		DependencyTimeout: 3 * time.Second, RecoveryInterval: 30 * time.Second, RecoveryStaleAfter: time.Minute,
	}
	if c.PricingURL != "" || c.PricingToken != "" || c.PricingSourceToken != "" {
		u, err := url.Parse(c.PricingURL)
		if err != nil || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || !validSecret(c.PricingToken) || !validSecret(c.PricingSourceToken) || c.PricingToken == c.PricingSourceToken {
			return c, errors.New("configure ORDER_PRICING_URL and distinct valid Pricing quote/source tokens")
		}
	}
	if c.ScheduleToken != "" && !validSecret(c.ScheduleToken) {
		return c, errors.New("ORDER_SCHEDULE_INTERNAL_TOKEN must be at least 32 characters")
	}
	if c.SupplierInternalURL != "" || c.SupplierInternalToken != "" {
		u, err := url.Parse(c.SupplierInternalURL)
		if !validSecret(c.SupplierInternalToken) || err != nil || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return c, errors.New("configure a valid ORDER_SUPPLIER_INTERNAL_URL and separate service token")
		}
	}
	var err error
	if c.VAPIDKey, err = loadVAPID(os.Getenv("ORDER_VAPID_PRIVATE_KEY"), os.Getenv("ORDER_VAPID_SUBJECT")); err != nil {
		return c, err
	}
	if c.VAPIDKey != nil {
		c.VAPIDSubject = os.Getenv("ORDER_VAPID_SUBJECT")
		if c.SupplierInternalURL == "" {
			return c, errors.New("nearby alerts need ORDER_SUPPLIER_INTERNAL_URL and its service token")
		}
	}
	if c.DatabaseURL == "" {
		return c, errors.New("ORDER_DATABASE_URL is required")
	}
	if c.SessionCookie == "" {
		c.SessionCookie = "foc_session"
		if origin, err := url.Parse(c.Origin); err == nil && origin.Scheme == "https" {
			c.SessionCookie = "__Host-foc_session"
		}
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
