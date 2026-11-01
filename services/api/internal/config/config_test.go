package config

import (
	"testing"
	"time"
)

func TestLoadUsesSafeLocalDefaults(t *testing.T) {
	cfg, err := Load(map[string]string{})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Environment != "local" {
		t.Fatalf("Environment = %q, want local", cfg.Environment)
	}
	if cfg.Port != 8080 {
		t.Fatalf("Port = %d, want 8080", cfg.Port)
	}
	if cfg.DatabaseURL == "" {
		t.Fatal("DatabaseURL is empty")
	}
	if cfg.RedisURL == "" {
		t.Fatal("RedisURL is empty")
	}
	if cfg.OpenAPIPath == "" {
		t.Fatal("OpenAPIPath is empty")
	}
	if cfg.APIServiceEnableRealProviderCalls {
		t.Fatal("API service must not enable real provider calls by default")
	}
	if cfg.DBMaxOpenConns != 20 || cfg.DBMaxIdleConns != 10 {
		t.Fatalf("database pool = %d open/%d idle, want 20 open/10 idle", cfg.DBMaxOpenConns, cfg.DBMaxIdleConns)
	}
	if cfg.DBConnMaxLifetime != 30*time.Minute || cfg.DBConnMaxIdleTime != 5*time.Minute {
		t.Fatalf("database lifetimes = %s/%s, want 30m/5m", cfg.DBConnMaxLifetime, cfg.DBConnMaxIdleTime)
	}
	if cfg.APIRequestTimeout != 2750*time.Millisecond || cfg.AuthRequestTimeout != 8*time.Second || cfg.HTTPWriteTimeout != 10*time.Second {
		t.Fatalf("request/auth/write timeouts = %s/%s/%s, want 2.75s/8s/10s", cfg.APIRequestTimeout, cfg.AuthRequestTimeout, cfg.HTTPWriteTimeout)
	}
	if cfg.HTTPReadHeaderTimeout != 5*time.Second || cfg.HTTPReadTimeout != 10*time.Second || cfg.HTTPIdleTimeout != time.Minute {
		t.Fatalf("HTTP timeouts = header %s read %s idle %s", cfg.HTTPReadHeaderTimeout, cfg.HTTPReadTimeout, cfg.HTTPIdleTimeout)
	}
	if cfg.HTTPMaxHeaderBytes != 64*1024 {
		t.Fatalf("HTTPMaxHeaderBytes = %d, want 65536", cfg.HTTPMaxHeaderBytes)
	}
	if cfg.GuestAnalysisViewLimit != 4 || cfg.GuestAnalysisWindow != 7*24*time.Hour {
		t.Fatalf("guest access defaults = %d/%s, want 4/168h", cfg.GuestAnalysisViewLimit, cfg.GuestAnalysisWindow)
	}
	if cfg.UserSessionTTL != 30*24*time.Hour || cfg.AdminSessionTTL != 8*time.Hour {
		t.Fatalf("session defaults = %s/%s", cfg.UserSessionTTL, cfg.AdminSessionTTL)
	}
	if cfg.PublicResultDetailWindow != 24*time.Hour {
		t.Fatalf("public result detail window = %s, want 24h", cfg.PublicResultDetailWindow)
	}
	if cfg.BillingMode != "disabled" || cfg.BillingProvider != "sandbox" {
		t.Fatalf("billing defaults = %s/%s, want disabled/sandbox", cfg.BillingMode, cfg.BillingProvider)
	}
}

