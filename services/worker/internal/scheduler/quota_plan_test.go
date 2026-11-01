package scheduler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/ingestion"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/quota"
)

func decisionWith(allowed ...quota.Priority) quota.Decision {
	decision := quota.Decision{
		DailyLimit: 7500, DailyRemaining: 6000, EmergencyReserve: 900, SafeDiscretionary: 1200,
		Allowed:          map[quota.Priority]bool{},
		LivePollInterval: 30 * time.Second,
	}
	for _, priority := range allowed {
		decision.Allowed[priority] = true
	}
	return decision
}

func TestApplyQuotaPlanAddsLiveJobWhenP0Allowed(t *testing.T) {
	base := []ScheduledJob{{Job: newBaseJob()}}
	decision := decisionWith(quota.P0, quota.P1)
	decision.LiveMatches = 4
	plan := ApplyQuotaPlan(context.Background(), base, decision, QuotaOptions{}, time.Now(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	liveFound := false
	for _, scheduled := range plan {
		if scheduled.Job.JobKey == ingestion.LiveFixturesJobKey() {
			liveFound = true
		}
	}
	if !liveFound {
		t.Fatal("expected a live polling job when P0 is allowed")
	}
}

func TestApplyQuotaPlanPacesBatchedStatisticsOnPro(t *testing.T) {
	base := []ScheduledJob{{Job: jobs.NewJob{JobKey: ingestion.InPlayStatisticsJobKey()}}}
	decision := decisionWith(quota.P1)
	decision.LiveMatches = 3
	decision.InPlayStatisticsPollInterval = 5 * time.Minute
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tt := range []struct{ minute, want int }{{0, 1}, {1, 0}, {5, 1}} {
		now := time.Date(2026, 10, 16, 12, tt.minute, 0, 0, time.UTC)
		plan := ApplyQuotaPlan(context.Background(), base, decision, QuotaOptions{}, now, logger)
		if len(plan) != tt.want {
			t.Fatalf("minute %d: got %d jobs, want %d", tt.minute, len(plan), tt.want)
		}
	}
}

func TestApplyQuotaPlanSkipsLivePollWhenNoMatchIsLive(t *testing.T) {
	base := []ScheduledJob{{Job: newBaseJob()}}
	plan := ApplyQuotaPlan(context.Background(), base, decisionWith(quota.P0), QuotaOptions{}, time.Now(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, scheduled := range plan {
		if scheduled.Job.JobKey == ingestion.LiveFixturesJobKey() {
			t.Fatal("live polling must be idle when the canonical catalogue has no live match")
		}
	}
}

func TestApplyQuotaPlanSkipsLiveWhenP0Disallowed(t *testing.T) {
	base := []ScheduledJob{{Job: newBaseJob()}}
	plan := ApplyQuotaPlan(context.Background(), base, decisionWith(quota.P1), QuotaOptions{}, time.Now(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, scheduled := range plan {
		if scheduled.Job.JobKey == ingestion.LiveFixturesJobKey() {
			t.Fatal("live job must not be scheduled when P0 is disallowed")
		}
	}
}

func TestApplyQuotaPlanBackfillRequiresBudgetAndP4(t *testing.T) {
	// No DB: backfill candidate query returns nothing, so no backfill jobs even
	// when allowed — safe when the scheduler has no catalogue handle.
	base := []ScheduledJob{{Job: newBaseJob()}}
	plan := ApplyQuotaPlan(context.Background(), base, decisionWith(quota.P0, quota.P4), QuotaOptions{DB: nil, BackfillMinSafeBudget: 100}, time.Now(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, scheduled := range plan {
		if scheduled.Job.JobKey == ingestion.BackfillFixturesJobKey() {
			t.Fatal("backfill must not be enqueued without a DB catalogue")
		}
	}
}

func TestFixtureBackfillPayloadOmitsUnsupportedPage(t *testing.T) {
	var payload map[string]any
	if err := json.Unmarshal(fixtureBackfillPayload("135", 2026, "2026-08-24", "2026-09-23"), &payload); err != nil {
		t.Fatalf("decode backfill payload: %v", err)
	}
	if payload["league_id"] != float64(135) || payload["season"] != float64(2026) ||
		payload["from"] != "2026-08-24" || payload["to"] != "2026-09-23" {
		t.Fatalf("backfill payload = %#v", payload)
	}
	if _, exists := payload["page"]; exists {
		t.Fatalf("fixtures backfill payload must omit unsupported page: %#v", payload)
	}
}

func newBaseJob() jobs.NewJob {
	return jobs.NewJob{JobKey: "base", Queue: "base", Priority: 100}
}

var _ = time.Now
