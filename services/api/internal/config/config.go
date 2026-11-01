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
	defaultEnvironment = "local"
	defaultPort        = 8080
	defaultDatabaseURL = "postgres://sports:sports@localhost:5433/sports_prediction?sslmode=disable"
	defaultRedisURL    = "redis://localhost:6379/0"
	defaultOpenAPIPath = "packages/contracts/openapi.yaml"
	localDevAppSecret  = "local-dev-secret-change-me"

	defaultDBMaxOpenConns     = 20
	defaultDBMaxIdleConns     = 10
	defaultDBConnMaxLifetime  = 30 * time.Minute
	defaultDBConnMaxIdleTime  = 5 * time.Minute
	defaultAPIRequestTimeout  = 2750 * time.Millisecond
	defaultAuthRequestTimeout = 8 * time.Second
	defaultReadHeaderTimeout  = 5 * time.Second
	defaultReadTimeout        = 10 * time.Second
	defaultWriteTimeout       = 10 * time.Second
	defaultIdleTimeout        = 60 * time.Second
	defaultMaxHeaderBytes     = 64 * 1024
	defaultGuestViewLimit     = 4
	defaultGuestViewWindow    = 7 * 24 * time.Hour
	defaultUserSessionTTL     = 30 * 24 * time.Hour
	defaultAdminSessionTTL    = 8 * time.Hour
	defaultAuthTokenTTL       = 30 * time.Minute
	defaultPublicResultHours  = 24
)

type Config struct {
	Environment                       string
	Port                              int
	DatabaseURL                       string
	RedisURL                          string
	OpenAPIPath                       string
	TypesenseURL                      string
	TypesenseAPIKey                   string
	AppSecret                         string
	APIServiceEnableRealProviderCalls bool
	DBMaxOpenConns                    int
	DBMaxIdleConns                    int
	DBConnMaxLifetime                 time.Duration
	DBConnMaxIdleTime                 time.Duration
	APIRequestTimeout                 time.Duration
	AuthRequestTimeout                time.Duration
	HTTPReadHeaderTimeout             time.Duration
	HTTPReadTimeout                   time.Duration
	HTTPWriteTimeout                  time.Duration
	HTTPIdleTimeout                   time.Duration
	HTTPMaxHeaderBytes                int
	GuestAnalysisViewLimit            int
	GuestAnalysisWindow               time.Duration
	UserSessionTTL                    time.Duration
	AdminSessionTTL                   time.Duration
	AdminMFAEnabled                   bool
	AuthTokenTTL                      time.Duration
	TurnstileSecretKey                string
	TurnstileRequired                 bool
	BillingProvider                   string
	BillingMode                       string
	BillingWebhookSecret              string
	PublicWebURL                      string
	PublicResultDetailWindow          time.Duration
}

