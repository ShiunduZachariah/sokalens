package matchstate

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

type recordingEnqueuer struct {
	enqueued []jobs.NewJob
	err      error
}

func (r *recordingEnqueuer) Enqueue(_ context.Context, job jobs.NewJob) (int64, error) {
	if r.err != nil {
		return 0, r.err
	}
	r.enqueued = append(r.enqueued, job)
	return int64(len(r.enqueued)), nil
}

func TestDateReconcilerRequestsTheFixtureDate(t *testing.T) {
	enqueuer := &recordingEnqueuer{}
	now := time.Date(2026, 9, 6, 17, 5, 0, 0, time.UTC)
	reconciler := DateReconciler{Jobs: enqueuer, JobKey: "provider.api-football.fixtures", Queue: "provider-fixtures", Now: func() time.Time { return now }}

	if err := reconciler.ReconcileDate(context.Background(), time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("ReconcileDate returned error: %v", err)
	}
	if len(enqueuer.enqueued) != 1 {
		t.Fatalf("enqueued %d jobs", len(enqueuer.enqueued))
	}
	job := enqueuer.enqueued[0]
	var payload map[string]string
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload["date"] != "2026-09-06" {
		t.Fatalf("payload = %v", payload)
	}
	// Corrective work must not outrank the live poll that would have made it
	// unnecessary.
	if job.Priority != 20 {
		t.Fatalf("priority = %d", job.Priority)
	}
}

func TestDateReconcilerDedupesWithinTheHourAndRetriesTheNextOne(t *testing.T) {
	enqueuer := &recordingEnqueuer{}
	day := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	at := func(hour, minute int) DateReconciler {
		moment := time.Date(2026, 9, 6, hour, minute, 0, 0, time.UTC)
		return DateReconciler{Jobs: enqueuer, JobKey: "k", Queue: "q", Now: func() time.Time { return moment }}
	}
	for _, reconciler := range []DateReconciler{at(17, 0), at(17, 2), at(17, 58), at(18, 0)} {
		if err := reconciler.ReconcileDate(context.Background(), day); err != nil {
			t.Fatalf("ReconcileDate returned error: %v", err)
		}
	}
	keys := map[string]bool{}
	for _, job := range enqueuer.enqueued {
		keys[job.IdempotencyKey] = true
	}
	// Three sweeps inside 17:00 share one key -- the queue collapses them.
	// The 18:00 sweep gets a new one, so a day that is still stuck is looked
	// at again rather than deduped away forever.
	if len(keys) != 2 {
		t.Fatalf("distinct idempotency keys = %d, want 2: %v", len(keys), keys)
	}
}

func TestDateReconcilerNeedsAJobKeyAndQueue(t *testing.T) {
	if err := (DateReconciler{Jobs: &recordingEnqueuer{}}).ReconcileDate(context.Background(), time.Now()); err == nil {
		t.Fatal("expected an error with no job key or queue")
	}
	// No queue configured at all is a no-op rather than an error: the reaper's
	// hard-bound work still runs, and that is the user-visible half.
	if err := (DateReconciler{}).ReconcileDate(context.Background(), time.Now()); err != nil {
		t.Fatalf("an unconfigured reconciler should be inert, got %v", err)
	}
}

func TestJobRunsASweep(t *testing.T) {
	store := &fakeStore{rows: []StuckMatch{
		{MatchID: 1, State: StateSecondHalf, KickoffAt: kickoff, LiveStateAt: kickoff.Add(50 * time.Minute)},
	}}
	job := Job{Store: store, Now: func() time.Time { return kickoff.Add(4 * time.Hour) }}
	if err := job.Run(context.Background(), jobs.Job{}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(store.marked) != 1 {
		t.Fatalf("sweep did not run: %v", store.marked)
	}
}
