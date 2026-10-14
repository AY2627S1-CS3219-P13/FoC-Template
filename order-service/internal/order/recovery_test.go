package order

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type escrowFaults struct {
	escrowClient
	reserve func(context.Context, string, string, int64) error
	release func(context.Context, string) error
}

func (f escrowFaults) Reserve(ctx context.Context, id, user string, amount int64) error {
	if f.reserve != nil {
		return f.reserve(ctx, id, user, amount)
	}
	return f.escrowClient.Reserve(ctx, id, user, amount)
}

func (f escrowFaults) Release(ctx context.Context, id string) error {
	if f.release != nil {
		return f.release(ctx, id)
	}
	return f.escrowClient.Release(ctx, id)
}

func assertFinancialResult(t *testing.T, app *App, f *fakeDependencies, key, state string, count int, available int64) {
	t.Helper()
	var gotState string
	var gotCount int
	if err := app.db.QueryRow(context.Background(), "SELECT state FROM creation_attempts WHERE idempotency_key=$1", key).Scan(&gotState); err != nil {
		t.Fatal(err)
	}
	if err := app.db.QueryRow(context.Background(), "SELECT count(*) FROM orders").Scan(&gotCount); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if gotState != state || gotCount != count || f.available != available {
		t.Fatalf("state=%s orders=%d available=%d; want %s/%d/%d", gotState, gotCount, f.available, state, count, available)
	}
}

func TestLostCreditResponseAbandonsAndRefunds(t *testing.T) {
	app, f := testApp(t)
	real := app.credit
	app.credit = escrowFaults{escrowClient: real, reserve: func(ctx context.Context, id, user string, amount int64) error {
		if err := real.Reserve(ctx, id, user, amount); err != nil {
			return err
		}
		return creditUnavailable() // Credit committed, its response was lost.
	}}
	body := validCreateBody(30)
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", "lost-credit", body), 503)
	assertFinancialResult(t, app, f, "lost-credit", "abandoned", 0, 100)
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", "lost-credit", body), 409)
	assertFinancialResult(t, app, f, "lost-credit", "abandoned", 0, 100)
}

func TestLateReservationCannotEscapeCleanup(t *testing.T) {
	app, f := testApp(t)
	real := app.credit
	var calls atomic.Int32
	late := make(chan error, 1)
	startLate := make(chan struct{})
	app.credit = escrowFaults{escrowClient: real, reserve: func(ctx context.Context, id, user string, amount int64) error {
		if calls.Add(1) == 1 {
			go func() { <-startLate; late <- real.Reserve(context.Background(), id, user, amount) }()
			return creditUnavailable()
		}
		return real.Reserve(ctx, id, user, amount)
	}}
	response := request(t, app, http.MethodPost, "/api/v1/orders", "session", "late-credit", validCreateBody(30))
	close(startLate)
	if err := <-late; err == nil {
		t.Fatal("late reserve must be rejected after the durable abort")
	}
	assertStatus(t, response, 503)
	assertFinancialResult(t, app, f, "late-credit", "abandoned", 0, 100)
	if calls.Load() != 1 {
		t.Fatal("cleanup must not acquire new funds")
	}
}

func TestRestartRetriesLostReleaseResponse(t *testing.T) {
	app, f := testApp(t)
	real := app.credit
	app.credit = escrowFaults{
		escrowClient: real,
		reserve: func(ctx context.Context, id, user string, amount int64) error {
			if err := real.Reserve(ctx, id, user, amount); err != nil {
				return err
			}
			return creditUnavailable()
		},
		release: func(ctx context.Context, id string) error {
			if err := real.Release(ctx, id); err != nil {
				return err
			}
			return creditUnavailable()
		},
	}
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", "lost-release", validCreateBody(30)), 503)
	assertFinancialResult(t, app, f, "lost-release", "release_pending", 0, 100)
	if _, err := app.db.Exec(context.Background(), "UPDATE creation_attempts SET updated_at=now()-interval '1 minute'"); err != nil {
		t.Fatal(err)
	}
	restarted := New(app.db, app.cfg, app.log)
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertFinancialResult(t, app, f, "lost-release", "abandoned", 0, 100)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.releases != 1 {
		t.Fatalf("released %d times", f.releases)
	}
}

