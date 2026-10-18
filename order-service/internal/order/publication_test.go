package order

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicationRetryRestartAndConcurrentPublishers(t *testing.T) {
	a, _ := testApp(t)
	createdForTest(t, a, "publish-one")
	createdForTest(t, a, "publish-two")
	ctx := context.Background()
	if _, err := a.db.Exec(ctx, `CREATE FUNCTION fail_publication() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'unavailable'; END $$;
 CREATE TRIGGER fail_publication BEFORE INSERT ON order_publications FOR EACH ROW EXECUTE FUNCTION fail_publication();`); err != nil {
		t.Fatal(err)
	}
	if err := a.PublishEvents(ctx); err == nil {
		t.Fatal("publication failure was ignored")
	}
	var head int
	if err := a.db.QueryRow(ctx, "SELECT position FROM order_publication_head").Scan(&head); err != nil || head != 0 {
		t.Fatal("failed publication advanced cursor", head, err)
	}
	if _, err := a.db.Exec(ctx, "DROP TRIGGER fail_publication ON order_publications"); err != nil {
		t.Fatal(err)
	}
	workflowSQL(t, a, "UPDATE workflow_retries SET next_attempt_at='-infinity'")
	// Reconstruct the app: publication is recovered from SQL, not memory.
	restarted := New(a.db, a.cfg, a.log)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := restarted.PublishEvents(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var count int
	if err := a.db.QueryRow(ctx, "SELECT count(*) FROM order_publications").Scan(&count); err != nil || count != 2 {
		t.Fatal("duplicate or missing publication", count, err)
	}
	if err := a.db.QueryRow(ctx, "SELECT position FROM order_publication_head").Scan(&head); err != nil || head != 2 {
		t.Fatal("wrong publication head", head, err)
	}
}

func TestPublicationCursorDoesNotSkipLateCommit(t *testing.T) {
	a, _ := testApp(t)
	base := createdForTest(t, a, "publication-base")
	ctx := context.Background()
	tx, err := a.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	late := base
	late.ID += "-late"
	if _, err = tx.Exec(ctx, `INSERT INTO creation_attempts(requester_id,idempotency_key,order_id,request_fingerprint,state,offered_credits)
 SELECT requester_id,idempotency_key||'-late',$2,request_fingerprint,state,offered_credits FROM creation_attempts WHERE order_id=$1`, base.ID, late.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO orders(id,requester_id,pickup,delivery,items,offered_credits,expires_at)
 SELECT $2,requester_id,pickup,delivery,items,offered_credits,expires_at FROM orders WHERE id=$1`, base.ID, late.ID); err != nil {
		t.Fatal(err)
	}
	if err = recordEvent(ctx, tx, late, "OrderCreated"); err != nil {
		t.Fatal(err)
	}
	createdForTest(t, a, "commits-before-late")
	if err = a.PublishEvents(ctx); err != nil {
		t.Fatal(err)
	}
	consume(t, a, timerConsumer)
	consume(t, a, settlementConsumer)
	if workflowCount(t, a, "SELECT count(*) FROM workflow_receipts WHERE event_id=$1", late.ID+":1") != 0 {
		t.Fatal("uncommitted event was consumed")
	}
	before, err := a.publicationBatch(ctx, 0, courierUser)
	if err != nil || len(before) != 2 {
		t.Fatal("published an uncommitted creation", before, err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.PublishEvents(ctx); err != nil {
		t.Fatal(err)
	}
	consume(t, a, timerConsumer)
	consume(t, a, settlementConsumer)
	if workflowCount(t, a, "SELECT count(*) FROM workflow_receipts WHERE event_id=$1", late.ID+":1") != 3 {
		t.Fatal("late commit skipped by a consumer")
	}
	after, err := a.publicationBatch(ctx, before[1].position, courierUser)
	if err != nil || len(after) != 1 || after[0].event.OrderID != late.ID {
		t.Fatal("cursor skipped the late commit", after, err)
	}
}

func TestFailedCreationIsNeverPublished(t *testing.T) {
	a, _ := testApp(t)
	real := a.credit
	a.credit = escrowFaults{escrowClient: real, reserve: func(ctx context.Context, id, user string, amount int64) error {
		if err := real.Reserve(ctx, id, user, amount); err != nil {
			return err
		}
		return creditUnavailable()
	}}
	if got := request(t, a, "POST", "/api/v1/orders", "requester", "failed-publication", validCreateBody(10)); got.Code != 503 {
		t.Fatal(got.Code, got.Body.String())
	}
	if err := a.PublishEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	batch, err := a.publicationBatch(context.Background(), 0, courierUser)
	if err != nil || len(batch) != 0 {
		t.Fatal("abandoned creation was published", batch, err)
	}
}

type sseFrame struct{ kind, id, data string }

func readFrame(t *testing.T, reader *bufio.Scanner) sseFrame {
	t.Helper()
	var frame sseFrame
	for reader.Scan() {
		line := reader.Text()
		if line == "" && frame.kind != "" {
			return frame
		}
		if value, ok := strings.CutPrefix(line, "event: "); ok {
			frame.kind = value
		}
		if value, ok := strings.CutPrefix(line, "id: "); ok {
			frame.id = value
		}
		if value, ok := strings.CutPrefix(line, "data: "); ok {
			frame.data = value
		}
	}
	t.Fatalf("stream ended before frame: %v", reader.Err())
	return frame
}

func openStream(t *testing.T, server *httptest.Server, token, cursor string) *bufio.Scanner {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/orders/stream", nil)
	req.AddCookie(&http.Cookie{Name: "foc_session", Value: token})
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("stream refused", response.StatusCode)
	}
	return bufio.NewScanner(response.Body)
}

func TestCreationStreamAuthorizationFilteringAndReplay(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	eligible := createdForTest(t, a, "stream-open")
	accepted := createdForTest(t, a, "stream-private")
	mustAction(t, a, accepted.ID, "other", "accept", "accepted")
	cancelled := createdForTest(t, a, "stream-cancelled")
	mustAction(t, a, cancelled.ID, "requester", "cancel", "cancelled")
	expired := createdForTest(t, a, "stream-expired")
	if _, err := a.db.Exec(context.Background(), "UPDATE orders SET created_at=clock_timestamp()-interval '2 hours',expires_at=clock_timestamp()-interval '1 hour' WHERE id=$1", expired.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.PublishEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a.Handler())
	t.Cleanup(server.Close)
	for _, token := range []string{"courier", "requester"} {
		t.Run(token, func(t *testing.T) {
			reader := openStream(t, server, token, "0")
			if frame := readFrame(t, reader); frame.kind != "snapshot" {
				t.Fatal(frame)
			}
			count := 0
			for {
				frame := readFrame(t, reader)
				if frame.kind == "checkpoint" {
					break
				}
				var event orderEvent
				if json.Unmarshal([]byte(frame.data), &event) != nil || event.SchemaVersion != 1 {
					t.Fatal(frame)
				}
				if frame.kind == "OrderCreated" {
					if token != "courier" || event.OrderID != eligible.ID || event.Version != 1 {
						t.Fatal("wrong creation audience", frame)
					}
				} else if frame.kind == "OrderStatusChanged" {
					if (event.OrderID != accepted.ID && event.OrderID != cancelled.ID) || event.Version != 2 {
						t.Fatal("wrong removal invalidation", frame)
					}
				} else {
					t.Fatal("unexpected public event", frame)
				}
				count++
			}
			want := 3
			if token == "requester" {
				want = 2
			}
			if count != want {
				t.Fatal("wrong audience", count)
			}
		})
	}
	if got := request(t, a, "GET", "/api/v1/orders/stream", "", "", nil); got.Code != 401 {
		t.Fatal("unauthenticated stream", got.Code)
	}
	for _, cursor := range []string{"-1", "invalid", "999999999999999999999", "999"} {
		req := httptest.NewRequest("GET", "/api/v1/orders/stream", nil)
		req.AddCookie(&http.Cookie{Name: "foc_session", Value: "courier"})
		req.Header.Set("Last-Event-ID", cursor)
		got := httptest.NewRecorder()
		a.Handler().ServeHTTP(got, req)
		if got.Code != 400 {
			t.Fatal("invalid cursor accepted", cursor, got.Code)
		}
	}
	for _, token := range []string{"courier", "admin"} {
		got := request(t, a, "GET", "/api/v1/orders/publication-status", token, "", nil)
		want := 403
		if token == "admin" {
			want = 200
		}
		if got.Code != want {
			t.Fatal("publication monitoring authorization", got.Code)
		}
	}
}

type revocableStreamAuth struct{ revoked atomic.Bool }

func (a *revocableStreamAuth) Validate(r *http.Request) (Identity, error) {
	if a.revoked.Load() {
		return Identity{}, authUnavailable()
	}
	return actorAuth{}.Validate(r)
}

func TestStreamRevalidatesSessionBeforeFurtherDelivery(t *testing.T) {
	a, _ := testApp(t)
	auth := &revocableStreamAuth{}
	a.auth = auth
	server := httptest.NewServer(a.Handler())
	t.Cleanup(server.Close)
	reader := openStream(t, server, "courier", "")
	if frame := readFrame(t, reader); frame.kind != "snapshot" {
		t.Fatal(frame)
	}
	if frame := readFrame(t, reader); frame.kind != "checkpoint" {
		t.Fatal(frame)
	}
	createdForTest(t, a, "after-connect")
	if err := a.PublishEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	auth.revoked.Store(true)
	if frame := readFrame(t, reader); frame.kind != "session_invalidated" {
		t.Fatal("delivered after session invalidation", frame)
	}
}

func TestStreamReconnectReplaysMissedCreation(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	first := createdForTest(t, a, "before-disconnect")
	if err := a.PublishEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a.Handler())
	t.Cleanup(server.Close)
	cursor := ""
	t.Run("first connection", func(t *testing.T) {
		reader := openStream(t, server, "courier", "0")
		readFrame(t, reader) // snapshot
		frame := readFrame(t, reader)
		if frame.kind != "OrderCreated" || !strings.Contains(frame.data, first.ID) {
			t.Fatal(frame)
		}
		cursor = frame.id
	})
	second := createdForTest(t, a, "during-disconnect")
	restarted := New(a.db, a.cfg, a.log)
	if err := restarted.PublishEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	reader := openStream(t, server, "courier", cursor)
	readFrame(t, reader)
	frame := readFrame(t, reader)
	if frame.kind != "OrderCreated" || !strings.Contains(frame.data, second.ID) || strings.Contains(frame.data, first.ID) {
		t.Fatal("missed or duplicated replay", frame)
	}
}
