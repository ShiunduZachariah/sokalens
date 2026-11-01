package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	ModeObserve = "observe"

	defaultAddress           = ":8090"
	defaultEnvironment       = "local"
	defaultLogLevel          = "info"
	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 10 * time.Second
	defaultWriteTimeout      = 10 * time.Second
	defaultIdleTimeout       = 60 * time.Second
	defaultShutdownTimeout   = 10 * time.Second
	defaultMaxHeaderBytes    = 64 * 1024
	defaultAlertMaxBodyBytes = 64 * 1024
	defaultAlertMaxClockSkew = 5 * time.Minute
	defaultAlertReplayLimit  = 10000
	defaultDBMaxOpenConns    = 10
	defaultDBMaxIdleConns    = 5
	defaultDBConnMaxLifetime = 30 * time.Minute
	defaultDBConnMaxIdleTime = 5 * time.Minute
	defaultDBPingTimeout     = 3 * time.Second
	defaultIncidentTimeout   = 2 * time.Second
	defaultCorrelationWindow = 15 * time.Minute

	// Telegram ops bridge (docs/operations/telegram-agent-ops.md). Every key is
	// OPS_-prefixed to match the 23 existing vars; the bridge is off unless
	// OPS_TELEGRAM_ENABLED is explicitly true.
	defaultTelegramPollTimeoutSeconds = 30
	defaultTelegramSendRatePerSec     = 1
	minTelegramPollTimeoutSeconds     = 1
	// Telegram rejects a getUpdates timeout above 50 seconds.
	maxTelegramPollTimeoutSeconds = 50
	minTelegramSendRatePerSec     = 1
	// The daily digest lands at 08:00 in the operator's zone (plan T2 item 4).
	defaultTelegramDigestHour = 8
	// Telegram throttles a bot to roughly 30 messages per second overall.
	maxTelegramSendRatePerSec = 30
	// AI spend cap in whole USD. 60 matches the documented planning cap.
	defaultAIMonthlyCapUSD = 60
	minAIMonthlyCapUSD     = 1
	maxAIMonthlyCapUSD     = 100000
)