func TestExpiryDuringReservationRejectsAndRefunds(t *testing.T) {
	app, f := testApp(t)
	real := app.credit
	expiry := time.Now().Add(time.Second)
	app.credit = escrowFaults{escrowClient: real, reserve: func(ctx context.Context, id, user string, amount int64) error {
		if err := real.Reserve(ctx, id, user, amount); err != nil {
			return err
		}
		timer := time.NewTimer(time.Until(expiry.Add(30 * time.Millisecond)))
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	body := validCreateBody(30)
	body["expiresAt"] = expiry.Format(time.RFC3339Nano)
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", "late-save", body), 400)
	assertFinancialResult(t, app, f, "late-save", "abandoned", 0, 100)
}

func TestRecoverySkipsActiveCreationAndNeverRefundsSavedOrder(t *testing.T) {
	app, f := testApp(t)
	real := app.credit
	entered := make(chan struct{})
	proceed := make(chan struct{})
	app.credit = escrowFaults{escrowClient: real, reserve: func(ctx context.Context, id, user string, amount int64) error {
		close(entered)
		<-proceed
		return real.Reserve(ctx, id, user, amount)
	}}
	result := make(chan int, 1)
	go func() {
		result <- request(t, app, http.MethodPost, "/api/v1/orders", "session", "active", validCreateBody(30)).Code
	}()
	<-entered
	if _, err := app.db.Exec(context.Background(), "UPDATE creation_attempts SET updated_at=now()-interval '1 minute'"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err := app.Recover(ctx)
	cancel()
	close(proceed)
	code := <-result
	if err != nil || code != 201 {
		t.Fatalf("recovery=%v create=%d", err, code)
	}
	if err := app.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertFinancialResult(t, app, f, "active", "completed", 1, 70)
}

func TestOneConnectionPoolDoesNotDeadlock(t *testing.T) {
	app, f := testApp(t)
	cfg := app.db.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	app.db = pool
	assertStatus(t, request(t, app, http.MethodPost, "/api/v1/orders", "session", "one-connection", validCreateBody(100)), 201)
	assertFinancialResult(t, app, f, "one-connection", "completed", 1, 0)
	var locked bool
	if err := pool.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory')").Scan(&locked); err != nil || locked {
		t.Fatalf("leaked session lock: %v, %v", locked, err)
	}
}

func TestRetryCannotCreateWhileRefundIsInFlight(t *testing.T) {
	app, f := testApp(t)
	real := app.credit
	entered, proceed := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	app.credit = escrowFaults{
		escrowClient: real,
		reserve: func(ctx context.Context, id, user string, amount int64) error {
			if err := real.Reserve(ctx, id, user, amount); err != nil {
				return err
			}
			return creditUnavailable()
		},
		release: func(ctx context.Context, id string) error {
			if once.CompareAndSwap(false, true) {
				close(entered)
				<-proceed
			}
			return real.Release(ctx, id)
		},
	}
	body := validCreateBody(30)
	first, second := make(chan int, 1), make(chan int, 1)
	go func() { first <- request(t, app, "POST", "/api/v1/orders", "session", "refund-race", body).Code }()
	<-entered
	go func() { second <- request(t, app, "POST", "/api/v1/orders", "session", "refund-race", body).Code }()
	close(proceed)
	firstCode, secondCode := <-first, <-second
	if firstCode != 503 || secondCode != 409 {
		t.Fatalf("create=%d retry=%d", firstCode, secondCode)
	}
	assertFinancialResult(t, app, f, "refund-race", "abandoned", 0, 100)
}

func TestDefinitiveWalletRefusalRemainsRetryable(t *testing.T) {
	app, f := testApp(t)
	real := app.credit
	var once atomic.Bool
	app.credit = escrowFaults{escrowClient: real, reserve: func(ctx context.Context, id, user string, amount int64) error {
		if once.CompareAndSwap(false, true) {
			return problem(409, "wallet_not_ready", "Wallet is pending. Please retry.")
		}
		return real.Reserve(ctx, id, user, amount)
	}}
	body := validCreateBody(30)
	assertStatus(t, request(t, app, "POST", "/api/v1/orders", "session", "pending-wallet", body), 409)
	assertFinancialResult(t, app, f, "pending-wallet", "pending", 0, 100)
	assertStatus(t, request(t, app, "POST", "/api/v1/orders", "session", "pending-wallet", body), 201)
	assertFinancialResult(t, app, f, "pending-wallet", "completed", 1, 70)
}
