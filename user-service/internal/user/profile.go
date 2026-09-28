package user

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (a *App) me(w http.ResponseWriter, r *http.Request) error {
	u, err := a.currentUser(r)
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"user": u})
	return nil
}

func (a *App) updateProfile(w http.ResponseWriter, r *http.Request) error {
	u, err := a.currentUser(r)
	if err != nil {
		return err
	}
	var body struct {
		DisplayName string `json:"displayName"`
	}
	if err = readJSON(w, r, &body); err != nil {
		return err
	}
	name, err := displayName(body.DisplayName)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(r.Context(), "UPDATE users SET display_name=$2 WHERE id=$1", u.ID, name)
	if uniqueViolation(err) {
		return problem(409, "display_name_taken", "That display name is already in use.")
	}
	if err != nil {
		return err
	}
	u.DisplayName = name
	writeJSON(w, 200, map[string]any{"user": u})
	return nil
}

func (a *App) changePassword(w http.ResponseWriter, r *http.Request) error {
	u, err := a.currentUser(r)
	if err != nil {
		return err
	}
	if err = a.limit(r.Context(), "password:"+u.ID, 5, 10*time.Minute); err != nil {
		return err
	}
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err = readJSON(w, r, &body); err != nil {
		return err
	}
	if !validPassword(body.NewPassword) {
		return passwordError()
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var hash string
	if err = tx.QueryRow(r.Context(), "SELECT password_hash FROM users WHERE id=$1 FOR UPDATE", u.ID).Scan(&hash); err != nil {
		return err
	}
	if !validPassword(body.CurrentPassword) || !checkPassword(hash, body.CurrentPassword) {
		return problem(401, "invalid_credentials", "The current password is incorrect.")
	}
	hash, err = hashPassword(body.NewPassword)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), "UPDATE users SET password_hash=$2 WHERE id=$1", u.ID, hash); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM sessions WHERE user_id=$1", u.ID); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	a.setCookie(w, "", time.Time{})
	a.log.Info("password changed; all sessions revoked", "userId", u.ID)
	writeJSON(w, 200, map[string]string{"message": "Password changed. Please log in again on all devices."})
	return nil
}

func (a *App) adminUsers(w http.ResponseWriter, r *http.Request) error {
	actor, err := a.currentUser(r)
	if err != nil {
		return err
	}
	if !hasRole(actor.Roles, "admin") {
		return problem(403, "admin_required", "An administrator is required.")
	}
	if err = a.limit(r.Context(), "admin-users:"+actor.ID, 60, time.Minute); err != nil {
		return err
	}
	if _, searching := r.URL.Query()["email"]; searching {
		return a.findUserByEmail(w, r)
	}
	return a.listUsers(w, r)
}

func (a *App) findUserByEmail(w http.ResponseWriter, r *http.Request) error {
	email, err := a.email(r.URL.Query().Get("email"))
	if err != nil {
		return err
	}
	var found User
	var admin bool
	err = a.db.QueryRow(r.Context(), `SELECT id,email,display_name,is_admin,verified_at
		FROM users WHERE email=$1 AND verified_at IS NOT NULL`, email).
		Scan(&found.ID, &found.Email, &found.DisplayName, &admin, &found.VerifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return problem(404, "user_not_found", "No verified account matches that email.")
	}
	if err != nil {
		return err
	}
	found.Roles = roles(admin)
	writeJSON(w, 200, map[string]any{"user": found})
	return nil
}

type adminUserSummary struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"displayName"`
	Roles       []string   `json:"roles"`
	VerifiedAt  *time.Time `json:"verifiedAt"`
	CreatedAt   time.Time  `json:"createdAt"`
}

func (a *App) listUsers(w http.ResponseWriter, r *http.Request) error {
	page, pageSize := 1, 20
	if raw := r.URL.Query().Get("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1000000 {
			return problem(400, "invalid_page", "Page must be a positive number.")
		}
		page = parsed
	}
	if raw := r.URL.Query().Get("pageSize"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			return problem(400, "invalid_page_size", "Page size must be between 1 and 100.")
		}
		pageSize = parsed
	}
	var total int
	if err := a.db.QueryRow(r.Context(), "SELECT count(*) FROM users").Scan(&total); err != nil {
		return err
	}
	rows, err := a.db.Query(r.Context(), `SELECT id,email,display_name,is_admin,verified_at,created_at
		FROM users ORDER BY created_at DESC,id DESC LIMIT $1 OFFSET $2`, pageSize, (page-1)*pageSize)
	if err != nil {
		return err
	}
	defer rows.Close()
	users := make([]adminUserSummary, 0, pageSize)
	for rows.Next() {
		var item adminUserSummary
		var admin bool
		if err = rows.Scan(&item.ID, &item.Email, &item.DisplayName, &admin, &item.VerifiedAt, &item.CreatedAt); err != nil {
			return err
		}
		item.Roles = roles(admin)
		users = append(users, item)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"users": users, "page": page, "pageSize": pageSize, "total": total, "totalPages": (total + pageSize - 1) / pageSize})
	return nil
}

func hasRole(assigned []string, role string) bool {
	for _, value := range assigned {
		if value == role {
			return true
		}
	}
	return false
}

func (a *App) changeRole(w http.ResponseWriter, r *http.Request) error {
	u, err := a.currentUser(r)
	if err != nil {
		return err
	}
	var body struct {
		Role string `json:"role"`
	}
	if err = readJSON(w, r, &body); err != nil {
		return err
	}
	if body.Role != "user" && body.Role != "admin" {
		return problem(400, "invalid_role", "Role must be user or admin.")
	}
	id := r.PathValue("id")
	var uuid pgtype.UUID
	if uuid.Scan(id) != nil {
		return problem(400, "invalid_user_id", "Use a valid user UUID.")
	}
	var actorUUID pgtype.UUID
	if err = actorUUID.Scan(u.ID); err != nil {
		return err
	}
	if uuid.Bytes == actorUUID.Bytes {
		return problem(400, "self_role_change", "Ask another admin to change your role.")
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	// Serialize role changes and recheck the actor to avoid mutually demoting admins.
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(321902)"); err != nil {
		return err
	}
	var admin bool
	if err = tx.QueryRow(r.Context(), "SELECT is_admin FROM users WHERE id=$1 FOR UPDATE", u.ID).Scan(&admin); err != nil {
		return err
	}
	if !admin {
		return problem(403, "admin_required", "An administrator is required.")
	}
	var target string
	err = tx.QueryRow(r.Context(), "UPDATE users SET is_admin=$2 WHERE id=$1 AND verified_at IS NOT NULL RETURNING id", id, body.Role == "admin").Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		return problem(404, "user_not_found", "No verified account matches that ID.")
	}
	if err != nil {
		return err
	}
	// Re-login is required after any privilege change, including promotion.
	if _, err = tx.Exec(r.Context(), "DELETE FROM sessions WHERE user_id=$1", id); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	a.log.Info("user role changed", "actorId", u.ID, "userId", id, "role", body.Role)
	writeJSON(w, 200, map[string]any{"userId": target, "roles": roles(body.Role == "admin")})
	return nil
}
