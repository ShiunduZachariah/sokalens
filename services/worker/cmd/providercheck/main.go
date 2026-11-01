package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/ingestion"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/footballdata"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type providerCheckClient interface {
	Get(context.Context, string, url.Values) (provider.Response, error)
}

type providerCheckRequestLogger interface {
	Insert(context.Context, ingestion.RequestLog) error
}

type providerCheckRateLimitWriter interface {
	UpdateFromHeaders(context.Context, ingestion.ProviderRateLimitUpdate) error
}

type validateProviderOptions struct {
	Client      providerCheckClient
	RequestLogs providerCheckRequestLogger
	RateLimits  providerCheckRateLimitWriter
	Now         func() time.Time
}

type validateProviderResult struct {
	Response      provider.Response
	CountryCount  int
	ResourceCount int
}

func main() {
	cfg, err := config.Load(nil)
	if err != nil {
		slog.Error("invalid provider validation configuration", "error", err)
		os.Exit(1)
	}
	if cfg.FootballProvider == footballdata.ProviderName && cfg.FootballDataOrgAPIKey == "" {
		slog.Error("FOOTBALL_DATA_ORG_API_KEY is required for live provider validation")
		os.Exit(1)
	}
	if cfg.FootballProvider != footballdata.ProviderName && cfg.APIFootballKey == "" {
		slog.Error("API_FOOTBALL_KEY is required for live provider validation")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	requestLogs := ingestion.NewRequestLogRepository(db)
	rateLimits := ingestion.NewProviderRateLimitRepository(db)
	var result validateProviderResult
	if cfg.FootballProvider == footballdata.ProviderName {
		client := footballdata.NewClient(footballdata.Config{
			BaseURL:     cfg.FootballDataOrgBaseURL,
			APIKey:      cfg.FootballDataOrgAPIKey,
			MinuteLimit: cfg.ProviderMinuteLimit,
			HTTPClient:  &http.Client{Timeout: 20 * time.Second},
		})
		result, err = validateFootballDataProvider(ctx, validateProviderOptions{
			Client:      client,
			RequestLogs: requestLogs,
			RateLimits:  rateLimits,
		})
	} else {
		client := apifootball.NewClient(apifootball.Config{
			BaseURL:       cfg.APIFootballBaseURL,
			APIKey:        cfg.APIFootballKey,
			DailyLimit:    cfg.ProviderDailyLimit,
			DailyReserve:  cfg.ProviderDailyReserve,
			MinuteReserve: cfg.ProviderMinuteReserve,
			HTTPClient:    &http.Client{Timeout: 20 * time.Second},
		})
		result, err = validateProvider(ctx, validateProviderOptions{
			Client:      client,
			RequestLogs: requestLogs,
			RateLimits:  rateLimits,
		})
	}
	if err != nil {
		slog.Error(
			"provider validation failed",
			"endpoint", result.Response.Endpoint,
			"status", result.Response.StatusCode,
			"daily_remaining", result.Response.RateLimit.DailyRemaining,
			"minute_remaining", result.Response.RateLimit.MinuteRemaining,
			"error", err,
		)
		os.Exit(1)
	}
	if cfg.FootballProvider == footballdata.ProviderName {
		fmt.Printf(
			"football-data.org validation ok: endpoint=%s status=%d competitions=%d minute_limit=%d minute_remaining=%d latency_ms=%d\n",
			result.Response.Endpoint,
			result.Response.StatusCode,
			result.ResourceCount,
			result.Response.RateLimit.MinuteLimit,
			result.Response.RateLimit.MinuteRemaining,
			result.Response.Latency.Milliseconds(),
		)
		return
	}

	fmt.Printf(
		"api-football validation ok: endpoint=%s status=%d countries=%d daily_limit=%d daily_remaining=%d minute_limit=%d minute_remaining=%d latency_ms=%d\n",
		result.Response.Endpoint,
		result.Response.StatusCode,
		result.CountryCount,
		result.Response.RateLimit.DailyLimit,
		result.Response.RateLimit.DailyRemaining,
		result.Response.RateLimit.MinuteLimit,
		result.Response.RateLimit.MinuteRemaining,
		result.Response.Latency.Milliseconds(),
	)
}

func validateFootballDataProvider(ctx context.Context, opts validateProviderOptions) (validateProviderResult, error) {
	now := time.Now().UTC
	if opts.Now != nil {
		now = opts.Now
	}
	resp, err := opts.Client.Get(ctx, "competitions", url.Values{})
	log := ingestion.RequestLog{
		Provider:      nonEmpty(resp.Provider, footballdata.ProviderName),
		Endpoint:      nonEmpty(resp.Endpoint, "competitions"),
		Method:        "GET",
		StatusCode:    resp.StatusCode,
		QuotaCost:     1,
		Latency:       resp.Latency,
		ResponseBytes: resp.ResponseBytes,
		RateLimit:     resp.RateLimit,
		RequestedAt:   now(),
	}
	if err != nil {
		log.ErrorCategory = categorizeProviderCheckError(resp, err)
		if opts.RequestLogs != nil {
			if logErr := opts.RequestLogs.Insert(ctx, log); logErr != nil {
				return validateProviderResult{Response: resp}, logErr
			}
		}
		return validateProviderResult{Response: resp}, err
	}
	if opts.RequestLogs != nil {
		if err := opts.RequestLogs.Insert(ctx, log); err != nil {
			return validateProviderResult{Response: resp}, err
		}
	}
	if opts.RateLimits != nil {
		if err := opts.RateLimits.UpdateFromHeaders(ctx, ingestion.ProviderRateLimitUpdate{
			Provider:        footballdata.ProviderName,
			DailyLimit:      resp.RateLimit.DailyLimit,
			DailyRemaining:  resp.RateLimit.DailyRemaining,
			MinuteLimit:     resp.RateLimit.MinuteLimit,
			MinuteRemaining: resp.RateLimit.MinuteRemaining,
			UpdatedAt:       now(),
		}); err != nil {
			return validateProviderResult{Response: resp}, err
		}
	}
	competitions, err := footballdata.ParseCompetitions(resp.Body)
	if err != nil {
		return validateProviderResult{Response: resp}, err
	}
	return validateProviderResult{Response: resp, ResourceCount: len(competitions)}, nil
}

func validateProvider(ctx context.Context, opts validateProviderOptions) (validateProviderResult, error) {
	now := time.Now().UTC
	if opts.Now != nil {
		now = opts.Now
	}
	resp, err := opts.Client.Get(ctx, "countries", url.Values{})
	log := ingestion.RequestLog{
		Provider:      nonEmpty(resp.Provider, apifootball.ProviderName),
		Endpoint:      nonEmpty(resp.Endpoint, "countries"),
		Method:        "GET",
		StatusCode:    resp.StatusCode,
		QuotaCost:     1,
		Latency:       resp.Latency,
		ResponseBytes: resp.ResponseBytes,
		RateLimit:     resp.RateLimit,
		RequestedAt:   now(),
	}
	if err != nil {
		log.ErrorCategory = categorizeProviderCheckError(resp, err)
		if opts.RequestLogs != nil {
			if logErr := opts.RequestLogs.Insert(ctx, log); logErr != nil {
				return validateProviderResult{Response: resp}, logErr
			}
		}
		return validateProviderResult{Response: resp}, err
	}
	if opts.RequestLogs != nil {
		if err := opts.RequestLogs.Insert(ctx, log); err != nil {
			return validateProviderResult{Response: resp}, err
		}
	}
	if opts.RateLimits != nil {
		if err := opts.RateLimits.UpdateFromHeaders(ctx, ingestion.ProviderRateLimitUpdate{
			Provider:        apifootball.ProviderName,
			DailyLimit:      resp.RateLimit.DailyLimit,
			DailyRemaining:  resp.RateLimit.DailyRemaining,
			MinuteLimit:     resp.RateLimit.MinuteLimit,
			MinuteRemaining: resp.RateLimit.MinuteRemaining,
			UpdatedAt:       now(),
		}); err != nil {
			return validateProviderResult{Response: resp}, err
		}
	}
	countries, err := apifootball.ParseCountries(resp.Body)
	if err != nil {
		return validateProviderResult{Response: resp}, err
	}
	return validateProviderResult{Response: resp, CountryCount: len(countries)}, nil
}

func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func categorizeProviderCheckError(resp provider.Response, err error) string {
	if resp.StatusCode >= 400 {
		return "provider_status"
	}
	if err != nil {
		return "provider_transport"
	}
	return ""
}
