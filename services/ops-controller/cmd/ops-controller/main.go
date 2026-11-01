package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/alerts"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/httpserver"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/incidents"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/logging"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telemetry"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	if err := run(); err != nil {
		slog.Error("ops controller stopped", "error", err)
		os.Exit(1)
	}
}

// buildVersion labels the /status header. Overridden at build time with
// -ldflags "-X main.buildVersion=...".
var buildVersion = "dev"

func run() error {
	cfg, err := config.Load(nil)
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, cfg.LogLevel, "ops-controller", cfg.Environment)
	slog.SetDefault(logger)
	logger.Info("starting ops controller",
		"autonomy_mode", cfg.AutonomyMode,
		"incident_store_enabled", cfg.IncidentStoreEnabled,
		"telegram_enabled", cfg.TelegramEnabled,
	)

	alertSink, incidentDB, err := openIncidentSink(context.Background(), cfg)
	if err != nil {
		return err
	}
	if incidentDB != nil {
		defer incidentDB.Close()
		if cfg.IncidentStoreEnabled {
			logger.Info("incident store ready", "db_max_open_connections", cfg.DBMaxOpenConns, "correlation_window", cfg.IncidentCorrelationWindow.String())
		} else {
			logger.Info("database ready for telegram bridge; incident store stays disabled", "db_max_open_connections", cfg.DBMaxOpenConns)
		}
	}

	listener, err := httpserver.Listen(cfg.Address)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	metrics := telemetry.NewMetrics()

	// The bridge is optional and must never block the controller. A
	// construction failure is logged and the service continues serving: a
	// broken chat bridge must not take down the service whose health it
	// reports on.
	built, err := buildBridge(ctx, cfg, incidentDB, metrics, logger)
	if err != nil {
		logger.Error("telegram bridge disabled", "error", err)
	}
	poller, sender := built.poller, built.sender

	// The runbook worker is what makes a dispatched runbook actually run.
	// Start it before the poller so a command can never be accepted while
	// nothing is draining the queue.
	runbookWorker, _, err := buildRunbookWorker(ctx, cfg, incidentDB, logger)
	if err != nil {
		logger.Error("runbook worker disabled", "error", err)
	}
	var workerDone chan struct{}
	if runbookWorker != nil {
		workerDone = make(chan struct{})
		go func() {
			defer close(workerDone)
			_ = runbookWorker.Run(ctx)
		}()
		// Return leases orphaned by a crash, or a job stays leased forever.
		go runbookWorker.Reclaim(ctx, time.Minute, 50)
	}

	// The notification worker delivers alerts through the same sender, with
	// email as the fallback so a Telegram outage cannot lose one.
	notifier, err := buildNotifier(cfg, incidentDB, sender, logger)
	if err != nil {
		logger.Error("notification worker disabled", "error", err)
	}
	var notifierDone chan struct{}
	if notifier != nil {
		notifierDone = make(chan struct{})
		go func() {
			defer close(notifierDone)
			_ = notifier.Run(ctx)
		}()
	}

	// The daily digest. It only enqueues, so it is started alongside the
	// notifier rather than instead of it: with no notifier running the digest
	// row would sit in the outbox undelivered, which is why it is gated on one.
	if notifier != nil {
		digest, err := buildDigest(cfg, incidentDB, built.deps, logger)
		if err != nil {
			logger.Error("daily digest disabled", "error", err)
		} else if digest != nil {
			logger.Info("daily digest scheduled",
				"hour", cfg.TelegramDigestHour, "minute", cfg.TelegramDigestMinute)
			go func() { _ = digest.Run(ctx) }()
		}
	} else if cfg.TelegramEnabled && cfg.TelegramDigestEnabled {
		logger.Warn("daily digest not scheduled because the notification worker is unavailable")
	}

	alertEvaluator, err := buildAlertEvaluator(cfg, incidentDB, logger)
	if err != nil {
		logger.Error("alert evaluator disabled", "error", err)
	} else if alertEvaluator != nil {
		logger.Info("prometheus alert evaluator started", "interval", time.Minute.String())
		go func() { _ = alertEvaluator.Run(ctx) }()
	}

	var bridgeDone chan struct{}
	if poller != nil {
		bridgeDone = make(chan struct{})
		go func() {
			defer close(bridgeDone)
			switch err := poller.Run(ctx); {
			case err == nil, errors.Is(err, context.Canceled):
			case errors.Is(err, telegram.ErrPollerLockHeld):
				// Another instance polls. Expected, not a fault.
				logger.Info("telegram poller standing by", "reason", "lock held elsewhere")
			default:
				// Fatal to the poller only. 409 and 401 land here, and both
				// mean retrying would hide the fault rather than fix it.
				logger.Error("telegram poller stopped", "error", err)
			}
		}()
	}

	serveErr := server(cfg, logger, metrics, alertSink).Serve(ctx, listener)

	// Let the poller finish its in-flight update's Postgres write before exit.
	if bridgeDone != nil {
		select {
		case <-bridgeDone:
		case <-time.After(cfg.ShutdownTimeout):
			logger.Warn("telegram poller did not stop within the shutdown timeout")
		}
	}
	// Let an in-flight runbook record its outcome rather than leaving the job
	// leased until the lease expires.
	if workerDone != nil {
		select {
		case <-workerDone:
		case <-time.After(cfg.ShutdownTimeout):
			logger.Warn("runbook worker did not stop within the shutdown timeout")
		}
	}
	if notifierDone != nil {
		select {
		case <-notifierDone:
		case <-time.After(cfg.ShutdownTimeout):
			logger.Warn("notification worker did not stop within the shutdown timeout")
		}
	}

	if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
		return serveErr
	}
	logger.Info("ops controller stopped cleanly")
	return nil
}

