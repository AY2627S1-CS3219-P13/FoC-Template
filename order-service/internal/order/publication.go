package order

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// PublishEvents delivers committed lifecycle facts to the durable browser feed.
// The head lock serializes publishers across replicas. A crash rolls back the
// whole event effect and receipt, so retries cannot duplicate or skip a committed publication.
func (a *App) PublishEvents(ctx context.Context) error {
	return a.ConsumeWorkflow(ctx, publicationConsumer)
}

func publishEvent(ctx context.Context, tx pgx.Tx, id string) error {
	var head int64
	if err := tx.QueryRow(ctx, "SELECT position FROM order_publication_head WHERE singleton FOR UPDATE").Scan(&head); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, "INSERT INTO order_publications(position,event_id) VALUES($1,$2) ON CONFLICT(event_id) DO NOTHING", head+1, id)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE order_publication_head SET position=$1 WHERE singleton", head+1)
	return err
}

type publishedEvent struct {
	position int64
	eligible bool
	event    orderEvent
}

func (a *App) publicationBatch(ctx context.Context, after int64, user string, admin ...bool) ([]publishedEvent, error) {
	rows, err := a.db.Query(ctx, `SELECT p.position,
 CASE WHEN e.event_type='OrderCreated' THEN o.requester_id<>$2 AND o.status='open' AND o.expires_at>clock_timestamp()
 ELSE o.requester_id=$2 OR o.courier_id=$2 OR ($3 AND o.status='dispute')
 OR (o.status='open' AND o.expires_at>clock_timestamp())
 OR e.domain_payload->>'previousStatus'='open' END,
 e.event_id,e.order_id,e.order_version,e.event_type,e.schema_version,e.created_at
 FROM order_publications p JOIN order_events e ON e.event_id=p.event_id
 JOIN orders o ON o.id=e.order_id WHERE p.position>$1 ORDER BY p.position LIMIT 100`, after, user, len(admin) > 0 && admin[0])
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
		batch, err := a.publicationBatch(r.Context(), cursor, identity.UserID, slices.Contains(identity.Roles, "admin"))
		if err != nil {
			a.log.Warn("event stream unavailable")
			return nil // Never append JSON errors to a started SSE response.
		}
		for _, p := range batch {
			if p.eligible && !send(p.event.Type, p.position, p.event) {
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
 FROM order_events e WHERE e.domain_type IS NOT NULL AND NOT EXISTS
 (SELECT 1 FROM order_publications p WHERE p.event_id=e.event_id)`).Scan(&published, &pending, &oldest)
	if err != nil {
		return err
	}
	workflow, err := a.workflowStatus(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"published": published, "pending": pending, "oldestPendingAt": oldest, "workflow": workflow})
	return nil
}
