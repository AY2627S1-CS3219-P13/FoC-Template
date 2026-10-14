package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"foc/supplier-service/internal/supplier"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "supplier-service")
	if err := run(log); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := supplier.LoadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	pool.MaxConns = 10
	db, err := pgxpool.NewWithConfig(ctx, pool)
	if err != nil {
		return errors.New("database connection failed")
	}
	defer db.Close()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = supplier.Migrate(startup, db)
	cancel()
	if err != nil {
		return errors.New("database migration failed")
	}
	app := supplier.New(db, cfg, log)
	srv := &http.Server{Addr: ":8080", Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	fail := make(chan error, 1)
	go func() { fail <- srv.ListenAndServe() }()
	log.Info("HTTP listener started", "port", 8080)
	select {
	case <-ctx.Done():
	case err = <-fail:
	}
	shutdown, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	_ = srv.Shutdown(shutdown)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.New("HTTP server stopped unexpectedly")
	}
	return nil
}
