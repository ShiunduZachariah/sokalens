package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	acca "github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/acca"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/billingevents"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/fixtureevents"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/ingestion"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/inplay"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/maintenance"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/matchstate"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/metrics"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/notifications"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/predictionenginepython"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/predictions"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/footballdata"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/thesportsdb"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/quota"
	recommendationjobs "github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/recommendations"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/scheduler"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/seourl"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/snapshots"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	cfg, err := config.Load(nil)
	if err != nil {
		slog.Error("invalid worker configuration", "error", err)
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := scheduler.CheckHeartbeat(cfg.HeartbeatPath, cfg.HeartbeatMaxAge, time.Now()); err != nil {
			slog.Error("worker healthcheck failed", "error", err)
			os.Exit(1)
		}
		return
	}

	slog.Info(
		"worker ready",
		"environment", cfg.Environment,
		"provider_mode", cfg.FootballProvider,
		"real_api_football", cfg.EnableRealAPIFootballCalls,
		"mock_api_football", cfg.EnableMockAPIFootballCalls,
		"real_football_data_org", cfg.EnableRealFootballDataOrgCalls,
		"real_thesportsdb", cfg.EnableRealTheSportsDBCalls,
		"poll_interval", cfg.PollInterval.String(),
		"provider_schedule_interval", cfg.ProviderScheduleInterval.String(),
		"prediction_schedule_interval", cfg.PredictionScheduleInterval.String(),
		"queue_concurrency", cfg.QueueConcurrency,
		"db_max_open_connections", cfg.DBMaxOpenConns,
		"heartbeat_path", cfg.HeartbeatPath,
	)

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	configureDatabasePool(db, cfg)
	dailyProviderSpend := 0
	if cfg.EnableRealAPIFootballCalls {
		quotaCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		dailyProviderSpend, err = loadDailyProviderSpend(quotaCtx, db, apifootball.ProviderName, time.Now())
		cancel()
		if err != nil {
			slog.Error("load provider daily quota spend", "provider", apifootball.ProviderName, "error", err)
			os.Exit(1)
		}
	}

	providerBaseURL := cfg.MockProviderURL
	if cfg.EnableRealAPIFootballCalls {
		providerBaseURL = cfg.APIFootballBaseURL
	}
	providerClient := apifootball.NewClient(apifootball.Config{
		BaseURL:           providerBaseURL,
		APIKey:            cfg.APIFootballKey,
		DailyLimit:        cfg.ProviderDailyLimit,
		DailyReserve:      cfg.ProviderDailyReserve,
		MinuteLimit:       cfg.ProviderMinuteLimit,
		MinuteReserve:     cfg.ProviderMinuteReserve,
		InitialDailySpent: dailyProviderSpend,
		HTTPClient:        &http.Client{Timeout: 20 * time.Second},
	})
	footballDataClient := footballdata.NewClient(footballdata.Config{
		BaseURL:     cfg.FootballDataOrgBaseURL,
		APIKey:      cfg.FootballDataOrgAPIKey,
		MinuteLimit: cfg.ProviderMinuteLimit,
		HTTPClient:  &http.Client{Timeout: 20 * time.Second},
	})
	// TheSportsDB is a second-tier reference provider: free-tier logo/metadata
	// backfill only. Its key lives in the URL path, so the client is only built
	// (and registered) when real calls are explicitly enabled.
	var theSportsDBClient *thesportsdb.Client
	if cfg.EnableRealTheSportsDBCalls {
		theSportsDBClient = thesportsdb.NewClient(thesportsdb.Config{
			BaseURL:    cfg.TheSportsDBBaseURL,
			APIKey:     cfg.TheSportsDBAPIKey,
			HTTPClient: &http.Client{Timeout: 20 * time.Second},
		})
	}
	jobRepo := jobs.NewRepositoryWithDefaults(db, cfg.JobMaxAttempts, cfg.JobLeaseDuration)
	leaseRefresh := cfg.JobLeaseDuration / 3
	runtimeWorkerID := workerID()
	requestDedupe := ingestion.NewRequestDedupe(db, cfg.RequestDedupeTTL)
	workerMetrics := metrics.New()
	// Nil unless the operator turned snapshots on. Passing nil rather than a
	// repository with a disabled flag inside keeps the "off" case free: the
	// job does not construct a snapshot, does not hash it, and does not touch
	// the database.
	var liveSnapshotWriter interface {
		Append(context.Context, snapshots.LiveSnapshot) (bool, error)
	}
	if cfg.EnableFixtureSnapshots {
		liveSnapshotWriter = snapshots.NewRepository(db)
		slog.Info("append-only live match history enabled")
	}
	// Same arrangement for the event timeline, and the same reason: nil means
	// the live job never parses the embedded event list at all. The events cost
	// no extra provider request, but they do cost a write per event per poll.
	var liveEventWriter interface {
		Append(context.Context, int64, string, time.Time, apifootball.FixtureEvent) (bool, error)
		ReconcileScoringEvents(context.Context, int64, string, time.Time, []apifootball.FixtureEvent, int64, int64, *int, *int) (int, error)
	}
	if cfg.EnableFixtureEvents {
		liveEventWriter = fixtureevents.NewRepository(db)
		slog.Info("live fixture event timeline enabled")
	}
	providerAdapters := []ingestion.ProviderAdapter{
		ingestion.NewAPIFootballAdapter(db, providerClient, jobRepo, requestDedupe, runtimeWorkerID, workerMetrics, liveSnapshotWriter, liveEventWriter, cfg.InPlayStatisticsPollEnabled),
		ingestion.NewFootballDataAdapter(db, footballDataClient, jobRepo),
	}
	if theSportsDBClient != nil {
		providerAdapters = append(providerAdapters, ingestion.NewTheSportsDBAdapter(db, theSportsDBClient, jobRepo, cfg.TheSportsDBMaxPerCycle))
	}
	providerRegistry, err := ingestion.NewProviderRegistry(providerAdapters...)
	if err != nil {
		slog.Error("register provider adapters", "error", err)
		os.Exit(1)
	}
	providerProcessor := ingestion.NewProcessor(ingestion.ProcessorOptions{
		Jobs:                 jobRepo,
		Queue:                ingestion.DefaultProviderQueue,
		WorkerID:             runtimeWorkerID,
		LeaseRefreshInterval: leaseRefresh,
		Handlers:             providerRegistry.Handlers(ingestion.DefaultProviderQueue),
	})
	fixturesProcessor := ingestion.NewProcessor(ingestion.ProcessorOptions{
		Jobs:                 jobRepo,
		Queue:                "provider-fixtures",
		WorkerID:             runtimeWorkerID,
		LeaseRefreshInterval: leaseRefresh,
		Handlers:             providerRegistry.Handlers("provider-fixtures"),
	})
	backfillRepository := ingestion.NewSQLBackfillRepository(db)
	fixtureHandlers := providerRegistry.Handlers("provider-fixtures")
	backfillProviders := make([]ingestion.BackfillProvider, 0, 3)
	if cfg.EnableRealFootballDataOrgCalls {
		backfillProviders = append(backfillProviders, ingestion.NewJobBackfillProvider(
			footballdata.ProviderName, []string{"results"}, fixtureHandlers[ingestion.FootballDataMatchesJobKey()], backfillRepository,
		))
	}
	if theSportsDBClient != nil {
		backfillProviders = append(backfillProviders, ingestion.NewJobBackfillProvider(
			thesportsdb.ProviderName, []string{"results"}, fixtureHandlers[ingestion.TheSportsDBTeamHistoryJobKey()], backfillRepository,
		))
	}
	if cfg.EnableRealAPIFootballCalls || cfg.EnableMockAPIFootballCalls {
		backfillProviders = append(backfillProviders,
			ingestion.NewJobBackfillProvider(apifootball.ProviderName, []string{"results"}, fixtureHandlers[ingestion.FixturesJobKey()], backfillRepository),
			ingestion.NewJobBackfillProvider(apifootball.ProviderName, []string{"rich_statistics"}, fixtureHandlers[ingestion.StatisticsJobKey()], backfillRepository),
		)
	}
	backfillProcessor := ingestion.NewProcessor(ingestion.ProcessorOptions{
		Jobs: jobRepo, Queue: ingestion.DataBackfillQueue, WorkerID: runtimeWorkerID,
		LeaseRefreshInterval: leaseRefresh,
		Handlers: map[string]ingestion.JobHandler{
			ingestion.BackfillTeamHistoryJobKey(): ingestion.NewBackfillTeamHistoryJob(ingestion.BackfillTeamHistoryJobOptions{
				Requests: backfillRepository, Providers: backfillProviders,
			}),
		},
	})
	oddsProcessor := ingestion.NewProcessor(ingestion.ProcessorOptions{
		Jobs:                 jobRepo,
		Queue:                "provider-odds",
		WorkerID:             runtimeWorkerID,
		LeaseRefreshInterval: leaseRefresh,
		Handlers:             providerRegistry.Handlers("provider-odds"),
	})
	pythonEngineConfig := predictionenginepython.LoadConfigFromEnv()
	if pythonEngineConfig.Env["DATABASE_URL"] == "" {
		pythonEngineConfig.Env["DATABASE_URL"] = cfg.DatabaseURL
	}
	pipelineLocker := ingestion.PostgresAdvisoryLocker{DB: db}
	generationHandler := ingestion.ExclusiveHandler{
		Handler: ingestion.FollowUpHandler{Handler: predictionenginepython.NewJobHandler(pythonEngineConfig, nil), Jobs: jobRepo, Build: func(source jobs.Job, now time.Time) []jobs.NewJob {
			return generationFollowUps(source, now, cfg.AccaInventoryV2Enabled)
		}},
		Locker: pipelineLocker, LockKey: "pipeline:predictions:generate", RetryAfter: time.Minute,
	}
	settlementHandler := ingestion.ExclusiveHandler{
		Handler: ingestion.FollowUpHandler{Handler: predictionenginepython.NewSettlementJobHandler(pythonEngineConfig, nil), Jobs: jobRepo, Build: predictions.SettlementFollowUps},
		Locker:  pipelineLocker, LockKey: "pipeline:predictions:settle", RetryAfter: time.Minute,
	}
	accuracyHandler := ingestion.ExclusiveHandler{
		Handler: predictionenginepython.NewAccuracySnapshotJobHandler(pythonEngineConfig, nil),
		Locker:  pipelineLocker, LockKey: "pipeline:predictions:accuracy", RetryAfter: time.Minute,
	}
	aggregatePruneHandler := ingestion.ExclusiveHandler{
		Handler: predictionenginepython.NewAggregatePruneJobHandler(pythonEngineConfig, nil),
		Locker:  pipelineLocker, LockKey: "pipeline:predictions:aggregate-prune", RetryAfter: time.Minute,
	}
	finaliseSnapshotsHandler := ingestion.ExclusiveHandler{
		Handler: predictionenginepython.NewFinaliseSnapshotsJobHandler(pythonEngineConfig, nil),
		Locker:  pipelineLocker, LockKey: "pipeline:predictions:finalise-snapshots", RetryAfter: time.Minute,
	}
	// Its own lock, not the in-play one.
	//
	// Sharing it would be wrong in the direction that matters: in-play
	// generation holds its lock for the whole live pass, and a tracker refresh
	// waiting behind it would restate pre-match picks no more often than the
	// model republishes. The two also write disjoint rows -- this one touches
	// only prediction_tracker_state -- so they have no reason to exclude each
	// other.
	refreshTrackersHandler := ingestion.ExclusiveHandler{
		Handler: predictionenginepython.NewRefreshTrackersJobHandler(pythonEngineConfig, nil),
		Locker:  pipelineLocker, LockKey: "pipeline:predictions:refresh-trackers", RetryAfter: time.Minute,
	}
	// In-play generation takes its own advisory lock, not the pre-match one.
	// Sharing `pipeline:predictions:generate` would make a 30-second in-play
	// tick wait behind a multi-thousand-fixture pre-match batch, which is the
	// one thing a live prediction cannot afford. The two paths write disjoint
	// rows -- different lifecycle stages, different markets -- so they have no
	// reason to exclude each other.
	inPlayHandler := ingestion.ExclusiveHandler{
		Handler: inplay.Job{
			Runner: predictionenginepython.NewInPlayJobHandler(pythonEngineConfig, nil),
			Jobs:   jobRepo,
			Logger: slog.Default(),
		},
		Locker: pipelineLocker, LockKey: "pipeline:predictions:in-play", RetryAfter: inplay.DefaultTick,
	}
	predictionProcessor := ingestion.NewProcessor(ingestion.ProcessorOptions{
		Jobs:                 jobRepo,
		Queue:                "predictions",
		WorkerID:             runtimeWorkerID,
		LeaseRefreshInterval: leaseRefresh,
		Handlers: map[string]ingestion.JobHandler{
			inplay.JobKey:                       inPlayHandler,
			predictions.GenerateUpcomingJobKey:  generationHandler,
			predictions.SettleFinishedJobKey:    settlementHandler,
			predictions.SnapshotAccuracyJobKey:  accuracyHandler,
			predictions.AggregatePruneJobKey:    aggregatePruneHandler,
			predictions.FinaliseSnapshotsJobKey: finaliseSnapshotsHandler,
			predictions.RefreshTrackersJobKey:   refreshTrackersHandler,
		},
	})
	recommendationService, err := recommendationjobs.NewService(recommendationjobs.NewPostgresRepository(db), recommendationjobs.Config{MaximumConcurrentUsers: cfg.QueueConcurrency}, time.Now)
	if err != nil {
		slog.Error("configure recommendation worker", "error", err)
		os.Exit(1)
	}
	// The crawlable URL set. Its own processor and queue because the sweep
	// reads only our own tables: it calls no provider, so it must not sit
	// behind the fixtures lock or compete for a slot live polling may want.
	//
	// Two bands, two keys. A fixture's indexability changes four times in
	// three weeks and the last change has no event behind it at all; a team's
	// does not change until the club does. Separate keys give them separate
	// schedules, idempotency and failure histories, so a stable sweep failing
	// overnight does not read as live fixtures having stopped being maintained.
	seoSweep := seourl.Sweep{
		Store:  seourl.NewRepository(db),
		Source: seourl.NewCatalogSource(db),
		Logger: slog.Default(),
		// The fixture history gate ships off, and that is not timidity: it is
		// expected to withhold thousands of URLs, so the population it would
		// exclude is logged from a real sweep first and read before anything is
		// de-indexed. Set SEO_FIXTURE_HISTORY_GATE=true to enforce.
		EnforceHistoryGate: strings.EqualFold(strings.TrimSpace(os.Getenv("SEO_FIXTURE_HISTORY_GATE")), "true"),
	}
	seoProcessor := ingestion.NewProcessor(ingestion.ProcessorOptions{
		Jobs: jobRepo, Queue: seourl.Queue, WorkerID: runtimeWorkerID,
		Handlers: map[string]ingestion.JobHandler{
			seourl.VolatileJobKey: seourl.Handler{Sweep: seoSweep, Volatile: true, Logger: slog.Default()},
			seourl.StableJobKey:   seourl.Handler{Sweep: seoSweep, Volatile: false, Logger: slog.Default()},
		},
	})
	personalizationProcessor := ingestion.NewProcessor(ingestion.ProcessorOptions{
		Jobs: jobRepo, Queue: "personalization", WorkerID: runtimeWorkerID,
		Handlers: map[string]ingestion.JobHandler{recommendationjobs.GenerateCandidatesJobKey: recommendationjobs.Job{Service: recommendationService}},
	})
	accaSource, accaSourceErr := acca.NewSQLCandidateSource(db, cfg.AccaDBSchema)
	if accaSourceErr != nil {
		slog.Error("configure ACCA source", "error", accaSourceErr)
		os.Exit(1)
	}
	accaRepo, accaRepoErr := acca.NewRepository(db, cfg.AccaDBSchema)
	if accaRepoErr != nil {
		slog.Error("configure ACCA repository", "error", accaRepoErr)
		os.Exit(1)
	}
	accaProcessor := ingestion.NewProcessor(ingestion.ProcessorOptions{Jobs: jobRepo, Queue: "acca", WorkerID: runtimeWorkerID, Handlers: map[string]ingestion.JobHandler{acca.GenerateInventoryJobKey: acca.Job{Source: accaSource, Repository: accaRepo}}})
	billingService, err := billingevents.NewService(billingevents.NewSQLStore(db), map[string]billingevents.Decoder{cfg.BillingProvider: billingevents.CanonicalJSONDecoder{}}, time.Now)
	if err != nil {
		slog.Error("configure billing reconciliation", "error", err)
		os.Exit(1)
	}
	reminderService, err := billingevents.NewReminderService(billingevents.NewReminderRepository(db), billingevents.ReminderOptions{}, time.Now)
	if err != nil {
		slog.Error("configure subscription reminder worker", "error", err)
		os.Exit(1)
	}
	billingProcessor := ingestion.NewProcessor(ingestion.ProcessorOptions{
		Jobs: jobRepo, Queue: billingevents.BillingQueue, WorkerID: runtimeWorkerID,
		Handlers: map[string]ingestion.JobHandler{
			billingevents.SubscriptionReminderJobKey: billingevents.ReminderJob{Service: reminderService},
		},
	})
	maintenanceProcessor := ingestion.NewProcessor(ingestion.ProcessorOptions{
		Jobs: jobRepo, Queue: maintenance.Queue, WorkerID: runtimeWorkerID,
		Handlers: map[string]ingestion.JobHandler{
			snapshots.PartitionJobKey: snapshots.PartitionJob{DB: db, Logger: slog.Default(), Metrics: workerMetrics},
			maintenance.PruneJobKey: maintenance.Job{
				Store: maintenance.NewRepository(db),
				Policy: maintenance.Payload{
					EnableDestructiveRetention: cfg.EnableDestructiveRetention,
					DryRun:                     cfg.RetentionDryRun,
				},
			},
			maintenance.RawPredictionPruneJobKey: maintenance.RawPredictionJob{
				Store: maintenance.NewRepository(db),
				Policy: maintenance.Payload{
					EnableDestructiveRetention: cfg.EnableRawPredictionDrain,
					BatchSize:                  cfg.RawPredictionDrainBatchSize,
					MaxBatches:                 cfg.RawPredictionDrainMaxBatches,
				},
			},
			// Bounds matches still claiming to be in play hours after kickoff.
			// Past the hard bound it needs no provider at all, so it keeps
			// working through an outage -- which is exactly when matches get
			// stranded in the first place.
			matchstate.ReaperJobKey: matchstate.Job{
				Store:  matchstate.NewRepository(db),
				Logger: slog.Default(),
				Reconciler: matchstate.DateReconciler{
					Jobs:   jobRepo,
					JobKey: ingestion.FixturesJobKey(),
					Queue:  "provider-fixtures",
				},
				// unknown is a moment, not a destination. A match the sweep has
				// just given up on has almost always finished, so ask for its
				// result now rather than leaving it to the hourly pass.
				Resolver: matchstate.ResultReconciler{
					Jobs:   jobRepo,
					JobKey: ingestion.ResultsReconcileJobKey(),
					Queue:  "provider-fixtures",
				},
			},
		},
	})
	notificationRepository := notifications.NewRepository(db).WithPayloadSecret(cfg.AppSecret)
	notificationGuards := notifications.NewSQLGuards(db, map[string]string{
		"favorite_alert": "notifications.realtime", "priority_tip": "notifications.realtime",
	})
	inAppWorker, err := notifications.NewWorker(notifications.WorkerOptions{Store: notificationRepository, Transport: notifications.InAppTransport{DB: db}, Channel: "in_app", BatchSize: cfg.NotificationBatchSize, MaxConcurrent: cfg.NotificationConcurrency, AccountGuard: notificationGuards, PreferenceGuard: notificationGuards, EntitlementGuard: notificationGuards, AllowedCategories: map[string]bool{}})
	if err != nil {
		slog.Error("configure in-app notification worker", "error", err)
		os.Exit(1)
	}
	providers := map[string]notifications.Transport{"log": notifications.LogTransport{Logger: slog.Default()}}
	if cfg.MailtrapHost != "" && cfg.MailtrapUsername != "" && cfg.MailtrapPassword != "" {
		providers["mailtrap"] = notifications.SMTPTransport{Host: cfg.MailtrapHost, Port: cfg.MailtrapPort, From: cfg.EmailFromAddress, FromName: cfg.EmailFromName, Username: cfg.MailtrapUsername, Password: cfg.MailtrapPassword, Encryption: cfg.MailtrapEncryption}
		providers["smtp"] = providers["mailtrap"]
	}
	if cfg.ResendAPIKey != "" {
		from := cfg.ResendFromAddress
		if from == "" {
			from = cfg.EmailFromAddress
		}
		providers["resend"] = notifications.ResendTransport{APIKey: cfg.ResendAPIKey, From: from, FromName: cfg.EmailFromName}
	}
	if cfg.CloudflareAccountID != "" && cfg.CloudflareEmailAPIToken != "" {
		providers["cloudflare"] = notifications.CloudflareTransport{AccountID: cfg.CloudflareAccountID, APIToken: cfg.CloudflareEmailAPIToken, From: cfg.EmailFromNoreply, FromName: cfg.EmailFromName, SupportsCustomHeaders: cfg.CloudflareEmailSupportsHeaders}
	}
	// Email delivery must survive one provider being down. The resolver walks
	// the configured chain and only hands a message on when the previous
	// provider demonstrably did not accept it, so an outage costs latency
	// rather than a lost password reset -- and an ambiguous failure never
	// produces a duplicate at a second provider.
	emailTransport := notifications.Transport(&notifications.RuntimeTransport{
		Lookup:          notificationRepository,
		Providers:       providers,
		DefaultProvider: cfg.EmailProvider,
		Fallbacks:       cfg.EmailFallbackProviders,
		Breaker:         &notifications.ProviderBreaker{},
		Logger:          slog.Default(),
	})
	// Verification and password reset are explicitly requested account messages,
	// not product notifications. They must pass the account lifecycle guard but must not be
	// blocked by dormant product preferences or entitlements.
	emailWorker, err := notifications.NewWorker(notifications.WorkerOptions{Store: notificationRepository, Transport: emailTransport, Channel: "email", BatchSize: cfg.NotificationBatchSize, MaxConcurrent: cfg.NotificationConcurrency, AccountGuard: notificationGuards, AllowedCategories: map[string]bool{"email_verification": true, "password_reset": true}})
	if err != nil {
		slog.Error("configure email notification worker", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	metricsServer := startMetricsServer(cfg.MetricsAddress, workerMetrics)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = metricsServer.Shutdown(shutdownCtx)
	}()
	go runMetricsRefresh(ctx, db, workerMetrics, cfg.PollInterval, cfg.ProviderDailyLimit)
	// Close requests when their 24-hour window elapses, even if a future
	// fixture's coordinator job has failed. Fresh prediction demand may reopen
	// the request before kickoff. Keep the sweep bounded so a large backlog
	// cannot monopolize the worker's database pool.
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			sweepCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			closed, err := backfillRepository.ExpireElapsedRequests(sweepCtx, 1000)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Warn("expire stale backfill requests", "error", err)
			} else if closed > 0 {
				slog.Info("expired stale backfill requests", "count", closed)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	planner, err := scheduler.NewPlanner(scheduler.PlanOptions{
		ProviderMode:              cfg.FootballProvider,
		EnableRealAPIFootball:     cfg.EnableRealAPIFootballCalls,
		EnableMockAPIFootball:     cfg.EnableMockAPIFootballCalls,
		EnableRealFootballDataOrg: cfg.EnableRealFootballDataOrgCalls,
		EnableRealTheSportsDB:     cfg.EnableRealTheSportsDBCalls,
		ProviderInterval:          cfg.ProviderScheduleInterval,
		PredictionInterval:        cfg.PredictionScheduleInterval,
		RecommendationInterval:    cfg.RecommendationScheduleInterval,
		OddsDelay:                 2 * cfg.PollInterval,
		PublicResultDetailHours:   cfg.PublicResultDetailHours,
		RawResultRetentionDays:    cfg.RawResultRetentionDays,
		InPlayStatisticsPoll:      cfg.InPlayStatisticsPollEnabled,
	})
	if err != nil {
		slog.Error("configure recurring scheduler", "error", err)
		os.Exit(1)
	}
	recurringScheduler := scheduler.New(planner, jobRepo, slog.Default())
	if cfg.EnableRealAPIFootballCalls {
		quotaCfg := quota.DefaultPlannerConfig()
		quotaCfg.DailyLimit = cfg.ProviderDailyLimit
		quotaCfg.DailyReserve = cfg.ProviderDailyReserve
		quotaCfg.MinuteLimit = cfg.ProviderMinuteLimit
		quotaCfg.LivePollSeconds = cfg.QuotaLivePollSeconds
		// Without this the forecast omits the recurring schedule's own cost
		// and reports it as spendable surplus.
		quotaCfg.FixedDailyOverhead = cfg.QuotaFixedDailyOverhead
		// The batched statistics poll competes for the same daily allowance,
		// so the forecast has to know whether it is running.
		quotaCfg.InPlayStatisticsPollEnabled = cfg.InPlayStatisticsPollEnabled
		quotaCfg.Remaining = func() int { return providerClient.DailyBudgetSnapshot().Remaining }
		// PlanLimit, not Limit. Both exist on the snapshot and the difference
		// decides whether the plan-size cadence clamps below stay on.
		//
		// `Limit` is whatever the provider's last header claimed. The planner
		// uses this value to decide that we are on the small plan and must
		// hold the live poll to 60s and the statistics poll to 5 minutes -- so
		// a header reading 75,000, during a plan change or simply in error,
		// switches every one of those clamps off. The budget governor would go
		// on refusing at 7,500 regardless, because that ceiling is config-owned
		// and a header cannot raise it, so the two would disagree: cadences
		// relax, the day's spend doubles, and the ceiling cuts in mid-afternoon
		// with live scores as the thing that stops.
		//
		// `PlanLimit` is the effective ceiling -- the configured package, or
		// less if a header says the package is smaller. Pacing against the
		// number we are actually refused at is the only way the two agree.
		quotaCfg.Limit = func() int { return providerClient.DailyBudgetSnapshot().PlanLimit }
		quotaPlanner := quota.NewPlanner(quotaCfg, quota.NewSQLDemandSource(db))
		recurringScheduler = recurringScheduler.WithQuotaPlanning(
			func(ctx context.Context, _ time.Time) (quota.Decision, error) {
				decision := quotaPlanner.Plan(ctx)
				publishQuotaDecision(workerMetrics, decision)
				return decision, nil
			},
			scheduler.QuotaOptions{
				DB:                            db,
				BackfillMinSafeBudget:         cfg.BackfillMinSafeBudget,
				MaxBackfillPerCycle:           cfg.BackfillMaxPerCycle,
				StatisticsBackfillMaxPerCycle: cfg.StatisticsBackfillMaxPerCycle,
				BackfillDays:                  cfg.BackfillDays,
				BackfillMaxRequestsPerDay:     cfg.BackfillMaxRequestsPerDay,
				CycleInterval:                 cfg.PollInterval,
			},
		)
		// Persist the client's minute-throttle state to provider_rate_limits so
		// it survives restarts and is visible to operators/admin.
		go func() {
			ticker := time.NewTicker(cfg.PollInterval)
			defer ticker.Stop()
			rateLimits := ingestion.NewProviderRateLimitRepository(db)
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					until := providerClient.ThrottleUntil()
					throttleCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					err := rateLimits.UpdateFromHeaders(throttleCtx, ingestion.ProviderRateLimitUpdate{
						Provider:      apifootball.ProviderName,
						ThrottleUntil: &until,
						UpdatedAt:     time.Now().UTC(),
					})
					cancel()
					if err != nil {
						slog.Warn("persist provider throttle state", "error", err)
					}
				}
			}
		}()
	}
	if err := scheduler.Run(ctx, scheduler.RuntimeOptions{
		PollInterval: cfg.PollInterval,
		Heartbeat: func(now time.Time) error {
			return scheduler.WriteHeartbeat(cfg.HeartbeatPath, now)
		},
		Schedule: recurringScheduler.Schedule,
		// Each queue drains on its own loop, continuously.
		//
		// The previous shape ran every queue inside one barrier per poll
		// interval and wrote the heartbeat only after all of them returned. One
		// fifteen-minute ACCA inventory build therefore stopped provider
		// ingestion dead and reported the worker unhealthy while it was working.
		// Throughput was also capped at roughly one job per queue per interval,
		// which is how a backlog of 2,500 accumulates.
		//
		// Concurrency is per queue, weighted by how much work each actually
		// carries, and the total is capped by MaxConcurrentJobs below so these
		// loops cannot starve the connection pool the handlers need.
		MaxConcurrentJobs: cfg.DBMaxOpenConns - 2,
		Workers: []scheduler.QueueWorker{
			{Queue: "provider", Concurrency: 2, ProcessOnce: providerProcessor.ProcessOnce},
			// Reserved, because this queue carries live match polling and it is
			// the one that stopped. On 2026-09-21 a restart left 4,076 backfill
			// jobs queued; data-backfill took thirteen of the shared slots within
			// seconds and live polling got none for forty minutes, with every
			// queue healthy and the heartbeat green throughout.
			//
			// A third of the queue's own concurrency is enough: live polling is
			// one bulk call every thirty seconds, so the reservation is almost
			// always idle and lent to the shared pool. It only has to exist at the
			// moment a backlog tries to take everything.
			{Queue: "provider-fixtures", Concurrency: cfg.QueueConcurrency, Reserved: reservedSlots(cfg.QueueConcurrency), ProcessOnce: fixturesProcessor.ProcessOnce},
			{Queue: "data-backfill", Concurrency: cfg.QueueConcurrency, ProcessOnce: backfillProcessor.ProcessOnce},
			{Queue: "provider-odds", Concurrency: 2, ProcessOnce: oddsProcessor.ProcessOnce},
			{Queue: "predictions", Concurrency: 2, ProcessOnce: predictionProcessor.ProcessOnce},
			{Queue: "personalization", Concurrency: 1, ProcessOnce: personalizationProcessor.ProcessOnce},
			// Its own queue and a single loop. The sweep reads our own tables
			// and calls no provider, so it neither needs nor deserves a slot
			// that live polling might want.
			{Queue: seourl.Queue, Concurrency: 1, ProcessOnce: seoProcessor.ProcessOnce},
			// Single slot on purpose: the inventory build is long and its work
			// is global, so a second one would duplicate rather than divide it.
			// It no longer blocks anything else, which was the actual problem.
			{Queue: "acca", Concurrency: 1, ProcessOnce: func(ctx context.Context) (bool, error) {
				if !cfg.AccaInventoryV2Enabled {
					return false, nil
				}
				return accaProcessor.ProcessOnce(ctx)
			}},
			{Queue: "billing", Concurrency: 1, ProcessOnce: billingProcessor.ProcessOnce},
			{Queue: "maintenance", Concurrency: 1, ProcessOnce: maintenanceProcessor.ProcessOnce},
			// The notification workers report how many they delivered and the
			// billing reconciler what it did with one event. Both are adapted to
			// "did I do any work", which is all the drain loop needs to choose
			// between claiming again and backing off.
			{Queue: "notifications-in-app", Concurrency: 2, ProcessOnce: func(ctx context.Context) (bool, error) {
				delivered, err := inAppWorker.RunOnce(ctx)
				return delivered > 0, err
			}},
			{Queue: "notifications-email", Concurrency: 2, ProcessOnce: func(ctx context.Context) (bool, error) {
				delivered, err := emailWorker.RunOnce(ctx)
				return delivered > 0, err
			}},
			{Queue: "billing-reconciliation", Concurrency: 1, ProcessOnce: func(ctx context.Context) (bool, error) {
				result, err := billingService.ProcessNext(ctx)
				return result.Processed, err
			}},
		},
	}); err != nil {
		slog.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}

