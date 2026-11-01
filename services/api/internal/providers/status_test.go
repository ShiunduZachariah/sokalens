package providers

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestStatusSourceListsProviderQuotaState(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	for _, stmt := range []string{
		"CREATE TABLE providers (provider TEXT PRIMARY KEY, display_name TEXT NOT NULL, enabled BOOLEAN NOT NULL);",
		"CREATE TABLE provider_rate_limits (provider TEXT PRIMARY KEY, daily_limit INTEGER, daily_remaining INTEGER, daily_reserve INTEGER, minute_limit INTEGER, minute_remaining INTEGER, minute_reserve INTEGER, throttle_until TIMESTAMP, updated_at TIMESTAMP);",
		"INSERT INTO providers (provider, display_name, enabled) VALUES ('api-football', 'API-Football', TRUE);",
		"INSERT INTO provider_rate_limits (provider, daily_limit, daily_remaining, daily_reserve, minute_limit, minute_remaining, minute_reserve, updated_at) VALUES ('api-football', 100, 99, 20, 10, 9, 2, '2026-04-29T00:00:00Z');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	statuses, err := NewStatusSource(db).ListProviderStatuses(context.Background())
	if err != nil {
		t.Fatalf("ListProviderStatuses returned error: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("len(statuses) = %d, want 1", len(statuses))
	}
	status := statuses[0]
	if status.Provider != "api-football" || status.Status != "ready" {
		t.Fatalf("status = %+v, want api-football ready", status)
	}
	if status.Quota.DailyLimit != 100 || status.Quota.MinuteRemaining != 9 {
		t.Fatalf("quota = %+v, want free-plan values", status.Quota)
	}
	if status.Quota.UpdatedAt.IsZero() || status.Quota.UpdatedAt.Equal(time.Time{}) {
		t.Fatalf("UpdatedAt = %s, want populated timestamp", status.Quota.UpdatedAt)
	}
}
