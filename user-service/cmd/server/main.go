package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"foc/user-service/internal/user"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "user-service")
	if err := run(log); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := user.LoadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	poolConfig.MaxConns = 10
	db, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("database connection failed")
	}
	defer db.Close()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = user.Migrate(startup, db)
	cancel()
	if err != nil {
		return errors.New("database migration failed")
	}
	if len(os.Args) > 1 {
		if len(os.Args) != 3 || os.Args[1] != "promote-admin" {
			return errors.New("usage: user-service [promote-admin verified-school-email]")
		}
		if err = user.PromoteAdmin(ctx, db, strings.ToLower(strings.TrimSpace(os.Args[2]))); err != nil {
			return errors.New("admin promotion failed: account must already be verified")
		}
		log.Info("operator promoted a verified account to admin")
		return nil
	}
	app, err := user.New(db, cfg, user.SMTPMailer{Config: cfg}, log)
	if err != nil {
		return errors.New("authentication initialization failed")
	}
	public := server(":8080", app.PublicHandler())
	internal := server(":8081", app.InternalHandler())
	fail := make(chan error, 2)
	go func() { fail <- public.ListenAndServe() }()
	go func() { fail <- internal.ListenAndServe() }()
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				clean, done := context.WithTimeout(ctx, 10*time.Second)
				app.Cleanup(clean)
				done()
			}
		}
	}()
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
	return &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
}
