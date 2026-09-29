package user

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// CreditClient uses Credit Service's private, idempotent allocation endpoint.
// The service credential never leaves the backend.
type CreditClient struct {
	URL, Token string
	Client     *http.Client
}

func (c CreditClient) AllocateInitial(ctx context.Context, userID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/internal/v1/wallets/"+userID+"/initial-allocation", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK {
		return errors.New("credit allocation was not confirmed")
	}
	return nil
}

func NewCreditClient(cfg Config) CreditClient {
	return CreditClient{
		URL: cfg.CreditServiceURL, Token: cfg.CreditServiceToken,
		Client: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
}

func creditPending() error {
	return problem(503, "credit_setup_pending", "Your email is verified, but credits are still being set up. Please try logging in shortly.")
}

// ensureAllocation is the fast path used by verification and login. A crash
// after Credit commits but before this job is marked done is safe to retry.
func (a *App) ensureAllocation(ctx context.Context, userID string) error {
	var completed bool
	err := a.db.QueryRow(ctx, "SELECT completed_at IS NOT NULL FROM credit_allocations WHERE user_id=$1", userID).Scan(&completed)
	if errors.Is(err, pgx.ErrNoRows) {
		// Pre-integration verified accounts are deliberately not backfilled.
		return nil
	}
	if err != nil {
		a.log.Error("could not read credit allocation status", "userId", userID)
		return creditPending()
	}
	if completed {
		return nil
	}
	if err = a.allocator.AllocateInitial(ctx, userID); err != nil {
		a.recordAllocationFailure(ctx, userID)
		return creditPending()
	}
	if _, err = a.db.Exec(ctx, "UPDATE credit_allocations SET completed_at=now(), lease_until=NULL WHERE user_id=$1 AND completed_at IS NULL", userID); err != nil {
		return creditPending()
	}
	return nil
}

func (a *App) recordAllocationFailure(ctx context.Context, userID string) {
	_, err := a.db.Exec(ctx, `UPDATE credit_allocations SET attempts=attempts+1,
		next_attempt_at=now()+make_interval(secs => LEAST(300, 5 * (1 << LEAST(attempts, 6))))
		WHERE user_id=$1 AND completed_at IS NULL`, userID)
	if err != nil {
		a.log.Error("could not record credit allocation retry", "userId", userID)
	}
}

// RunAllocationWorker retries committed jobs after process restarts and outages.
// Leases let multiple User Service replicas safely claim work without holding
// a database transaction open during the network request.
func (a *App) RunAllocationWorker(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		attempt, cancel := context.WithTimeout(ctx, 8*time.Second)
		err := a.processAllocation(attempt)
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			a.log.Error("credit allocation retry failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) processAllocation(ctx context.Context) error {
	var userID string
	err := a.db.QueryRow(ctx, `WITH candidate AS (
		SELECT user_id FROM credit_allocations
		WHERE completed_at IS NULL AND next_attempt_at<=now()
		AND (lease_until IS NULL OR lease_until<now())
		ORDER BY next_attempt_at, user_id FOR UPDATE SKIP LOCKED LIMIT 1
	)
	UPDATE credit_allocations AS a SET lease_until=now()+interval '15 seconds'
	FROM candidate WHERE a.user_id=candidate.user_id RETURNING a.user_id`,
	).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = a.allocator.AllocateInitial(ctx, userID); err != nil {
		a.recordAllocationFailure(ctx, userID)
		_, _ = a.db.Exec(ctx, "UPDATE credit_allocations SET lease_until=NULL WHERE user_id=$1 AND completed_at IS NULL", userID)
		return errors.New("credit allocation not confirmed")
	}
	_, err = a.db.Exec(ctx, "UPDATE credit_allocations SET completed_at=now(), lease_until=NULL WHERE user_id=$1 AND completed_at IS NULL", userID)
	return err
}

var _ InitialAllocator = CreditClient{}