func server(cfg config.Config, logger *slog.Logger, metrics *telemetry.Metrics, alertSink alerts.Sink) *httpserver.Server {
	return httpserver.NewWithAlertSink(cfg, logger, metrics, alertSink)
}

// openIncidentSink opens the database when either the incident store or the
// Telegram bridge needs it, and returns an alert sink that is durable only
// when the incident store is explicitly enabled.
//
// The bridge is entirely Postgres-backed — offsets, the update ledger and the
// poller's advisory lock all live there — so it needs a handle even when the
// incident store is off.
func openIncidentSink(parent context.Context, cfg config.Config) (alerts.Sink, *sql.DB, error) {
	if !cfg.IncidentStoreEnabled && !cfg.TelegramEnabled {
		return alerts.NoopSink{}, nil, nil
	}
	if cfg.DatabaseURL == "" {
		if !cfg.IncidentStoreEnabled {
			// Telegram alone asked for the database and cannot have it. Report
			// it here rather than letting the bridge fail opaquely later.
			return nil, nil, errors.New("OPS_DATABASE_URL is required when the telegram bridge is enabled")
		}
		return nil, nil, errors.New("OPS_DATABASE_URL is required when the incident store is enabled")
	}
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return nil, nil, errors.New("open incident database")
	}
	configureDatabasePool(db, cfg)
	err = pingWithTimeout(parent, db, cfg.DBPingTimeout)
	if err != nil {
		_ = db.Close()
		return nil, nil, errors.New("incident database readiness failed")
	}
	if !cfg.IncidentStoreEnabled {
		// Only the bridge asked for the database. Alerts keep their existing
		// no-op behaviour: enabling the bridge must not silently turn on
		// durable incident ingestion.
		return alerts.NoopSink{}, db, nil
	}
	if err = verifyIncidentSchema(parent, db, cfg.DBPingTimeout); err != nil {
		_ = db.Close()
		return nil, nil, errors.New("incident database schema readiness failed")
	}
	repository, err := incidents.NewPostgresRepository(incidents.SQLDB{DB: db}, cfg.IncidentCorrelationWindow)
	if err != nil {
		_ = db.Close()
		return nil, nil, errors.New("configure incident repository")
	}
	return incidents.NewSink(repository), db, nil
}

func configureDatabasePool(db *sql.DB, cfg config.Config) {
	db.SetMaxOpenConns(cfg.DBMaxOpenConns)
	db.SetMaxIdleConns(cfg.DBMaxIdleConns)
	db.SetConnMaxLifetime(cfg.DBConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.DBConnMaxIdleTime)
}

type pinger interface {
	PingContext(context.Context) error
}

func pingWithTimeout(parent context.Context, db pinger, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	return db.PingContext(ctx)
}

func verifyIncidentSchema(parent context.Context, db *sql.DB, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var incidentsReady, observationsReady bool
	err := db.QueryRowContext(ctx, `SELECT
  to_regclass('public.ops_incidents') IS NOT NULL,
  to_regclass('public.ops_incident_observations') IS NOT NULL`).Scan(&incidentsReady, &observationsReady)
	if err != nil || !incidentsReady || !observationsReady {
		return errors.New("required incident relations are unavailable")
	}
	return nil
}
