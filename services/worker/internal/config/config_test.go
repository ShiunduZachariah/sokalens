package config

import (
	"testing"
	"time"
)

func TestLoadUsesSafeWorkerDefaults(t *testing.T) {
	cfg, err := Load(map[string]string{})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Environment != "local" {
		t.Fatalf("Environment = %q, want local", cfg.Environment)
	}
	if cfg.EnableDestructiveRetention {
		t.Fatal("destructive retention must be disabled by default")
	}
	if cfg.EnableRawPredictionDrain {
		t.Fatal("raw prediction drain must be disabled by default")
	}
	if cfg.RawPredictionDrainBatchSize != 1000 || cfg.RawPredictionDrainMaxBatches != 10 {
		t.Fatalf("raw prediction drain bounds=%d x %d", cfg.RawPredictionDrainBatchSize, cfg.RawPredictionDrainMaxBatches)
	}
	if !cfg.RetentionDryRun {
		t.Fatal("retention dry-run must be enabled by default")
	}
	if cfg.MockProviderURL == "" {
		t.Fatal("MockProviderURL is empty")
	}
	if cfg.DatabaseURL == "" {
		t.Fatal("DatabaseURL is empty")
	}
	if cfg.APIFootballBaseURL != "https://v3.football.api-sports.io" {
		t.Fatalf("APIFootballBaseURL = %q, want official v3 base URL", cfg.APIFootballBaseURL)
	}
	if cfg.FootballDataOrgBaseURL != "https://api.football-data.org/v4" {
		t.Fatalf("FootballDataOrgBaseURL = %q, want official v4 base URL", cfg.FootballDataOrgBaseURL)
	}
	if cfg.EnableRealAPIFootballCalls {
		t.Fatal("worker must not enable real API-Football calls by default")
	}
	if cfg.EnableRealFootballDataOrgCalls {
		t.Fatal("worker must not enable real football-data.org calls by default")
	}
	if !cfg.EnableMockAPIFootballCalls {
		t.Fatal("local worker must explicitly enable mock API-Football calls by default")
	}
	if cfg.FootballProvider != "api-football" {
		t.Fatalf("FootballProvider = %q, want api-football", cfg.FootballProvider)
	}
	if cfg.PollInterval != 30*time.Second {
		t.Fatalf("PollInterval = %s, want 30s", cfg.PollInterval)
	}
	if cfg.ProviderScheduleInterval != 4*time.Hour {
		t.Fatalf("ProviderScheduleInterval = %s, want 4h", cfg.ProviderScheduleInterval)
	}
	if cfg.PredictionScheduleInterval != time.Minute {
		t.Fatalf("PredictionScheduleInterval = %s, want 1m", cfg.PredictionScheduleInterval)
	}
	if cfg.HeartbeatPath == "" {
		t.Fatal("HeartbeatPath is empty")
	}
	if cfg.HeartbeatMaxAge != 10*time.Minute {
		t.Fatalf("HeartbeatMaxAge = %s, want 10m", cfg.HeartbeatMaxAge)
	}
	if cfg.QueueConcurrency != 4 {
		t.Fatalf("QueueConcurrency = %d, want 4", cfg.QueueConcurrency)
	}
	if cfg.StatisticsBackfillMaxPerCycle != 0 {
		t.Fatalf("statistics backfill must be off by default, got %d jobs/cycle", cfg.StatisticsBackfillMaxPerCycle)
	}
	// Sized for the per-queue drain loops. Every concurrent handler needs a
	// connection, and the runtime caps handlers at DBMaxOpenConns-2, so a pool
	// of 12 would have made the pool the bottleneck the drain loops exist to
	// remove.
	if cfg.DBMaxOpenConns != 24 || cfg.DBMaxIdleConns != 12 {
		t.Fatalf("worker database pool = %d open/%d idle, want 24 open/12 idle", cfg.DBMaxOpenConns, cfg.DBMaxIdleConns)
	}
	// The package we pay for, and a hard ceiling rather than a target: the
	// budget governor refuses past it on its own count of requests sent. A
	// default that guessed high would authorise spend nobody agreed to, so
	// this is the number to change when the plan changes.
	if cfg.ProviderDailyLimit != 7500 {
		t.Fatalf("ProviderDailyLimit = %d, want 7500", cfg.ProviderDailyLimit)
	}
	// Absolute per 3.3, and sized to this plan. 4,000 was right for 75,000 and
	// reserves 53% of 7,500; 1,500 was right for 7,500 before the pacer
	// existed and is 2% of 75,000.
	if cfg.ProviderDailyReserve != 900 {
		t.Fatalf("ProviderDailyReserve = %d, want 900", cfg.ProviderDailyReserve)
	}
	if cfg.ProviderDailyReserve >= cfg.ProviderDailyLimit/5 {
		t.Fatalf("reserve %d is a fifth of the package; 3.3 wants it absolute", cfg.ProviderDailyReserve)
	}
	// The ceiling has to be reachable by the schedule it governs. The
	// recurring overhead plus the backfill ceiling plus a day of live polling
	// at the default cadence must leave room, or the platform throttles itself
	// every day and the first symptom is frozen scores.
	committed := cfg.QuotaFixedDailyOverhead + cfg.BackfillMaxRequestsPerDay + 86400/cfg.QuotaLivePollSeconds
	if committed+cfg.ProviderDailyReserve >= cfg.ProviderDailyLimit {
		t.Fatalf("recurring commitments %d + reserve %d do not fit in the %d package",
			committed, cfg.ProviderDailyReserve, cfg.ProviderDailyLimit)
	}
	if cfg.BackfillMaxRequestsPerDay != 1200 {
		t.Fatalf("BackfillMaxRequestsPerDay = %d, want 1200", cfg.BackfillMaxRequestsPerDay)
	}
	if cfg.QuotaFixedDailyOverhead != 900 {
		t.Fatalf("QuotaFixedDailyOverhead = %d, want 900", cfg.QuotaFixedDailyOverhead)
	}
	if cfg.QuotaLivePollSeconds != 60 {
		t.Fatalf("QuotaLivePollSeconds = %d, want 60", cfg.QuotaLivePollSeconds)
	}
	if cfg.ProviderMinuteLimit != 300 {
		t.Fatalf("ProviderMinuteLimit = %d, want 300", cfg.ProviderMinuteLimit)
	}
	if cfg.ProviderMinuteReserve != 15 {
		t.Fatalf("ProviderMinuteReserve = %d, want 15", cfg.ProviderMinuteReserve)
	}
}