func generationFollowUps(source jobs.Job, now time.Time, accaEnabled bool) []jobs.NewJob {
	followUps := predictions.GenerationFollowUps(source, now)
	if accaEnabled {
		followUps = append(followUps, acca.GenerationFollowUp(source, now))
	}
	return followUps
}

func runBillingReconciliation(ctx context.Context, maxConcurrent int, service billingevents.Service) error {
	return runWorkerBurst(ctx, maxConcurrent, func(ctx context.Context) error {
		_, err := service.ProcessNext(ctx)
		return err
	})
}

// runWorkerBurst lets a backlog-heavy queue use the configured concurrency in
// every scheduler cycle. Provider clients still enforce their own independent
// minute/daily governors, so concurrency improves throughput without bursting
// past an upstream allowance.
func runWorkerBurst(ctx context.Context, maxConcurrent int, task func(context.Context) error) error {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	tasks := make([]func(context.Context) error, maxConcurrent)
	for index := range tasks {
		tasks[index] = task
	}
	return runWorkerTasks(ctx, maxConcurrent, tasks...)
}

func runWorkerTasks(ctx context.Context, maxConcurrent int, tasks ...func(context.Context) error) error {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	semaphore := make(chan struct{}, maxConcurrent)
	taskErrors := make([]error, len(tasks))
	var workers sync.WaitGroup
	for index, task := range tasks {
		if task == nil {
			continue
		}
		workers.Add(1)
		go func(index int, task func(context.Context) error) {
			defer workers.Done()
			if ctx.Err() != nil {
				return
			}
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			err := task(ctx)
			if err != nil && !(ctx.Err() != nil && errors.Is(err, ctx.Err())) {
				taskErrors[index] = err
			}
		}(index, task)
	}
	workers.Wait()
	return errors.Join(taskErrors...)
}

