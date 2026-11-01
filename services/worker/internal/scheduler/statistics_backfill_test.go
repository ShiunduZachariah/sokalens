package scheduler

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/ingestion"
)

func TestDemandStatisticsJobHasDurableFixtureIdentity(t *testing.T) {
	now := time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC)
	job, ok := newStatisticsBackfillJob("12345", now)
	if !ok {
		t.Fatal("valid provider fixture ID was rejected")
	}
	if job.Job.JobKey != ingestion.StatisticsJobKey() || job.Job.Queue != "provider-fixtures" ||
		job.Job.IdempotencyKey != "schedule:provider:api-football:stats-demand:12345" ||
		!job.Job.DeduplicateCompleted {
		t.Fatalf("statistics job identity or queue is wrong: %#v", job.Job)
	}
	var payload struct {
		FixtureID int64 `json:"fixture_id"`
	}
	if err := json.Unmarshal(job.Job.Payload, &payload); err != nil || payload.FixtureID != 12345 {
		t.Fatalf("fixture payload = %#v, err = %v", payload, err)
	}
	for _, providerID := range []string{"", "0", "-1", "not-a-number"} {
		if _, ok := newStatisticsBackfillJob(providerID, now); ok {
			t.Fatalf("invalid provider fixture ID %q was accepted", providerID)
		}
	}
}
