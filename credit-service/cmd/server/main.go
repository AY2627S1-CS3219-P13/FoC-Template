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

	"foc/credit-service/internal/credit"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "credit-service")
	if err := run(log); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := credit.LoadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	// Room for concurrent transfers (NFR1.1.2); tune once load-tested.
	poolConfig.MaxConns = 20
	db, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("database connection failed")
	}
	defer db.Close()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = credit.Migrate(startup, db)
	cancel()
	if err != nil {
		return errors.New("database migration failed")
	}
	sessions := credit.UserServiceSessions{URL: cfg.UserServiceURL, Token: cfg.UserServiceToken, Client: &http.Client{Timeout: 5 * time.Second}}
	app := credit.New(db, cfg, sessions, log)
	public := server(":8080", app.PublicHandler())
	internal := server(":8081", app.InternalHandler())
	fail := make(chan error, 2)
	go func() { fail <- public.ListenAndServe() }()
	go func() { fail <- internal.ListenAndServe() }()
	log.Info("HTTP listeners started", "publicPort", 8080, "internalPort", 8081)
	select {
	case <-ctx.Done():
	case err = <-fail:
	}
	shutdown, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	_ = public.Shutdown(shutdown)
	_ = internal.Shutdown(shutdown)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.New("HTTP server stopped unexpectedly")
	}
	return nil
}

func server(address string, handler http.Handler) *http.Server {
	return &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
}
