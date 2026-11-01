package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/aispend"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/deployments"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/incidents"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/notifications"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/operations"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/provider"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/releases"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/sysmetrics"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram/commands"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telemetry"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/useraccounts"
)

// eatZone is the operator's timezone. Falling back to UTC is deliberate: a
// container without tzdata should render honest UTC timestamps rather than
// silently label UTC as local time.
func eatZone(logger *slog.Logger) *time.Location {
	loc, err := time.LoadLocation("Africa/Nairobi")
	if err != nil {
		logger.Warn("timezone database unavailable; rendering timestamps in UTC", "error", err)
		return time.UTC
	}
	return loc
}

// bridge is what one successful buildBridge produces: the poller that reads
// commands, the sender the notifier and digest write through, and the read
// projections both the commands and the daily digest render from. The
// projections are carried rather than rebuilt so /status and the 08:00 digest
// read through exactly one set of queries.
type bridge struct {
	poller *telegram.Poller
	sender telegram.Sender
	deps   commands.Deps
}

// buildBridge assembles the Telegram poller. It returns a zero bridge when the
// bridge is disabled, which is the normal case.
//
// Fail closed: any error here leaves the poller down. The controller itself
// keeps serving, because a broken chat bridge must not take down the service
// whose health it reports on.
func buildBridge(
	ctx context.Context,
	cfg config.Config,
	db *sql.DB,
	metrics *telemetry.Metrics,
	logger *slog.Logger,
) (bridge, error) {
	if !cfg.TelegramEnabled {
		return bridge{}, nil
	}
	if db == nil {
		return bridge{}, errors.New("telegram bridge requires OPS_DATABASE_URL")
	}

	client, err := telegram.NewClient(cfg.TelegramBotToken)
	if err != nil {
		return bridge{}, fmt.Errorf("telegram client: %w", err)
	}

	// Validate the token at boot. A failure here is a configuration error, not
	// a runtime one, so it is reported plainly and the poller stays down
	// rather than retrying a credential that will never work.
	verifyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	me, err := client.GetMe(verifyCtx)
	if err != nil {
		return bridge{}, fmt.Errorf("telegram token verification failed: %w", err)
	}
	logger.Info("telegram bot authenticated", "bot_username", me.Username, "bot_id", me.ID)

	store, err := telegram.NewPostgresStore(telegram.SQLDB{DB: db})
	if err != nil {
		return bridge{}, fmt.Errorf("telegram store: %w", err)
	}

	sender, err := telegram.NewRateLimitedSender(client, metrics, float64(cfg.TelegramSendRatePerSec))
	if err != nil {
		return bridge{}, fmt.Errorf("telegram sender: %w", err)
	}

	registry := telegram.NewRegistry()
	deps, err := buildCommandDeps(cfg, db, logger)
	if err != nil {
		return bridge{}, err
	}
	if err := commands.Register(registry, deps); err != nil {
		return bridge{}, fmt.Errorf("register commands: %w", err)
	}
	// Observability reads: host resources, provider quota, user counts. Each
	// registers independently so one unavailable source costs only its own
	// command, not the whole surface.
	registerObservabilityCommands(cfg, db, registry, deps.Location, logger)

	if err := registerMutatingCommands(cfg, db, registry, deps.Location, logger); err != nil {
		// A broken mutating surface must not take the read commands down with
		// it: /status at 3am is worth more than /run.
		logger.Error("mutating telegram commands unavailable", "error", err)
	}

	poller, err := telegram.NewPoller(telegram.PollerOptions{
		Client:      client,
		Store:       store,
		Auth:        telegram.NewAccess(cfg.TelegramChatID, cfg.TelegramAllowedUserIDs),
		Dispatcher:  telegram.NewRouter(registry, metrics),
		Sender:      sender,
		Metrics:     metrics,
		Logger:      logger,
		ChatID:      cfg.TelegramChatID,
		PollTimeout: time.Duration(cfg.TelegramPollTimeoutSeconds) * time.Second,
	})
	if err != nil {
		return bridge{}, err
	}
	return bridge{poller: poller, sender: sender, deps: deps}, nil
}

