package credit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Endpoint tests: the same edge cases as credit_test.go, through HTTP, plus
// authentication. Status codes and error codes follow API.md.

// statusOf sends a request without *testing.T, so it can run on other goroutines.
func statusOf(h http.Handler, method, path, body string, cookie *http.Cookie, headers map[string]string) int {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

// concurrentStatuses starts n requests at once and returns each HTTP status.
func concurrentStatuses(n int, fn func(i int) int) []int {
	codes := make([]int, n)
	concurrently(n, func(i int) error {
		codes[i] = fn(i)
		return nil
	})
	return codes
}

func countStatus(codes []int, status int) int {
	n := 0
	for _, c := range codes {
		if c == status {
			n++
		}
	}
	return n
}

var serviceAuth = map[string]string{"Authorization": "Bearer " + testInternalToken}

func escrowPath(orderID string) string { return "/internal/v1/escrows/" + orderID }

func TestInternalEndpointsRequireServiceToken(t *testing.T) {
	e := newTestEnv(t)
	u, order := newID(t), newID(t)
	e.seedWallet(t, u, 100)
	e.seedEscrow(t, u, newID(t), 30)
	routes := []struct {
		method, path string
		body         any
	}{
		{"GET", "/internal/v1/wallets/" + u, nil},
		{"GET", "/internal/v1/wallets/" + u + "/transactions", nil},
		{"POST", "/internal/v1/wallets/" + newID(t) + "/initial-allocation", nil},
		{"PUT", escrowPath(order), map[string]any{"userId": u, "amount": 10}},
		{"POST", escrowPath(order) + "/release", nil},
		{"POST", escrowPath(order) + "/payout", map[string]any{"courierId": newID(t)}},
	}
	for _, rt := range routes {
		for _, auth := range []map[string]string{nil, {"Authorization": "Bearer wrong-token"}, {"Authorization": testInternalToken}} {
			w := send(t, e.internal, rt.method, rt.path, rt.body, nil, auth)
			wantError(t, w, http.StatusUnauthorized, "invalid_service_credentials")
		}
	}
	e.wantStored(t, u, 70, 30)
	if e.totalCredits(t) != 100 {
		t.Fatal("unauthenticated calls must not change any wallet")
	}
}

func TestPublicEndpointsRequireSession(t *testing.T) {
	e := newTestEnv(t)
	u := newID(t)
	e.seedWallet(t, u, 100)

	t.Run("no cookie or unknown session", func(t *testing.T) {
		wantError(t, e.api(t, "GET", "/api/v1/wallets/me", nil, nil), http.StatusUnauthorized, "invalid_session")
		unknown := &http.Cookie{Name: testCookie, Value: "unknown"}
		wantError(t, e.api(t, "GET", "/api/v1/wallets/me", nil, unknown), http.StatusUnauthorized, "invalid_session")
	})

	t.Run("User Service unavailable fails closed", func(t *testing.T) {
		cookie := e.signIn(t, u, false)
		e.sessions.mu.Lock()
		e.sessions.down = true
		e.sessions.mu.Unlock()
		t.Cleanup(func() {
			e.sessions.mu.Lock()
			e.sessions.down = false
			e.sessions.mu.Unlock()
		})
		wantError(t, e.api(t, "GET", "/api/v1/wallets/me", nil, cookie), http.StatusServiceUnavailable, "auth_unavailable")
	})
}

func TestPortsAreSeparate(t *testing.T) {
	e := newTestEnv(t)
	u := newID(t)
	e.seedWallet(t, u, 100)
	cookie := e.signIn(t, u, true)
	// Internal routes are not reachable from the public port, even for admins.
	wantStatus(t, send(t, e.public, "POST", "/internal/v1/wallets/"+newID(t)+"/initial-allocation", nil, cookie, map[string]string{"Origin": testOrigin}), http.StatusNotFound)
	wantStatus(t, send(t, e.public, "GET", "/internal/v1/wallets/"+u, nil, cookie, map[string]string{"Origin": testOrigin}), http.StatusNotFound)
	// Browser-session routes are not served on the internal port.
	wantStatus(t, e.service(t, "GET", "/api/v1/wallets/me", nil), http.StatusNotFound)
}

func TestBalanceEndpoints(t *testing.T) {
	t.Run("own wallet shows available and reserved", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		e.seedEscrow(t, u, newID(t), 30)
		wantWalletResponse(t, e.api(t, "GET", "/api/v1/wallets/me", nil, e.signIn(t, u, false)), u, 70, 30)
	})

	t.Run("signed in without a wallet", func(t *testing.T) {
		e := newTestEnv(t)
		w := e.api(t, "GET", "/api/v1/wallets/me", nil, e.signIn(t, newID(t), false))
		wantError(t, w, http.StatusNotFound, "wallet_not_found")
	})

	t.Run("no public route to another user's wallet", func(t *testing.T) {
		e := newTestEnv(t)
		u, other := newID(t), newID(t)
		e.seedWallet(t, other, 100)
		wantStatus(t, e.api(t, "GET", "/api/v1/wallets/"+other, nil, e.signIn(t, u, false)), http.StatusNotFound)
	})

	t.Run("internal by user ID", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		e.seedEscrow(t, u, newID(t), 30)
		wantWalletResponse(t, e.service(t, "GET", "/internal/v1/wallets/"+u, nil), u, 70, 30)
	})

	t.Run("internal unknown user", func(t *testing.T) {
		e := newTestEnv(t)
		wantError(t, e.service(t, "GET", "/internal/v1/wallets/"+newID(t), nil), http.StatusNotFound, "wallet_not_found")
	})

	t.Run("internal malformed user ID", func(t *testing.T) {
		e := newTestEnv(t)
		wantError(t, e.service(t, "GET", "/internal/v1/wallets/not-a-uuid", nil), http.StatusBadRequest, "invalid_input")
	})
}

