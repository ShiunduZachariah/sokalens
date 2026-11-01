package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultEnvironment             = "local"
	defaultMockProviderURL         = "http://localhost:8090"
	defaultAPIFootballURL          = "https://v3.football.api-sports.io"
	defaultFootballDataURL         = "https://api.football-data.org/v4"
	defaultTheSportsDBURL          = "https://www.thesportsdb.com/api/v1/json/123/"
	defaultTheSportsDBKey          = "123"
	defaultTheSportsDBMaxPerCycle  = 20
	defaultFootballProvider        = "api-football"
	defaultDatabaseURL             = "postgres://sports:sports@localhost:5433/sports_prediction?sslmode=disable"
	defaultPollInterval            = 30 * time.Second
	defaultProviderSchedule        = 4 * time.Hour
	defaultPredictionSchedule      = time.Minute
	defaultRecommendationSchedule  = 15 * time.Minute
	defaultPublicResultDetailHours = 24
	defaultRawResultRetentionDays  = 2
	defaultHeartbeatMaxAge         = 10 * time.Minute
	defaultQueueConcurrency        = 4
	// Sized for the per-queue drain loops: every concurrent handler needs a
	// connection, and the runtime caps handlers at this minus two.
	defaultDBMaxOpenConns = 24
	defaultDBMaxIdleConns = 12
	// The API-Football package we pay for: 7,500 requests a provider day, and
	// 300 a minute.
	//
	// Both are provider-confirmed. The daily figure is a hard ceiling, not a
	// target: quota.DailyBudget refuses past it on its own count of requests
	// sent, so a provider header reporting a larger package cannot raise it --
	// see planLimit there. Changing this number is how you change the plan;
	// nothing else grants more.
	defaultDailyLimit = 7500
	// 900, 12% of the package.
	//
	// Not 4,000. That was sized for 75,000, where it is 5% of a day, and
	// carried to 7,500 unchanged it reserves 53% -- a governor that spends
	// half the package on being careful. Nor 1,500, the original: that was 20%
	// of this plan and reasonable before the pacer existed to keep the day's
	// spend level without a large static block. What the reserve has to buy is
	// live polling from here to midnight, and at a 60-second cadence that is
	// bounded by the hours remaining.
	defaultDailyReserve = 900
	// 300, provider-confirmed, down from the 450 assumed for the Ultra plan.
	defaultMinuteLimit      = 300
	defaultMinuteReserve    = 15
	defaultMetricsAddress   = "127.0.0.1:9091"
	defaultRequestDedupeTTL = 2 * time.Minute
	defaultJobMaxAttempts   = 3
	defaultJobLeaseDuration = 15 * time.Minute
)

