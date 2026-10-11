package order

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSnapshotParallelCatalogueWithinRefreshBudget(t *testing.T) {
	a, f := testApp(t)
	a.auth = actorAuth{}
	f.available = 10000
	for i := range 24 {
		o := createdForTest(t, a, fmt.Sprintf("parallel-%d", i))
		if _, err := a.db.Exec(context.Background(), `UPDATE orders SET
 pickup=jsonb_set(pickup,'{id}',to_jsonb($2::text)),
 delivery=jsonb_set(delivery,'{id}',to_jsonb($3::text)) WHERE id=$1`, o.ID, fmt.Sprintf("pickup-%d", i), fmt.Sprintf("delivery-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	var calls, active, peak atomic.Int64
	a.catalogue = resolverFunc(func(ctx context.Context, ref PlaceRef, token string) (Place, error) {
		if token != "courier" {
			t.Error("wrong snapshot session")
		}
		calls.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		select {
		case <-ctx.Done():
			return Place{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		return Place{Type: ref.Type, ID: ref.ID, Name: "Current " + ref.ID}, nil
	})
	start := time.Now()
	got := request(t, a, "GET", "/api/v1/orders?scope=open", "courier", "", nil)
	var body struct{ Orders []Order }
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &body) != nil || len(body.Orders) != 24 {
		t.Fatal(got.Code, got.Body.String())
	}
	if elapsed := time.Since(start); elapsed >= snapshotBudget {
		t.Fatal("snapshot missed refresh budget", elapsed)
	}
	if calls.Load() != 48 || peak.Load() <= 1 || peak.Load() > catalogueConcurrency || active.Load() != 0 {
		t.Fatal("unbounded, serial or incomplete resolution", calls.Load(), peak.Load(), active.Load())
	}
	for _, o := range body.Orders {
		if !strings.HasPrefix(o.Pickup.Name, "Current ") || !strings.HasPrefix(o.Delivery.Name, "Current ") {
			t.Fatal("stale snapshot", o.ID)
		}
	}
}

func TestSnapshotDeadlineIncludesAuthenticationAndCatalogue(t *testing.T) {
	for _, dependency := range []string{"authentication", "catalogue"} {
		t.Run(dependency, func(t *testing.T) {
			a, _ := testApp(t)
			a.auth = actorAuth{}
			o := createdForTest(t, a, "snapshot-deadline")
			if dependency == "authentication" {
				delayAdmissionAuth(t, a, "requester", false)
			} else {
				a.catalogue = resolverFunc(func(ctx context.Context, _ PlaceRef, _ string) (Place, error) {
					<-ctx.Done()
					return Place{}, ctx.Err()
				})
			}
			start := time.Now()
			got := request(t, a, "GET", "/api/v1/orders?scope=mine", "requester", "", nil)
			if got.Code != 503 || !strings.Contains(got.Body.String(), "snapshot_timeout") || time.Since(start) > 2500*time.Millisecond {
				t.Fatal("unbounded or falsely fresh snapshot", got.Code, got.Body.String(), time.Since(start))
			}
			if readStored(t, a, o.ID).Status != "open" {
				t.Fatal("timeout treated as deletion")
			}
		})
	}
}

func TestSnapshotCannotOverwriteRefreshThatWonDuringCatalogueIO(t *testing.T) {
	a, _ := testApp(t)
	a.auth = actorAuth{}
	o := createdForTest(t, a, "snapshot-version-fence")
	b := admissionReplica(a)
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	a.catalogue = resolverFunc(func(ctx context.Context, ref PlaceRef, _ string) (Place, error) {
		if first.CompareAndSwap(false, true) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return Place{}, ctx.Err()
			}
		}
		return Place{Type: ref.Type, ID: ref.ID, Name: "Older lookup"}, nil
	})
	b.catalogue = resolverFunc(func(_ context.Context, ref PlaceRef, _ string) (Place, error) {
		return Place{Type: ref.Type, ID: ref.ID, Name: "Newer lookup"}, nil
	})
	result := make(chan int, 1)
	go func() { result <- request(t, a, "GET", "/api/v1/orders?scope=open", "courier", "", nil).Code }()
	awaitSignal(t, entered)
	got := request(t, b, "POST", "/api/v1/orders/"+o.ID+"/refresh", "requester", "", map[string]any{})
	close(release)
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	awaitCode(t, result, 503)
	stored := readStored(t, a, o.ID)
	if stored.Pickup.Name != "Newer lookup" || stored.Delivery.Name != "Newer lookup" {
		t.Fatal("stale catalogue result overwrote newer revision")
	}
}