func TestAllocateEndpoint(t *testing.T) {
	path := func(u string) string { return "/internal/v1/wallets/" + u + "/initial-allocation" }

	t.Run("creates a wallet with the initial credits", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		wantWalletResponse(t, e.service(t, "POST", path(u), nil), u, initialCredits, 0)
		e.wantLedger(t, u, "allocation 100")
	})

	t.Run("repeat allocates nothing", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		wantStatus(t, e.service(t, "POST", path(u), nil), http.StatusOK)
		wantWalletResponse(t, e.service(t, "POST", path(u), nil), u, initialCredits, 0)
		e.wantLedger(t, u, "allocation 100")
	})

	t.Run("repeat after spending does not top up", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		wantStatus(t, e.service(t, "POST", path(u), nil), http.StatusOK)
		e.seedEscrow(t, u, newID(t), 60)
		wantWalletResponse(t, e.service(t, "POST", path(u), nil), u, 40, 60)
	})

	t.Run("concurrent calls allocate exactly once", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		codes := concurrentStatuses(10, func(int) int { return statusOf(e.internal, "POST", path(u), "", nil, serviceAuth) })
		if countStatus(codes, http.StatusOK) != 10 {
			t.Fatalf("every call should succeed: %v", codes)
		}
		e.wantLedger(t, u, "allocation 100")
	})

	t.Run("malformed user ID", func(t *testing.T) {
		e := newTestEnv(t)
		wantError(t, e.service(t, "POST", path("not-a-uuid"), nil), http.StatusBadRequest, "invalid_input")
	})
}

