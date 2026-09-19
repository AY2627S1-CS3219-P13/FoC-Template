package user

import (
	"context"
	"embed"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Migrate(ctx context.Context, db *pgxpool.Pool) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Only one process may apply migrations at a time.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(321901)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY)"); err != nil {
		return err
	}
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	for _, file := range files {
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)", file.Name()).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sql, err := migrations.ReadFile("migrations/" + file.Name())
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations(name) VALUES($1)", file.Name()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type User struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"displayName"`
	Roles       []string  `json:"roles"`
	VerifiedAt  time.Time `json:"verifiedAt"`
}

func roles(admin bool) []string {
	if admin {
		return []string{"user", "admin"}
	}
	return []string{"user"}
}

func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (a *App) session(ctx context.Context, token string) (User, time.Time, error) {
	var u User
	var expires time.Time
	var admin bool
	if !validToken(token) {
		return u, expires, errInvalidSession
	}
	err := a.db.QueryRow(ctx, `SELECT u.id,u.email,u.display_name,u.is_admin,u.verified_at,s.expires_at
		FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=$1 AND s.expires_at>now() AND u.verified_at IS NOT NULL`, tokenHash(token)).Scan(&u.ID, &u.Email, &u.DisplayName, &admin, &u.VerifiedAt, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		err = errInvalidSession
	}
	u.Roles = roles(admin)
	return u, expires, err
}

// PromoteAdmin is an operator-only command, never a registration option.
func PromoteAdmin(ctx context.Context, db *pgxpool.Pool, email string) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(321902)"); err != nil {
		return err
	}
	var id string
	err = tx.QueryRow(ctx, "UPDATE users SET is_admin=true WHERE email=$1 AND verified_at IS NOT NULL RETURNING id", email).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("no verified account matches that email")
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (a *App) Cleanup(ctx context.Context) {
	_, err := a.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at<=now(); DELETE FROM rate_limits WHERE expires_at<=now()`)
	if err != nil {
		a.log.Error("expired authentication data cleanup failed")
	}
}
