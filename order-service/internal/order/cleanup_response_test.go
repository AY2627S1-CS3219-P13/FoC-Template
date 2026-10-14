package order

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestReview05ExpiryWithFailedRefundReportsCleanupPending(t *testing.T) {
	a, f := testApp(t)
	real := a.credit
	expiry := time.Now().Add(time.Second)
	a.credit = escrowFaults{
		escrowClient: real,
		reserve: func(ctx context.Context, id, user string, amount int64) error {
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
		},
		release: func(context.Context, string) error { return creditUnavailable() },
	}
	body := validCreateBody(30)
	body["expiresAt"] = expiry.Format(time.RFC3339Nano)
	response := request(t, a, "POST", "/api/v1/orders", "session", "expired-unrefunded", body)
	assertFinancialResult(t, a, f, "expired-unrefunded", "release_pending", 0, 70)
	if !strings.Contains(response.Body.String(), "creation_cleanup_pending") {
		t.Fatal(response.Body.String())
	}
	if response.Code != 503 {
		t.Fatalf("reservation succeeded and refund failed: got HTTP %d with 30 credits still reserved; want 503 cleanup-pending so the client retains its key", response.Code)
	}
	assertStatus(t, request(t, a, "POST", "/api/v1/orders", "session", "expired-unrefunded", body), 503)
	a.credit = real
	assertStatus(t, request(t, a, "POST", "/api/v1/orders", "session", "expired-unrefunded", body), 503)
	assertFinancialResult(t, a, f, "expired-unrefunded", "abandoned", 0, 100)
	assertStatus(t, request(t, a, "POST", "/api/v1/orders", "session", "expired-unrefunded", body), 409)

}