func TestReserveEndpoint(t *testing.T) {
	reserve := func(u string, amount int64) map[string]any { return map[string]any{"userId": u, "amount": amount} }

	t.Run("moves credits from available to reserved", func(t *testing.T) {
		e := newTestEnv(t)
		u, order := newID(t), newID(t)
		e.seedWallet(t, u, 100)
		wantWalletResponse(t, e.service(t, "PUT", escrowPath(order), reserve(u, 30)), u, 70, 30)
		e.wantLedger(t, u, "reserve 30")
	})

	t.Run("whole available balance", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		wantWalletResponse(t, e.service(t, "PUT", escrowPath(newID(t)), reserve(u, 100)), u, 0, 100)
	})

	t.Run("more than available", func(t *testing.T) {
		e := newTestEnv(t)
		u, order := newID(t), newID(t)
		e.seedWallet(t, u, 100)
		wantError(t, e.service(t, "PUT", escrowPath(order), reserve(u, 101)), http.StatusConflict, "insufficient_credits")
		e.wantStored(t, u, 100, 0)
		if e.escrowExists(t, order) {
			t.Fatal("a rejected reservation must not create an escrow")
		}
	})

	t.Run("reserved credits cannot be reserved again", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		e.seedEscrow(t, u, newID(t), 70)
		wantError(t, e.service(t, "PUT", escrowPath(newID(t)), reserve(u, 40)), http.StatusConflict, "insufficient_credits")
	})

	t.Run("non-positive or missing amount", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		for _, amount := range []int64{0, -1} {
			wantError(t, e.service(t, "PUT", escrowPath(newID(t)), reserve(u, amount)), http.StatusBadRequest, "invalid_amount")
		}
		wantError(t, e.service(t, "PUT", escrowPath(newID(t)), map[string]any{"userId": u}), http.StatusBadRequest, "invalid_amount")
		e.wantStored(t, u, 100, 0)
	})

	t.Run("fractional or non-numeric amount", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		for _, amount := range []string{"1.5", `"30"`, "null", "1e999"} {
			body := fmt.Sprintf(`{"userId":%q,"amount":%s}`, u, amount)
			wantStatus(t, e.service(t, "PUT", escrowPath(newID(t)), body), http.StatusBadRequest)
		}
		e.wantStored(t, u, 100, 0)
	})

	t.Run("unknown wallet", func(t *testing.T) {
		e := newTestEnv(t)
		wantError(t, e.service(t, "PUT", escrowPath(newID(t)), reserve(newID(t), 10)), http.StatusNotFound, "wallet_not_found")
	})

	t.Run("malformed or missing user ID", func(t *testing.T) {
		e := newTestEnv(t)
		wantError(t, e.service(t, "PUT", escrowPath(newID(t)), reserve("not-a-uuid", 10)), http.StatusBadRequest, "invalid_input")
		wantError(t, e.service(t, "PUT", escrowPath(newID(t)), map[string]any{"amount": 10}), http.StatusBadRequest, "invalid_input")
	})

	t.Run("malformed JSON or unknown field", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		wantError(t, e.service(t, "PUT", escrowPath(newID(t)), "{"), http.StatusBadRequest, "invalid_json")
		body := map[string]any{"userId": u, "amount": 10, "courierId": newID(t)}
		wantError(t, e.service(t, "PUT", escrowPath(newID(t)), body), http.StatusBadRequest, "invalid_json")
		e.wantStored(t, u, 100, 0)
	})

	t.Run("retry with the same order reserves once", func(t *testing.T) {
		e := newTestEnv(t)
		u, order := newID(t), newID(t)
		e.seedWallet(t, u, 100)
		wantStatus(t, e.service(t, "PUT", escrowPath(order), reserve(u, 30)), http.StatusOK)
		wantWalletResponse(t, e.service(t, "PUT", escrowPath(order), reserve(u, 30)), u, 70, 30)
		e.wantLedger(t, u, "reserve 30")
	})

	t.Run("same order with a different amount or user", func(t *testing.T) {
		e := newTestEnv(t)
		u, other, order := newID(t), newID(t), newID(t)
		e.seedWallet(t, u, 100)
		e.seedWallet(t, other, 100)
		wantStatus(t, e.service(t, "PUT", escrowPath(order), reserve(u, 30)), http.StatusOK)
		wantError(t, e.service(t, "PUT", escrowPath(order), reserve(u, 40)), http.StatusConflict, "idempotency_conflict")
		wantError(t, e.service(t, "PUT", escrowPath(order), reserve(other, 30)), http.StatusConflict, "idempotency_conflict")
		e.wantStored(t, u, 70, 30)
		e.wantStored(t, other, 100, 0)
	})

	t.Run("concurrent reservations cannot overspend", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 100)
		orders := make([]string, 10)
		for i := range orders {
			orders[i] = newID(t)
		}
		body := fmt.Sprintf(`{"userId":%q,"amount":20}`, u)
		codes := concurrentStatuses(10, func(i int) int { return statusOf(e.internal, "PUT", escrowPath(orders[i]), body, nil, serviceAuth) })
		if countStatus(codes, http.StatusOK) != 5 || countStatus(codes, http.StatusConflict) != 5 {
			t.Fatalf("expected five 200s and five 409s: %v", codes)
		}
		e.wantStored(t, u, 0, 100)
	})

	t.Run("POST is not allowed", func(t *testing.T) {
		e := newTestEnv(t)
		wantStatus(t, e.service(t, "POST", escrowPath(newID(t)), reserve(newID(t), 10)), http.StatusMethodNotAllowed)
	})
}

