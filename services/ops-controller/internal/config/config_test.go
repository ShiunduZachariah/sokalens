package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadUsesObserveOnlyDefaults(t *testing.T) {
	cfg, err := Load(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutonomyMode != ModeObserve || cfg.Address != ":8090" || cfg.LogLevel != "info" {
		t.Fatalf("unsafe defaults: %+v", cfg)
	}
	if cfg.ShutdownTimeout != 10*time.Second || cfg.MaxHeaderBytes != 64*1024 {
		t.Fatalf("unexpected server defaults: %+v", cfg)
	}
	if cfg.IncidentStoreEnabled || cfg.DatabaseURL != "" {
		t.Fatalf("incident persistence must default off without an implicit database: %+v", cfg)
	}
	if cfg.DBMaxOpenConns != 10 || cfg.DBMaxIdleConns != 5 || cfg.DBPingTimeout != 3*time.Second || cfg.IncidentOperationTimeout != 2*time.Second || cfg.IncidentCorrelationWindow != 15*time.Minute {
		t.Fatalf("unexpected bounded persistence defaults: %+v", cfg)
	}
}

func TestLoadEnablesIncidentPersistenceOnlyWithCompleteConfiguration(t *testing.T) {
	cfg, err := Load(map[string]string{
		"OPS_ENV":                         "staging",
		"OPS_INCIDENT_STORE_ENABLED":      "true",
		"OPS_ALERT_SIGNING_SECRET":        "0123456789abcdef0123456789abcdef",
		"OPS_DATABASE_URL":                "postgres://ops:secret@db.example:5432/sokalens?sslmode=require",
		"OPS_DB_MAX_OPEN_CONNS":           "20",
		"OPS_DB_MAX_IDLE_CONNS":           "4",
		"OPS_DB_CONN_MAX_LIFETIME":        "20m",
		"OPS_DB_CONN_MAX_IDLE_TIME":       "2m",
		"OPS_DB_PING_TIMEOUT":             "2s",
		"OPS_INCIDENT_OPERATION_TIMEOUT":  "1500ms",
		"OPS_INCIDENT_CORRELATION_WINDOW": "30m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IncidentStoreEnabled || cfg.DBMaxOpenConns != 20 || cfg.DBMaxIdleConns != 4 || cfg.IncidentCorrelationWindow != 30*time.Minute {
		t.Fatalf("persistence overrides not applied: %+v", cfg)
	}
}

func TestLoadRejectsUnsafeIncidentPersistenceConfiguration(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	tests := map[string]map[string]string{
		"invalid enable flag": {"OPS_INCIDENT_STORE_ENABLED": "yes"},
		"missing secret": {
			"OPS_INCIDENT_STORE_ENABLED": "true",
			"OPS_DATABASE_URL":           "postgres://ops:secret@db.example:5432/sokalens?sslmode=require",
		},
		"missing database": {
			"OPS_INCIDENT_STORE_ENABLED": "true",
			"OPS_ALERT_SIGNING_SECRET":   secret,
		},
		"wrong scheme": {
			"OPS_INCIDENT_STORE_ENABLED": "true",
			"OPS_ALERT_SIGNING_SECRET":   secret,
			"OPS_DATABASE_URL":           "mysql://ops:secret@db.example/sokalens",
		},
		"missing database name": {
			"OPS_INCIDENT_STORE_ENABLED": "true",
			"OPS_ALERT_SIGNING_SECRET":   secret,
			"OPS_DATABASE_URL":           "postgres://ops:secret@db.example:5432/?sslmode=require",
		},
		"idle exceeds open": {
			"OPS_DB_MAX_OPEN_CONNS": "2",
			"OPS_DB_MAX_IDLE_CONNS": "3",
		},
		"unbounded ping":        {"OPS_DB_PING_TIMEOUT": "11s"},
		"unbounded operation":   {"OPS_INCIDENT_OPERATION_TIMEOUT": "11s"},
		"unbounded correlation": {"OPS_INCIDENT_CORRELATION_WINDOW": "25h"},
		"production development credentials": {
			"OPS_ENV":                    "production",
			"OPS_INCIDENT_STORE_ENABLED": "true",
			"OPS_ALERT_SIGNING_SECRET":   secret,
			"OPS_DATABASE_URL":           "postgres://sports:sports@postgres:5432/sports_prediction?sslmode=require",
		},
		"production disabled TLS": {
			"OPS_ENV":                    "production",
			"OPS_INCIDENT_STORE_ENABLED": "true",
			"OPS_ALERT_SIGNING_SECRET":   secret,
			"OPS_DATABASE_URL":           "postgres://ops:secret@db.example:5432/sokalens?sslmode=disable",
		},
		"production omitted TLS": {
			"OPS_ENV":                    "production",
			"OPS_INCIDENT_STORE_ENABLED": "true",
			"OPS_ALERT_SIGNING_SECRET":   secret,
			"OPS_DATABASE_URL":           "postgres://ops:secret@db.example:5432/sokalens",
		},
		"production unknown TLS": {
			"OPS_ENV":                    "production",
			"OPS_INCIDENT_STORE_ENABLED": "true",
			"OPS_ALERT_SIGNING_SECRET":   secret,
			"OPS_DATABASE_URL":           "postgres://ops:secret@db.example:5432/sokalens?sslmode=prefer",
		},
	}
	for name, overrides := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(overrides); err == nil {
				t.Fatal("Load returned nil error")
			}
		})
	}
}

