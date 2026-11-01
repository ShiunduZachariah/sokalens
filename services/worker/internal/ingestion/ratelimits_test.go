package ingestion

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestProviderRateLimitRepositoryIgnoresMissingHeaderZeros(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE provider_rate_limits (
  provider TEXT PRIMARY KEY,
  daily_limit INTEGER NOT NULL,
  daily_remaining INTEGER NOT NULL,
  minute_limit INTEGER NOT NULL,
  minute_remaining INTEGER NOT NULL,
  throttle_until TIMESTAMP,
  updated_at TIMESTAMP NOT NULL
);
INSERT INTO provider_rate_limits (provider, daily_limit, daily_remaining, minute_limit, minute_remaining, updated_at)
VALUES ('api-football', 100, 92, 10, 5, '2026-07-03T00:00:00Z')`); err != nil {
		t.Fatalf("seed rate limits: %v", err)
	}

	repo := NewProviderRateLimitRepository(db)
	if err := repo.UpdateFromHeaders(context.Background(), ProviderRateLimitUpdate{
		Provider:  "api-football",
		UpdatedAt: time.Date(2026, 7, 3, 1, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpdateFromHeaders missing values returned error: %v", err)
	}

	var dailyRemaining int
	var minuteRemaining int
	if err := db.QueryRowContext(context.Background(), `SELECT daily_remaining, minute_remaining FROM provider_rate_limits WHERE provider = 'api-football'`).Scan(&dailyRemaining, &minuteRemaining); err != nil {
		t.Fatalf("scan rate limits: %v", err)
	}
	if dailyRemaining != 92 || minuteRemaining != 5 {
		t.Fatalf("remaining = %d/%d, want 92/5", dailyRemaining, minuteRemaining)
	}

	if err := repo.UpdateFromHeaders(context.Background(), ProviderRateLimitUpdate{
		Provider:        "api-football",
		DailyLimit:      100,
		DailyRemaining:  0,
		MinuteLimit:     10,
		MinuteRemaining: 0,
		UpdatedAt:       time.Date(2026, 7, 3, 1, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpdateFromHeaders zero remaining returned error: %v", err)
	}

	if err := db.QueryRowContext(context.Background(), `SELECT daily_remaining, minute_remaining FROM provider_rate_limits WHERE provider = 'api-football'`).Scan(&dailyRemaining, &minuteRemaining); err != nil {
		t.Fatalf("scan zero remaining rate limits: %v", err)
	}
	if dailyRemaining != 0 || minuteRemaining != 0 {
		t.Fatalf("remaining = %d/%d, want 0/0", dailyRemaining, minuteRemaining)
	}
}