func TestReleaseEndpoint(t *testing.T) {
	release := func(order string) string { return escrowPath(order) + "/release" }

	t.Run("returns reserved credits to available", func(t *testing.T) {
		e := newTestEnv(t)
		u, order := newID(t), newID(t)
		e.seedWallet(t, u, 100)
		e.seedEscrow(t, u, order, 30)
		wantWalletResponse(t, e.service(t, "POST", release(order), nil), u, 100, 0)
		e.wantLedger(t, u, "release 30")
	})

	t.Run("releases only that order", func(t *testing.T) {
		e := newTestEnv(t)
		u, first := newID(t), newID(t)
		e.seedWallet(t, u, 100)
		e.seedEscrow(t, u, first, 30)
		e.seedEscrow(t, u, newID(t), 20)
		wantWalletResponse(t, e.service(t, "POST", release(first), nil), u, 80, 20)
	})

	t.Run("repeat releases once", func(t *testing.T) {
		e := newTestEnv(t)
		u, order := newID(t), newID(t)
		e.seedWallet(t, u, 100)
		e.seedEscrow(t, u, order, 30)
		wantStatus(t, e.service(t, "POST", release(order), nil), http.StatusOK)
		wantWalletResponse(t, e.service(t, "POST", release(order), nil), u, 100, 0)
		e.wantLedger(t, u, "release 30")
	})

	t.Run("after payout", func(t *testing.T) {
		e := newTestEnv(t)
		u, courier, order := newID(t), newID(t), newID(t)
		e.seedWallet(t, u, 100)
		e.seedWallet(t, courier, 100)
		e.seedEscrow(t, u, order, 30)
		e.seedPaidOut(t, order, courier)
		wantError(t, e.service(t, "POST", release(order), nil), http.StatusConflict, "escrow_settled")
		e.wantStored(t, u, 70, 0)
	})

	t.Run("unknown order", func(t *testing.T) {
		e := newTestEnv(t)
		wantError(t, e.service(t, "POST", release(newID(t)), nil), http.StatusNotFound, "escrow_not_found")
	})

	t.Run("concurrent releases release once", func(t *testing.T) {
		e := newTestEnv(t)
		u, order := newID(t), newID(t)
		e.seedWallet(t, u, 100)
		e.seedEscrow(t, u, order, 30)
		codes := concurrentStatuses(10, func(int) int { return statusOf(e.internal, "POST", release(order), "", nil, serviceAuth) })
		if countStatus(codes, http.StatusOK) != 10 {
			t.Fatalf("every release should succeed: %v", codes)
		}
		e.wantLedger(t, u, "release 30")
	})
}

