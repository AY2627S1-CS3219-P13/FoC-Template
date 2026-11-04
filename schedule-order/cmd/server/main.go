package main

import (
	"context"
	"errors"
	"foc/schedule-order/internal/schedule"
	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func run(log *slog.Logger) error {
	cfg, e := schedule.LoadConfig()
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, e := pgxpool.New(ctx, cfg.DatabaseURL)
	if e != nil {
		return errors.New("invalid database configuration")
	}
	defer db.Close()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	e = schedule.Migrate(startup, db)
	cancel()
	if e != nil {
		return errors.New("schedule migration failed")
	}
	app := schedule.New(db, cfg, log)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			work, cancel := context.WithTimeout(ctx, 10*time.Second)
			if app.Recover(work) != nil {
				log.Warn("schedule recovery pending")
			}
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	server := &http.Server{Addr: ":8080", Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second}
	fail := make(chan error, 1)
	go func() { fail <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case e = <-fail:
	}
	shutdown, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	_ = server.Shutdown(shutdown)
	if e != nil && !errors.Is(e, http.ErrServerClosed) {
		return errors.New("HTTP server stopped")
	}
	return nil
}
func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "schedule-order")
	if e := run(log); e != nil {
		log.Error(e.Error())
		os.Exit(1)
	}
}