func Load(overrides map[string]string) (Config, error) {
	env := mergedEnv(overrides)

	port, err := parsePort(value(env, "PORT", strconv.Itoa(defaultPort)))
	if err != nil {
		return Config{}, err
	}
	dbMaxOpenConns, err := parseBoundedInt(value(env, "DB_MAX_OPEN_CONNS", strconv.Itoa(defaultDBMaxOpenConns)), "DB_MAX_OPEN_CONNS", 1, 200)
	if err != nil {
		return Config{}, err
	}
	dbMaxIdleConns, err := parseBoundedInt(value(env, "DB_MAX_IDLE_CONNS", strconv.Itoa(defaultDBMaxIdleConns)), "DB_MAX_IDLE_CONNS", 0, 200)
	if err != nil {
		return Config{}, err
	}
	dbConnMaxLifetime, err := parseDuration(value(env, "DB_CONN_MAX_LIFETIME", defaultDBConnMaxLifetime.String()), "DB_CONN_MAX_LIFETIME")
	if err != nil {
		return Config{}, err
	}
	dbConnMaxIdleTime, err := parseDuration(value(env, "DB_CONN_MAX_IDLE_TIME", defaultDBConnMaxIdleTime.String()), "DB_CONN_MAX_IDLE_TIME")
	if err != nil {
		return Config{}, err
	}
	apiRequestTimeout, err := parseDuration(value(env, "API_REQUEST_TIMEOUT", defaultAPIRequestTimeout.String()), "API_REQUEST_TIMEOUT")
	if err != nil {
		return Config{}, err
	}
	authRequestTimeout, err := parseDuration(value(env, "AUTH_REQUEST_TIMEOUT", defaultAuthRequestTimeout.String()), "AUTH_REQUEST_TIMEOUT")
	if err != nil {
		return Config{}, err
	}
	httpReadHeaderTimeout, err := parseDuration(value(env, "HTTP_READ_HEADER_TIMEOUT", defaultReadHeaderTimeout.String()), "HTTP_READ_HEADER_TIMEOUT")
	if err != nil {
		return Config{}, err
	}
	httpReadTimeout, err := parseDuration(value(env, "HTTP_READ_TIMEOUT", defaultReadTimeout.String()), "HTTP_READ_TIMEOUT")
	if err != nil {
		return Config{}, err
	}
	httpWriteTimeout, err := parseDuration(value(env, "HTTP_WRITE_TIMEOUT", defaultWriteTimeout.String()), "HTTP_WRITE_TIMEOUT")
	if err != nil {
		return Config{}, err
	}
	httpIdleTimeout, err := parseDuration(value(env, "HTTP_IDLE_TIMEOUT", defaultIdleTimeout.String()), "HTTP_IDLE_TIMEOUT")
	if err != nil {
		return Config{}, err
	}
	httpMaxHeaderBytes, err := parseBoundedInt(value(env, "HTTP_MAX_HEADER_BYTES", strconv.Itoa(defaultMaxHeaderBytes)), "HTTP_MAX_HEADER_BYTES", 4096, 1024*1024)
	if err != nil {
		return Config{}, err
	}
	guestAnalysisViewLimit, err := parseBoundedInt(value(env, "GUEST_ANALYSIS_VIEW_LIMIT", strconv.Itoa(defaultGuestViewLimit)), "GUEST_ANALYSIS_VIEW_LIMIT", 1, 100)
	if err != nil {
		return Config{}, err
	}
	guestAnalysisWindow, err := parseLongDuration(value(env, "GUEST_ANALYSIS_WINDOW", defaultGuestViewWindow.String()), "GUEST_ANALYSIS_WINDOW", time.Hour, 90*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	userSessionTTL, err := parseLongDuration(value(env, "USER_SESSION_TTL", defaultUserSessionTTL.String()), "USER_SESSION_TTL", time.Hour, 90*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	adminSessionTTL, err := parseLongDuration(value(env, "ADMIN_SESSION_TTL", defaultAdminSessionTTL.String()), "ADMIN_SESSION_TTL", 5*time.Minute, 8*time.Hour)
	if err != nil {
		return Config{}, err
	}
	authTokenTTL, err := parseLongDuration(value(env, "AUTH_TOKEN_TTL", defaultAuthTokenTTL.String()), "AUTH_TOKEN_TTL", 5*time.Minute, 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	publicResultHours, err := parseBoundedInt(value(env, "PUBLIC_RESULT_DETAIL_HOURS", strconv.Itoa(defaultPublicResultHours)), "PUBLIC_RESULT_DETAIL_HOURS", 1, 168)
	if err != nil {
		return Config{}, err
	}
	billingMode := value(env, "BILLING_MODE", "disabled")
	if billingMode != "disabled" && billingMode != "sandbox" && billingMode != "live" {
		return Config{}, errors.New("BILLING_MODE must be disabled, sandbox, or live")
	}
	billingProvider := value(env, "BILLING_PROVIDER", "sandbox")
	billingWebhookSecret := env["BILLING_WEBHOOK_SECRET"]
	if billingMode == "live" && billingProvider == "sandbox" {
		return Config{}, errors.New("BILLING_PROVIDER must not be sandbox in live billing mode")
	}
	if billingMode != "disabled" && billingWebhookSecret == "" {
		return Config{}, errors.New("BILLING_WEBHOOK_SECRET is required when billing is enabled")
	}
	if dbMaxIdleConns > dbMaxOpenConns {
		return Config{}, errors.New("DB_MAX_IDLE_CONNS must not exceed DB_MAX_OPEN_CONNS")
	}
	if httpReadTimeout < httpReadHeaderTimeout {
		return Config{}, errors.New("HTTP_READ_TIMEOUT must be greater than or equal to HTTP_READ_HEADER_TIMEOUT")
	}
	if apiRequestTimeout >= 3*time.Second {
		return Config{}, errors.New("API_REQUEST_TIMEOUT must be less than 3s")
	}
	if httpWriteTimeout <= apiRequestTimeout {
		return Config{}, errors.New("HTTP_WRITE_TIMEOUT must be greater than API_REQUEST_TIMEOUT")
	}
	if authRequestTimeout <= apiRequestTimeout || authRequestTimeout >= httpWriteTimeout {
		return Config{}, errors.New("AUTH_REQUEST_TIMEOUT must be greater than API_REQUEST_TIMEOUT and less than HTTP_WRITE_TIMEOUT")
	}

	cfg := Config{
		Environment:                       value(env, "APP_ENV", defaultEnvironment),
		Port:                              port,
		DatabaseURL:                       value(env, "DATABASE_URL", defaultDatabaseURL),
		RedisURL:                          value(env, "REDIS_URL", defaultRedisURL),
		OpenAPIPath:                       value(env, "OPENAPI_PATH", defaultOpenAPIPath),
		TypesenseURL:                      value(env, "TYPESENSE_URL", ""),
		TypesenseAPIKey:                   value(env, "TYPESENSE_API_KEY", ""),
		AppSecret:                         env["APP_SECRET"],
		APIServiceEnableRealProviderCalls: parseBool(value(env, "API_SERVICE_ENABLE_REAL_PROVIDER_CALLS", "false")),
		DBMaxOpenConns:                    dbMaxOpenConns,
		DBMaxIdleConns:                    dbMaxIdleConns,
		DBConnMaxLifetime:                 dbConnMaxLifetime,
		DBConnMaxIdleTime:                 dbConnMaxIdleTime,
		APIRequestTimeout:                 apiRequestTimeout,
		AuthRequestTimeout:                authRequestTimeout,
		HTTPReadHeaderTimeout:             httpReadHeaderTimeout,
		HTTPReadTimeout:                   httpReadTimeout,
		HTTPWriteTimeout:                  httpWriteTimeout,
		HTTPIdleTimeout:                   httpIdleTimeout,
		HTTPMaxHeaderBytes:                httpMaxHeaderBytes,
		GuestAnalysisViewLimit:            guestAnalysisViewLimit,
		GuestAnalysisWindow:               guestAnalysisWindow,
		UserSessionTTL:                    userSessionTTL,
		AdminSessionTTL:                   adminSessionTTL,
		AdminMFAEnabled:                   parseBool(value(env, "ADMIN_MFA_ENABLED", "true")),
		AuthTokenTTL:                      authTokenTTL,
		TurnstileSecretKey:                env["TURNSTILE_SECRET_KEY"],
		TurnstileRequired:                 parseBool(value(env, "TURNSTILE_REQUIRED", "false")),
		BillingProvider:                   billingProvider,
		BillingMode:                       billingMode,
		BillingWebhookSecret:              billingWebhookSecret,
		PublicWebURL:                      value(env, "PUBLIC_WEB_URL", "http://localhost:3000"),
		PublicResultDetailWindow:          time.Duration(publicResultHours) * time.Hour,
	}
	if cfg.TurnstileRequired && cfg.TurnstileSecretKey == "" {
		return Config{}, errors.New("TURNSTILE_SECRET_KEY is required when TURNSTILE_REQUIRED is enabled")
	}
	if cfg.Environment == "production" && (!cfg.TurnstileRequired || cfg.TurnstileSecretKey == "") {
		return Config{}, errors.New("Turnstile must be required and configured in production")
	}
	parsedPublicWebURL, err := url.Parse(cfg.PublicWebURL)
	if err != nil || parsedPublicWebURL.Host == "" || (parsedPublicWebURL.Scheme != "http" && parsedPublicWebURL.Scheme != "https") {
		return Config{}, errors.New("PUBLIC_WEB_URL must be an absolute http or https URL")
	}
	if cfg.Environment == "production" {
		hostIP := net.ParseIP(parsedPublicWebURL.Hostname())
		if parsedPublicWebURL.Scheme != "https" || strings.EqualFold(parsedPublicWebURL.Hostname(), "localhost") || (hostIP != nil && hostIP.IsLoopback()) {
			return Config{}, errors.New("PUBLIC_WEB_URL must use https and a public hostname in production")
		}
	}

	if cfg.Environment != "local" && cfg.AppSecret == "" {
		return Config{}, errors.New("APP_SECRET is required outside local")
	}
	if cfg.Environment == "production" && cfg.AppSecret == localDevAppSecret {
		return Config{}, errors.New("APP_SECRET must not use the local development value in production")
	}
	if cfg.Environment == "production" && isDevelopmentDatabaseURL(cfg.DatabaseURL) {
		return Config{}, errors.New("DATABASE_URL must not use development credentials in production")
	}
	if cfg.Environment == "production" && isUnauthenticatedRedisURL(cfg.RedisURL) {
		return Config{}, errors.New("REDIS_URL must use authentication in production")
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

func isUnauthenticatedRedisURL(raw string) bool {
	if raw == defaultRedisURL {
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if parsed.Scheme == "rediss" {
		return false
	}
	if parsed.User == nil {
		return true
	}
	password, hasPassword := parsed.User.Password()
	return !hasPassword || password == ""
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

func parsePort(raw string) (int, error) {
	port, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("PORT must be a number: %w", err)
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("PORT must be between 1 and 65535")
	}
	return port, nil
}

func parseBoundedInt(raw string, name string, minimum int, maximum int) (int, error) {
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number: %w", name, err)
	}
	if parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", name, minimum, maximum)
	}
	return parsed, nil
}

func parseDuration(raw string, name string) (time.Duration, error) {
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %w", name, err)
	}
	if parsed <= 0 || parsed > 24*time.Hour {
		return 0, fmt.Errorf("%s must be greater than zero and no more than 24h", name)
	}
	return parsed, nil
}

func parseLongDuration(raw string, name string, minimum, maximum time.Duration) (time.Duration, error) {
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %w", name, err)
	}
	if parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be between %s and %s", name, minimum, maximum)
	}
	return parsed, nil
}

func parseBool(raw string) bool {
	return raw == "1" || raw == "true" || raw == "TRUE" || raw == "yes" || raw == "YES"
}

func value(env map[string]string, key, fallback string) string {
	if val := env[key]; val != "" {
		return val
	}
	return fallback
}
