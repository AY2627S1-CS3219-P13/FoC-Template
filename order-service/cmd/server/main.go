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
	schedule "foc/schedule-order"
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
	scheduleStartup, scheduleCancel := context.WithTimeout(ctx, 30*time.Second)
	defer scheduleCancel()
	if err := schedule.Migrate(scheduleStartup, db); err != nil {
		return errors.New("schedule migration failed")
	}
	if len(os.Args) > 1 && os.Args[1] == "import-schedules" {
		source := os.Getenv("ORDER_LEGACY_SCHEDULE_DATABASE_URL")
		if source == "" {
			return errors.New("ORDER_LEGACY_SCHEDULE_DATABASE_URL required for offline import")
		}
		work, done := context.WithTimeout(ctx, 5*time.Minute)
		defer done()
		if err := schedule.ImportLegacy(work, db, source); err != nil {
			return errors.New("offline schedule import failed; target must be empty and legacy database reachable")
		}
		log.Info("legacy schedules imported; source retained")
		return nil
	}
	if cfg.SupplierInternalURL == "" || cfg.SupplierInternalToken == "" {
		return errors.New("Order scheduling requires ORDER_SUPPLIER_INTERNAL_URL and ORDER_SUPPLIER_INTERNAL_TOKEN")
	}
	admissionConfig := poolConfig.Copy()
	admissionConfig.MaxConns = 4
	admissions, err := pgxpool.NewWithConfig(ctx, admissionConfig)
	if err != nil {
		return errors.New("admission database connection failed")
	}
	defer admissions.Close()
	app := order.NewWithAdmissionPool(db, admissions, cfg, log)
	scheduling := schedule.New(db, schedule.Config{
		Origin: cfg.Origin, SessionCookie: cfg.SessionCookie,
		UserServiceURL: cfg.UserServiceURL, UserServiceToken: cfg.UserServiceToken,
		SupplierServiceURL: cfg.SupplierInternalURL, SupplierServiceToken: cfg.SupplierInternalToken,
		CreditServiceURL: cfg.CreditServiceURL, CreditServiceToken: cfg.CreditServiceToken,
		DependencyTimeout: cfg.DependencyTimeout,
	}, log, app.PrepareScheduledEvent)
	// Old instances must be stopped before this resumable upgrade pass.
	if err := app.BootstrapWorkflow(ctx); err != nil {
		return errors.New("workflow bootstrap failed; legacy repair remains pending")
	}
	go scheduleLoop(ctx, scheduling.Recover, "schedule recovery pending", log)
	go scheduleLoop(ctx, scheduling.ConsumeEvents, "schedule acceptance pending", log)
	go recoveryLoop(ctx, app, cfg.RecoveryInterval, log)
	for _, consumer := range []string{"workflow-timers", "workflow-settlement", "workflow-nearby-alerts"} {
		go workflowLoop(ctx, app, consumer, log)
	}
	go lifecycleLoop(ctx, app, log)
	go publicationLoop(ctx, app, log)
	go pushLoop(ctx, app, log)
	server := &http.Server{Addr: ":8080", Handler: schedule.Handler(app.Handler(), scheduling), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second}
	private := &http.Server{Addr: ":8081", Handler: app.InternalHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second}
	failure := make(chan error, 2)
	go func() { failure <- server.ListenAndServe() }()
	go func() { failure <- private.ListenAndServe() }()
	log.Info("HTTP listener started", "publicPort", 8080)
	select {
	case <-ctx.Done():
	case err = <-failure:
	}
	shutdown, done := context.WithTimeout(context.Background(), 15*time.Second)
	defer done()
	_ = server.Shutdown(shutdown)
	_ = private.Shutdown(shutdown)
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

// pushLoop sends nearby-alert notifications every second and removes finished
// delivery records older than seven days every ten minutes.
func pushLoop(ctx context.Context, app *order.App, log *slog.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var cleaned time.Time
	for {
		work, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := app.SendPushAlerts(work); err != nil {
			log.Warn("push delivery pending; retrying")
		}
		if time.Since(cleaned) >= 10*time.Minute {
			if err := app.CleanupPushDeliveries(work); err != nil {
				log.Warn("push delivery cleanup failed")
			} else {
				cleaned = time.Now()
			}
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

func scheduleLoop(ctx context.Context, run func(context.Context) error, pending string, log *slog.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		work, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := run(work); err != nil {
			log.Warn(pending)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
