package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

type fakeProviderPredictionsEnqueuer struct {
	jobs []jobs.NewJob
	err  error
}

func (f *fakeProviderPredictionsEnqueuer) Enqueue(_ context.Context, job jobs.NewJob) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.jobs = append(f.jobs, job)
	return int64(len(f.jobs)), nil
}

func TestPlanProviderPredictionJobsEnqueuesPerFixtureFetch(t *testing.T) {
	enqueuer := &fakeProviderPredictionsEnqueuer{}
	now := time.Date(2026, time.August, 5, 13, 37, 0, 0, time.UTC)
	pending := []PendingProviderPrediction{
		{ProviderFixtureID: "10401"},
		{ProviderFixtureID: "10402"},
	}

	enqueued, err := planProviderPredictionJobs(context.Background(), func() time.Time { return now }, pending, enqueuer)
	if err != nil {
		t.Fatalf("planProviderPredictionJobs returned error: %v", err)
	}
	if enqueued != 2 || len(enqueuer.jobs) != 2 {
		t.Fatalf("enqueued = %d with %d jobs, want 2", enqueued, len(enqueuer.jobs))
	}
	for index, job := range enqueuer.jobs {
		if job.JobKey != ProviderPredictionsJobKey() {
			t.Fatalf("job[%d] key = %q, want %q", index, job.JobKey, ProviderPredictionsJobKey())
		}
		if job.Queue != "provider-fixtures" || job.Priority != 33 || job.DeduplicateCompleted {
			t.Fatalf("job[%d] = %+v, want provider-fixtures queue, priority 33, no completed dedupe", index, job)
		}
		if !job.RunAfter.Equal(now) {
			t.Fatalf("job[%d] RunAfter = %s, want %s", index, job.RunAfter, now)
		}
		var payload providerPredictionsJobPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			t.Fatalf("job[%d] payload invalid: %v", index, err)
		}
		if payload.FixtureID != 10401+index {
			t.Fatalf("job[%d] fixture_id = %d, want %d", index, payload.FixtureID, 10401+index)
		}
		wantKey := "provider:api-football:predictions:" + pending[index].ProviderFixtureID
		if job.IdempotencyKey != wantKey {
			t.Fatalf("job[%d] idempotency = %q, want %q", index, job.IdempotencyKey, wantKey)
		}
	}
}

func TestPlanProviderPredictionJobsSurfacesEnqueueError(t *testing.T) {
	enqueuer := &fakeProviderPredictionsEnqueuer{err: errors.New("queue full")}
	_, err := planProviderPredictionJobs(context.Background(), nil, []PendingProviderPrediction{{ProviderFixtureID: "7"}}, enqueuer)
	if err == nil || !strings.Contains(err.Error(), "queue full") {
		t.Fatalf("error = %v, want enqueue failure surfaced", err)
	}
}

func TestPlanProviderPredictionJobsRequiresEnqueuer(t *testing.T) {
	if _, err := planProviderPredictionJobs(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("error = nil, want missing-enqueuer error")
	}
}

func TestParseIntOrZero(t *testing.T) {
	if got := parseIntOrZero("10401"); got != 10401 {
		t.Fatalf("parseIntOrZero(10401) = %d", got)
	}
	if got := parseIntOrZero("not-a-number"); got != 0 {
		t.Fatalf("parseIntOrZero(invalid) = %d, want 0", got)
	}
	if got := parseIntOrZero(""); got != 0 {
		t.Fatalf("parseIntOrZero(empty) = %d, want 0", got)
	}
}
