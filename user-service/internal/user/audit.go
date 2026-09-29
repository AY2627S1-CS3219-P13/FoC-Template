package user

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type roleEvent struct {
	ID           string    `json:"id"`
	ActorKind    string    `json:"actorKind"`
	ActorUserID  *string   `json:"actorUserId"`
	ActorLabel   string    `json:"actorLabel"`
	TargetUserID string    `json:"targetUserId"`
	TargetEmail  string    `json:"targetEmail"`
	PreviousRole string    `json:"previousRole"`
	NewRole      string    `json:"newRole"`
	Reason       string    `json:"reason"`
	OccurredAt   time.Time `json:"occurredAt"`
}

// The event is written in the same transaction as the role change: neither can
// commit without the other. Labels are snapshots so the history survives deletion.
func recordRoleEvent(ctx context.Context, tx pgx.Tx, kind string, actorID *string, actorLabel, targetID, targetEmail, previousRole, newRole, reason string) error {
	_, err := tx.Exec(ctx, `INSERT INTO admin_role_events
		(actor_kind,actor_user_id,actor_label,target_user_id,target_email,previous_role,new_role,reason)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, kind, actorID, actorLabel, targetID, targetEmail, previousRole, newRole, reason)
	return err
}

func (a *App) roleEvents(w http.ResponseWriter, r *http.Request) error {
	actor, err := a.currentUser(r)
	if err != nil {
		return err
	}
	if !hasRole(actor.Roles, "admin") {
		return problem(403, "admin_required", "An administrator is required.")
	}
	if err = a.limit(r.Context(), "admin-events:"+actor.ID, 60, time.Minute); err != nil {
		return err
	}
	page, pageSize := 1, 20
	if raw := r.URL.Query().Get("page"); raw != "" {
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 || page > 1000000 {
			return problem(400, "invalid_page", "Page must be a positive number.")
		}
	}
	if raw := r.URL.Query().Get("pageSize"); raw != "" {
		pageSize, err = strconv.Atoi(raw)
		if err != nil || pageSize < 1 || pageSize > 100 {
			return problem(400, "invalid_page_size", "Page size must be between 1 and 100.")
		}
	}
	var total int
	if err = a.db.QueryRow(r.Context(), "SELECT count(*) FROM admin_role_events").Scan(&total); err != nil {
		return err
	}
	rows, err := a.db.Query(r.Context(), `SELECT id,actor_kind,actor_user_id,actor_label,target_user_id,target_email,previous_role,new_role,reason,occurred_at
		FROM admin_role_events ORDER BY occurred_at DESC,id DESC LIMIT $1 OFFSET $2`, pageSize, (page-1)*pageSize)
	if err != nil {
		return err
	}
	defer rows.Close()
	events := make([]roleEvent, 0, pageSize)
	for rows.Next() {
		var event roleEvent
		if err = rows.Scan(&event.ID, &event.ActorKind, &event.ActorUserID, &event.ActorLabel, &event.TargetUserID, &event.TargetEmail, &event.PreviousRole, &event.NewRole, &event.Reason, &event.OccurredAt); err != nil {
			return err
		}
		events = append(events, event)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"events": events, "page": page, "pageSize": pageSize, "total": total, "totalPages": (total + pageSize - 1) / pageSize})
	return nil
}