func TestPayoutEndpoint(t *testing.T) {
	payout := func(order string) string { return escrowPath(order) + "/payout" }
	to := func(courier string) map[string]any { return map[string]any{"courierId": courier} }
	setup := func(t *testing.T) (e *testEnv, requester, courier, order string) {
		e = newTestEnv(t)
		requester, courier, order = newID(t), newID(t), newID(t)
		e.seedWallet(t, requester, 100)
		e.seedWallet(t, courier, 100)
		e.seedEscrow(t, requester, order, 30)
		return e, requester, courier, order
	}

	t.Run("pays the escrow to the courier", func(t *testing.T) {
		e, requester, courier, order := setup(t)
		wantStatus(t, e.service(t, "POST", payout(order), to(courier)), http.StatusNoContent)
		e.wantStored(t, requester, 70, 0)
		e.wantStored(t, courier, 130, 0)
		e.wantLedger(t, requester, "spend 30")
		e.wantLedger(t, courier, "receive 30")
	})

	t.Run("repeat to the same courier pays once", func(t *testing.T) {
		e, _, courier, order := setup(t)
		wantStatus(t, e.service(t, "POST", payout(order), to(courier)), http.StatusNoContent)
		wantStatus(t, e.service(t, "POST", payout(order), to(courier)), http.StatusNoContent)
		e.wantStored(t, courier, 130, 0)
	})

	t.Run("repeat to a different courier", func(t *testing.T) {
		e, _, courier, order := setup(t)
		other := newID(t)
		e.seedWallet(t, other, 100)
		wantStatus(t, e.service(t, "POST", payout(order), to(courier)), http.StatusNoContent)
		wantError(t, e.service(t, "POST", payout(order), to(other)), http.StatusConflict, "escrow_settled")
		e.wantStored(t, other, 100, 0)
	})

	t.Run("after release", func(t *testing.T) {
		e, _, courier, order := setup(t)
		e.seedReleased(t, order)
		wantError(t, e.service(t, "POST", payout(order), to(courier)), http.StatusConflict, "escrow_settled")
		e.wantStored(t, courier, 100, 0)
	})

	t.Run("courier is the requester", func(t *testing.T) {
		e, requester, _, order := setup(t)
		wantError(t, e.service(t, "POST", payout(order), to(requester)), http.StatusConflict, "courier_is_requester")
		e.wantStored(t, requester, 70, 30)
	})

	t.Run("courier has no wallet leaves everything unchanged", func(t *testing.T) {
		e, requester, _, order := setup(t)
		wantError(t, e.service(t, "POST", payout(order), to(newID(t))), http.StatusNotFound, "wallet_not_found")
		e.wantStored(t, requester, 70, 30)
	})

	t.Run("unknown order", func(t *testing.T) {
		e, _, courier, _ := setup(t)
		wantError(t, e.service(t, "POST", payout(newID(t)), to(courier)), http.StatusNotFound, "escrow_not_found")
	})

	t.Run("malformed or missing courier ID", func(t *testing.T) {
		e, requester, _, order := setup(t)
		wantError(t, e.service(t, "POST", payout(order), to("not-a-uuid")), http.StatusBadRequest, "invalid_input")
		wantError(t, e.service(t, "POST", payout(order), map[string]any{}), http.StatusBadRequest, "invalid_input")
		e.wantStored(t, requester, 70, 30)
	})

	t.Run("concurrent payout and release: exactly one wins", func(t *testing.T) {
		e, _, courier, order := setup(t)
		body := fmt.Sprintf(`{"courierId":%q}`, courier)
		codes := concurrentStatuses(2, func(i int) int {
			if i == 0 {
				return statusOf(e.internal, "POST", payout(order), body, nil, serviceAuth)
			}
			return statusOf(e.internal, "POST", escrowPath(order)+"/release", "", nil, serviceAuth)
		})
		if countStatus(codes, http.StatusConflict) != 1 || codes[0] == codes[1] {
			t.Fatalf("expected one success and one 409: %v", codes)
		}
		if e.totalCredits(t) != 200 {
			t.Fatal("a race must not create or destroy credits")
		}
	})
}

