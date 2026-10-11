package order

import (
	"context"
	"net/http"
)

// These consumer-owned contracts keep creation independent of HTTP transports
// and sibling service implementations. PostgreSQL remains an explicit dependency:
// its transactions and advisory locks enforce creation/recovery invariants.
type sessionValidator interface {
	Validate(*http.Request) (Identity, error)
}

type placeResolver interface {
	Resolve(context.Context, PlaceRef, string) (Place, error)
}

type placeLister interface {
	List(context.Context, string, int) ([]Place, bool, error)
}

type escrowClient interface {
	Reserve(context.Context, string, string, int64) error
	Release(context.Context, string) error
	Payout(context.Context, string, string) error
}