// Config contains process-level settings, the bounded alert transport
// contract, and the explicitly enabled durable incident-store boundary.
type Config struct {
	Environment               string
	Address                   string
	LogLevel                  string
	AutonomyMode              string
	ReadHeaderTimeout         time.Duration
	ReadTimeout               time.Duration
	WriteTimeout              time.Duration
	IdleTimeout               time.Duration
	ShutdownTimeout           time.Duration
	MaxHeaderBytes            int
	AlertSigningSecret        string
	AlertMaxBodyBytes         int64
	AlertMaxClockSkew         time.Duration
	AlertReplayLimit          int
	IncidentStoreEnabled      bool
	DatabaseURL               string
	DBMaxOpenConns            int
	DBMaxIdleConns            int
	DBConnMaxLifetime         time.Duration
	DBConnMaxIdleTime         time.Duration
	DBPingTimeout             time.Duration
	IncidentOperationTimeout  time.Duration
	IncidentCorrelationWindow time.Duration

	// TelegramEnabled is the master kill switch for the ops bridge. When it is
	// false nothing else in this block is read or validated.
	TelegramEnabled bool
	// TelegramBotToken is a production credential. It must never appear in a
	// log line, an error, or a rendered config dump.
	TelegramBotToken string
	// TelegramChatID is the single chat the bridge talks to. Group and
	// supergroup identifiers are negative.
	TelegramChatID int64
	// TelegramAllowedUserIDs is the sender allowlist. An empty slice denies
	// every sender; it never means "allow everyone".
	TelegramAllowedUserIDs []int64
	// TelegramPollTimeoutSeconds is the getUpdates long-poll timeout.
	TelegramPollTimeoutSeconds int
	// TelegramRunbooksEnabled gates /run and /confirm independently of the
	// bridge itself, so read-only commands can ship without runbook authority.
	TelegramRunbooksEnabled bool
	// TelegramSendRatePerSec bounds outbound sends.
	TelegramSendRatePerSec int
	// TelegramDigestEnabled gates the daily digest (plan T2 item 4). It
	// defaults to true: the digest is the daily proof that the bridge is
	// alive, and its silence is indistinguishable from a broken bridge, so it
	// has to be opt-out rather than opt-in. It does nothing when the bridge
	// itself is disabled.
	TelegramDigestEnabled bool
	// TelegramDigestHour and TelegramDigestMinute are the local send time in
	// the operator's zone (EAT in production). Default 08:00.
	TelegramDigestHour   int
	TelegramDigestMinute int
	// AIMonthlyCapUSD is the AI spend ceiling /budget reports against and
	// derives its escalation tier from. Spend is reported as a percentage of
	// this, so a wrong value produces a confidently wrong tier.
	AIMonthlyCapUSD int

	// GitHub issue filing (/issue, Phase T3). Optional: with no token the
	// command reports that filing is unavailable and returns the assembled
	// body as text, rather than silently dropping the report.
	GitHubToken string
	GitHubOwner string
	GitHubRepo  string

	// Email is the notification fallback (Phase T2). Telegram must never be
	// the only delivery path: a Telegram outage during an incident would
	// otherwise lose the notification entirely.
	EmailSMTPAddress  string
	EmailSMTPUsername string
	EmailSMTPPassword string
	EmailFromAddress  string
	EmailToAddress    string

	// RunbookServices bounds which containers /run restart-service may touch.
	// A free-form target would let a compromised chat account reach anything
	// the Docker daemon can see.
	RunbookServices []string
	// DockerEnabled turns on container control. It is off by default and
	// stays off unless the socket is deliberately mounted.
	//
	// SECURITY: the Docker socket is root-equivalent on the host. Enabling
	// this widens the blast radius of a stolen bot token from "read ops data"
	// to "control every container on the box". Read docs/operations/telegram-agent-ops.md
	// before turning it on.
	DockerEnabled bool
	// DockerSocketPath is the Engine API socket.
	DockerSocketPath string

	// PrometheusURL backs /resources. The observability stack already runs in
	// the compose network, so host CPU, memory and disk come from there rather
	// than from inside this container, which can only see its own cgroup.
	PrometheusURL string
}

// GitHubConfigured reports whether /issue can reach GitHub.
func (c Config) GitHubConfigured() bool {
	return c.GitHubToken != "" && c.GitHubOwner != "" && c.GitHubRepo != ""
}

// EmailConfigured reports whether the notification fallback is available.
func (c Config) EmailConfigured() bool {
	return c.EmailSMTPAddress != "" && c.EmailFromAddress != "" && c.EmailToAddress != ""
}

