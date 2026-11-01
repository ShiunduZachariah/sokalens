package ingestion

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

func TestFollowUpHandlerEnqueuesOnlyAfterSuccessfulPrimaryWork(t *testing.T) {
	now := time.Date(2026, time.August, 5, 13, 0, 0, 0, time.UTC)
	enqueuer := &fakePredictionJobEnqueuer{}
	handler := FollowUpHandler{
		Handler: &fakeHandler{}, Jobs: enqueuer, Now: func() time.Time { return now },
		Build: func(source jobs.Job, at time.Time) []jobs.NewJob {
			return []jobs.NewJob{{JobKey: "read-model.refresh", Queue: "personalization", IdempotencyKey: source.IdempotencyKey, RunAfter: at, DeduplicateCompleted: true}}
		},
	}
	if err := handler.Run(context.Background(), jobs.Job{ID: 1, IdempotencyKey: "source-1"}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(enqueuer.jobs) != 1 || enqueuer.jobs[0].IdempotencyKey != "source-1" || !enqueuer.jobs[0].RunAfter.Equal(now) {
		t.Fatalf("follow-ups=%+v", enqueuer.jobs)
	}

	enqueuer.jobs = nil
	handler.Handler = &fakeHandler{err: errors.New("primary failed")}
	if err := handler.Run(context.Background(), jobs.Job{ID: 2}); err == nil {
		t.Fatal("Run returned nil after primary failure")
	}
	if len(enqueuer.jobs) != 0 {
		t.Fatalf("follow-ups were enqueued after failure: %+v", enqueuer.jobs)
	}
}
