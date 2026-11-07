// Package schedule embeds scheduling in the Order application.
package schedule

import (
	"context"
	internal "foc/schedule-order/internal/schedule"
	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"
	"net/http"
)

type Config = internal.Config
type Importer = internal.Importer
type Effect = internal.Effect
type App = internal.App

func Migrate(ctx context.Context, db *pgxpool.Pool) error { return internal.Migrate(ctx, db) }
func New(db *pgxpool.Pool, cfg Config, log *slog.Logger, importer Importer) *App {
	a := internal.New(db, cfg, log)
	a.SetImporter(importer)
	return a
}
func Handler(order http.Handler, app *App) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/v1/scheduled-orders", app.Handler())
	mux.Handle("/api/v1/scheduled-orders/", app.Handler())
	mux.Handle("/", order)
	return mux
}

func ImportLegacy(ctx context.Context, db *pgxpool.Pool, sourceURL string) error {
	return internal.ImportLegacy(ctx, db, sourceURL)
}