func Load(overrides map[string]string) (Config, error) {
	env := mergedEnv(overrides)

	cfg := Config{
		Environment:  value(env, "OPS_ENV", defaultEnvironment),
		Address:      value(env, "OPS_HTTP_ADDRESS", defaultAddress),
		LogLevel:     strings.ToLower(value(env, "OPS_LOG_LEVEL", defaultLogLevel)),
		AutonomyMode: strings.ToLower(value(env, "OPS_AUTONOMY_MODE", ModeObserve)),
	}

	var err error
	if cfg.ReadHeaderTimeout, err = duration(env, "OPS_HTTP_READ_HEADER_TIMEOUT", defaultReadHeaderTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ReadTimeout, err = duration(env, "OPS_HTTP_READ_TIMEOUT", defaultReadTimeout); err != nil {
		return Config{}, err
	}
	if cfg.WriteTimeout, err = duration(env, "OPS_HTTP_WRITE_TIMEOUT", defaultWriteTimeout); err != nil {
		return Config{}, err
	}
	if cfg.IdleTimeout, err = duration(env, "OPS_HTTP_IDLE_TIMEOUT", defaultIdleTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = duration(env, "OPS_SHUTDOWN_TIMEOUT", defaultShutdownTimeout); err != nil {
		return Config{}, err
	}
	if cfg.MaxHeaderBytes, err = boundedInt(env, "OPS_HTTP_MAX_HEADER_BYTES", defaultMaxHeaderBytes, 4*1024, 1024*1024); err != nil {
		return Config{}, err
	}
	cfg.AlertSigningSecret = strings.TrimSpace(env["OPS_ALERT_SIGNING_SECRET"])
	alertBodyBytes, err := boundedInt(env, "OPS_ALERT_MAX_BODY_BYTES", defaultAlertMaxBodyBytes, 1024, 1024*1024)
	if err != nil {
		return Config{}, err
	}
	cfg.AlertMaxBodyBytes = int64(alertBodyBytes)
	if cfg.AlertMaxClockSkew, err = duration(env, "OPS_ALERT_MAX_CLOCK_SKEW", defaultAlertMaxClockSkew); err != nil {
		return Config{}, err
	}
	if cfg.AlertReplayLimit, err = boundedInt(env, "OPS_ALERT_REPLAY_LIMIT", defaultAlertReplayLimit, 100, 100000); err != nil {
		return Config{}, err
	}
	if cfg.IncidentStoreEnabled, err = boolean(env, "OPS_INCIDENT_STORE_ENABLED", false); err != nil {
		return Config{}, err
	}
	cfg.DatabaseURL = strings.TrimSpace(env["OPS_DATABASE_URL"])
	if cfg.DBMaxOpenConns, err = boundedInt(env, "OPS_DB_MAX_OPEN_CONNS", defaultDBMaxOpenConns, 1, 100); err != nil {
		return Config{}, err
	}
	if cfg.DBMaxIdleConns, err = boundedInt(env, "OPS_DB_MAX_IDLE_CONNS", defaultDBMaxIdleConns, 0, 100); err != nil {
		return Config{}, err
	}
	if cfg.DBConnMaxLifetime, err = duration(env, "OPS_DB_CONN_MAX_LIFETIME", defaultDBConnMaxLifetime); err != nil {
		return Config{}, err
	}
	if cfg.DBConnMaxIdleTime, err = duration(env, "OPS_DB_CONN_MAX_IDLE_TIME", defaultDBConnMaxIdleTime); err != nil {
		return Config{}, err
	}
	if cfg.DBPingTimeout, err = duration(env, "OPS_DB_PING_TIMEOUT", defaultDBPingTimeout); err != nil {
		return Config{}, err
	}
	if cfg.IncidentOperationTimeout, err = duration(env, "OPS_INCIDENT_OPERATION_TIMEOUT", defaultIncidentTimeout); err != nil {
		return Config{}, err
	}
	if cfg.IncidentCorrelationWindow, err = duration(env, "OPS_INCIDENT_CORRELATION_WINDOW", defaultCorrelationWindow); err != nil {
		return Config{}, err
	}

	if cfg.TelegramEnabled, err = boolean(env, "OPS_TELEGRAM_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.TelegramRunbooksEnabled, err = boolean(env, "OPS_TELEGRAM_RUNBOOKS_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.TelegramPollTimeoutSeconds, err = boundedInt(env, "OPS_TELEGRAM_POLL_TIMEOUT_SECONDS", defaultTelegramPollTimeoutSeconds, minTelegramPollTimeoutSeconds, maxTelegramPollTimeoutSeconds); err != nil {
		return Config{}, err
	}
	if cfg.TelegramSendRatePerSec, err = boundedInt(env, "OPS_TELEGRAM_SEND_RATE_PER_SEC", defaultTelegramSendRatePerSec, minTelegramSendRatePerSec, maxTelegramSendRatePerSec); err != nil {
		return Config{}, err
	}
	if cfg.TelegramDigestEnabled, err = boolean(env, "OPS_TELEGRAM_DIGEST_ENABLED", true); err != nil {
		return Config{}, err
	}
	if cfg.TelegramDigestHour, err = boundedInt(env, "OPS_TELEGRAM_DIGEST_HOUR", defaultTelegramDigestHour, 0, 23); err != nil {
		return Config{}, err
	}
	if cfg.TelegramDigestMinute, err = boundedInt(env, "OPS_TELEGRAM_DIGEST_MINUTE", 0, 0, 59); err != nil {
		return Config{}, err
	}
	if cfg.AIMonthlyCapUSD, err = boundedInt(env, "OPS_AI_MONTHLY_CAP_USD", defaultAIMonthlyCapUSD, minAIMonthlyCapUSD, maxAIMonthlyCapUSD); err != nil {
		return Config{}, err
	}

	cfg.GitHubToken = strings.TrimSpace(env["OPS_GITHUB_TOKEN"])
	cfg.GitHubOwner = strings.TrimSpace(env["OPS_GITHUB_OWNER"])
	cfg.GitHubRepo = strings.TrimSpace(env["OPS_GITHUB_REPO"])

	cfg.EmailSMTPAddress = strings.TrimSpace(env["OPS_EMAIL_SMTP_ADDRESS"])
	cfg.EmailSMTPUsername = strings.TrimSpace(env["OPS_EMAIL_SMTP_USERNAME"])
	cfg.EmailSMTPPassword = env["OPS_EMAIL_SMTP_PASSWORD"]
	cfg.EmailFromAddress = strings.TrimSpace(env["OPS_EMAIL_FROM_ADDRESS"])
	cfg.EmailToAddress = strings.TrimSpace(env["OPS_EMAIL_TO_ADDRESS"])

	if cfg.DockerEnabled, err = boolean(env, "OPS_DOCKER_ENABLED", false); err != nil {
		return Config{}, err
	}
	cfg.DockerSocketPath = value(env, "OPS_DOCKER_SOCKET_PATH", "/var/run/docker.sock")
	cfg.RunbookServices = splitList(value(env, "OPS_RUNBOOK_SERVICES", "sports-prediction-platform-worker-1,sports-prediction-platform-api-1"))
	cfg.PrometheusURL = strings.TrimSpace(value(env, "OPS_PROMETHEUS_URL", "http://prometheus:9090"))
	// The credential trio is only read when the bridge is on: a disabled
	// bridge must not be able to fail the process over unused values.
	if cfg.TelegramEnabled {
		cfg.TelegramBotToken = strings.TrimSpace(env["OPS_TELEGRAM_BOT_TOKEN"])
		if cfg.TelegramChatID, err = telegramChatID(env); err != nil {
			return Config{}, err
		}
		if cfg.TelegramAllowedUserIDs, err = telegramAllowedUserIDs(env); err != nil {
			return Config{}, err
		}
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Environment) == "" {
		return errors.New("OPS_ENV must not be empty")
	}
	if err := validateAddress(c.Address); err != nil {
		return err
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("OPS_LOG_LEVEL must be debug, info, warn, or error")
	}
	// Wave 1 intentionally fails closed. Adding another accepted value requires
	// a later policy implementation and acceptance gate, not a config change.
	if c.AutonomyMode != ModeObserve {
		return fmt.Errorf("OPS_AUTONOMY_MODE must be %q in this release", ModeObserve)
	}
	if c.ReadTimeout < c.ReadHeaderTimeout {
		return errors.New("OPS_HTTP_READ_TIMEOUT must be at least OPS_HTTP_READ_HEADER_TIMEOUT")
	}
	if c.AlertSigningSecret != "" && len(c.AlertSigningSecret) < 32 {
		return errors.New("OPS_ALERT_SIGNING_SECRET must contain at least 32 characters when alert ingestion is enabled")
	}
	if c.AlertMaxClockSkew > 15*time.Minute {
		return errors.New("OPS_ALERT_MAX_CLOCK_SKEW must be no more than 15m")
	}
	if c.DBMaxIdleConns > c.DBMaxOpenConns {
		return errors.New("OPS_DB_MAX_IDLE_CONNS must not exceed OPS_DB_MAX_OPEN_CONNS")
	}
	if c.DBPingTimeout > 10*time.Second {
		return errors.New("OPS_DB_PING_TIMEOUT must be no more than 10s")
	}
	if c.IncidentOperationTimeout > 10*time.Second {
		return errors.New("OPS_INCIDENT_OPERATION_TIMEOUT must be no more than 10s")
	}
	if c.IncidentCorrelationWindow > 24*time.Hour {
		return errors.New("OPS_INCIDENT_CORRELATION_WINDOW must be no more than 24h")
	}
	if c.IncidentStoreEnabled {
		if c.AlertSigningSecret == "" {
			return errors.New("OPS_ALERT_SIGNING_SECRET is required when OPS_INCIDENT_STORE_ENABLED is true")
		}
		if err := validateDatabaseURL(c.DatabaseURL); err != nil {
			return err
		}
		if strings.EqualFold(c.Environment, "production") {
			if isDevelopmentDatabaseURL(c.DatabaseURL) {
				return errors.New("OPS_DATABASE_URL must not use development credentials in production")
			}
			if !usesRequiredProductionTLS(c.DatabaseURL) {
				return errors.New("OPS_DATABASE_URL must set sslmode=require, verify-ca, or verify-full in production")
			}
		}
	}
	if err := c.validateTelegram(); err != nil {
		return err
	}
	return nil
}

// validateTelegram fails closed. Every message below names the key only: the
// bot token's value must never reach an error string, a log, or a test dump.
func (c Config) validateTelegram() error {
	if c.TelegramPollTimeoutSeconds < minTelegramPollTimeoutSeconds || c.TelegramPollTimeoutSeconds > maxTelegramPollTimeoutSeconds {
		return fmt.Errorf("OPS_TELEGRAM_POLL_TIMEOUT_SECONDS must be between %d and %d", minTelegramPollTimeoutSeconds, maxTelegramPollTimeoutSeconds)
	}
	if c.TelegramSendRatePerSec < minTelegramSendRatePerSec {
		return errors.New("OPS_TELEGRAM_SEND_RATE_PER_SEC must be greater than zero")
	}
	if !c.TelegramEnabled {
		// Disabled means disabled: the credential trio is not inspected.
		return nil
	}
	if err := validateTelegramBotToken(c.TelegramBotToken); err != nil {
		return err
	}
	if c.TelegramChatID == 0 {
		return errors.New("OPS_TELEGRAM_CHAT_ID is required when OPS_TELEGRAM_ENABLED is true")
	}
	if len(c.TelegramAllowedUserIDs) == 0 {
		// An empty allowlist denies everything. It is never a wildcard, so a
		// missing value is a configuration error rather than an open door.
		return errors.New("OPS_TELEGRAM_ALLOWED_USER_IDS must list at least one user id when OPS_TELEGRAM_ENABLED is true")
	}
	for _, id := range c.TelegramAllowedUserIDs {
		if id <= 0 {
			return errors.New("OPS_TELEGRAM_ALLOWED_USER_IDS entries must be positive user ids")
		}
	}
	return nil
}

// validateTelegramBotToken checks shape only, and reports shape only.
func validateTelegramBotToken(token string) error {
	if token == "" {
		return errors.New("OPS_TELEGRAM_BOT_TOKEN is required when OPS_TELEGRAM_ENABLED is true")
	}
	if strings.ContainsAny(token, " \t\r\n") {
		return errors.New("OPS_TELEGRAM_BOT_TOKEN must not contain whitespace")
	}
	botID, secret, ok := strings.Cut(token, ":")
	if !ok || botID == "" || secret == "" {
		return errors.New("OPS_TELEGRAM_BOT_TOKEN must be in <bot_id>:<secret> form")
	}
	if _, err := strconv.ParseInt(botID, 10, 64); err != nil {
		return errors.New("OPS_TELEGRAM_BOT_TOKEN must start with a numeric bot id")
	}
	return nil
}

// telegramChatID parses OPS_TELEGRAM_CHAT_ID. Group and supergroup ids are
// negative, so only zero and unparseable values are rejected.
func telegramChatID(env map[string]string) (int64, error) {
	raw := strings.TrimSpace(env["OPS_TELEGRAM_CHAT_ID"])
	if raw == "" {
		return 0, errors.New("OPS_TELEGRAM_CHAT_ID is required when OPS_TELEGRAM_ENABLED is true")
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, errors.New("OPS_TELEGRAM_CHAT_ID must be a 64-bit integer chat id")
	}
	if parsed == 0 {
		return 0, errors.New("OPS_TELEGRAM_CHAT_ID must not be zero")
	}
	return parsed, nil
}

// telegramAllowedUserIDs parses the comma-separated sender allowlist. Empty,
// blank, and unparseable inputs are errors rather than an empty allowlist,
// because an operator who meant to allow nobody would disable the bridge.
func telegramAllowedUserIDs(env map[string]string) ([]int64, error) {
	raw := strings.TrimSpace(env["OPS_TELEGRAM_ALLOWED_USER_IDS"])
	if raw == "" {
		return nil, errors.New("OPS_TELEGRAM_ALLOWED_USER_IDS is required when OPS_TELEGRAM_ENABLED is true")
	}
	fields := strings.Split(raw, ",")
	ids := make([]int64, 0, len(fields))
	seen := make(map[int64]struct{}, len(fields))
	for _, field := range fields {
		trimmed := strings.TrimSpace(field)
		if trimmed == "" {
			return nil, errors.New("OPS_TELEGRAM_ALLOWED_USER_IDS must not contain empty entries")
		}
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return nil, errors.New("OPS_TELEGRAM_ALLOWED_USER_IDS must be a comma-separated list of 64-bit integer user ids")
		}
		if parsed <= 0 {
			return nil, errors.New("OPS_TELEGRAM_ALLOWED_USER_IDS entries must be positive user ids")
		}
		if _, duplicate := seen[parsed]; duplicate {
			continue
		}
		seen[parsed] = struct{}{}
		ids = append(ids, parsed)
	}
	if len(ids) == 0 {
		return nil, errors.New("OPS_TELEGRAM_ALLOWED_USER_IDS must list at least one user id when OPS_TELEGRAM_ENABLED is true")
	}
	return ids, nil
}

func validateDatabaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" || parsed.Path == "" || parsed.Path == "/" {
		return errors.New("OPS_DATABASE_URL must be a valid postgres URL when incident persistence is enabled")
	}
	return nil
}

func isDevelopmentDatabaseURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if parsed.User == nil {
		return false
	}
	password, _ := parsed.User.Password()
	return parsed.User.Username() == "sports" && password == "sports"
}

func usesRequiredProductionTLS(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Query().Get("sslmode")) {
	case "require", "verify-ca", "verify-full":
		return true
	default:
		return false
	}
}

func validateAddress(address string) error {
	if strings.TrimSpace(address) == "" {
		return errors.New("OPS_HTTP_ADDRESS must not be empty")
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("OPS_HTTP_ADDRESS must be host:port: %w", err)
	}
	parsed, err := strconv.Atoi(port)
	if err != nil || parsed < 1 || parsed > 65535 {
		return errors.New("OPS_HTTP_ADDRESS port must be between 1 and 65535")
	}
	return nil
}

func mergedEnv(overrides map[string]string) map[string]string {
	env := make(map[string]string)
	for _, pair := range os.Environ() {
		key, val, ok := strings.Cut(pair, "=")
		if ok {
			env[key] = val
		}
	}
	for key, val := range overrides {
		env[key] = val
	}
	return env
}

func value(env map[string]string, key, fallback string) string {
	if val := strings.TrimSpace(env[key]); val != "" {
		return val
	}
	return fallback
}

func duration(env map[string]string, key string, fallback time.Duration) (time.Duration, error) {
	raw := value(env, key, fallback.String())
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %w", key, err)
	}
	if parsed <= 0 || parsed > 24*time.Hour {
		return 0, fmt.Errorf("%s must be greater than zero and no more than 24h", key)
	}
	return parsed, nil
}

func boundedInt(env map[string]string, key string, fallback, minimum, maximum int) (int, error) {
	raw := value(env, key, strconv.Itoa(fallback))
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number: %w", key, err)
	}
	if parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", key, minimum, maximum)
	}
	return parsed, nil
}

func boolean(env map[string]string, key string, fallback bool) (bool, error) {
	raw := strings.ToLower(value(env, key, strconv.FormatBool(fallback)))
	if raw != "true" && raw != "false" {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return raw == "true", nil
}

// splitList parses a comma-separated allowlist, trimming and dropping blanks.
func splitList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
