package schedule

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const requester = "11111111-1111-4111-8111-111111111111"
const courier = "55555555-5555-4555-8555-555555555555"

type dependencies struct {
	mu                        sync.Mutex
	server                    *httptest.Server
	reserve, release, imports int
	outage, lost, malformed   bool
	outcome                   string
	escrows                   map[string]bool
}

func fixture(t *testing.T) (*App, *dependencies) {
	t.Helper()
	ctx := context.Background()
	cfg, e := pgxpool.ParseConfig(os.Getenv("SCHEDULE_TEST_DATABASE_URL"))
	if e != nil || cfg.ConnConfig.Database != "schedule_order_test" {
		t.Fatal("dedicated SCHEDULE_TEST_DATABASE_URL required", e)
	}
	admin, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("test_%x", time.Now().UnixNano())
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		db.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	})
	if e = Migrate(ctx, db); e != nil {
		t.Fatal(e)
	}
	f := &dependencies{escrows: map[string]bool{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/internal/v1/sessions/validate":
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["sessionToken"] == "" || b["sessionToken"] == "invalid" {
				w.WriteHeader(401)
				_, _ = io.WriteString(w, `{"error":{"code":"invalid_session"}}`)
				return
			}
			id := requester
			if b["sessionToken"] == "courier" {
				id = courier
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"userId": id, "roles": []string{"user"}, "expiresAt": time.Now().Add(time.Hour)})
		case r.URL.Path == "/internal/v1/locations":
			_ = json.NewEncoder(w).Encode(map[string]any{"locations": []map[string]string{{"id": "com2", "name": "COM2"}, {"id": "utown", "name": "UTown"}}})
		case strings.HasPrefix(r.URL.Path, "/internal/v1/escrows/"):
			id := strings.TrimPrefix(r.URL.Path, "/internal/v1/escrows/")
			if r.Method == "PUT" {
				if !f.escrows[id] {
					f.reserve++
					f.escrows[id] = true
				}
				_, _ = io.WriteString(w, `{"userId":"`+requester+`","available":90,"reserved":10}`)
			} else {
				key := strings.Split(id, "/")[0]
				if f.escrows[key] {
					f.release++
					f.escrows[key] = false
				}
				if strings.HasSuffix(id, "/abort") {
					w.WriteHeader(204)
				} else {
					_, _ = io.WriteString(w, `{"userId":"`+requester+`","available":100,"reserved":0}`)
				}
			}
		case strings.HasPrefix(r.URL.Path, "/internal/v1/scheduled-orders/"):
			f.imports++
			if f.outage {
				w.WriteHeader(503)
				return
			}
			f.outcome = "accepted"
			if f.lost {
				w.WriteHeader(503)
				return
			}
			_, _ = io.WriteString(w, `{"outcome":"accepted"}`)
			if f.malformed {
				_, _ = io.WriteString(w, "trailing garbage")
			}
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.server.Close)
	c := Config{DatabaseURL: "unused", Origin: "http://localhost:3000", SessionCookie: "foc_session", UserServiceURL: f.server.URL, UserServiceToken: strings.Repeat("u", 32), SupplierServiceURL: f.server.URL, SupplierServiceToken: strings.Repeat("s", 32), CreditServiceURL: f.server.URL, CreditServiceToken: strings.Repeat("c", 32), DependencyTimeout: time.Second}
	a := New(db, c, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.SetImporter(func(ctx context.Context, transfer string, raw []byte) (Effect, error) {
		return func(ctx context.Context, tx pgx.Tx) (string, error) {
			req, _ := http.NewRequestWithContext(ctx, "PUT", f.server.URL+"/internal/v1/scheduled-orders/"+transfer, bytes.NewReader(raw))
			resp, err := a.client.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				return "", errors.New("import unavailable")
			}
			var result struct{ Outcome string }
			dec := json.NewDecoder(resp.Body)
			if dec.Decode(&result) != nil || dec.Decode(new(any)) != io.EOF {
				return "", errors.New("invalid outcome")
			}
			return result.Outcome, nil
		}, nil
	})
	a.now = func() time.Time { return instant("2026-11-03T08:00:00+08:00") }
	return a, f
}
func call(t *testing.T, a *App, method, path, who, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("Origin", "http://localhost:3000")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", key)
	if who != "" {
		r.AddCookie(&http.Cookie{Name: "foc_session", Value: who})
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}
func validBody() map[string]any {
	return map[string]any{"pickup": map[string]string{"type": "location", "id": "com2"}, "delivery": map[string]string{"type": "location", "id": "utown"}, "items": []map[string]any{{"name": "Tea", "quantity": 1}}, "offeredCredits": 10, "windowStart": "2026-11-03T09:00:00+08:00"}
}
func create(t *testing.T, a *App, key string) string {
	t.Helper()
	w := call(t, a, "POST", "/api/v1/scheduled-orders", "requester", key, validBody())
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var b struct{ Order struct{ ID string } }
	if e := json.Unmarshal(w.Body.Bytes(), &b); e != nil {
		t.Fatal(e)
	}
	return b.Order.ID
}
func TestCreateRetryCancellationAndExpiry(t *testing.T) {
	a, f := fixture(t)
	id := create(t, a, "one")
	a.now = func() time.Time { return instant("2026-11-03T08:30:00+08:00") }
	if w := call(t, a, "POST", "/api/v1/scheduled-orders", "requester", "one", validBody()); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	changed := validBody()
	changed["offeredCredits"] = 11
	if w := call(t, a, "POST", "/api/v1/scheduled-orders", "requester", "one", changed); w.Code != 409 {
		t.Fatal("changed key", w.Code)
	}
	if w := call(t, a, "POST", "/api/v1/scheduled-orders/"+id+"/actions", "courier", "", map[string]string{"action": "cancel"}); w.Code != 403 {
		t.Fatal("unauthorized cancel", w.Code)
	}
	if w := call(t, a, "POST", "/api/v1/scheduled-orders/"+id+"/actions", "requester", "", map[string]string{"action": "cancel"}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for i := 0; i < 2; i++ {
		if e := recoverFixture(t, a, context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	if f.reserve != 1 || f.release != 1 {
		t.Fatal("financial effects", f.reserve, f.release)
	}
}
func TestAsyncTransferLostResponseFencesCancellationAndRestart(t *testing.T) {
	a, f := fixture(t)
	id := create(t, a, "transfer")
	f.outage = true
	if w := call(t, a, "POST", "/api/v1/scheduled-orders/"+id+"/actions", "courier", "", map[string]string{"action": "accept"}); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	_ = recoverFixture(t, a, context.Background())
	if f.imports == 0 {
		t.Fatal("no asynchronous dispatch")
	}
	if w := call(t, a, "POST", "/api/v1/scheduled-orders/"+id+"/actions", "requester", "", map[string]string{"action": "cancel"}); w.Code != 409 {
		t.Fatal("unresolved transfer refunded", w.Code)
	}
	f.outage = false
	f.lost = true
	_ = recoverFixture(t, a, context.Background())
	f.lost = false
	restarted := New(a.db, a.cfg, a.log)
	restarted.now = a.now
	restarted.SetImporter(a.importer)
	if e := recoverFixture(t, restarted, context.Background()); e != nil {
		t.Fatal(e)
	}
	var state string
	if e := a.db.QueryRow(context.Background(), "SELECT state FROM schedules WHERE id=$1", id).Scan(&state); e != nil || state != "transferred" {
		t.Fatal(state, e)
	}
	if f.release != 0 || f.reserve != 1 {
		t.Fatal("transfer moved credits", f.release, f.reserve)
	}
}
func TestValidationAuthorizationAndLateExpiry(t *testing.T) {
	a, f := fixture(t)
	for _, who := range []string{"", "invalid"} {
		if w := call(t, a, "POST", "/api/v1/scheduled-orders", who, "key", validBody()); w.Code != 401 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	b := validBody()
	b["expiresAt"] = "2026-11-03T12:00:00+08:00"
	if w := call(t, a, "POST", "/api/v1/scheduled-orders", "requester", "unknown", b); w.Code != 400 {
		t.Fatal("editable expiry", w.Code)
	}
	id := create(t, a, "expiry")
	a.now = func() time.Time { return instant("2026-11-03T09:30:00+08:00") }
	if e := recoverFixture(t, a, context.Background()); e != nil {
		t.Fatal(e)
	}
	if w := call(t, a, "POST", "/api/v1/scheduled-orders/"+id+"/actions", "courier", "", map[string]string{"action": "accept"}); w.Code != 409 {
		t.Fatal("accepted expired", w.Code)
	}
	if f.release != 1 {
		t.Fatal("expiry refund", f.release)
	}
}

func TestFailedSaveAbortsReservationAndCannotCompleteOnRetry(t *testing.T) {
	a, f := fixture(t)
	ctx := context.Background()
	_, e := a.db.Exec(ctx, `CREATE FUNCTION reject_schedule_save() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='scheduled' THEN RAISE EXCEPTION 'save failed'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_save BEFORE UPDATE ON schedules FOR EACH ROW EXECUTE FUNCTION reject_schedule_save()`)
	if e != nil {
		t.Fatal(e)
	}
	if w := call(t, a, "POST", "/api/v1/scheduled-orders", "requester", "failed-save", validBody()); w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
	if f.reserve != 1 {
		t.Fatal("reservation not attempted")
	}
	if w := call(t, a, "POST", "/api/v1/scheduled-orders", "requester", "failed-save", validBody()); w.Code != 503 || !strings.Contains(w.Body.String(), "creation_cleanup_pending") {
		t.Fatalf("unconfirmed cleanup claimed abandoned: %d %s", w.Code, w.Body.String())
	}
	if e = recoverFixture(t, a, ctx); e != nil {
		t.Fatal(e)
	}
	if f.release != 1 {
		t.Fatal("orphaned reservation", f.release)
	}
	if w := call(t, a, "POST", "/api/v1/scheduled-orders", "requester", "failed-save", validBody()); w.Code != 409 {
		t.Fatal("abandoned attempt completed", w.Code)
	}
}
func TestConcurrentScheduleAcceptHasOnePendingWinner(t *testing.T) {
	a, _ := fixture(t)
	id := create(t, a, "race")
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := call(t, a, "POST", "/api/v1/scheduled-orders/"+id+"/actions", "courier", "", map[string]string{"action": "accept"})
			if w.Code != 202 {
				t.Errorf("same-actor retry %d %s", w.Code, w.Body.String())
			}
		}()
	}
	wg.Wait()
	var generation int
	if e := a.db.QueryRow(context.Background(), "SELECT generation FROM schedules WHERE id=$1", id).Scan(&generation); e != nil || generation != 1 {
		t.Fatal("duplicate transfer generation", generation, e)
	}
}

func TestMalformedTransferReplyKeepsOwnershipUnresolved(t *testing.T) {
	a, f := fixture(t)
	id := create(t, a, "malformed")
	if w := call(t, a, "POST", "/api/v1/scheduled-orders/"+id+"/actions", "courier", "", map[string]string{"action": "accept"}); w.Code != 202 {
		t.Fatal(w.Code)
	}
	f.malformed = true
	if e := recoverFixture(t, a, context.Background()); e == nil {
		t.Fatal("malformed reply finalized ownership")
	}
	var state string
	if e := a.db.QueryRow(context.Background(), "SELECT state FROM schedules WHERE id=$1", id).Scan(&state); e != nil || state != "transfer_pending" {
		t.Fatal(state, e)
	}
	f.malformed = false
	if e := recoverFixture(t, a, context.Background()); e != nil {
		t.Fatal(e)
	}
}

func TestCreationReplayAfterTransferUsesAuthoritativeOrder(t *testing.T) {
	a, _ := fixture(t)
	id := create(t, a, "authoritative-retry")
	if w := call(t, a, "POST", "/api/v1/scheduled-orders/"+id+"/actions", "courier", "", map[string]string{"action": "accept"}); w.Code != 202 {
		t.Fatal(w.Code)
	}
	if e := recoverFixture(t, a, context.Background()); e != nil {
		t.Fatal(e)
	}
	w := call(t, a, "POST", "/api/v1/scheduled-orders", "requester", "authoritative-retry", validBody())
	if w.Code != 303 || w.Header().Get("Location") != "/api/v1/orders/"+id {
		t.Fatalf("replayed stale Schedule-owned state after transfer: %d %s", w.Code, w.Body.String())
	}
}

func recoverFixture(t *testing.T, a *App, ctx context.Context) error {
	t.Helper()
	if _, err := a.db.Exec(ctx, "UPDATE schedule_retries SET next_attempt_at=clock_timestamp()"); err != nil {
		t.Fatal(err)
	}
	return errors.Join(a.ConsumeEvents(ctx), a.Recover(ctx))
}
