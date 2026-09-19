package user

import (
	"crypto/hmac"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

func (a *App) email(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return "", problem(400, "invalid_email", "Use a valid school email address.")
	}
	_, domain, ok := strings.Cut(value, "@")
	if ok {
		for _, allowed := range a.cfg.AllowedDomains {
			if domain == allowed {
				return value, nil
			}
		}
	}
	return "", problem(400, "invalid_email", "Use an allowed school email domain.")
}

func displayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 50 {
		return "", problem(400, "invalid_display_name", "Use a display name between 1 and 50 characters.")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", problem(400, "invalid_display_name", "Display names cannot contain control characters.")
		}
	}
	return value, nil
}

func passwordError() error {
	return problem(400, "invalid_password", "Passwords must contain between 12 and 128 characters.")
}
func verificationError() error {
	return problem(400, "invalid_verification", "The code is incorrect, expired, already used or has too many failed attempts.")
}
func accepted(w http.ResponseWriter) {
	writeJSON(w, 202, map[string]string{"message": "If the account is eligible, a verification email will be sent. Use resend if needed."})
}

func registrationAccepted(w http.ResponseWriter, token string) {
	writeJSON(w, 202, map[string]string{"message": "If the account is eligible, a verification email will be sent.", "registrationToken": token})
}

func (a *App) register(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
		Password    string `json:"password"`
	}
	if err := readJSON(w, r, &body); err != nil {
		return err
	}
	email, err := a.email(body.Email)
	if err != nil {
		return err
	}
	name, err := displayName(body.DisplayName)
	if err != nil {
		return err
	}
	if !validPassword(body.Password) {
		return passwordError()
	}
	if err = a.limit(r.Context(), "register:"+email, 5, 10*time.Minute); err != nil {
		return err
	}
	hash, err := hashPassword(body.Password)
	if err != nil {
		return err
	}
	code, err := newCode()
	if err != nil {
		return err
	}
	digest := codeHash(a.cfg.CodeSecret, email, code)
	registrationToken, err := randomToken()
	if err != nil {
		return err
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var taken bool
	if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM users WHERE lower(display_name)=lower($1) AND verified_at IS NOT NULL)", name).Scan(&taken); err != nil {
		return err
	}
	if taken {
		return problem(409, "display_name_taken", "That display name is already in use.")
	}
	var id string
	// Restarting a pending signup replaces its password AND verification context.
	// The browser must hold that context as well as the emailed code to activate it.
	err = tx.QueryRow(r.Context(), `INSERT INTO users(email,display_name,password_hash) VALUES($1,$2,$3)
		ON CONFLICT(email) DO UPDATE SET display_name=EXCLUDED.display_name,password_hash=EXCLUDED.password_hash
		WHERE users.verified_at IS NULL RETURNING id`, email, name, hash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		registrationAccepted(w, registrationToken)
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO verification_challenges(user_id,code_hash,registration_hash,expires_at)
		VALUES($1,$2,$3,now()+interval '30 minutes') ON CONFLICT(user_id) DO UPDATE SET
		code_hash=EXCLUDED.code_hash,registration_hash=EXCLUDED.registration_hash,expires_at=EXCLUDED.expires_at,attempts=0,sent_at=now()`, id, digest, tokenHash(registrationToken)); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	if err = a.deliver(r, id, email, code, digest); err != nil {
		writeJSON(w, 503, map[string]any{"error": map[string]string{"code": "email_unavailable", "message": "Email delivery failed. Keep the registration token and retry using resend verification."}, "registrationToken": registrationToken})
		return nil
	}
	a.log.Info("account registration accepted", "userId", id)
	registrationAccepted(w, registrationToken)
	return nil
}

func (a *App) deliver(r *http.Request, id, email, code string, digest []byte) error {
	if err := a.mailer.SendVerification(r.Context(), email, code); err != nil {
		// Keep the pending account and permit a prompt retry. A newer resend wins.
		_, _ = a.db.Exec(r.Context(), "UPDATE verification_challenges SET sent_at=now()-interval '60 seconds' WHERE user_id=$1 AND code_hash=$2", id, digest)
		a.log.Error("verification email delivery failed", "userId", id)
		return problem(503, "email_unavailable", "Email delivery failed. Retry using resend verification.")
	}
	return nil
}

func (a *App) resend(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Email string `json:"email"`
	}
	if err := readJSON(w, r, &body); err != nil {
		return err
	}
	email, err := a.email(body.Email)
	if err != nil {
		return err
	}
	if err = a.limit(r.Context(), "resend:"+email, 5, 10*time.Minute); err != nil {
		return err
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var id string
	err = tx.QueryRow(r.Context(), "SELECT id FROM users WHERE email=$1 AND verified_at IS NULL FOR UPDATE", email).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		accepted(w)
		return nil
	}
	if err != nil {
		return err
	}
	code, err := newCode()
	if err != nil {
		return err
	}
	digest := codeHash(a.cfg.CodeSecret, email, code)
	result, err := tx.Exec(r.Context(), `UPDATE verification_challenges SET code_hash=$2,expires_at=now()+interval '30 minutes',attempts=0,sent_at=now()
		WHERE user_id=$1 AND sent_at<=now()-interval '60 seconds'`, id, digest)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		accepted(w)
		return nil
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	if err = a.deliver(r, id, email, code, digest); err != nil {
		return err
	}
	accepted(w)
	return nil
}

func (a *App) verifyEmail(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Email             string  `json:"email"`
		Code              string  `json:"code"`
		RegistrationToken string  `json:"registrationToken"`
		DisplayName       *string `json:"displayName"`
	}
	if err := readJSON(w, r, &body); err != nil {
		return err
	}
	email, err := a.email(body.Email)
	if err != nil {
		return err
	}
	if err = a.limit(r.Context(), "verify:"+email, 20, 10*time.Minute); err != nil {
		return err
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var id, name string
	err = tx.QueryRow(r.Context(), "SELECT id,display_name FROM users WHERE email=$1 AND verified_at IS NULL FOR UPDATE", email).Scan(&id, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return verificationError()
	}
	if err != nil {
		return err
	}
	var digest, registrationDigest []byte
	var eligible bool
	err = tx.QueryRow(r.Context(), "SELECT code_hash,registration_hash,expires_at>now() AND attempts<5 FROM verification_challenges WHERE user_id=$1", id).Scan(&digest, &registrationDigest, &eligible)
	if errors.Is(err, pgx.ErrNoRows) {
		return verificationError()
	}
	if err != nil {
		return err
	}
	if !eligible || !validToken(body.RegistrationToken) || !hmac.Equal(registrationDigest, tokenHash(body.RegistrationToken)) {
		return verificationError()
	}
	if !hmac.Equal(digest, codeHash(a.cfg.CodeSecret, email, body.Code)) {
		if _, err = tx.Exec(r.Context(), "UPDATE verification_challenges SET attempts=attempts+1 WHERE user_id=$1", id); err != nil {
			return err
		}
		if err = tx.Commit(r.Context()); err != nil {
			return err
		}
		return verificationError()
	}
	if body.DisplayName != nil {
		name, err = displayName(*body.DisplayName)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(r.Context(), "UPDATE users SET verified_at=now(),display_name=$2 WHERE id=$1", id, name)
	if uniqueViolation(err) {
		return problem(409, "display_name_taken", "That name was taken. Retry this code with a different displayName.")
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM verification_challenges WHERE user_id=$1", id); err != nil {
		return err
	}
	// Activation and consumption commit together. Credit integration is deferred.
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	a.log.Info("account verified", "userId", id)
	writeJSON(w, 200, map[string]string{"message": "Email verified. You can now log in."})
	return nil
}

func (a *App) login(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := readJSON(w, r, &body); err != nil {
		return err
	}
	email, err := a.email(body.Email)
	if err != nil {
		return err
	}
	if err = a.limit(r.Context(), "login:"+email, 10, 10*time.Minute); err != nil {
		return err
	}
	invalid := problem(401, "invalid_credentials", "Email or password is incorrect, or the account is not verified.")
	if !validPassword(body.Password) {
		return invalid
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var u User
	var hash string
	var admin bool
	err = tx.QueryRow(r.Context(), `SELECT id,email,display_name,password_hash,is_admin,verified_at FROM users WHERE email=$1 AND verified_at IS NOT NULL FOR UPDATE`, email).Scan(&u.ID, &u.Email, &u.DisplayName, &hash, &admin, &u.VerifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		checkPassword(a.dummyHash, body.Password)
		return invalid
	}
	if err != nil {
		return err
	}
	if !checkPassword(hash, body.Password) {
		return invalid
	}
	token, err := randomToken()
	if err != nil {
		return err
	}
	expiry := time.Now().UTC().Add(a.cfg.SessionTTL)
	if _, err = tx.Exec(r.Context(), "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,$3)", tokenHash(token), u.ID, expiry); err != nil {
		return err
	}
	// Replace this browser's prior session; other devices remain logged in.
	if old, err := r.Cookie(a.cfg.CookieName()); err == nil {
		if _, err = tx.Exec(r.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(old.Value)); err != nil {
			return err
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	u.Roles = roles(admin)
	a.setCookie(w, token, expiry)
	a.log.Info("session created", "userId", u.ID)
	writeJSON(w, 200, map[string]any{"user": u, "expiresAt": expiry})
	return nil
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) error {
	if cookie, err := r.Cookie(a.cfg.CookieName()); err == nil {
		if _, err = a.db.Exec(r.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookie.Value)); err != nil {
			return err
		}
	}
	a.setCookie(w, "", time.Time{})
	w.WriteHeader(204)
	return nil
}
