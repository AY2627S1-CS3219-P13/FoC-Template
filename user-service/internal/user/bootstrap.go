package user

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// bootstrapClaim lets the designated, verified account claim the first admin role once.
func (a *App) bootstrapClaim(w http.ResponseWriter, r *http.Request) error {
	if a.cfg.BootstrapAdminEmail == "" {
		return problem(404, "bootstrap_disabled", "Administrator setup is not enabled.")
	}
	u, err := a.currentUser(r)
	if err != nil {
		return err
	}
	if u.Email != a.cfg.BootstrapAdminEmail {
		return problem(403, "bootstrap_not_authorized", "This account is not designated for administrator setup.")
	}
	if err = a.limit(r.Context(), "bootstrap:"+u.ID, 10, time.Minute); err != nil {
		return err
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	// Shared with operator promotion and role changes; serializes all admin grants.
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(321902)"); err != nil {
		return err
	}
	var email string
	err = tx.QueryRow(r.Context(), "SELECT email FROM users WHERE id=$1 AND verified_at IS NOT NULL FOR UPDATE", u.ID).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return problem(401, "invalid_session", "Please log in again.")
	}
	if err != nil {
		return err
	}
	if email != a.cfg.BootstrapAdminEmail {
		return problem(403, "bootstrap_not_authorized", "This account is not designated for administrator setup.")
	}
	// A password change may have revoked the session after currentUser ran.
	cookie, err := r.Cookie(a.cfg.CookieName())
	if err != nil {
		return problem(401, "invalid_session", "Please log in again.")
	}
	var sessionActive bool
	if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>now())", tokenHash(cookie.Value), u.ID).Scan(&sessionActive); err != nil {
		return err
	}
	if !sessionActive {
		return problem(401, "invalid_session", "Please log in again.")
	}
	var consumed, hasAdmin bool
	if err = tx.QueryRow(r.Context(), "SELECT consumed_at IS NOT NULL FROM admin_bootstrap WHERE singleton=true FOR UPDATE").Scan(&consumed); err != nil {
		return err
	}
	if consumed {
		return problem(409, "bootstrap_used", "Administrator setup has already been completed.")
	}
	if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM users WHERE is_admin)").Scan(&hasAdmin); err != nil {
		return err
	}
	if hasAdmin {
		return problem(409, "bootstrap_used", "Administrator setup has already been completed.")
	}
	if _, err = tx.Exec(r.Context(), "UPDATE users SET is_admin=true WHERE id=$1", u.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), "UPDATE admin_bootstrap SET consumed_at=now(),claimed_by=$1 WHERE singleton=true", u.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM sessions WHERE user_id=$1", u.ID); err != nil {
		return err
	}
	if err = recordRoleEvent(r.Context(), tx, "bootstrap", &u.ID, u.Email, u.ID, u.Email, "user", "admin", ""); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	a.setCookie(w, "", time.Time{})
	a.log.Info("first administrator claimed", "userId", u.ID)
	writeJSON(w, 200, map[string]string{"message": "Administrator access granted. Please log in again."})
	return nil
}