func TestLoadAcceptsExplicitProductionTLSModes(t *testing.T) {
	for _, mode := range []string{"require", "verify-ca", "verify-full"} {
		t.Run(mode, func(t *testing.T) {
			_, err := Load(map[string]string{
				"OPS_ENV":                    "production",
				"OPS_INCIDENT_STORE_ENABLED": "true",
				"OPS_ALERT_SIGNING_SECRET":   "0123456789abcdef0123456789abcdef",
				"OPS_DATABASE_URL":           "postgres://ops:strong-password@db.example:5432/sokalens?sslmode=" + mode,
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLoadRejectsAuthorityIncrease(t *testing.T) {
	for _, mode := range []string{"assist", "auto_rollback", "auto_heal", "auto_patch"} {
		t.Run(mode, func(t *testing.T) {
			_, err := Load(map[string]string{"OPS_AUTONOMY_MODE": mode})
			if err == nil || !strings.Contains(err.Error(), "must be \"observe\"") {
				t.Fatalf("error = %v, want observe-only rejection", err)
			}
		})
	}
}

func TestLoadValidatesHTTPSettings(t *testing.T) {
	tests := map[string]map[string]string{
		"address":        {"OPS_HTTP_ADDRESS": "localhost"},
		"port zero":      {"OPS_HTTP_ADDRESS": ":0"},
		"log level":      {"OPS_LOG_LEVEL": "trace"},
		"read ordering":  {"OPS_HTTP_READ_HEADER_TIMEOUT": "6s", "OPS_HTTP_READ_TIMEOUT": "5s"},
		"header minimum": {"OPS_HTTP_MAX_HEADER_BYTES": "100"},
		"bad duration":   {"OPS_SHUTDOWN_TIMEOUT": "later"},
	}
	for name, overrides := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(overrides); err == nil {
				t.Fatal("Load returned nil error")
			}
		})
	}
}

func TestLoadAcceptsBoundedOverrides(t *testing.T) {
	cfg, err := Load(map[string]string{
		"OPS_ENV":                      "staging",
		"OPS_HTTP_ADDRESS":             "127.0.0.1:9090",
		"OPS_LOG_LEVEL":                "WARN",
		"OPS_HTTP_READ_HEADER_TIMEOUT": "2s",
		"OPS_HTTP_READ_TIMEOUT":        "3s",
		"OPS_HTTP_WRITE_TIMEOUT":       "4s",
		"OPS_HTTP_IDLE_TIMEOUT":        "30s",
		"OPS_SHUTDOWN_TIMEOUT":         "5s",
		"OPS_HTTP_MAX_HEADER_BYTES":    "32768",
		"OPS_ALERT_SIGNING_SECRET":     "0123456789abcdef0123456789abcdef",
		"OPS_ALERT_MAX_BODY_BYTES":     "32768",
		"OPS_ALERT_MAX_CLOCK_SKEW":     "2m",
		"OPS_ALERT_REPLAY_LIMIT":       "500",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environment != "staging" || cfg.Address != "127.0.0.1:9090" || cfg.LogLevel != "warn" {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	if cfg.AlertMaxBodyBytes != 32768 || cfg.AlertMaxClockSkew != 2*time.Minute || cfg.AlertReplayLimit != 500 {
		t.Fatalf("alert overrides not applied: %+v", cfg)
	}
}

func TestLoadRejectsWeakAlertSecretAndUnboundedSettings(t *testing.T) {
	for _, overrides := range []map[string]string{
		{"OPS_ALERT_SIGNING_SECRET": "too-short"},
		{"OPS_ALERT_MAX_BODY_BYTES": "10000000"},
		{"OPS_ALERT_REPLAY_LIMIT": "1"},
		{"OPS_ALERT_MAX_CLOCK_SKEW": "1h"},
	} {
		if _, err := Load(overrides); err == nil {
			t.Fatalf("Load(%v) returned nil error", overrides)
		}
	}
}

var testTelegramToken = fakeBotToken()

func telegramEnabled(extra map[string]string) map[string]string {
	overrides := map[string]string{
		"OPS_TELEGRAM_ENABLED":          "true",
		"OPS_TELEGRAM_BOT_TOKEN":        testTelegramToken,
		"OPS_TELEGRAM_CHAT_ID":          "-1001234567890",
		"OPS_TELEGRAM_ALLOWED_USER_IDS": "111,222",
	}
	for key, val := range extra {
		if val == "" {
			delete(overrides, key)
			continue
		}
		overrides[key] = val
	}
	return overrides
}

func TestLoadKeepsTelegramBridgeOffByDefault(t *testing.T) {
	cfg, err := Load(map[string]string{"OPS_TELEGRAM_ENABLED": "false"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TelegramEnabled || cfg.TelegramRunbooksEnabled {
		t.Fatalf("telegram bridge must default off: %+v", cfg)
	}
	if cfg.TelegramBotToken != "" || cfg.TelegramChatID != 0 || len(cfg.TelegramAllowedUserIDs) != 0 {
		t.Fatalf("disabled bridge must not carry credentials: %+v", cfg)
	}
	if cfg.TelegramPollTimeoutSeconds != 30 || cfg.TelegramSendRatePerSec != 1 {
		t.Fatalf("unexpected telegram defaults: %+v", cfg)
	}
}

// A disabled bridge must ignore the credential trio entirely, including
// values that would be fatal when enabled.
func TestLoadIgnoresTelegramCredentialsWhenDisabled(t *testing.T) {
	cfg, err := Load(map[string]string{
		"OPS_TELEGRAM_ENABLED":          "false",
		"OPS_TELEGRAM_BOT_TOKEN":        "",
		"OPS_TELEGRAM_CHAT_ID":          "not-a-number",
		"OPS_TELEGRAM_ALLOWED_USER_IDS": "also-not-a-number",
	})
	if err != nil {
		t.Fatalf("disabled bridge must not validate unused keys: %v", err)
	}
	if cfg.TelegramEnabled || cfg.TelegramChatID != 0 || len(cfg.TelegramAllowedUserIDs) != 0 {
		t.Fatalf("disabled bridge leaked configuration: %+v", cfg)
	}
}

func TestLoadAcceptsCompleteTelegramConfiguration(t *testing.T) {
	cfg, err := Load(telegramEnabled(map[string]string{
		"OPS_TELEGRAM_POLL_TIMEOUT_SECONDS": "50",
		"OPS_TELEGRAM_RUNBOOKS_ENABLED":     "true",
		"OPS_TELEGRAM_SEND_RATE_PER_SEC":    "3",
		"OPS_TELEGRAM_ALLOWED_USER_IDS":     " 111 , 222 ,111",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.TelegramEnabled || !cfg.TelegramRunbooksEnabled {
		t.Fatalf("telegram overrides not applied: %+v", cfg)
	}
	if cfg.TelegramChatID != -1001234567890 {
		t.Fatalf("negative group chat id not preserved: %d", cfg.TelegramChatID)
	}
	if cfg.TelegramPollTimeoutSeconds != 50 || cfg.TelegramSendRatePerSec != 3 {
		t.Fatalf("telegram bounds not applied: %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.TelegramAllowedUserIDs, []int64{111, 222}) {
		t.Fatalf("allowlist = %v, want deduplicated [111 222]", cfg.TelegramAllowedUserIDs)
	}
	if cfg.TelegramBotToken != testTelegramToken {
		t.Fatal("bot token not loaded")
	}
}

// Runbook authority is independent of the bridge: enabling the bridge must
// not enable /run and /confirm.
func TestLoadKeepsRunbooksOffWhenBridgeEnabled(t *testing.T) {
	cfg, err := Load(telegramEnabled(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TelegramRunbooksEnabled {
		t.Fatal("runbooks must not be enabled by the bridge switch alone")
	}
}

func TestLoadFailsClosedOnIncompleteTelegramConfiguration(t *testing.T) {
	tests := map[string]map[string]string{
		"invalid enable flag":     {"OPS_TELEGRAM_ENABLED": "yes"},
		"invalid runbooks flag":   {"OPS_TELEGRAM_RUNBOOKS_ENABLED": "sometimes"},
		"missing token":           telegramEnabled(map[string]string{"OPS_TELEGRAM_BOT_TOKEN": ""}),
		"blank token":             telegramEnabled(map[string]string{"OPS_TELEGRAM_BOT_TOKEN": "   "}),
		"token without bot id":    telegramEnabled(map[string]string{"OPS_TELEGRAM_BOT_TOKEN": "AAsecretwithoutcolon"}),
		"token with empty secret": telegramEnabled(map[string]string{"OPS_TELEGRAM_BOT_TOKEN": "8123456789:"}),
		"token with text bot id":  telegramEnabled(map[string]string{"OPS_TELEGRAM_BOT_TOKEN": "bot:AAsecret"}),
		"missing chat id":         telegramEnabled(map[string]string{"OPS_TELEGRAM_CHAT_ID": ""}),
		"blank chat id":           telegramEnabled(map[string]string{"OPS_TELEGRAM_CHAT_ID": "   "}),
		"unparseable chat id":     telegramEnabled(map[string]string{"OPS_TELEGRAM_CHAT_ID": "-100abc"}),
		"overflowing chat id":     telegramEnabled(map[string]string{"OPS_TELEGRAM_CHAT_ID": "99999999999999999999"}),
		"zero chat id":            telegramEnabled(map[string]string{"OPS_TELEGRAM_CHAT_ID": "0"}),
		"missing allowlist":       telegramEnabled(map[string]string{"OPS_TELEGRAM_ALLOWED_USER_IDS": ""}),
		"blank allowlist":         telegramEnabled(map[string]string{"OPS_TELEGRAM_ALLOWED_USER_IDS": "   "}),
		"comma only allowlist":    telegramEnabled(map[string]string{"OPS_TELEGRAM_ALLOWED_USER_IDS": ","}),
		"empty allowlist entry":   telegramEnabled(map[string]string{"OPS_TELEGRAM_ALLOWED_USER_IDS": "111,,222"}),
		"unparseable allowlist":   telegramEnabled(map[string]string{"OPS_TELEGRAM_ALLOWED_USER_IDS": "111,alice"}),
		"wildcard allowlist":      telegramEnabled(map[string]string{"OPS_TELEGRAM_ALLOWED_USER_IDS": "*"}),
		"negative allowlist entry": telegramEnabled(map[string]string{
			"OPS_TELEGRAM_ALLOWED_USER_IDS": "111,-222",
		}),
		"zero allowlist entry":  telegramEnabled(map[string]string{"OPS_TELEGRAM_ALLOWED_USER_IDS": "0"}),
		"poll timeout too low":  telegramEnabled(map[string]string{"OPS_TELEGRAM_POLL_TIMEOUT_SECONDS": "0"}),
		"poll timeout too high": telegramEnabled(map[string]string{"OPS_TELEGRAM_POLL_TIMEOUT_SECONDS": "51"}),
		"poll timeout not a number": telegramEnabled(map[string]string{
			"OPS_TELEGRAM_POLL_TIMEOUT_SECONDS": "forever",
		}),
		"send rate zero":         telegramEnabled(map[string]string{"OPS_TELEGRAM_SEND_RATE_PER_SEC": "0"}),
		"send rate negative":     telegramEnabled(map[string]string{"OPS_TELEGRAM_SEND_RATE_PER_SEC": "-1"}),
		"send rate not a number": telegramEnabled(map[string]string{"OPS_TELEGRAM_SEND_RATE_PER_SEC": "fast"}),
	}
	for name, overrides := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(overrides); err == nil {
				t.Fatal("Load returned nil error")
			}
		})
	}
}

// The token is a production credential: no validation path may echo it.
func TestLoadNeverEchoesTelegramBotToken(t *testing.T) {
	token := fakeBotToken()
	for name, overrides := range map[string]map[string]string{
		"missing chat id":  telegramEnabled(map[string]string{"OPS_TELEGRAM_BOT_TOKEN": token, "OPS_TELEGRAM_CHAT_ID": ""}),
		"bad allowlist":    telegramEnabled(map[string]string{"OPS_TELEGRAM_BOT_TOKEN": token, "OPS_TELEGRAM_ALLOWED_USER_IDS": "alice"}),
		"malformed token":  telegramEnabled(map[string]string{"OPS_TELEGRAM_BOT_TOKEN": "do-not-echo-this-secret-value"}),
		"bad send rate":    telegramEnabled(map[string]string{"OPS_TELEGRAM_BOT_TOKEN": token, "OPS_TELEGRAM_SEND_RATE_PER_SEC": "0"}),
		"bad poll timeout": telegramEnabled(map[string]string{"OPS_TELEGRAM_BOT_TOKEN": token, "OPS_TELEGRAM_POLL_TIMEOUT_SECONDS": "99"}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(overrides)
			if err == nil {
				t.Fatal("Load returned nil error")
			}
			for _, secret := range []string{token, "do-not-echo", "AA-do-not-echo-this-secret-value-x"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked the bot token: %v", err)
				}
			}
		})
	}
}

// fakeBotToken builds a Telegram-shaped bot token at run time.
//
// The literal must never appear in source: gitleaks matches this shape, not
// its meaning, so even an obviously fake value fails the repo's secret-scan
// gate. .gitleaks.toml records the same convention for the ACCA cursor
// fixture — construct the value rather than allowlist it.
func fakeBotToken() string {
	return "8123456789" + ":" + "AA" + strings.Repeat("z", 34)
}

// TestLoadSchedulesTheDailyDigestByDefault covers the reason the operator saw
// no daily updates at all: an opt-in digest that nobody opted into is a digest
// that never arrives, and its silence looks exactly like a healthy quiet day.
func TestLoadSchedulesTheDailyDigestByDefault(t *testing.T) {
	cfg, err := Load(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.TelegramDigestEnabled {
		t.Fatal("the daily digest must default on")
	}
	if cfg.TelegramDigestHour != 8 || cfg.TelegramDigestMinute != 0 {
		t.Fatalf("digest defaults to %02d:%02d, want 08:00", cfg.TelegramDigestHour, cfg.TelegramDigestMinute)
	}
}

func TestLoadValidatesDigestSchedule(t *testing.T) {
	cfg, err := Load(map[string]string{
		"OPS_TELEGRAM_DIGEST_ENABLED": "false",
		"OPS_TELEGRAM_DIGEST_HOUR":    "21",
		"OPS_TELEGRAM_DIGEST_MINUTE":  "45",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TelegramDigestEnabled || cfg.TelegramDigestHour != 21 || cfg.TelegramDigestMinute != 45 {
		t.Fatalf("digest overrides not applied: %+v", cfg)
	}
	for key, bad := range map[string]string{
		"OPS_TELEGRAM_DIGEST_HOUR":    "24",
		"OPS_TELEGRAM_DIGEST_MINUTE":  "60",
		"OPS_TELEGRAM_DIGEST_ENABLED": "yes",
	} {
		if _, err := Load(map[string]string{key: bad}); err == nil {
			t.Fatalf("%s=%s was accepted", key, bad)
		}
	}
}
