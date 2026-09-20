package user

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testPassword = "a safe testing password"

type testMailer struct {
	mu     sync.Mutex
	codes  map[string]string
	tokens map[string]string
	fail   bool
}

func (m *testMailer) SendVerification(_ context.Context, email, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("simulated SMTP failure")
	}
	m.codes[email] = code
	return nil
}
func (m *testMailer) code(email string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.codes[email]
}
func (m *testMailer) token(email string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tokens[email]
}
func rememberRegistration(t *testing.T, m *testMailer, email string, w *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		RegistrationToken string `json:"registrationToken"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || !validToken(body.RegistrationToken) {
		t.Fatal("registration context missing")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[email] = body.RegistrationToken
}

func testApp(t *testing.T) (*App, *testMailer) {
	t.Helper()
	url := os.Getenv("USER_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set USER_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Database != "user_service_test" {
		t.Fatal("tests require the dedicated user_service_test database")
	}
	ctx := context.Background()
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	token, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(token[:16], "-", "a"), "_", "b"))
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	})
	if err = Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, db); err != nil {
		t.Fatal("migration was not repeatable:", err)
	}
	mailer := &testMailer{codes: map[string]string{}, tokens: map[string]string{}}
	app, err := New(db, Config{Origin: "http://localhost:3000", AllowedDomains: []string{"u.nus.edu"}, SessionTTL: 24 * time.Hour, InternalToken: strings.Repeat("i", 32), CodeSecret: strings.Repeat("c", 32)}, mailer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return app, mailer
}

func request(t *testing.T, h http.Handler, method, path string, body any, cookie *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://localhost:3000")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func status(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("HTTP %d, expected %d: %s", w.Code, want, w.Body.String())
	}
}
func registerTest(t *testing.T, a *App, email, name string) {
	t.Helper()
	w := request(t, a.PublicHandler(), "POST", "/api/v1/auth/register", map[string]string{"email": email, "displayName": name, "password": testPassword}, nil, nil)
	status(t, w, 202)
	rememberRegistration(t, a.mailer.(*testMailer), email, w)
}
func verifyTest(t *testing.T, a *App, m *testMailer, email string) {
	t.Helper()
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/verify-email", map[string]string{"email": email, "code": m.code(email), "registrationToken": m.token(email)}, nil, nil), 200)
}
func loginTest(t *testing.T, a *App, email, password string) *http.Cookie {
	t.Helper()
	w := request(t, a.PublicHandler(), "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": password}, nil, nil)
	status(t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("expected one session cookie")
	}
	return cookies[0]
}
func validateTest(t *testing.T, a *App, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	return request(t, a.InternalHandler(), "POST", "/internal/v1/sessions/validate", map[string]string{"sessionToken": cookie.Value}, nil, map[string]string{"Authorization": "Bearer " + a.cfg.InternalToken})
}

func TestAccountLifecycle(t *testing.T) {
	a, m := testApp(t)
	email := "keith@u.nus.edu"
	registerTest(t, a, email, "Keith")
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": testPassword}, nil, nil), 401)
	verifyTest(t, a, m, email)
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/verify-email", map[string]string{"email": email, "code": m.code(email), "registrationToken": m.token(email)}, nil, nil), 400)
	cookie := loginTest(t, a, email, testPassword)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatal("cookie protections missing")
	}
	status(t, request(t, a.PublicHandler(), "GET", "/api/v1/users/me", nil, cookie, nil), 200)
	status(t, validateTest(t, a, cookie), 200)
	var storedHash string
	var sessions int
	if err := a.db.QueryRow(context.Background(), "SELECT password_hash FROM users WHERE email=$1", email).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(storedHash, passwordPrefix) {
		t.Fatal("password not Argon2id hashed")
	}
	if err := a.db.QueryRow(context.Background(), "SELECT count(*) FROM sessions WHERE token_hash=$1", tokenHash(cookie.Value)).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatal("session digest missing")
	}
	status(t, request(t, a.PublicHandler(), "PATCH", "/api/v1/users/me", map[string]string{"displayName": "Keith Updated"}, cookie, nil), 200)
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/logout", nil, cookie, nil), 204)
	status(t, validateTest(t, a, cookie), 401)
	status(t, request(t, a.PublicHandler(), "GET", "/api/v1/users/me", nil, cookie, nil), 401)
}

func TestVerificationLimitsResendAndExpiry(t *testing.T) {
	a, m := testApp(t)
	email := "codes@u.nus.edu"
	registerTest(t, a, email, "Codes")
	old := m.code(email)
	for i := 0; i < 5; i++ {
		status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/verify-email", map[string]string{"email": email, "code": "wrong", "registrationToken": m.token(email)}, nil, nil), 400)
	}
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/verify-email", map[string]string{"email": email, "code": old, "registrationToken": m.token(email)}, nil, nil), 400)
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/resend-verification", map[string]string{"email": email}, nil, nil), 202)
	if m.code(email) != old {
		t.Fatal("resend cooldown ignored")
	}
	if _, err := a.db.Exec(context.Background(), "UPDATE verification_challenges SET sent_at=now()-interval '61 seconds'"); err != nil {
		t.Fatal(err)
	}
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/resend-verification", map[string]string{"email": email}, nil, nil), 202)
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/verify-email", map[string]string{"email": email, "code": old, "registrationToken": m.token(email)}, nil, nil), 400)
	if _, err := a.db.Exec(context.Background(), "UPDATE verification_challenges SET expires_at=now()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/verify-email", map[string]string{"email": email, "code": m.code(email), "registrationToken": m.token(email)}, nil, nil), 400)
}

func TestConcurrentVerificationAndNameConflict(t *testing.T) {
	a, m := testApp(t)
	email := "one@u.nus.edu"
	registerTest(t, a, email, "Same")
	registerTest(t, a, "two@u.nus.edu", "same")
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := request(t, a.PublicHandler(), "POST", "/api/v1/auth/verify-email", map[string]string{"email": email, "code": m.code(email), "registrationToken": m.token(email)}, nil, nil)
			results <- w.Code
		}()
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for code := range results {
		counts[code]++
	}
	if counts[200] != 1 || counts[400] != 1 {
		t.Fatalf("one activation expected: %v", counts)
	}
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/verify-email", map[string]string{"email": "two@u.nus.edu", "code": m.code("two@u.nus.edu"), "registrationToken": m.token("two@u.nus.edu")}, nil, nil), 409)
	// Name conflict must roll back challenge consumption, so a new name can succeed.
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/verify-email", map[string]string{"email": "two@u.nus.edu", "code": m.code("two@u.nus.edu"), "registrationToken": m.token("two@u.nus.edu"), "displayName": "Different"}, nil, nil), 200)
}

func TestPasswordChangeRevokesAllSessions(t *testing.T) {
	a, m := testApp(t)
	email := "password@u.nus.edu"
	registerTest(t, a, email, "Password")
	verifyTest(t, a, m, email)
	one := loginTest(t, a, email, testPassword)
	two := loginTest(t, a, email, testPassword)
	status(t, request(t, a.PublicHandler(), "PUT", "/api/v1/users/me/password", map[string]string{"currentPassword": "wrong password", "newPassword": "another long password"}, one, nil), 401)
	status(t, validateTest(t, a, two), 200)
	status(t, request(t, a.PublicHandler(), "PUT", "/api/v1/users/me/password", map[string]string{"currentPassword": testPassword, "newPassword": "another long password"}, one, nil), 200)
	status(t, validateTest(t, a, one), 401)
	status(t, validateTest(t, a, two), 401)
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": testPassword}, nil, nil), 401)
	loginTest(t, a, email, "another long password")
}

func TestAdminPermissions(t *testing.T) {
	a, m := testApp(t)
	registerTest(t, a, "admin@u.nus.edu", "Admin")
	verifyTest(t, a, m, "admin@u.nus.edu")
	registerTest(t, a, "student@u.nus.edu", "Student")
	verifyTest(t, a, m, "student@u.nus.edu")
	student := loginTest(t, a, "student@u.nus.edu", testPassword)
	admin := loginTest(t, a, "admin@u.nus.edu", testPassword)
	u, _, err := a.session(context.Background(), student.Value)
	if err != nil {
		t.Fatal(err)
	}
	status(t, request(t, a.PublicHandler(), "PUT", "/api/v1/admin/users/"+u.ID+"/role", map[string]string{"role": "admin"}, admin, nil), 403)
	if err = PromoteAdmin(context.Background(), a.db, "admin@u.nus.edu"); err != nil {
		t.Fatal(err)
	}
	admin = loginTest(t, a, "admin@u.nus.edu", testPassword)
	status(t, request(t, a.PublicHandler(), "PUT", "/api/v1/admin/users/"+u.ID+"/role", map[string]string{"role": "admin"}, admin, nil), 200)
	status(t, validateTest(t, a, student), 401)
	student = loginTest(t, a, "student@u.nus.edu", testPassword)
	w := validateTest(t, a, student)
	status(t, w, 200)
	if !strings.Contains(w.Body.String(), "admin") {
		t.Fatal("role missing")
	}
	status(t, request(t, a.PublicHandler(), "PUT", "/api/v1/admin/users/"+u.ID+"/role", map[string]string{"role": "user"}, student, nil), 400)
	status(t, request(t, a.PublicHandler(), "PUT", "/api/v1/admin/users/"+strings.ToUpper(u.ID)+"/role", map[string]string{"role": "user"}, student, nil), 400)
	status(t, request(t, a.PublicHandler(), "PUT", "/api/v1/admin/users/"+u.ID+"/role", map[string]string{"role": "user"}, admin, nil), 200)
	status(t, validateTest(t, a, student), 401)
}

func TestBoundaryProtection(t *testing.T) {
	a, m := testApp(t)
	for _, origin := range []string{"", "https://evil.example", "null"} {
		status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/logout", nil, nil, map[string]string{"Origin": origin}), 403)
	}
	status(t, request(t, a.PublicHandler(), "POST", "/internal/v1/sessions/validate", map[string]string{"sessionToken": "x"}, nil, nil), 404)
	status(t, request(t, a.InternalHandler(), "POST", "/internal/v1/sessions/validate", map[string]string{"sessionToken": "x"}, nil, nil), 401)
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/register", map[string]any{"email": "x@u.nus.edu", "displayName": "X", "password": testPassword, "isAdmin": true}, nil, nil), 400)
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/register", map[string]string{"email": "x@u.nus.edu.evil.com", "displayName": "X", "password": testPassword}, nil, nil), 400)
	registerTest(t, a, "expire@u.nus.edu", "Expire")
	verifyTest(t, a, m, "expire@u.nus.edu")
	cookie := loginTest(t, a, "expire@u.nus.edu", testPassword)
	if _, err := a.db.Exec(context.Background(), "UPDATE sessions SET expires_at=now()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	status(t, validateTest(t, a, cookie), 401)
}

func TestEmailFailureAndDuplicateRegistration(t *testing.T) {
	a, m := testApp(t)
	m.fail = true
	w := request(t, a.PublicHandler(), "POST", "/api/v1/auth/register", map[string]string{"email": "retry@u.nus.edu", "displayName": "Retry", "password": testPassword}, nil, nil)
	status(t, w, 503)
	rememberRegistration(t, m, "retry@u.nus.edu", w)
	m.fail = false
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/resend-verification", map[string]string{"email": "retry@u.nus.edu"}, nil, nil), 202)
	verifyTest(t, a, m, "retry@u.nus.edu")
	// Registration can never replace a verified account's password.
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/register", map[string]string{"email": "retry@u.nus.edu", "displayName": "Other", "password": "attacker password"}, nil, nil), 202)
	loginTest(t, a, "retry@u.nus.edu", testPassword)
}

func TestPendingRegistrationCannotBeHijacked(t *testing.T) {
	a, m := testApp(t)
	email := "owner@u.nus.edu"
	w := request(t, a.PublicHandler(), "POST", "/api/v1/auth/register", map[string]string{"email": email, "displayName": "Attacker", "password": "attacker chosen password"}, nil, nil)
	status(t, w, 202)
	rememberRegistration(t, m, email, w)
	oldToken := m.token(email)
	oldCode := m.code(email)
	registerTest(t, a, email, "Owner")
	for _, pair := range [][2]string{{oldToken, m.code(email)}, {m.token(email), oldCode}, {"", m.code(email)}} {
		status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/verify-email", map[string]string{"email": email, "code": pair[1], "registrationToken": pair[0]}, nil, nil), 400)
	}
	verifyTest(t, a, m, email)
	loginTest(t, a, email, testPassword)
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": "attacker chosen password"}, nil, nil), 401)
}

func TestLoginRateLimit(t *testing.T) {
	a, _ := testApp(t)
	for i := 0; i < 10; i++ {
		status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/login", map[string]string{"email": "unknown@u.nus.edu", "password": testPassword}, nil, nil), 401)
	}
	status(t, request(t, a.PublicHandler(), "POST", "/api/v1/auth/login", map[string]string{"email": "unknown@u.nus.edu", "password": testPassword}, nil, map[string]string{"X-Forwarded-For": "8.8.8.8"}), 429)
}