func configureDatabasePool(db *sql.DB, cfg config.Config) {
	db.SetMaxOpenConns(cfg.DBMaxOpenConns)
	db.SetMaxIdleConns(cfg.DBMaxIdleConns)
}

func workerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker-local"
	}
	return host + "-" + strconv.Itoa(os.Getpid())
}

// loadDailyProviderSpend reconstructs today's provider spend for a worker
// that has just started and has no in-memory budget yet.
//
// It prefers `provider_rate_limits`, which is written from the provider's own
// `x-ratelimit-requests-remaining` header and is therefore the only
// authoritative account of what we have spent. The request log is a fallback.
//
// The order matters, and getting it wrong took production down. Until
// 2026-09-21 this read only the log, and the log over-counts: it recorded
// 68,344 billable requests for a day the provider itself put at 51,509, a gap
// of nearly 17,000 across 2.7 million rows. A restart therefore seeded the
// budget with 6,656 remaining instead of 23,491, the pacing reserve refused
// everything below about 22,000, and the governor deadlocked -- it could not
// spend, and could not learn it was wrong without spending. Every provider
// call stopped, live match polling included.
//
// A stale row is worse than no row, so the authoritative value is only used
// when it belongs to the current provider day.
func loadDailyProviderSpend(ctx context.Context, db *sql.DB, providerName string, now time.Time) (int, error) {
	if db == nil {
		return 0, errors.New("provider quota DB is nil")
	}
	dayStart := now.UTC().Truncate(24 * time.Hour)

	var (
		limit     sql.NullInt64
		remaining sql.NullInt64
		updatedAt sql.NullTime
	)
	err := db.QueryRowContext(ctx, `
SELECT daily_limit, daily_remaining, updated_at
FROM provider_rate_limits
WHERE provider = $1`, providerName).Scan(&limit, &remaining, &updatedAt)
	// The dayStart comparison is the whole safety property, not a nicety.
	//
	// API-Football's quota resets at 00:00 UTC, which we know from the clock
	// without asking anyone. So a reading taken before today's reset describes
	// a day that is over, and must never gate today's calls: treating it as
	// current is self-sealing, because the refusals it causes prevent the
	// request that would replace it. That is exactly how ingestion deadlocked
	// on 2026-09-22 -- a daily_remaining of 33 carried over a reset that had
	// already restored the full 75,000.
	//
	// Falling through to the request-log sum is safe in a way trusting a stale
	// row is not: the log only ever counts calls we really made today, so the
	// worst case is that we start the day believing we have spent slightly
	// more than we have, and the first real response corrects it.
	if err == nil && limit.Valid && remaining.Valid && updatedAt.Valid &&
		limit.Int64 > 0 && remaining.Int64 >= 0 && remaining.Int64 <= limit.Int64 &&
		!updatedAt.Time.UTC().Before(dayStart) {
		return int(limit.Int64 - remaining.Int64), nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("could not read authoritative provider quota, falling back to the request log",
			"provider", providerName, "error", err)
	}

	var spent int
	err = db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(quota_cost), 0)
FROM provider_request_logs
WHERE provider = $1
  AND requested_at >= $2
  AND requested_at < $3`, providerName, dayStart, dayStart.Add(24*time.Hour)).Scan(&spent)
	return spent, err
}

// reservedSlots is the share of a queue's concurrency held back for it.
//
// A third, with a floor of one. The floor matters more than the fraction: a
// reservation that rounds to zero is a queue the configuration claims is
// protected and is not, and nobody goes looking at a setting that says it is
// already handled.
func reservedSlots(concurrency int) int {
	if concurrency < 1 {
		return 1
	}
	if reserved := concurrency / 3; reserved > 0 {
		return reserved
	}
	return 1
}