func TestStatisticsBackfillRequiresExplicitBoundedOptIn(t *testing.T) {
	cfg, err := Load(map[string]string{"QUOTA_STATISTICS_BACKFILL_MAX_PER_CYCLE": "1"})
	if err != nil || cfg.StatisticsBackfillMaxPerCycle != 1 {
		t.Fatalf("opt-in ceiling = %d, err = %v", cfg.StatisticsBackfillMaxPerCycle, err)
	}
	if _, err := Load(map[string]string{"QUOTA_STATISTICS_BACKFILL_MAX_PER_CYCLE": "26"}); err == nil {
		t.Fatal("statistics backfill ceiling above 25 must be rejected")
	}
}

func TestLoadAcceptsCloudflareEmailConfiguration(t *testing.T) {
	cfg, err := Load(map[string]string{
		"EMAIL_PROVIDER":             "cloudflare",
		"CLOUDFLARE_ACCOUNT_ID":      "account-id",
		"CLOUDFLARE_EMAIL_API_TOKEN": "scoped-token",
		"EMAIL_FROM_NOTIFICATIONS":   "notifications@sokalens.com",
		"EMAIL_FROM_SUPPORT":         "support@sokalens.com",
		"EMAIL_FROM_NOREPLY":         "no-reply@sokalens.com",
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.EmailProvider != "cloudflare" || cfg.CloudflareAccountID != "account-id" || cfg.CloudflareEmailAPIToken != "scoped-token" {
		t.Fatalf("cloudflare config = %+v", cfg)
	}
}

func TestLoadParsesOrderedEmailFallbackProviders(t *testing.T) {
	cfg, err := Load(map[string]string{
		"EMAIL_PROVIDER":             "cloudflare",
		"CLOUDFLARE_ACCOUNT_ID":      "account-id",
		"CLOUDFLARE_EMAIL_API_TOKEN": "scoped-token",
		"EMAIL_FALLBACK_PROVIDERS":   " resend , smtp ",
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.EmailFallbackProviders) != 2 || cfg.EmailFallbackProviders[0] != "resend" || cfg.EmailFallbackProviders[1] != "smtp" {
		t.Fatalf("EmailFallbackProviders = %v", cfg.EmailFallbackProviders)
	}
}

func TestLoadRejectsUnsafeEmailFallbackChains(t *testing.T) {
	base := map[string]string{
		"EMAIL_PROVIDER":             "cloudflare",
		"CLOUDFLARE_ACCOUNT_ID":      "account-id",
		"CLOUDFLARE_EMAIL_API_TOKEN": "scoped-token",
	}
	for name, chain := range map[string]string{
		// LogTransport reports every message as delivered, so a fallback to it
		// turns an outage into silent loss that looks like success.
		"log discards messages":       "log",
		"unknown provider":            "sendgrid",
		"repeats the active provider": "cloudflare",
	} {
		env := map[string]string{}
		for key, value := range base {
			env[key] = value
		}
		env["EMAIL_FALLBACK_PROVIDERS"] = chain
		if _, err := Load(env); err == nil {
			t.Errorf("Load accepted a fallback chain that %s: %q", name, chain)
		}
	}
}

func TestLoadRejectsCloudflareWithoutScopedCredentials(t *testing.T) {
	if _, err := Load(map[string]string{"EMAIL_PROVIDER": "cloudflare"}); err == nil {
		t.Fatal("Load accepted Cloudflare without account ID and token")
	}
}

func TestLoadRequiresExplicitTwoSwitchRetentionActivation(t *testing.T) {
	cfg, err := Load(map[string]string{
		"ENABLE_DESTRUCTIVE_RETENTION": "true",
		"RETENTION_DRY_RUN":            "false",
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.EnableDestructiveRetention || cfg.RetentionDryRun {
		t.Fatalf("retention switches enabled=%t dry_run=%t, want true/false", cfg.EnableDestructiveRetention, cfg.RetentionDryRun)
	}
}

func TestLoadEnablesRawPredictionDrainIndependently(t *testing.T) {
	cfg, err := Load(map[string]string{
		"ENABLE_RAW_PREDICTION_DRAIN":      "true",
		"RAW_PREDICTION_DRAIN_BATCH_SIZE":  "500",
		"RAW_PREDICTION_DRAIN_MAX_BATCHES": "50",
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.EnableRawPredictionDrain || cfg.EnableDestructiveRetention {
		t.Fatalf("raw prediction drain=%t legacy retention=%t", cfg.EnableRawPredictionDrain, cfg.EnableDestructiveRetention)
	}
	if cfg.RawPredictionDrainBatchSize != 500 || cfg.RawPredictionDrainMaxBatches != 50 {
		t.Fatalf("raw prediction drain bounds=%d x %d", cfg.RawPredictionDrainBatchSize, cfg.RawPredictionDrainMaxBatches)
	}
}

func TestLoadRejectsUnboundedRawPredictionDrain(t *testing.T) {
	if _, err := Load(map[string]string{"RAW_PREDICTION_DRAIN_MAX_BATCHES": "101"}); err == nil {
		t.Fatal("unbounded raw prediction drain was accepted")
	}
}

func TestLoadAcceptsBoundedWorkerConcurrencyOverrides(t *testing.T) {
	cfg, err := Load(map[string]string{
		"WORKER_QUEUE_CONCURRENCY": "3",
		"WORKER_DB_MAX_OPEN_CONNS": "9",
		"WORKER_DB_MAX_IDLE_CONNS": "3",
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.QueueConcurrency != 3 || cfg.DBMaxOpenConns != 9 || cfg.DBMaxIdleConns != 3 {
		t.Fatalf("worker concurrency overrides not applied: %+v", cfg)
	}
}

func TestLoadRejectsUnsafeWorkerConcurrencySettings(t *testing.T) {
	for name, overrides := range map[string]map[string]string{
		"zero queue concurrency":   {"WORKER_QUEUE_CONCURRENCY": "0"},
		"excess queue concurrency": {"WORKER_QUEUE_CONCURRENCY": "17"},
		"zero open connections":    {"WORKER_DB_MAX_OPEN_CONNS": "0"},
		"idle exceeds open": {
			"WORKER_DB_MAX_OPEN_CONNS": "4",
			"WORKER_DB_MAX_IDLE_CONNS": "5",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(overrides); err == nil {
				t.Fatal("Load returned nil error")
			}
		})
	}
}

func TestLoadRejectsInvalidPollInterval(t *testing.T) {
	_, err := Load(map[string]string{"WORKER_POLL_INTERVAL": "soon"})
	if err == nil {
		t.Fatal("Load returned nil error for invalid poll interval")
	}
}

func TestLoadRejectsInvalidScheduleInterval(t *testing.T) {
	_, err := Load(map[string]string{"PROVIDER_SCHEDULE_INTERVAL": "constantly"})
	if err == nil {
		t.Fatal("Load returned nil error for invalid provider schedule interval")
	}
}

func TestLoadDisablesMockProviderOutsideLocalUnlessExplicitlyEnabled(t *testing.T) {
	productionDatabaseURL := "postgres://app:strong-test-password@db.internal:5432/sports_prediction?sslmode=require"
	cfg, err := Load(map[string]string{
		"APP_ENV":                        "production",
		"DATABASE_URL":                   productionDatabaseURL,
		"ENABLE_MOCK_API_FOOTBALL_CALLS": "",
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.EnableMockAPIFootballCalls {
		t.Fatal("production worker enabled mock API-Football implicitly")
	}

	cfg, err = Load(map[string]string{
		"APP_ENV":                        "production",
		"DATABASE_URL":                   productionDatabaseURL,
		"ENABLE_MOCK_API_FOOTBALL_CALLS": "true",
	})
	if err != nil {
		t.Fatalf("Load returned error for explicit production mock: %v", err)
	}
	if !cfg.EnableMockAPIFootballCalls {
		t.Fatal("production worker did not honor explicit mock API-Football enablement")
	}
}

func TestLoadRejectsContainerDevelopmentDatabaseInProduction(t *testing.T) {
	_, err := Load(map[string]string{
		"APP_ENV":      "production",
		"DATABASE_URL": "postgres://sports:sports@postgres:5432/sports_prediction?sslmode=disable",
	})
	if err == nil {
		t.Fatal("Load returned nil error for container development database in production")
	}
}

func TestLoadRequiresAPIKeyWhenRealProviderCallsAreEnabled(t *testing.T) {
	_, err := Load(map[string]string{"ENABLE_REAL_API_FOOTBALL_CALLS": "true"})
	if err == nil {
		t.Fatal("Load returned nil error without API-Football key")
	}
}

func TestLoadAllowsRealProviderCallsOnlyWithAPIKey(t *testing.T) {
	cfg, err := Load(map[string]string{
		"ENABLE_REAL_API_FOOTBALL_CALLS": "true",
		"API_FOOTBALL_KEY":               "test-key",
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.EnableRealAPIFootballCalls {
		t.Fatal("EnableRealAPIFootballCalls = false, want true")
	}
}

func TestLoadRequiresFootballDataOrgAPIKeyWhenRealCallsAreEnabled(t *testing.T) {
	_, err := Load(map[string]string{"ENABLE_REAL_FOOTBALL_DATA_ORG_CALLS": "true"})
	if err == nil {
		t.Fatal("Load returned nil error without football-data.org key")
	}
}

func TestLoadAllowsFootballDataOrgRealCallsOnlyWithAPIKey(t *testing.T) {
	cfg, err := Load(map[string]string{
		"ENABLE_REAL_FOOTBALL_DATA_ORG_CALLS": "true",
		"FOOTBALL_DATA_ORG_API_KEY":           "test-key",
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.EnableRealFootballDataOrgCalls {
		t.Fatal("EnableRealFootballDataOrgCalls = false, want true")
	}
	if cfg.FootballDataOrgAPIKey != "test-key" {
		t.Fatal("FootballDataOrgAPIKey was not loaded")
	}
}

func TestLoadRejectsInvalidProviderQuotaConfig(t *testing.T) {
	_, err := Load(map[string]string{"PROVIDER_DAILY_RESERVE": "many"})
	if err == nil {
		t.Fatal("Load returned nil error for invalid provider quota config")
	}
}

func TestLoadRejectsProviderReservesAtOrAboveLimits(t *testing.T) {
	for name, overrides := range map[string]map[string]string{
		"daily":  {"PROVIDER_DAILY_LIMIT": "20", "PROVIDER_DAILY_RESERVE": "20"},
		"minute": {"PROVIDER_MINUTE_LIMIT": "2", "PROVIDER_MINUTE_RESERVE": "2"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(overrides); err == nil {
				t.Fatal("Load returned nil error for reserve at configured limit")
			}
		})
	}
}

func TestLoadAllowsFootballDataOrgAsPrimaryFootballProvider(t *testing.T) {
	cfg, err := Load(map[string]string{"FOOTBALL_PROVIDER": "football-data-org"})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.FootballProvider != "football-data-org" {
		t.Fatalf("FootballProvider = %q, want football-data-org", cfg.FootballProvider)
	}
}

func TestLoadAllowsHybridFootballProviderRole(t *testing.T) {
	cfg, err := Load(map[string]string{"FOOTBALL_PROVIDER": "hybrid"})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.FootballProvider != "hybrid" {
		t.Fatalf("FootballProvider = %q, want hybrid", cfg.FootballProvider)
	}
}

func TestLoadRejectsUnknownFootballProvider(t *testing.T) {
	_, err := Load(map[string]string{"FOOTBALL_PROVIDER": "unknown"})
	if err == nil {
		t.Fatal("Load returned nil error for unknown football provider")
	}
}

func TestLoadIgnoresDeprecatedGoPredictionEngineConfig(t *testing.T) {
	_, err := Load(map[string]string{
		"PREDICTION_ENGINE_CONFIG_JSON": `{"form":{"decay_rate":1}}`,
	})
	if err != nil {
		t.Fatalf("Load returned error for deprecated Go prediction engine config: %v", err)
	}
}