func TestLoadAcceptsConcurrencyOverrides(t *testing.T) {
	cfg, err := Load(map[string]string{
		"DB_MAX_OPEN_CONNS":        "12",
		"DB_MAX_IDLE_CONNS":        "4",
		"DB_CONN_MAX_LIFETIME":     "20m",
		"DB_CONN_MAX_IDLE_TIME":    "2m",
		"API_REQUEST_TIMEOUT":      "2500ms",
		"AUTH_REQUEST_TIMEOUT":     "7s",
		"HTTP_READ_HEADER_TIMEOUT": "2s",
		"HTTP_READ_TIMEOUT":        "8s",
		"HTTP_WRITE_TIMEOUT":       "9s",
		"HTTP_IDLE_TIMEOUT":        "45s",
		"HTTP_MAX_HEADER_BYTES":    "32768",
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.DBMaxOpenConns != 12 || cfg.DBMaxIdleConns != 4 || cfg.APIRequestTimeout != 2500*time.Millisecond || cfg.AuthRequestTimeout != 7*time.Second || cfg.HTTPMaxHeaderBytes != 32768 {
		t.Fatalf("concurrency overrides not applied: %+v", cfg)
	}
}

func TestLoadValidatesPublicResultDetailHours(t *testing.T) {
	cfg, err := Load(map[string]string{"PUBLIC_RESULT_DETAIL_HOURS": "36"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicResultDetailWindow != 36*time.Hour {
		t.Fatalf("public result detail window = %s, want 36h", cfg.PublicResultDetailWindow)
	}
	for _, value := range []string{"0", "169", "tomorrow"} {
		if _, err := Load(map[string]string{"PUBLIC_RESULT_DETAIL_HOURS": value}); err == nil {
			t.Fatalf("PUBLIC_RESULT_DETAIL_HOURS=%q returned nil error", value)
		}
	}
}

func TestLoadRejectsUnsafeConcurrencySettings(t *testing.T) {
	tests := map[string]map[string]string{
		"open connections zero": {"DB_MAX_OPEN_CONNS": "0"},
		"idle exceeds open":     {"DB_MAX_OPEN_CONNS": "5", "DB_MAX_IDLE_CONNS": "6"},
		"invalid lifetime":      {"DB_CONN_MAX_LIFETIME": "forever"},
		"read below header":     {"HTTP_READ_HEADER_TIMEOUT": "6s", "HTTP_READ_TIMEOUT": "5s"},
		"write below request":   {"API_REQUEST_TIMEOUT": "6s", "HTTP_WRITE_TIMEOUT": "6s"},
		"request exceeds SLO":   {"API_REQUEST_TIMEOUT": "3s"},
		"auth below request":    {"AUTH_REQUEST_TIMEOUT": "2s"},
		"auth reaches write":    {"AUTH_REQUEST_TIMEOUT": "10s"},
		"header cap too small":  {"HTTP_MAX_HEADER_BYTES": "1024"},
	}
	for name, overrides := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(overrides); err == nil {
				t.Fatal("Load returned nil error")
			}
		})
	}
}

func TestLoadValidatesAccountAndBillingSettings(t *testing.T) {
	cfg, err := Load(map[string]string{
		"GUEST_ANALYSIS_VIEW_LIMIT": "6",
		"GUEST_ANALYSIS_WINDOW":     "336h",
		"USER_SESSION_TTL":          "720h",
		"ADMIN_SESSION_TTL":         "20m",
		"AUTH_TOKEN_TTL":            "45m",
		"BILLING_MODE":              "sandbox",
		"BILLING_PROVIDER":          "sandbox",
		"BILLING_WEBHOOK_SECRET":    "test-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GuestAnalysisViewLimit != 6 || cfg.GuestAnalysisWindow != 14*24*time.Hour {
		t.Fatalf("guest overrides not applied: %+v", cfg)
	}
	for name, overrides := range map[string]map[string]string{
		"limit":         {"GUEST_ANALYSIS_VIEW_LIMIT": "0"},
		"window":        {"GUEST_ANALYSIS_WINDOW": "30m"},
		"admin session": {"ADMIN_SESSION_TTL": "9h"},
		"billing mode":  {"BILLING_MODE": "maybe"},
		"missing hook":  {"BILLING_MODE": "sandbox"},
		"live sandbox":  {"BILLING_MODE": "live", "BILLING_PROVIDER": "sandbox", "BILLING_WEBHOOK_SECRET": "secret"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(overrides); err == nil {
				t.Fatal("Load returned nil error")
			}
		})
	}
}

func TestLoadAcceptsOpenAPIPathOverride(t *testing.T) {
	cfg, err := Load(map[string]string{"OPENAPI_PATH": "/contracts/openapi.yaml"})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.OpenAPIPath != "/contracts/openapi.yaml" {
		t.Fatalf("OpenAPIPath = %q, want /contracts/openapi.yaml", cfg.OpenAPIPath)
	}
}

func TestLoadRejectsInvalidPort(t *testing.T) {
	_, err := Load(map[string]string{"PORT": "abc"})
	if err == nil {
		t.Fatal("Load returned nil error for invalid port")
	}
}

func TestLoadRequiresProductionSecrets(t *testing.T) {
	_, err := Load(map[string]string{"APP_ENV": "production"})
	if err == nil {
		t.Fatal("Load returned nil error without production secrets")
	}
}

func TestLoadRejectsLocalDevelopmentSecretInProduction(t *testing.T) {
	_, err := Load(map[string]string{
		"APP_ENV":      "production",
		"APP_SECRET":   "local-dev-secret-change-me",
		"DATABASE_URL": "postgres://user:pass@db:5432/sports?sslmode=require",
		"REDIS_URL":    "redis://redis:6379/0",
	})
	if err == nil {
		t.Fatal("Load returned nil error for local development secret in production")
	}
}

func TestLoadAcceptsProductionSecrets(t *testing.T) {
	cfg, err := Load(map[string]string{
		"APP_ENV":              "production",
		"APP_SECRET":           "test-production-secret",
		"DATABASE_URL":         "postgres://user:pass@db:5432/sports?sslmode=require",
		"REDIS_URL":            "rediss://:secret@redis.example:6380/0",
		"TURNSTILE_REQUIRED":   "true",
		"TURNSTILE_SECRET_KEY": "test-turnstile-secret",
		"PUBLIC_WEB_URL":       "https://sokalens.com",
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Environment != "production" {
		t.Fatalf("Environment = %q, want production", cfg.Environment)
	}
}

func TestLoadRejectsNonPublicWebURLInProduction(t *testing.T) {
	base := map[string]string{
		"APP_ENV":              "production",
		"APP_SECRET":           "test-production-secret",
		"DATABASE_URL":         "postgres://user:pass@db:5432/sports?sslmode=require",
		"REDIS_URL":            "rediss://:secret@redis.example:6380/0",
		"TURNSTILE_REQUIRED":   "true",
		"TURNSTILE_SECRET_KEY": "test-turnstile-secret",
	}
	for _, publicWebURL := range []string{"http://localhost:3000", "http://sokalens.com", "https://127.0.0.1"} {
		t.Run(publicWebURL, func(t *testing.T) {
			env := make(map[string]string, len(base)+1)
			for key, value := range base {
				env[key] = value
			}
			env["PUBLIC_WEB_URL"] = publicWebURL
			if _, err := Load(env); err == nil {
				t.Fatalf("production accepted PUBLIC_WEB_URL=%q", publicWebURL)
			}
		})
	}
}

func TestLoadRejectsContainerDevelopmentServicesInProduction(t *testing.T) {
	for name, overrides := range map[string]map[string]string{
		"database": {
			"APP_ENV":      "production",
			"APP_SECRET":   "test-production-secret",
			"DATABASE_URL": "postgres://sports:sports@postgres:5432/sports_prediction?sslmode=disable",
			"REDIS_URL":    "rediss://:secret@redis.example:6380/0",
		},
		"redis": {
			"APP_ENV":      "production",
			"APP_SECRET":   "test-production-secret",
			"DATABASE_URL": "postgres://user:pass@db.example:5432/sports?sslmode=require",
			"REDIS_URL":    "redis://redis:6379/0",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(overrides); err == nil {
				t.Fatal("Load returned nil error for container development service in production")
			}
		})
	}
}

func TestLoadTurnstileIsOptionalButRequiredModeNeedsSecret(t *testing.T) {
	cfg, err := Load(map[string]string{})
	if err != nil || cfg.TurnstileRequired || cfg.TurnstileSecretKey != "" {
		t.Fatalf("optional Turnstile config=%+v err=%v", cfg, err)
	}
	if _, err := Load(map[string]string{"TURNSTILE_REQUIRED": "true"}); err == nil {
		t.Fatal("required Turnstile accepted without secret")
	}
	cfg, err = Load(map[string]string{"TURNSTILE_REQUIRED": "true", "TURNSTILE_SECRET_KEY": "test-secret"})
	if err != nil || !cfg.TurnstileRequired || cfg.TurnstileSecretKey != "test-secret" {
		t.Fatalf("required Turnstile config=%+v err=%v", cfg, err)
	}
}

func TestLoadRequiresTurnstileInProduction(t *testing.T) {
	base := map[string]string{
		"APP_ENV": "production", "APP_SECRET": "test-production-secret",
		"DATABASE_URL": "postgres://user:pass@db:5432/sports?sslmode=require",
		"REDIS_URL":    "rediss://:secret@redis.example:6380/0",
	}
	if _, err := Load(base); err == nil {
		t.Fatal("production accepted without Turnstile")
	}
}
