package credit

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	testOrigin        = "http://localhost:3000"
	testInternalToken = "test-internal-token-0123456789abcdef"
	testCookie        = "foc_session"
	initialCredits    = 100
)

// fakeSessions stands in for User Service's session validation.
type fakeSessions struct {
	mu      sync.Mutex
	callers map[string]Caller
	down    bool
}

func (f *fakeSessions) Validate(_ context.Context, token string) (Caller, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return Caller{}, errors.New("simulated User Service outage")
	}
	c, ok := f.callers[token]
	if !ok {
		return Caller{}, ErrInvalidSession
	}
	return c, nil
}

type testEnv struct {
	app      *App
	sessions *fakeSessions
	public   http.Handler
	internal http.Handler
}

// newTestEnv gives each test an empty, migrated schema in the dedicated test database.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	url := os.Getenv("CREDIT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set CREDIT_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Database != "credit_service_test" {
		t.Fatal("tests require the dedicated credit_service_test database")
	}
	ctx := context.Background()
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + randomHex(t, 8)
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
	sessions := &fakeSessions{callers: map[string]Caller{}}
	cfgApp := Config{InitialCredits: initialCredits, Origin: testOrigin, InternalToken: testInternalToken, SessionCookie: testCookie}
	app := New(db, cfgApp, sessions, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e := &testEnv{app: app, sessions: sessions, public: app.PublicHandler(), internal: app.InternalHandler()}
	// Runs before the database closes: every test must leave history and balances in agreement.
	t.Cleanup(func() { e.checkReconciled(t) })
	return e
}