func TestAdminDebitEndpoint(t *testing.T) {
	path := func(u string) string { return "/api/v1/admin/wallets/" + u + "/debits" }
	debit := func(amount int64, reason, requestID string) map[string]any {
		return map[string]any{"amount": amount, "reason": reason, "requestId": requestID}
	}
	setup := func(t *testing.T) (e *testEnv, u string, admin *http.Cookie) {
		e = newTestEnv(t)
		u = newID(t)
		e.seedWallet(t, u, 100)
		return e, u, e.signIn(t, newID(t), true)
	}

	t.Run("admin removes available credits", func(t *testing.T) {
		e, u, admin := setup(t)
		wantWalletResponse(t, e.api(t, "POST", path(u), debit(30, "duplicate account", newID(t)), admin), u, 70, 0)
		e.wantLedger(t, u, "admin_debit 30")
	})

	t.Run("not an admin", func(t *testing.T) {
		e, u, _ := setup(t)
		user := e.signIn(t, newID(t), false)
		wantError(t, e.api(t, "POST", path(u), debit(30, "abuse", newID(t)), user), http.StatusForbidden, "admin_required")
		// Users cannot debit themselves either.
		self := e.signIn(t, u, false)
		wantError(t, e.api(t, "POST", path(u), debit(30, "abuse", newID(t)), self), http.StatusForbidden, "admin_required")
		e.wantStored(t, u, 100, 0)
	})

	t.Run("not signed in", func(t *testing.T) {
		e, u, _ := setup(t)
		wantError(t, e.api(t, "POST", path(u), debit(30, "abuse", newID(t)), nil), http.StatusUnauthorized, "invalid_session")
		e.wantStored(t, u, 100, 0)
	})

	t.Run("missing or foreign Origin", func(t *testing.T) {
		e, u, admin := setup(t)
		for _, headers := range []map[string]string{nil, {"Origin": "https://evil.example"}} {
			w := send(t, e.public, "POST", path(u), debit(30, "abuse", newID(t)), admin, headers)
			wantError(t, w, http.StatusForbidden, "origin_rejected")
		}
		e.wantStored(t, u, 100, 0)
	})

	t.Run("User Service unavailable fails closed", func(t *testing.T) {
		e, u, admin := setup(t)
		e.sessions.mu.Lock()
		e.sessions.down = true
		e.sessions.mu.Unlock()
		wantError(t, e.api(t, "POST", path(u), debit(30, "abuse", newID(t)), admin), http.StatusServiceUnavailable, "auth_unavailable")
		e.wantStored(t, u, 100, 0)
	})

	t.Run("never touches reserved credits", func(t *testing.T) {
		e, u, admin := setup(t)
		e.seedEscrow(t, u, newID(t), 60)
		wantError(t, e.api(t, "POST", path(u), debit(50, "abuse", newID(t)), admin), http.StatusConflict, "insufficient_credits")
		e.wantStored(t, u, 40, 60)
	})

	t.Run("non-positive amount", func(t *testing.T) {
		e, u, admin := setup(t)
		for _, amount := range []int64{0, -1} {
			wantError(t, e.api(t, "POST", path(u), debit(amount, "abuse", newID(t)), admin), http.StatusBadRequest, "invalid_amount")
		}
		e.wantStored(t, u, 100, 0)
	})

	t.Run("missing reason or request ID, malformed user", func(t *testing.T) {
		e, u, admin := setup(t)
		wantError(t, e.api(t, "POST", path(u), debit(10, "", newID(t)), admin), http.StatusBadRequest, "invalid_input")
		wantError(t, e.api(t, "POST", path(u), debit(10, "abuse", ""), admin), http.StatusBadRequest, "invalid_input")
		wantError(t, e.api(t, "POST", path("not-a-uuid"), debit(10, "abuse", newID(t)), admin), http.StatusBadRequest, "invalid_input")
		e.wantStored(t, u, 100, 0)
	})

	t.Run("retry with the same request ID debits once", func(t *testing.T) {
		e, u, admin := setup(t)
		body := debit(30, "abuse", newID(t))
		wantStatus(t, e.api(t, "POST", path(u), body, admin), http.StatusOK)
		wantWalletResponse(t, e.api(t, "POST", path(u), body, admin), u, 70, 0)
		e.wantLedger(t, u, "admin_debit 30")
	})

	t.Run("same request ID with a different amount", func(t *testing.T) {
		e, u, admin := setup(t)
		request := newID(t)
		wantStatus(t, e.api(t, "POST", path(u), debit(30, "abuse", request), admin), http.StatusOK)
		wantError(t, e.api(t, "POST", path(u), debit(50, "abuse", request), admin), http.StatusConflict, "idempotency_conflict")
		e.wantStored(t, u, 70, 0)
	})

	t.Run("unknown wallet", func(t *testing.T) {
		e, _, admin := setup(t)
		wantError(t, e.api(t, "POST", path(newID(t)), debit(10, "abuse", newID(t)), admin), http.StatusNotFound, "wallet_not_found")
	})

	t.Run("concurrent debits cannot go negative", func(t *testing.T) {
		e, u, admin := setup(t)
		requests := make([]string, 10)
		for i := range requests {
			requests[i] = newID(t)
		}
		codes := concurrentStatuses(10, func(i int) int {
			body := fmt.Sprintf(`{"amount":20,"reason":"abuse","requestId":%q}`, requests[i])
			return statusOf(e.public, "POST", path(u), body, admin, map[string]string{"Origin": testOrigin})
		})
		if countStatus(codes, http.StatusOK) != 5 || countStatus(codes, http.StatusConflict) != 5 {
			t.Fatalf("expected five 200s and five 409s: %v", codes)
		}
		e.wantStored(t, u, 0, 0)
	})
}

