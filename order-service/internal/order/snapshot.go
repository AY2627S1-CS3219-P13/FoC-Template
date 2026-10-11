package order

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

const snapshotBudget = 2 * time.Second
const catalogueConcurrency = 8

// Include authentication, pool waits and catalogue I/O in one refresh budget.
// A missed budget is an unavailable snapshot, never a successful stale read.
func (a *App) snapshot(next endpoint) endpoint {
	return func(w http.ResponseWriter, r *http.Request) error {
		ctx, cancel := context.WithTimeout(r.Context(), snapshotBudget)
		defer cancel()
		err := next(w, r.WithContext(ctx))
		if err != nil && ctx.Err() != nil {
			return problem(503, "snapshot_timeout", "List is not updated. Refresh could not finish in time.")
		}
		return err
	}
}

// Resolve distinct references concurrently before locking orders. Maps belong
// only to this authenticated snapshot; they are populated by one goroutine.
func (a *App) snapshotPlaces(ctx context.Context, orders []Order, session string) (map[PlaceRef]Place, map[PlaceRef]bool, error) {
	refs := map[PlaceRef]bool{}
	for _, o := range orders {
		refs[PlaceRef{Type: o.Pickup.Type, ID: o.Pickup.ID}] = true
		refs[PlaceRef{Type: o.Delivery.Type, ID: o.Delivery.ID}] = true
	}
	jobs := make(chan PlaceRef, len(refs))
	for ref := range refs {
		jobs <- ref
	}
	close(jobs)
	type result struct {
		ref   PlaceRef
		place Place
		err   error
	}
	results := make(chan result, len(refs))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	for range min(catalogueConcurrency, len(refs)) {
		workers.Go(func() {
			for ref := range jobs {
				if ctx.Err() != nil {
					return
				}
				p, err := a.catalogue.Resolve(ctx, ref, session)
				results <- result{ref, p, err}
			}
		})
	}
	workers.Wait()
	close(results)
	resolved, missing := map[PlaceRef]Place{}, map[PlaceRef]bool{}
	for result := range results {
		var api *apiError
		if errors.As(result.err, &api) && api.code == "invalid_place" {
			missing[result.ref] = true
		} else if result.err != nil {
			return nil, nil, result.err
		} else {
			resolved[result.ref] = result.place
		}
	}
	return resolved, missing, ctx.Err()
}
