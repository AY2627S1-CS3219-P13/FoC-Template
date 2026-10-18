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

	"foc/order-service/internal/order"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "order-service")
	if err := run(log); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := order.LoadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	poolConfig.MaxConns = 20
	db, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("database connection failed")
	}
	defer db.Close()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = order.Migrate(startup, db)
	cancel()
	if err != nil {
		return errors.New("database migration failed")
	}
	admissionConfig := poolConfig.Copy()
	admissionConfig.MaxConns = 4
	admissions, err := pgxpool.NewWithConfig(ctx, admissionConfig)
	if err != nil {
		return errors.New("admission database connection failed")
	}
	defer admissions.Close()
	app := order.NewWithAdmissionPool(db, admissions, cfg, log)
	// Old instances must be stopped before this resumable upgrade pass.
	if err := app.BootstrapWorkflow(ctx); err != nil {
		return errors.New("workflow bootstrap failed; legacy repair remains pending")
	}
	go recoveryLoop(ctx, app, cfg.RecoveryInterval, log)
	for _, consumer := range []string{"workflow-timers", "workflow-settlement"} {
		go workflowLoop(ctx, app, consumer, log)
	}
	go lifecycleLoop(ctx, app, log)
	go publicationLoop(ctx, app, log)
	server := &http.Server{Addr: ":8080", Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second}
	failure := make(chan error, 1)
	go func() { failure <- server.ListenAndServe() }()
	log.Info("HTTP listener started", "publicPort", 8080)
	select {
	case <-ctx.Done():
	case err = <-failure:
	}
	shutdown, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	_ = server.Shutdown(shutdown)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.New("HTTP server stopped unexpectedly")
	}
	return nil
}

func publicationLoop(ctx context.Context, app *order.App, log *slog.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		work, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := app.PublishEvents(work); err != nil {
			log.Warn("event publication pending; retrying")
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func recoveryLoop(ctx context.Context, app *order.App, interval time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		recovery, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := app.Recover(recovery); err != nil {
			log.Warn("creation recovery failed")
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func lifecycleLoop(ctx context.Context, app *order.App, log *slog.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		work, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := app.Advance(work); err != nil {
			log.Warn("lifecycle recovery failed")
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func workflowLoop(ctx context.Context, app *order.App, consumer string, log *slog.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		work, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := app.ConsumeWorkflow(work, consumer); err != nil {
			log.Warn("workflow consumption pending; retrying", "consumer", consumer)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