type Config struct {
	Environment                string
	AppSecret                  string
	DatabaseURL                string
	MockProviderURL            string
	APIFootballBaseURL         string
	APIFootballKey             string
	EnableRealAPIFootballCalls bool
	// InPlayStatisticsPollEnabled gates the batched fixtures?ids= statistics
	// poll. Default false: the embedded statistics shape is an assumption this
	// repository has never observed on a real response, and a wrong assumption
	// decodes to zero teams without erroring -- a poll that spends quota,
	// stores nothing, and makes every league look as though it has no corner
	// or card data. Confirm the shape with
	// services/worker/cmd/capturefixtureids before turning it on, and account
	// for its spend in the quota planner at the same time.
	InPlayStatisticsPollEnabled    bool
	EnableMockAPIFootballCalls     bool
	FootballDataOrgBaseURL         string
	FootballDataOrgAPIKey          string
	EnableRealFootballDataOrgCalls bool
	TheSportsDBBaseURL             string
	TheSportsDBAPIKey              string
	EnableRealTheSportsDBCalls     bool
	TheSportsDBMaxPerCycle         int
	FootballProvider               string
	PollInterval                   time.Duration
	ProviderScheduleInterval       time.Duration
	PredictionScheduleInterval     time.Duration
	HeartbeatPath                  string
	HeartbeatMaxAge                time.Duration
	QueueConcurrency               int
	DBMaxOpenConns                 int
	DBMaxIdleConns                 int
	ProviderDailyLimit             int
	ProviderDailyReserve           int
	ProviderMinuteLimit            int
	ProviderMinuteReserve          int
	BackfillMinSafeBudget          int
	BackfillMaxPerCycle            int
	StatisticsBackfillMaxPerCycle  int
	BackfillDays                   int
	BackfillMaxRequestsPerDay      int
	QuotaFixedDailyOverhead        int
	QuotaLivePollSeconds           int
	MetricsAddress                 string
	RequestDedupeTTL               time.Duration
	JobMaxAttempts                 int
	JobLeaseDuration               time.Duration
	AccaInventoryV2Enabled         bool
	AccaDBSchema                   string
	RecommendationScheduleInterval time.Duration
	PublicResultDetailHours        int
	RawResultRetentionDays         int
	NotificationBatchSize          int
	NotificationConcurrency        int
	EmailProvider                  string
	// EmailFallbackProviders is the ordered chain tried when the active
	// provider is demonstrably unable to accept a message. Empty means the
	// active provider is the only one, which is the historical behaviour.
	EmailFallbackProviders  []string
	EmailFromAddress        string
	EmailFromName           string
	EmailTestTo             string
	MailtrapHost            string
	MailtrapPort            int
	MailtrapUsername        string
	MailtrapPassword        string
	MailtrapEncryption      string
	ResendAPIKey            string
	ResendFromAddress       string
	CloudflareAccountID     string
	CloudflareEmailAPIToken string
	// CloudflareEmailSupportsHeaders is opt-in and must only be set once the
	// account's Email Sending API has been observed accepting a "headers"
	// object. Until then the resolver routes bulk mail, which must carry
	// List-Unsubscribe, to a provider that can.
	CloudflareEmailSupportsHeaders bool
	EmailFromNotifications         string
	EmailFromSupport               string
	EmailFromNoreply               string
	SMTPAddress                    string
	SMTPFrom                       string
	SMTPUsername                   string
	SMTPPassword                   string
	BillingProvider                string
	EnableDestructiveRetention     bool
	// EnableRawPredictionDrain independently gates the bounded, indexed
	// engine_predictions cleanup. Turn it on only after QA validation.
	EnableRawPredictionDrain     bool
	RawPredictionDrainBatchSize  int
	RawPredictionDrainMaxBatches int
	// EnableFixtureSnapshots turns on the append-only live match history
	// (in-play architecture 5). Off by default: it adds a write per in-play
	// fixture per poll, so switching it on is a deliberate act rather than
	// something that arrives with a deploy. Partition maintenance runs
	// regardless -- a partition that exists costs nothing, and one that does
	// not is an insert failure the moment the flag is turned on.
	EnableFixtureSnapshots bool
	// EnableFixtureEvents turns on the live event writer that fills
	// fixture_events (in-play plan §4). Off by default and separate from
	// EnableFixtureSnapshots so the two append-only writers can be switched on
	// one at a time: the events writer can then ship to production dark, be
	// enabled for a single live window, and be switched off again without
	// touching the snapshot history.
	//
	// It costs no additional provider request -- the events are already in the
	// fixtures?live=all response the live job polls -- so the flag guards the
	// database write, not the fetch. Partition maintenance runs regardless
	// (see snapshots.PartitionedTables): a partition that exists costs an
	// empty table, and one that does not is an insert failure the moment the
	// flag is turned on.
	EnableFixtureEvents bool
	RetentionDryRun     bool
}

