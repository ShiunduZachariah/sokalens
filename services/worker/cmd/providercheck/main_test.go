package main

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/ingestion"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/footballdata"
)

func TestValidateProviderLogsRequestAndUpdatesRateLimits(t *testing.T) {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	client := fakeProviderCheckClient{
		response: provider.Response{
			Provider:      apifootball.ProviderName,
			Endpoint:      "countries",
			StatusCode:    200,
			Body:          []byte(`{"errors":[],"response":[{"name":"Kenya","code":"KE","flag":"https://example.test/ke.svg"}]}`),
			ResponseBytes: 100,
			Latency:       25 * time.Millisecond,
			RateLimit: provider.RateLimitHeaders{
				DailyLimit:      100,
				DailyRemaining:  88,
				MinuteLimit:     10,
				MinuteRemaining: 9,
			},
		},
	}
	logs := &fakeProviderCheckLogs{}
	limits := &fakeProviderCheckRateLimits{}

	result, err := validateProvider(context.Background(), validateProviderOptions{
		Client:      client,
		RequestLogs: logs,
		RateLimits:  limits,
		Now:         func() time.Time { return now },
	})

	if err != nil {
		t.Fatalf("validateProvider returned error: %v", err)
	}
	if result.CountryCount != 1 {
		t.Fatalf("CountryCount = %d, want 1", result.CountryCount)
	}
	if len(logs.rows) != 1 {
		t.Fatalf("request logs = %d, want 1", len(logs.rows))
	}
	if logs.rows[0].Provider != apifootball.ProviderName || logs.rows[0].Endpoint != "countries" {
		t.Fatalf("request log = %+v", logs.rows[0])
	}
	if len(limits.rows) != 1 {
		t.Fatalf("rate limit updates = %d, want 1", len(limits.rows))
	}
	if limits.rows[0].DailyRemaining != 88 || limits.rows[0].MinuteRemaining != 9 {
		t.Fatalf("rate limit update = %+v", limits.rows[0])
	}
}

func TestValidateFootballDataProviderLogsCompetitionsAndUpdatesRateLimits(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	client := fakeProviderCheckClient{
		response: provider.Response{
			Provider:      footballdata.ProviderName,
			Endpoint:      "competitions",
			StatusCode:    200,
			Body:          []byte(`{"count":1,"competitions":[{"id":2021,"area":{"id":2072,"name":"England","code":"ENG"},"name":"Premier League","code":"PL","type":"LEAGUE","plan":"TIER_ONE"}]}`),
			ResponseBytes: 100,
			Latency:       25 * time.Millisecond,
			RateLimit: provider.RateLimitHeaders{
				MinuteLimit:     10,
				MinuteRemaining: 8,
			},
		},
	}
	logs := &fakeProviderCheckLogs{}
	limits := &fakeProviderCheckRateLimits{}

	result, err := validateFootballDataProvider(context.Background(), validateProviderOptions{
		Client:      client,
		RequestLogs: logs,
		RateLimits:  limits,
		Now:         func() time.Time { return now },
	})

	if err != nil {
		t.Fatalf("validateFootballDataProvider returned error: %v", err)
	}
	if result.ResourceCount != 1 {
		t.Fatalf("ResourceCount = %d, want 1", result.ResourceCount)
	}
	if len(logs.rows) != 1 {
		t.Fatalf("request logs = %d, want 1", len(logs.rows))
	}
	if logs.rows[0].Provider != footballdata.ProviderName || logs.rows[0].Endpoint != "competitions" {
		t.Fatalf("request log = %+v", logs.rows[0])
	}
	if len(limits.rows) != 1 {
		t.Fatalf("rate limit updates = %d, want 1", len(limits.rows))
	}
	if limits.rows[0].Provider != footballdata.ProviderName || limits.rows[0].MinuteRemaining != 8 {
		t.Fatalf("rate limit update = %+v", limits.rows[0])
	}
}

type fakeProviderCheckClient struct {
	response provider.Response
	err      error
}

func (f fakeProviderCheckClient) Get(context.Context, string, url.Values) (provider.Response, error) {
	return f.response, f.err
}

type fakeProviderCheckLogs struct {
	rows []ingestion.RequestLog
}

func (f *fakeProviderCheckLogs) Insert(_ context.Context, row ingestion.RequestLog) error {
	f.rows = append(f.rows, row)
	return nil
}

type fakeProviderCheckRateLimits struct {
	rows []ingestion.ProviderRateLimitUpdate
}

func (f *fakeProviderCheckRateLimits) UpdateFromHeaders(_ context.Context, row ingestion.ProviderRateLimitUpdate) error {
	f.rows = append(f.rows, row)
	return nil
}