// checkReconciled fails the test if any wallet differs from the sum of its
// ledger entries, or from the balances recorded on its newest entry.
func (e *testEnv) checkReconciled(t *testing.T) {
	t.Helper()
	rows, err := e.app.db.Query(context.Background(), `SELECT w.user_id::text FROM wallets w
		LEFT JOIN (SELECT user_id, sum(available_delta) AS a, sum(reserved_delta) AS r FROM transactions GROUP BY user_id) s ON s.user_id = w.user_id
		LEFT JOIN LATERAL (SELECT available_after, reserved_after FROM transactions t WHERE t.user_id = w.user_id ORDER BY id DESC LIMIT 1) l ON true
		WHERE coalesce(s.a, 0) <> w.available OR coalesce(s.r, 0) <> w.reserved
		   OR coalesce(l.available_after, 0) <> w.available OR coalesce(l.reserved_after, 0) <> w.reserved`)
	if err != nil {
		t.Errorf("reconciling ledger: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var userID string
		if err = rows.Scan(&userID); err == nil {
			t.Errorf("wallet %s does not match its ledger", userID)
		}
	}
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// newID returns a random UUID, used for both user and order IDs.
func newID(t *testing.T) string {
	t.Helper()
	h := randomHex(t, 16)
	return h[:8] + "-" + h[8:12] + "-4" + h[13:16] + "-a" + h[17:20] + "-" + h[20:]
}

// --- Setup through SQL, so each function's tests do not depend on the others.

func (e *testEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.app.db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// seedMove changes a wallet and appends the matching ledger entry, marked
// 'seed' so ledger() leaves it out, as apply would.
func (e *testEnv) seedMove(t *testing.T, userID, orderID string, kind Kind, amount int64) {
	t.Helper()
	effect := effects[kind]
	e.exec(t, `WITH w AS (UPDATE wallets SET available=available+$4::bigint, reserved=reserved+$5::bigint
			WHERE user_id=$1::uuid RETURNING available, reserved)
		INSERT INTO transactions(user_id, kind, amount, order_id, available_delta, reserved_delta, available_after, reserved_after, note)
		SELECT $1::uuid, $2::text, $3::bigint, NULLIF($6::text, ''), $4::bigint, $5::bigint, w.available, w.reserved, 'seed' FROM w`,
		userID, string(kind), amount, effect.available*amount, effect.reserved*amount, orderID)
}

// seedWallet creates a wallet holding available credits and nothing reserved.
func (e *testEnv) seedWallet(t *testing.T, userID string, available int64) {
	t.Helper()
	e.exec(t, "INSERT INTO wallets(user_id, available) VALUES($1, 0)", userID)
	if available > 0 {
		e.seedMove(t, userID, "", KindAllocation, available)
	}
}

// seedEscrow holds amount of userID's credits for orderID, as Reserve would.
func (e *testEnv) seedEscrow(t *testing.T, userID, orderID string, amount int64) {
	t.Helper()
	e.exec(t, "INSERT INTO reservations(order_id, user_id, amount) VALUES($1, $2, $3)", orderID, userID, amount)
	e.seedMove(t, userID, orderID, KindReserve, amount)
}

func (e *testEnv) escrowOf(t *testing.T, orderID string) (userID string, amount int64) {
	t.Helper()
	if err := e.app.db.QueryRow(context.Background(), "SELECT user_id::text, amount FROM reservations WHERE order_id=$1", orderID).Scan(&userID, &amount); err != nil {
		t.Fatal(err)
	}
	return userID, amount
}

// seedReleased settles an escrow back to its requester, as Release would.
func (e *testEnv) seedReleased(t *testing.T, orderID string) {
	t.Helper()
	userID, amount := e.escrowOf(t, orderID)
	e.exec(t, "UPDATE reservations SET status='released', settled_at=now() WHERE order_id=$1", orderID)
	e.seedMove(t, userID, orderID, KindRelease, amount)
}

// seedPaidOut settles an escrow to courierID, as Transfer would.
func (e *testEnv) seedPaidOut(t *testing.T, orderID, courierID string) {
	t.Helper()
	userID, amount := e.escrowOf(t, orderID)
	e.exec(t, "UPDATE reservations SET status='transferred', courier_id=$2, settled_at=now() WHERE order_id=$1", orderID, courierID)
	e.seedMove(t, userID, orderID, KindSpend, amount)
	e.seedMove(t, courierID, orderID, KindReceive, amount)
}

// signIn registers a session with the fake User Service and returns its cookie.
func (e *testEnv) signIn(t *testing.T, userID string, admin bool) *http.Cookie {
	t.Helper()
	token := randomHex(t, 16)
	e.sessions.mu.Lock()
	e.sessions.callers[token] = Caller{UserID: userID, Admin: admin}
	e.sessions.mu.Unlock()
	return &http.Cookie{Name: testCookie, Value: token}
}

// --- Reading state through SQL, independent of Balance.

func (e *testEnv) stored(t *testing.T, userID string) Wallet {
	t.Helper()
	w := Wallet{UserID: userID}
	err := e.app.db.QueryRow(context.Background(), "SELECT available, reserved FROM wallets WHERE user_id=$1", userID).Scan(&w.Available, &w.Reserved)
	if err != nil {
		t.Fatalf("reading wallet %s: %v", userID, err)
	}
	return w
}

func (e *testEnv) walletExists(t *testing.T, userID string) bool {
	t.Helper()
	var exists bool
	if err := e.app.db.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM wallets WHERE user_id=$1)", userID).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

// escrow returns the escrow's status and, once paid out, its courier.
func (e *testEnv) escrow(t *testing.T, orderID string) (status, courierID string) {
	t.Helper()
	err := e.app.db.QueryRow(context.Background(), "SELECT status, coalesce(courier_id::text, '') FROM reservations WHERE order_id=$1", orderID).Scan(&status, &courierID)
	if err != nil {
		t.Fatalf("reading escrow %s: %v", orderID, err)
	}
	return status, courierID
}

func (e *testEnv) escrowExists(t *testing.T, orderID string) bool {
	t.Helper()
	var exists bool
	if err := e.app.db.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM reservations WHERE order_id=$1)", orderID).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

// ledger lists a user's history entries, oldest first, as "kind amount".
// Entries written by seed helpers are left out.
func (e *testEnv) ledger(t *testing.T, userID string) []string {
	t.Helper()
	rows, err := e.app.db.Query(context.Background(), "SELECT kind, amount FROM transactions WHERE user_id=$1 AND note IS DISTINCT FROM 'seed' ORDER BY id", userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	entries := []string{}
	for rows.Next() {
		var kind string
		var amount int64
		if err = rows.Scan(&kind, &amount); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, fmt.Sprintf("%s %d", kind, amount))
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return entries
}

// totalCredits sums every wallet. Only AllocateInitial and AdminDebit may change it.
func (e *testEnv) totalCredits(t *testing.T) int64 {
	t.Helper()
	var total int64
	if err := e.app.db.QueryRow(context.Background(), "SELECT coalesce(sum(available + reserved), 0) FROM wallets").Scan(&total); err != nil {
		t.Fatal(err)
	}
	return total
}

// --- Assertions.

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func wantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error %v, expected %v", err, target)
	}
}

func wantWallet(t *testing.T, got Wallet, userID string, available, reserved int64) {
	t.Helper()
	want := Wallet{UserID: userID, Available: available, Reserved: reserved}
	if got != want {
		t.Fatalf("wallet %+v, expected %+v", got, want)
	}
}

func (e *testEnv) wantStored(t *testing.T, userID string, available, reserved int64) {
	t.Helper()
	wantWallet(t, e.stored(t, userID), userID, available, reserved)
}

func (e *testEnv) wantLedger(t *testing.T, userID string, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if got := e.ledger(t, userID); !slices.Equal(got, want) {
		t.Fatalf("ledger %q, expected %q", got, want)
	}
}

// concurrently starts n calls of fn at the same moment and returns each error.
// fn must not call t.Fatal; it runs on other goroutines.
func concurrently(n int, fn func(i int) error) []error {
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = fn(i)
		}()
	}
	close(start)
	wg.Wait()
	return errs
}

// count reports how many errors match target; a nil target counts successes.
func count(errs []error, target error) int {
	n := 0
	for _, err := range errs {
		if errors.Is(err, target) {
			n++
		}
	}
	return n
}

// --- HTTP.

// send issues a request. A string body is sent as-is, to test malformed JSON.
func send(t *testing.T, h http.Handler, method, path string, body any, cookie *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader = http.NoBody
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
	default:
		data, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	r := httptest.NewRequest(method, path, reader)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
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

// api calls the public port as the frontend would.
func (e *testEnv) api(t *testing.T, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	return send(t, e.public, method, path, body, cookie, map[string]string{"Origin": testOrigin})
}

// service calls the internal port as Order or User Service would.
func (e *testEnv) service(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return send(t, e.internal, method, path, body, nil, map[string]string{"Authorization": "Bearer " + testInternalToken})
}

func wantStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("HTTP %d, expected %d: %s", w.Code, status, w.Body.String())
	}
}

func wantError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	wantStatus(t, w, status)
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Code != code {
		t.Fatalf("error code %q, expected %q: %s", body.Error.Code, code, w.Body.String())
	}
}

func wantWalletResponse(t *testing.T, w *httptest.ResponseRecorder, userID string, available, reserved int64) {
	t.Helper()
	wantStatus(t, w, http.StatusOK)
	var got Wallet
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not a Wallet: %s", w.Body.String())
	}
	wantWallet(t, got, userID, available, reserved)
}
