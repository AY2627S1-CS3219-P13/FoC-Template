package order

import (
	"context"
	"errors"
	"sync"
)

// Resolve outside locks; refreshHistory fences the snapshot with order versions
// and defers to live admissions. Failed lookups rotate without cancelling orders.
func (a *App) cancelInactiveSuppliers(ctx context.Context) error {
	if a.backgroundCatalogue == nil {
		return nil
	}
	rows, err := a.db.Query(ctx, "SELECT "+orderColumns+" FROM orders WHERE status IN ('open','accepted','picked up') ORDER BY supplier_checked_at,id LIMIT 100")
	if err != nil {
		return err
	}
	var orders []Order
	for rows.Next() {
		o, e := scanOrder(rows)
		if e != nil {
			rows.Close()
			return e
		}
		orders = append(orders, o)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, o := range orders {
		if _, err = a.db.Exec(ctx, "UPDATE orders SET supplier_checked_at=clock_timestamp() WHERE id=$1", o.ID); err != nil {
			return err
		}
	}
	resolved, missing, lookupErrors := a.workerPlaces(ctx, orders)
	var failures error
	for _, o := range orders {
		if lookupErrors[PlaceRef{Type: o.Pickup.Type, ID: o.Pickup.ID}] != nil || lookupErrors[PlaceRef{Type: o.Delivery.Type, ID: o.Delivery.ID}] != nil {
			failures = errors.Join(failures, lookupErrors[PlaceRef{Type: o.Pickup.Type, ID: o.Pickup.ID}], lookupErrors[PlaceRef{Type: o.Delivery.Type, ID: o.Delivery.ID}])
			continue
		}
		_, err = a.refreshHistory(ctx, o, "", resolved, missing)
		var api *apiError
		if errors.As(err, &api) && api.code == "order_updating" {
			continue
		}
		failures = errors.Join(failures, err)
	}
	return failures
}

// A failed Supplier lookup blocks only orders that reference it, not the batch.
func (a *App) workerPlaces(ctx context.Context, orders []Order) (map[PlaceRef]Place, map[PlaceRef]bool, map[PlaceRef]error) {
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
	var workers sync.WaitGroup
	for range min(catalogueConcurrency, len(refs)) {
		workers.Go(func() {
			for ref := range jobs {
				p, err := a.backgroundCatalogue.Resolve(ctx, ref, "")
				results <- result{ref, p, err}
			}
		})
	}
	workers.Wait()
	close(results)
	resolved, missing, failures := map[PlaceRef]Place{}, map[PlaceRef]bool{}, map[PlaceRef]error{}
	for result := range results {
		var api *apiError
		if errors.As(result.err, &api) && api.code == "invalid_place" {
			missing[result.ref] = true
		} else if result.err != nil {
			failures[result.ref] = result.err
		} else {
			resolved[result.ref] = result.place
		}
	}
	return resolved, missing, failures
}
