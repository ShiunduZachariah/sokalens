package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/ingestion"
)

func TestProviderJobsToQueueUsesSafeHybridSchedulerPlan(t *testing.T) {
	cfg := config.Config{
		FootballProvider:               "hybrid",
		EnableRealAPIFootballCalls:     true,
		EnableRealFootballDataOrgCalls: true,
		ProviderScheduleInterval:       4 * time.Hour,
		PredictionScheduleInterval:     15 * time.Minute,
		PollInterval:                   10 * time.Second,
	}
	queued, err := providerJobsToQueue(cfg, time.Date(2026, time.July, 14, 13, 37, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("providerJobsToQueue returned error: %v", err)
	}
	// 18 API-Football (the 17 date/reference jobs plus the hourly results
	// reconcile) and 10 football-data.org.
	if len(queued) != 28 {
		t.Fatalf("provider jobs = %d, want 18 API-Football plus 10 football-data.org", len(queued))
	}
	fixtureJobs := 0
	for _, job := range queued {
		if !job.DeduplicateCompleted || !strings.HasPrefix(job.IdempotencyKey, "schedule:provider:") {
			t.Fatalf("job = %+v, want durable schedule-bucket idempotency", job)
		}
		if strings.Contains(string(job.Payload), `"last"`) {
			t.Fatalf("job uses free-plan-blocked last parameter: %s", job.Payload)
		}
		if job.JobKey == ingestion.FixturesJobKey() {
			fixtureJobs++
		}
	}
	if fixtureJobs != 11 {
		t.Fatalf("API-Football fixture jobs = %d, want three-day status catch-up through next-week window", fixtureJobs)
	}
}