// buildCommandDeps wires the read-only projections behind the commands.
//
// Each projection is independent: one that fails to construct is left nil and
// its command renders "unavailable", rather than preventing the whole bridge
// from starting. A bridge that reports four of five sections is far more
// useful at 3am than one that refuses to boot.
func buildCommandDeps(cfg config.Config, db *sql.DB, logger *slog.Logger) (commands.Deps, error) {
	deps := commands.Deps{
		Version:      buildVersion,
		Environment:  cfg.Environment,
		Location:     eatZone(logger),
		QueryTimeout: 5 * time.Second,
	}

	if q, err := incidents.NewQueries(incidents.SQLQueryer{DB: db}); err != nil {
		logger.Error("incident queries unavailable", "error", err)
	} else {
		deps.Incidents = q
	}

	if q, err := jobs.NewQueries(jobs.SQLQueryer{DB: db}, nil); err != nil {
		logger.Error("job queries unavailable", "error", err)
	} else {
		deps.Jobs = q
	}

	if q, err := deployments.NewQueries(deployments.SQLQueryer{DB: db}, nil); err != nil {
		logger.Error("deployment queries unavailable", "error", err)
	} else {
		deps.Deployments = q
	}

	budgetConfig := aispend.SpendConfig{
		MonthlyCapMicros: int64(cfg.AIMonthlyCapUSD) * 1_000_000,
		Location:         deps.Location,
	}
	if q, err := aispend.NewQueries(aispend.SQLQueryer{DB: db}, budgetConfig); err != nil {
		logger.Error("budget queries unavailable", "error", err)
	} else {
		deps.Budget = q
	}

	return deps, nil
}

// RunbookQueue is the durable queue runbook jobs are dispatched onto.
const RunbookQueue = "runbooks"