type historyPage struct {
	Transactions []Transaction `json:"transactions"`
	NextBefore   *int64        `json:"nextBefore"`
}

func wantHistoryPage(t *testing.T, w *httptest.ResponseRecorder, entries int, more bool) historyPage {
	t.Helper()
	wantStatus(t, w, http.StatusOK)
	var page historyPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("response is not a history page: %s", w.Body.String())
	}
	if len(page.Transactions) != entries || (page.NextBefore != nil) != more {
		t.Fatalf("%d entries and nextBefore %v, expected %d entries and more=%v", len(page.Transactions), page.NextBefore, entries, more)
	}
	return page
}

func TestHistoryEndpoints(t *testing.T) {
	const mine = "/api/v1/wallets/me/transactions"

	t.Run("own history pages newest first, 30 by default", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		withEntries(t, e, u, 34)
		cookie := e.signIn(t, u, false)
		first := wantHistoryPage(t, e.api(t, "GET", mine, nil, cookie), 30, true)
		next := fmt.Sprintf("%s?before=%d", mine, *first.NextBefore)
		rest := wantHistoryPage(t, e.api(t, "GET", next, nil, cookie), 5, false)
		if rest.Transactions[0].ID >= first.Transactions[29].ID {
			t.Fatal("second page overlaps the first")
		}
		if last := rest.Transactions[4]; last.Kind != KindAllocation || last.AvailableAfter != initialCredits {
			t.Fatalf("oldest entry %+v, expected the allocation", last)
		}
	})

	t.Run("limit parameter", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		withEntries(t, e, u, 9)
		wantHistoryPage(t, e.api(t, "GET", mine+"?limit=5", nil, e.signIn(t, u, false)), 5, true)
		wantHistoryPage(t, e.api(t, "GET", mine+"?limit=100", nil, e.signIn(t, u, false)), 10, false)
	})

	t.Run("entry fields", func(t *testing.T) {
		e := newTestEnv(t)
		requester, courier, order := newID(t), newID(t), newID(t)
		e.seedWallet(t, requester, 100)
		e.seedWallet(t, courier, 100)
		e.seedEscrow(t, requester, order, 30)
		mustOK(t, e.app.Transfer(context.Background(), order, courier))
		w := e.api(t, "GET", mine+"?limit=1", nil, e.signIn(t, requester, false))
		wantStatus(t, w, http.StatusOK)
		var raw struct {
			Transactions []map[string]any `json:"transactions"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil || len(raw.Transactions) != 1 {
			t.Fatalf("unexpected body: %s", w.Body.String())
		}
		got := raw.Transactions[0]
		want := map[string]any{"kind": "spend", "amount": 30.0, "availableDelta": 0.0, "reservedDelta": -30.0,
			"availableAfter": 70.0, "reservedAfter": 0.0, "orderId": order, "counterpartyId": courier}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("%s is %v, expected %v: %s", k, got[k], v, w.Body.String())
			}
		}
		if _, ok := got["id"]; !ok {
			t.Fatal("missing id")
		}
		if _, ok := got["createdAt"]; !ok {
			t.Fatal("missing createdAt")
		}
	})

	t.Run("empty history is an empty list", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		e.seedWallet(t, u, 0)
		w := e.api(t, "GET", mine, nil, e.signIn(t, u, false))
		wantHistoryPage(t, w, 0, false)
		if !strings.Contains(w.Body.String(), `"transactions":[]`) {
			t.Fatalf("expected an empty JSON array: %s", w.Body.String())
		}
	})

	t.Run("invalid limit or cursor", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		withEntries(t, e, u, 0)
		cookie := e.signIn(t, u, false)
		for _, q := range []string{"limit=0", "limit=101", "limit=-1", "limit=abc", "before=0", "before=-5", "before=x"} {
			wantError(t, e.api(t, "GET", mine+"?"+q, nil, cookie), http.StatusBadRequest, "invalid_input")
		}
	})

	t.Run("signed in without a wallet", func(t *testing.T) {
		e := newTestEnv(t)
		wantError(t, e.api(t, "GET", mine, nil, e.signIn(t, newID(t), false)), http.StatusNotFound, "wallet_not_found")
	})

	t.Run("requires a session", func(t *testing.T) {
		e := newTestEnv(t)
		wantError(t, e.api(t, "GET", mine, nil, nil), http.StatusUnauthorized, "invalid_session")
	})

	t.Run("no public route to another user's history", func(t *testing.T) {
		e := newTestEnv(t)
		other := newID(t)
		withEntries(t, e, other, 1)
		wantStatus(t, e.api(t, "GET", "/api/v1/wallets/"+other+"/transactions", nil, e.signIn(t, newID(t), false)), http.StatusNotFound)
	})

	t.Run("internal by user ID", func(t *testing.T) {
		e := newTestEnv(t)
		u := newID(t)
		withEntries(t, e, u, 2)
		wantHistoryPage(t, e.service(t, "GET", "/internal/v1/wallets/"+u+"/transactions", nil), 3, false)
		wantError(t, e.service(t, "GET", "/internal/v1/wallets/not-a-uuid/transactions", nil), http.StatusBadRequest, "invalid_input")
		wantError(t, e.service(t, "GET", "/internal/v1/wallets/"+newID(t)+"/transactions", nil), http.StatusNotFound, "wallet_not_found")
	})
}
