package supplier

import (
	"context"
	"embed"
	"sort"
	"time"

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
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(321903)"); err != nil {
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
		data, err := migrations.ReadFile("migrations/" + file.Name())
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(data)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations(name) VALUES($1)", file.Name()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type Supplier struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Category     string    `json:"category"`
	LocationID   string    `json:"locationId"`
	Description  string    `json:"description"`
	OpeningHours string    `json:"openingHours"`
	Active       bool      `json:"active"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type Location struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

const supplierColumns = "id,name,category,location_id,description,opening_hours,active,created_at,updated_at"

type scanner interface{ Scan(...any) error }

func scanSupplier(row scanner) (Supplier, error) {
	var s Supplier
	err := row.Scan(&s.ID, &s.Name, &s.Category, &s.LocationID, &s.Description, &s.OpeningHours, &s.Active, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}
