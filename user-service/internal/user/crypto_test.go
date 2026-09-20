package user

import (
	"strings"
	"testing"
)

func TestPasswordsAndTokens(t *testing.T) {
	password := "a long test password"
	a, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	b, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || !checkPassword(a, password) || checkPassword(a, "wrong password") || checkPassword("malformed", password) {
		t.Fatal("hash salt or verification failed")
	}
	if validPassword("too short") || validPassword(strings.Repeat("x", 129)) || !validPassword(strings.Repeat("界", 12)) {
		t.Fatal("password boundaries failed")
	}
	token, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	if !validToken(token) || validToken("short") || string(tokenHash(token)) == token {
		t.Fatal("token handling failed")
	}
	code, err := newCode()
	if err != nil || len(code) != 8 {
		t.Fatal("code generation failed")
	}
	if string(codeHash("secret", "a@school.edu", code)) == string(codeHash("secret", "b@school.edu", code)) {
		t.Fatal("code must be bound to its account")
	}
}

func TestSecureCookie(t *testing.T) {
	if (Config{SecureCookies: true}).CookieName() != "__Host-foc_session" {
		t.Fatal("deployed cookie must use host prefix")
	}
}