// buildRunbookWorker assembles the durable job worker that actually executes
// runbooks, plus the typed executor behind it.
//
// This is the piece whose absence made /run unshippable: ops_jobs accepted
// work and nothing drained it, so a dispatched runbook would report success
// and do nothing. Returns (nil, nil, nil) when runbooks are disabled.
func buildRunbookWorker(
	ctx context.Context,
	cfg config.Config,
	db *sql.DB,
	logger *slog.Logger,
) (*jobs.Worker, *operations.SafeExecutor, error) {
	if !cfg.TelegramRunbooksEnabled {
		return nil, nil, nil
	}
	if db == nil {
		return nil, nil, errors.New("runbooks require OPS_DATABASE_URL")
	}

	// Container control is opt-in. The Docker socket is root-equivalent on the
	// host, so an unset flag must mean "no container operations offered",
	// never "try anyway".
	var docker *operations.DockerClient
	if cfg.DockerEnabled {
		client, err := operations.NewDockerClient(cfg.DockerSocketPath)
		if err != nil {
			return nil, nil, fmt.Errorf("docker client: %w", err)
		}
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := client.Ping(pingCtx); err != nil {
			// Surface this at boot rather than at 3am on the first restart.
			return nil, nil, fmt.Errorf("docker socket %s unreachable: %w", cfg.DockerSocketPath, err)
		}
		docker = client
		logger.Warn("docker container control is ENABLED for runbooks; the socket is root-equivalent on the host",
			"socket", cfg.DockerSocketPath, "allowed_services", cfg.RunbookServices)
	}

	executor, err := operations.NewSafeExecutor(db, docker, cfg.RunbookServices)
	if err != nil {
		return nil, nil, fmt.Errorf("operation executor: %w", err)
	}

	repo, err := jobs.NewRepository(jobs.SQLDB{DB: db}, nil, jobs.Config{
		MinLease:        5 * time.Second,
		MaxLease:        5 * time.Minute,
		InitialBackoff:  10 * time.Second,
		MaxBackoff:      10 * time.Minute,
		MaxPayloadBytes: 16 * 1024,
		Now:             time.Now,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("job repository: %w", err)
	}

	handler := jobs.HandlerFunc(func(ctx context.Context, job jobs.Job) error {
		payload, err := operations.ParseJobPayload(job.Payload)
		if err != nil {
			// A malformed payload will never parse on a retry.
			return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
		}
		result, err := executor.Execute(ctx, operations.Request{
			Kind:           payload.Kind,
			Target:         payload.Target,
			Scope:          payload.Scope,
			IdempotencyKey: job.IdempotencyKey,
			Timeout:        2 * time.Minute,
		})
		if errors.Is(err, operations.ErrNotSupported) {
			return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
		}
		if err != nil {
			return err
		}
		logger.Info("runbook executed",
			"job_id", job.ID, "kind", payload.Kind, "target", payload.Target,
			"accepted", result.Accepted, "reason", result.Reason,
			"requested_by", payload.RequestedBy)
		return nil
	})

	worker, err := jobs.NewWorker(repo, handler, jobs.WorkerConfig{
		Queue:       RunbookQueue,
		Name:        "ops-controller-runbooks",
		Concurrency: 1, // Runbooks are serialized: one production change at a time.
		Lease:       2 * time.Minute,
		Logger:      logger,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("job worker: %w", err)
	}
	logger.Info("runbook worker ready",
		"queue", RunbookQueue, "supported_operations", executor.SupportedKinds())
	return worker, executor, nil
}

// registerMutatingCommands adds the release train and, when enabled, runbooks.
//
// Release-train state lives entirely in Postgres and deploys nothing itself,
// so /release and /hold need no extra privilege. Runbooks do execute work, so
// they are gated separately on OPS_TELEGRAM_RUNBOOKS_ENABLED.
func registerMutatingCommands(
	cfg config.Config,
	db *sql.DB,
	registry *telegram.Registry,
	location *time.Location,
	logger *slog.Logger,
) error {
	trains, err := releases.NewManager(releases.SQLDB{DB: db}, releases.WindowPolicy{Location: location}, time.Now)
	if err != nil {
		return fmt.Errorf("release manager: %w", err)
	}
	confirmations, err := releases.NewConfirmations(releases.SQLDB{DB: db}, releases.DefaultConfirmationTTL, time.Now)
	if err != nil {
		return fmt.Errorf("confirmations: %w", err)
	}
	if err := commands.RegisterRelease(registry, commands.ReleaseDeps{
		Trains:        trains,
		Confirmations: confirmations,
		Location:      location,
		QueryTimeout:  10 * time.Second,
	}); err != nil {
		return fmt.Errorf("register release commands: %w", err)
	}

	if !cfg.TelegramRunbooksEnabled {
		logger.Info("telegram runbooks are disabled; /run and /confirm are not registered")
		return nil
	}

	// Build the executor the command consults so an unsupported operation is
	// refused up front rather than enqueued and silently discarded.
	var docker *operations.DockerClient
	if cfg.DockerEnabled {
		if docker, err = operations.NewDockerClient(cfg.DockerSocketPath); err != nil {
			return fmt.Errorf("docker client: %w", err)
		}
	}
	executor, err := operations.NewSafeExecutor(db, docker, cfg.RunbookServices)
	if err != nil {
		return fmt.Errorf("operation executor: %w", err)
	}
	repo, err := jobs.NewRepository(jobs.SQLDB{DB: db}, nil, jobs.Config{
		MinLease:        5 * time.Second,
		MaxLease:        5 * time.Minute,
		InitialBackoff:  10 * time.Second,
		MaxBackoff:      10 * time.Minute,
		MaxPayloadBytes: 16 * 1024,
		Now:             time.Now,
	})
	if err != nil {
		return fmt.Errorf("job repository: %w", err)
	}
	if err := commands.RegisterRun(registry, commands.RunDeps{
		Jobs:          repo,
		Executor:      executor,
		Confirmations: confirmations,
		Queue:         RunbookQueue,
		QueryTimeout:  10 * time.Second,
	}); err != nil {
		return fmt.Errorf("register runbook commands: %w", err)
	}
	logger.Info("telegram runbooks registered", "supported_operations", executor.SupportedKinds())
	return nil
}

// buildNotifier assembles the outbox worker that delivers alerts.
//
// Telegram is the primary transport and email is the fallback. Both are
// required: spec §12 says a Telegram send failure must never lose a
// notification, and without a second transport there is nothing to fall back
// to. If email is not configured the worker still runs — a single-transport
// notifier is worse than a dual one but far better than none — and says so.
//
// Returns nil when the bridge is disabled.
func buildNotifier(
	cfg config.Config,
	db *sql.DB,
	sender telegram.Sender,
	logger *slog.Logger,
) (*notifications.Worker, error) {
	if !cfg.TelegramEnabled || db == nil || sender == nil {
		return nil, nil
	}
	store, err := notifications.NewPostgresStore(notifications.SQLDB{DB: db})
	if err != nil {
		return nil, fmt.Errorf("notification store: %w", err)
	}
	primary, err := notifications.NewTelegramTransport(sender, cfg.TelegramChatID)
	if err != nil {
		return nil, fmt.Errorf("telegram transport: %w", err)
	}

	var fallback notifications.Transport
	if cfg.EmailConfigured() {
		mailer := &notifications.SMTPMailer{
			Addr:     cfg.EmailSMTPAddress,
			Username: cfg.EmailSMTPUsername,
			Password: cfg.EmailSMTPPassword,
			Timeout:  30 * time.Second,
		}
		emailTransport, err := notifications.NewEmailTransport(mailer, cfg.EmailFromAddress, cfg.EmailToAddress)
		if err != nil {
			return nil, fmt.Errorf("email transport: %w", err)
		}
		fallback = emailTransport
	} else {
		logger.Warn("no email fallback configured; a Telegram outage will delay notifications until it recovers",
			"set", "OPS_EMAIL_SMTP_ADDRESS, OPS_EMAIL_FROM_ADDRESS, OPS_EMAIL_TO_ADDRESS")
	}

	return notifications.NewWorker(store, primary, fallback, nil, logger, notifications.WorkerConfig{Now: time.Now})
}

// buildDigest assembles the daily digest scheduler.
//
// The digest is what makes the bridge answer "is everything fine" without
// anyone asking, and its absence is why the operator saw no daily updates: the
// outbox, the backlog query and the once-for-all-time dedup key all existed,
// and nothing ever called them.
//
// It enqueues into the same outbox the notifier drains, so it inherits retries
// and the email fallback rather than sending on its own. Returns nil when the
// bridge or the digest is disabled.
func buildDigest(
	cfg config.Config,
	db *sql.DB,
	deps commands.Deps,
	logger *slog.Logger,
) (*notifications.Digest, error) {
	if !cfg.TelegramEnabled || !cfg.TelegramDigestEnabled || db == nil {
		return nil, nil
	}
	store, err := notifications.NewPostgresStore(notifications.SQLDB{DB: db})
	if err != nil {
		return nil, fmt.Errorf("notification store: %w", err)
	}
	location := deps.Location
	if location == nil {
		location = eatZone(logger)
	}
	return notifications.NewDigest(store, deps, logger, notifications.DigestConfig{
		Hour:     cfg.TelegramDigestHour,
		Minute:   cfg.TelegramDigestMinute,
		Location: location,
		// Six projection reads at deps.QueryTimeout each, plus the backlog
		// read, must all fit inside one assembly.
		Timeout: 60 * time.Second,
	})
}

// registerObservabilityCommands adds /resources, /quota and /users.
//
// Failures are logged and skipped rather than returned: a missing Prometheus
// or an unreadable provider table should cost that one command, not the read
// surface an operator relies on at 3am.
func registerObservabilityCommands(
	cfg config.Config,
	db *sql.DB,
	registry *telegram.Registry,
	location *time.Location,
	logger *slog.Logger,
) {
	// Host CPU, memory and disk come from the observability stack: this
	// container can only see its own cgroup, never the host filesystem.
	if client, err := sysmetrics.New(cfg.PrometheusURL); err != nil {
		logger.Error("/resources unavailable", "error", err)
	} else if err := commands.RegisterResources(registry, commands.ResourceDeps{
		Metrics:      client,
		QueryTimeout: 12 * time.Second,
	}); err != nil {
		logger.Error("register /resources", "error", err)
	}

	if q, err := provider.NewQueries(provider.SQLQueryer{DB: db}, time.Now); err != nil {
		logger.Error("/quota unavailable", "error", err)
	} else if err := commands.RegisterQuota(registry, commands.QuotaDeps{
		Quota:        q,
		Location:     location,
		QueryTimeout: 5 * time.Second,
	}); err != nil {
		logger.Error("register /quota", "error", err)
	}

	if q, err := useraccounts.NewQueries(useraccounts.SQLQueryer{DB: db}, time.Now); err != nil {
		logger.Error("/users unavailable", "error", err)
	} else if err := commands.RegisterUsers(registry, commands.UserDeps{
		Users:        q,
		Location:     location,
		QueryTimeout: 5 * time.Second,
	}); err != nil {
		logger.Error("register /users", "error", err)
	}
}