func Load(overrides map[string]string) (Config, error) {
	env := mergedEnv(overrides)

	environment := value(env, "APP_ENV", defaultEnvironment)
	pollInterval, err := parseDuration(value(env, "WORKER_POLL_INTERVAL", defaultPollInterval.String()), "WORKER_POLL_INTERVAL")
	if err != nil {
		return Config{}, err
	}
	providerScheduleInterval, err := parseDuration(value(env, "PROVIDER_SCHEDULE_INTERVAL", defaultProviderSchedule.String()), "PROVIDER_SCHEDULE_INTERVAL")
	if err != nil {
		return Config{}, err
	}
	predictionScheduleInterval, err := parseDuration(value(env, "PREDICTION_SCHEDULE_INTERVAL", defaultPredictionSchedule.String()), "PREDICTION_SCHEDULE_INTERVAL")
	if err != nil {
		return Config{}, err
	}
	recommendationScheduleInterval, err := parseDuration(value(env, "RECOMMENDATION_SCHEDULE_INTERVAL", defaultRecommendationSchedule.String()), "RECOMMENDATION_SCHEDULE_INTERVAL")
	if err != nil {
		return Config{}, err
	}
	heartbeatMaxAge, err := parseDuration(value(env, "WORKER_HEARTBEAT_MAX_AGE", defaultHeartbeatMaxAge.String()), "WORKER_HEARTBEAT_MAX_AGE")
	if err != nil {
		return Config{}, err
	}
	queueConcurrency, err := parseBoundedInt(value(env, "WORKER_QUEUE_CONCURRENCY", strconv.Itoa(defaultQueueConcurrency)), "WORKER_QUEUE_CONCURRENCY", 1, 16)
	if err != nil {
		return Config{}, err
	}
	notificationBatchSize, err := parseBoundedInt(value(env, "NOTIFICATION_BATCH_SIZE", "50"), "NOTIFICATION_BATCH_SIZE", 1, 500)
	if err != nil {
		return Config{}, err
	}
	notificationConcurrency, err := parseBoundedInt(value(env, "NOTIFICATION_CONCURRENCY", "4"), "NOTIFICATION_CONCURRENCY", 1, 32)
	if err != nil {
		return Config{}, err
	}
	publicResultDetailHours, err := parseBoundedInt(value(env, "PUBLIC_RESULT_DETAIL_HOURS", strconv.Itoa(defaultPublicResultDetailHours)), "PUBLIC_RESULT_DETAIL_HOURS", 1, 168)
	if err != nil {
		return Config{}, err
	}
	rawResultRetentionDays, err := parseBoundedInt(value(env, "PREDICTION_RESULT_RAW_RETENTION_DAYS", strconv.Itoa(defaultRawResultRetentionDays)), "PREDICTION_RESULT_RAW_RETENTION_DAYS", 1, 30)
	if err != nil {
		return Config{}, err
	}
	rawPredictionDrainBatchSize, err := parseBoundedInt(value(env, "RAW_PREDICTION_DRAIN_BATCH_SIZE", "1000"), "RAW_PREDICTION_DRAIN_BATCH_SIZE", 1, 5000)
	if err != nil {
		return Config{}, err
	}
	rawPredictionDrainMaxBatches, err := parseBoundedInt(value(env, "RAW_PREDICTION_DRAIN_MAX_BATCHES", "10"), "RAW_PREDICTION_DRAIN_MAX_BATCHES", 1, 100)
	if err != nil {
		return Config{}, err
	}
	mailtrapPort, err := parseBoundedInt(value(env, "MAILTRAP_PORT", "587"), "MAILTRAP_PORT", 1, 65535)
	if err != nil {
		return Config{}, err
	}
	dbMaxOpenConns, err := parseBoundedInt(value(env, "WORKER_DB_MAX_OPEN_CONNS", strconv.Itoa(defaultDBMaxOpenConns)), "WORKER_DB_MAX_OPEN_CONNS", 1, 64)
	if err != nil {
		return Config{}, err
	}
	dbMaxIdleConns, err := parseBoundedInt(value(env, "WORKER_DB_MAX_IDLE_CONNS", strconv.Itoa(defaultDBMaxIdleConns)), "WORKER_DB_MAX_IDLE_CONNS", 0, 64)
	if err != nil {
		return Config{}, err
	}
	if dbMaxIdleConns > dbMaxOpenConns {
		return Config{}, errors.New("WORKER_DB_MAX_IDLE_CONNS must not exceed WORKER_DB_MAX_OPEN_CONNS")
	}
	dailyLimit, err := parsePositiveInt(value(env, "PROVIDER_DAILY_LIMIT", fmt.Sprintf("%d", defaultDailyLimit)), "PROVIDER_DAILY_LIMIT")
	if err != nil {
		return Config{}, err
	}
	dailyReserve, err := parsePositiveInt(value(env, "PROVIDER_DAILY_RESERVE", fmt.Sprintf("%d", defaultDailyReserve)), "PROVIDER_DAILY_RESERVE")
	if err != nil {
		return Config{}, err
	}
	minuteLimit, err := parsePositiveInt(value(env, "PROVIDER_MINUTE_LIMIT", fmt.Sprintf("%d", defaultMinuteLimit)), "PROVIDER_MINUTE_LIMIT")
	if err != nil {
		return Config{}, err
	}
	minuteReserve, err := parsePositiveInt(value(env, "PROVIDER_MINUTE_RESERVE", fmt.Sprintf("%d", defaultMinuteReserve)), "PROVIDER_MINUTE_RESERVE")
	if err != nil {
		return Config{}, err
	}
	backfillMinSafeBudget, err := parseBoundedInt(value(env, "QUOTA_BACKFILL_MIN_SAFE_BUDGET", "200"), "QUOTA_BACKFILL_MIN_SAFE_BUDGET", 0, 100000)
	if err != nil {
		return Config{}, err
	}
	backfillMaxPerCycle, err := parseBoundedInt(value(env, "QUOTA_BACKFILL_MAX_PER_CYCLE", "25"), "QUOTA_BACKFILL_MAX_PER_CYCLE", 0, 200)
	if err != nil {
		return Config{}, err
	}
	statisticsBackfillMaxPerCycle, err := parseBoundedInt(value(env, "QUOTA_STATISTICS_BACKFILL_MAX_PER_CYCLE", "0"), "QUOTA_STATISTICS_BACKFILL_MAX_PER_CYCLE", 0, 25)
	if err != nil {
		return Config{}, err
	}
	backfillDays, err := parseBoundedInt(value(env, "QUOTA_BACKFILL_DAYS", "30"), "QUOTA_BACKFILL_DAYS", 1, 730)
	if err != nil {
		return Config{}, err
	}
	// 1,200 requests a day for discretionary backfill: 16% of the package,
	// after the recurring schedule and live polling are paid for. Zero turns
	// the ceiling off, which is an operator choice and not a default.
	backfillMaxRequestsPerDay, err := parseBoundedInt(value(env, "QUOTA_BACKFILL_MAX_REQUESTS_PER_DAY", "1200"), "QUOTA_BACKFILL_MAX_REQUESTS_PER_DAY", 0, 200000)
	if err != nil {
		return Config{}, err
	}
	// What the recurring schedule costs per day regardless of the fixture
	// calendar. See quota.PlannerConfig.FixedDailyOverhead: the forecast used
	// to omit it entirely and hand it to the pacer as surplus.
	quotaFixedDailyOverhead, err := parseBoundedInt(value(env, "QUOTA_FIXED_DAILY_OVERHEAD", "900"), "QUOTA_FIXED_DAILY_OVERHEAD", 0, 50000)
	if err != nil {
		return Config{}, err
	}
	// Sixty seconds. A full day at 30s is 2,880 requests -- 38% of a 7,500
	// package spent on polling alone.
	quotaLivePollSeconds, err := parseBoundedInt(value(env, "QUOTA_LIVE_POLL_SECONDS", "60"), "QUOTA_LIVE_POLL_SECONDS", 10, 3600)
	if err != nil {
		return Config{}, err
	}
	theSportsDBMaxPerCycle, err := parseBoundedInt(value(env, "THE_SPORTS_DB_MAX_PER_CYCLE", strconv.Itoa(defaultTheSportsDBMaxPerCycle)), "THE_SPORTS_DB_MAX_PER_CYCLE", 1, 100)
	if err != nil {
		return Config{}, err
	}
	requestDedupeTTL, err := parseDuration(value(env, "WORKER_REQUEST_DEDUPE_TTL", defaultRequestDedupeTTL.String()), "WORKER_REQUEST_DEDUPE_TTL")
	if err != nil {
		return Config{}, err
	}
	jobMaxAttempts, err := parseBoundedInt(value(env, "WORKER_JOB_MAX_ATTEMPTS", strconv.Itoa(defaultJobMaxAttempts)), "WORKER_JOB_MAX_ATTEMPTS", 1, 10)
	if err != nil {
		return Config{}, err
	}
	jobLeaseDuration, err := parseDuration(value(env, "WORKER_JOB_LEASE_DURATION", defaultJobLeaseDuration.String()), "WORKER_JOB_LEASE_DURATION")
	if err != nil {
		return Config{}, err
	}
	if jobLeaseDuration < 5*time.Minute || jobLeaseDuration > 2*time.Hour {
		return Config{}, errors.New("WORKER_JOB_LEASE_DURATION must be between 5m and 2h")
	}
	mockAPIFootballDefault := "false"
	if environment == defaultEnvironment {
		mockAPIFootballDefault = "true"
	}
	cfg := Config{
		Environment:                    environment,
		AppSecret:                      env["APP_SECRET"],
		DatabaseURL:                    value(env, "DATABASE_URL", defaultDatabaseURL),
		MockProviderURL:                value(env, "MOCK_API_FOOTBALL_URL", defaultMockProviderURL),
		APIFootballBaseURL:             value(env, "API_FOOTBALL_BASE_URL", defaultAPIFootballURL),
		APIFootballKey:                 env["API_FOOTBALL_KEY"],
		EnableRealAPIFootballCalls:     parseBool(value(env, "ENABLE_REAL_API_FOOTBALL_CALLS", "false")),
		InPlayStatisticsPollEnabled:    parseBool(value(env, "INPLAY_STATISTICS_POLL_ENABLED", "false")),
		EnableMockAPIFootballCalls:     parseBool(value(env, "ENABLE_MOCK_API_FOOTBALL_CALLS", mockAPIFootballDefault)),
		FootballDataOrgBaseURL:         value(env, "FOOTBALL_DATA_ORG_BASE_URL", defaultFootballDataURL),
		FootballDataOrgAPIKey:          env["FOOTBALL_DATA_ORG_API_KEY"],
		EnableRealFootballDataOrgCalls: parseBool(value(env, "ENABLE_REAL_FOOTBALL_DATA_ORG_CALLS", "false")),
		TheSportsDBBaseURL:             value(env, "THE_SPORTS_DB_BASE_URL", defaultTheSportsDBURL),
		TheSportsDBAPIKey:              value(env, "THE_SPORTS_DB_API_KEY", defaultTheSportsDBKey),
		EnableRealTheSportsDBCalls:     parseBool(value(env, "ENABLE_REAL_THE_SPORTS_DB_CALLS", "false")),
		TheSportsDBMaxPerCycle:         theSportsDBMaxPerCycle,
		FootballProvider:               value(env, "FOOTBALL_PROVIDER", defaultFootballProvider),
		PollInterval:                   pollInterval,
		ProviderScheduleInterval:       providerScheduleInterval,
		PredictionScheduleInterval:     predictionScheduleInterval,
		HeartbeatPath:                  value(env, "WORKER_HEARTBEAT_PATH", filepath.Join(os.TempDir(), "sports-prediction-worker.heartbeat")),
		HeartbeatMaxAge:                heartbeatMaxAge,
		QueueConcurrency:               queueConcurrency,
		DBMaxOpenConns:                 dbMaxOpenConns,
		DBMaxIdleConns:                 dbMaxIdleConns,
		ProviderDailyLimit:             dailyLimit,
		ProviderDailyReserve:           dailyReserve,
		ProviderMinuteLimit:            minuteLimit,
		ProviderMinuteReserve:          minuteReserve,
		BackfillMinSafeBudget:          backfillMinSafeBudget,
		BackfillMaxPerCycle:            backfillMaxPerCycle,
		StatisticsBackfillMaxPerCycle:  statisticsBackfillMaxPerCycle,
		BackfillMaxRequestsPerDay:      backfillMaxRequestsPerDay,
		QuotaFixedDailyOverhead:        quotaFixedDailyOverhead,
		BackfillDays:                   backfillDays,
		QuotaLivePollSeconds:           quotaLivePollSeconds,
		MetricsAddress:                 value(env, "WORKER_METRICS_ADDRESS", defaultMetricsAddress),
		RequestDedupeTTL:               requestDedupeTTL,
		JobMaxAttempts:                 jobMaxAttempts,
		JobLeaseDuration:               jobLeaseDuration,
		AccaInventoryV2Enabled:         parseBool(value(env, "ACCA_INVENTORY_V2_ENABLED", "false")),
		AccaDBSchema:                   value(env, "ACCA_DB_SCHEMA", "public"),
		RecommendationScheduleInterval: recommendationScheduleInterval,
		PublicResultDetailHours:        publicResultDetailHours,
		RawResultRetentionDays:         rawResultRetentionDays,
		EnableDestructiveRetention:     parseBool(value(env, "ENABLE_DESTRUCTIVE_RETENTION", "false")),
		EnableRawPredictionDrain:       parseBool(value(env, "ENABLE_RAW_PREDICTION_DRAIN", "false")),
		RawPredictionDrainBatchSize:    rawPredictionDrainBatchSize,
		RawPredictionDrainMaxBatches:   rawPredictionDrainMaxBatches,
		EnableFixtureSnapshots:         parseBool(value(env, "ENABLE_FIXTURE_SNAPSHOTS", "false")),
		EnableFixtureEvents:            parseBool(value(env, "ENABLE_FIXTURE_EVENTS", "false")),
		RetentionDryRun:                parseBool(value(env, "RETENTION_DRY_RUN", "true")),
		NotificationBatchSize:          notificationBatchSize,
		NotificationConcurrency:        notificationConcurrency,
		EmailProvider:                  strings.ToLower(value(env, "EMAIL_PROVIDER", "log")),
		EmailFallbackProviders:         splitProviderList(env["EMAIL_FALLBACK_PROVIDERS"]),
		EmailFromAddress:               value(env, "EMAIL_FROM_ADDRESS", env["SMTP_FROM"]),
		EmailFromName:                  value(env, "EMAIL_FROM_NAME", "SokaLens"),
		EmailTestTo:                    env["EMAIL_TEST_TO"],
		MailtrapHost:                   value(env, "MAILTRAP_HOST", "sandbox.smtp.mailtrap.io"),
		MailtrapPort:                   mailtrapPort,
		MailtrapUsername:               value(env, "MAILTRAP_USERNAME", env["SMTP_USERNAME"]),
		MailtrapPassword:               value(env, "MAILTRAP_PASSWORD", env["SMTP_PASSWORD"]),
		MailtrapEncryption:             strings.ToLower(value(env, "MAILTRAP_ENCRYPTION", "starttls")),
		ResendAPIKey:                   env["RESEND_API_KEY"],
		ResendFromAddress:              env["RESEND_FROM_ADDRESS"],
		CloudflareAccountID:            env["CLOUDFLARE_ACCOUNT_ID"],
		CloudflareEmailAPIToken:        env["CLOUDFLARE_EMAIL_API_TOKEN"],
		CloudflareEmailSupportsHeaders: strings.EqualFold(strings.TrimSpace(env["CLOUDFLARE_EMAIL_SUPPORTS_HEADERS"]), "true"),
		EmailFromNotifications:         value(env, "EMAIL_FROM_NOTIFICATIONS", "notifications@sokalens.com"),
		EmailFromSupport:               value(env, "EMAIL_FROM_SUPPORT", "support@sokalens.com"),
		EmailFromNoreply:               value(env, "EMAIL_FROM_NOREPLY", "no-reply@sokalens.com"),
		SMTPAddress:                    env["SMTP_ADDRESS"],
		SMTPFrom:                       env["SMTP_FROM"],
		SMTPUsername:                   env["SMTP_USERNAME"],
		SMTPPassword:                   env["SMTP_PASSWORD"],
		BillingProvider:                value(env, "BILLING_PROVIDER", "sandbox"),
	}
	if !knownEmailProvider(cfg.EmailProvider) {
		return Config{}, errors.New("EMAIL_PROVIDER must be log, mailtrap, smtp, resend, or cloudflare")
	}
	for _, fallback := range cfg.EmailFallbackProviders {
		if !knownEmailProvider(fallback) {
			return Config{}, errors.New("EMAIL_FALLBACK_PROVIDERS must list only log, mailtrap, smtp, resend, or cloudflare")
		}
		if fallback == cfg.EmailProvider {
			return Config{}, errors.New("EMAIL_FALLBACK_PROVIDERS must not repeat EMAIL_PROVIDER")
		}
		// "log" reports every message as delivered. As a fallback it would
		// convert a provider outage into silent data loss that looks like
		// success in the outbox, which is worse than a visible retry.
		if fallback == "log" {
			return Config{}, errors.New("EMAIL_FALLBACK_PROVIDERS must not include log, which discards messages")
		}
	}
	if (cfg.EmailProvider == "mailtrap" || cfg.EmailProvider == "smtp") && (cfg.MailtrapHost == "" || cfg.EmailFromAddress == "" || cfg.MailtrapUsername == "" || cfg.MailtrapPassword == "") {
		return Config{}, errors.New("SMTP email delivery requires host, from address, username, and password")
	}
	if cfg.MailtrapEncryption != "none" && cfg.MailtrapEncryption != "starttls" && cfg.MailtrapEncryption != "tls" {
		return Config{}, errors.New("MAILTRAP_ENCRYPTION must be none, starttls, or tls")
	}
	if cfg.EmailProvider == "resend" && (cfg.ResendAPIKey == "" || value(env, "RESEND_FROM_ADDRESS", cfg.EmailFromAddress) == "") {
		return Config{}, errors.New("Resend email delivery requires RESEND_API_KEY and a from address")
	}
	if cfg.EmailProvider == "cloudflare" && (cfg.CloudflareAccountID == "" || cfg.CloudflareEmailAPIToken == "") {
		return Config{}, errors.New("Cloudflare email delivery requires CLOUDFLARE_ACCOUNT_ID and CLOUDFLARE_EMAIL_API_TOKEN")
	}
	if (cfg.SMTPAddress == "") != (cfg.SMTPFrom == "") {
		return Config{}, errors.New("SMTP_ADDRESS and SMTP_FROM must be configured together")
	}
	if cfg.ProviderDailyReserve >= cfg.ProviderDailyLimit {
		return Config{}, errors.New("PROVIDER_DAILY_RESERVE must be less than PROVIDER_DAILY_LIMIT")
	}
	if cfg.ProviderMinuteReserve >= cfg.ProviderMinuteLimit {
		return Config{}, errors.New("PROVIDER_MINUTE_RESERVE must be less than PROVIDER_MINUTE_LIMIT")
	}
	if cfg.Environment == "production" && isDevelopmentDatabaseURL(cfg.DatabaseURL) {
		return Config{}, errors.New("DATABASE_URL must not use development credentials in production")
	}

	if cfg.EnableRealAPIFootballCalls && cfg.APIFootballKey == "" {
		return Config{}, errors.New("API_FOOTBALL_KEY is required when real API-Football calls are enabled")
	}
	if cfg.EnableRealFootballDataOrgCalls && cfg.FootballDataOrgAPIKey == "" {
		return Config{}, errors.New("FOOTBALL_DATA_ORG_API_KEY is required when real football-data.org calls are enabled")
	}
	if cfg.FootballProvider != "api-football" && cfg.FootballProvider != "football-data-org" && cfg.FootballProvider != "hybrid" {
		return Config{}, errors.New("FOOTBALL_PROVIDER must be api-football, football-data-org, or hybrid")
	}

	return cfg, nil
}

