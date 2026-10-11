package order

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"
)

// PublishEvents delivers committed creation facts to the durable browser feed.
// The head lock serializes publishers across replicas. A crash rolls back the
// whole batch, so retries cannot duplicate or skip a committed publication.
func (a *App) PublishEvents(ctx context.Context) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var head int64
	if err = tx.QueryRow(ctx, "SELECT position FROM order_publication_head WHERE singleton FOR UPDATE").Scan(&head); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT e.event_id FROM order_events e
 WHERE e.event_type='OrderCreated' AND NOT EXISTS
 (SELECT 1 FROM order_publications p WHERE p.event_id=e.event_id)
 ORDER BY e.created_at,e.event_id LIMIT 100`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		head++
		if _, err = tx.Exec(ctx, "INSERT INTO order_publications(position,event_id) VALUES($1,$2)", head, id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE order_publication_head SET position=$1 WHERE singleton", head); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type publishedEvent struct {
	position int64
	eligible bool
	event    orderEvent
}

func (a *App) publicationBatch(ctx context.Context, after int64, user string) ([]publishedEvent, error) {
	rows, err := a.db.Query(ctx, `SELECT p.position,
 o.requester_id<>$2 AND o.status='open' AND o.expires_at>clock_timestamp(),
 e.event_id,e.order_id,e.order_version,e.event_type,e.schema_version,e.created_at
 FROM order_publications p JOIN order_events e ON e.event_id=p.event_id
 JOIN orders o ON o.id=e.order_id WHERE p.position>$1 ORDER BY p.position LIMIT 100`, after, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []publishedEvent{}
	for rows.Next() {
		var p publishedEvent
		if err = rows.Scan(&p.position, &p.eligible, &p.event.ID, &p.event.OrderID, &p.event.Version, &p.event.Type, &p.event.SchemaVersion, &p.event.CreatedAt); err != nil {
			return nil, err
		}
		p.event.CreatedAt = p.event.CreatedAt.In(time.FixedZone("Asia/Singapore", 8*60*60))
		result = append(result, p)
	}
	return result, rows.Err()
}

// Streams rotate before the normal request/write deadlines. Each batch validates
// the current User session again; revocation or a changed account ends delivery.
// Only minimal event metadata is sent. Clients fetch authorized current details.
func (a *App) streamOrders(w http.ResponseWriter, r *http.Request) error {
	var head int64
	if err := a.db.QueryRow(r.Context(), "SELECT position FROM order_publication_head WHERE singleton").Scan(&head); err != nil {
		return err
	}
	cursor := head
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		var err error
		cursor, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || cursor < 0 || cursor > head {
			return problem(400, "invalid_event_cursor", "Reconnect without a cursor and synchronize the open list.")
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	send := func(kind string, position int64, value any) bool {
		data, err := json.Marshal(value)
		if err != nil {
			return false
		}
		if _, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", position, kind, data); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	if _, err := fmt.Fprint(w, "retry: 1000\n\n"); err != nil {
		return nil
	}
	// A reconnect always reconciles a snapshot, including missed removals and
	// publications no longer eligible for this viewer. Cursor alone is not state.
	if !send("snapshot", cursor, map[string]string{"userId": actor(r)}) {
		return nil
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lifetime := time.NewTimer(8 * time.Second)
	defer lifetime.Stop()
	for {
		identity, err := a.auth.Validate(r)
		if err != nil || identity.UserID != actor(r) {
			send("session_invalidated", cursor, struct{}{})
			return nil
		}
		batch, err := a.publicationBatch(r.Context(), cursor, identity.UserID)
		if err != nil {
			a.log.Warn("event stream unavailable")
			return nil // Never append JSON errors to a started SSE response.
		}
		for _, p := range batch {
			if p.eligible && !send("OrderCreated", p.position, p.event) {
				return nil
			}
			cursor = p.position
		}
		if !send("checkpoint", cursor, struct{}{}) {
			return nil
		}
		select {
		case <-r.Context().Done():
			return nil
		case <-lifetime.C:
			return nil
		case <-ticker.C:
		}
	}
}

func (a *App) publicationStatus(w http.ResponseWriter, r *http.Request) error {
	if !slices.Contains(r.Context().Value(identityKey{}).(Identity).Roles, "admin") {
		return problem(403, "admin_required", "An administrator is required.")
	}
	var pending, published int64
	var oldest *time.Time
	err := a.db.QueryRow(r.Context(), `SELECT
 (SELECT count(*) FROM order_publications),count(*),min(e.created_at)
 FROM order_events e WHERE e.event_type='OrderCreated' AND NOT EXISTS
 (SELECT 1 FROM order_publications p WHERE p.event_id=e.event_id)`).Scan(&published, &pending, &oldest)
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"published": published, "pending": pending, "oldestPendingAt": oldest})
	return nil
}