func isDevelopmentDatabaseURL(raw string) bool {
	if raw == defaultDatabaseURL {
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return false
	}
	password, _ := parsed.User.Password()
	return parsed.User.Username() == "sports" && password == "sports"
}

func mergedEnv(overrides map[string]string) map[string]string {
	env := map[string]string{}
	for _, pair := range os.Environ() {
		for i, r := range pair {
			if r == '=' {
				env[pair[:i]] = pair[i+1:]
				break
			}
		}
	}
	for key, val := range overrides {
		env[key] = val
	}
	return env
}

func parseDuration(raw, key string) (time.Duration, error) {
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration like 30s: %w", key, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return duration, nil
}

func parseBool(raw string) bool {
	return raw == "1" || raw == "true" || raw == "TRUE" || raw == "yes" || raw == "YES"
}

func parsePositiveInt(raw, key string) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a positive integer: %w", key, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return value, nil
}

func parseBoundedInt(raw, key string, minimum, maximum int) (int, error) {
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	if parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", key, minimum, maximum)
	}
	return parsed, nil
}

func value(env map[string]string, key, fallback string) string {
	if val := env[key]; val != "" {
		return val
	}
	return fallback
}

func knownEmailProvider(name string) bool {
	switch name {
	case "log", "mailtrap", "smtp", "resend", "cloudflare":
		return true
	}
	return false
}

func splitProviderList(raw string) []string {
	providers := []string{}
	for _, part := range strings.Split(raw, ",") {
		if name := strings.ToLower(strings.TrimSpace(part)); name != "" {
			providers = append(providers, name)
		}
	}
	return providers
}
