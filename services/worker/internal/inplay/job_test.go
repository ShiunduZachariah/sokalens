package inplay

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/predictionenginepython"
)

var now = time.Date(2026, 9, 21, 9, 52, 30, 0, time.UTC)

type fakeEnqueuer struct {
	enqueued []jobs.NewJob
	err      error
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, job jobs.NewJob) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.enqueued = append(f.enqueued, job)
	return int64(len(f.enqueued)), nil
}

type fakeRunner struct {
	summaries []predictionenginepython.Summary
	err       error
	received  []jobs.Job
}

func (f *fakeRunner) RunWithSummaries(_ context.Context, job jobs.Job) ([]predictionenginepython.Summary, error) {
	f.received = append(f.received, job)
	return f.summaries, f.err
}

func summary(processed, published int) predictionenginepython.Summary {
	return predictionenginepython.Summary{
		Scope:             InPlayScopeName,
		FixturesProcessed: processed,
		FixturesPublished: published,
	}
}

// InPlayScopeName mirrors the engine's scope name so the test reads without a
// package qualifier on every line.
const InPlayScopeName = predictionenginepython.InPlayScope

// The loop exists to keep itself alive exactly as long as there is football
// being played, and no longer. Both halves of that are load-bearing: without
// the first an in-play prediction goes stale between ingest polls, and without
// the second the queue carries a job every thirty seconds through the night.
func TestTheLoopContinuesWhileFixturesAreLive(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	job := Job{
		Runner: &fakeRunner{summaries: []predictionenginepython.Summary{summary(4, 3)}},
		Jobs:   enqueuer,
		Now:    func() time.Time { return now },
	}

	if err := job.Run(context.Background(), jobs.Job{ID: 7, JobKey: JobKey}); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(enqueuer.enqueued) != 1 {
		t.Fatalf("expected one follow-up tick, got %d", len(enqueuer.enqueued))
	}
	next := enqueuer.enqueued[0]
	if next.JobKey != JobKey {
		t.Fatalf("follow-up job key = %q, want %q", next.JobKey, JobKey)
	}
	if want := now.Add(DefaultTick); !next.RunAfter.Equal(want) {
		t.Fatalf("follow-up RunAfter = %s, want %s", next.RunAfter, want)
	}
}

func TestTheLoopStopsWhenNothingIsLive(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	job := Job{
		Runner: &fakeRunner{summaries: []predictionenginepython.Summary{summary(0, 0)}},
		Jobs:   enqueuer,
		Now:    func() time.Time { return now },
	}

	if err := job.Run(context.Background(), jobs.Job{ID: 7}); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(enqueuer.enqueued) != 0 {
		t.Fatalf("expected the loop to stop, got %d follow-ups", len(enqueuer.enqueued))
	}
}

// A run that processed fixtures but published none is still a live match --
// every one of them may simply have been inside its regeneration interval.
// Stopping the loop there would mean the next goal waits for the ingest.
func TestASuppressedRunStillKeepsTheLoopAlive(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	job := Job{
		Runner: &fakeRunner{summaries: []predictionenginepython.Summary{{
			Scope:              InPlayScopeName,
			FixturesProcessed:  5,
			FixturesPublished:  0,
			FixturesSuppressed: 5,
			SuppressedByReason: map[string]int{"CADENCE_UNCHANGED": 5},
		}}},
		Jobs: enqueuer,
		Now:  func() time.Time { return now },
	}

	if err := job.Run(context.Background(), jobs.Job{ID: 7}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(enqueuer.enqueued) != 1 {
		t.Fatalf("expected the loop to continue, got %d follow-ups", len(enqueuer.enqueued))
	}
}

// Rescheduling before the run would keep the loop alive through a
// persistently failing engine, turning one broken deploy into an endless queue.
func TestAFailedRunDoesNotScheduleAnotherTick(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	job := Job{
		Runner: &fakeRunner{err: errors.New("engine exited 1")},
		Jobs:   enqueuer,
		Now:    func() time.Time { return now },
	}

	if err := job.Run(context.Background(), jobs.Job{ID: 7}); err == nil {
		t.Fatal("expected the run error to propagate")
	}
	if len(enqueuer.enqueued) != 0 {
		t.Fatalf("expected no follow-up after a failure, got %d", len(enqueuer.enqueued))
	}
}

// The ingest enqueues on every poll and the previous run re-enqueues too. They
// have to collapse onto one job per tick or the queue multiplies with the
// number of live matches.
func TestTicksInTheSameBucketShareAnIdempotencyKey(t *testing.T) {
	first := NewJob(now, DefaultTick, now)
	second := NewJob(now.Add(5*time.Second), DefaultTick, now.Add(5*time.Second))
	third := NewJob(now, DefaultTick, now.Add(DefaultTick))

	if first.IdempotencyKey != second.IdempotencyKey {
		t.Fatalf("same bucket produced different keys: %q vs %q", first.IdempotencyKey, second.IdempotencyKey)
	}
	if first.IdempotencyKey == third.IdempotencyKey {
		t.Fatal("a later tick must not deduplicate onto the current one")
	}
	if !first.DeduplicateCompleted {
		t.Fatal("a completed tick must not be re-run from the same key")
	}
}

func TestTheEnqueuedPayloadNamesTheInPlayScope(t *testing.T) {
	var payload struct {
		Scope     string `json:"scope"`
		BatchSize int    `json:"batch_size"`
	}
	if err := json.Unmarshal(NewJob(now, DefaultTick, now).Payload, &payload); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if payload.Scope != predictionenginepython.InPlayScope {
		t.Fatalf("scope = %q, want %q", payload.Scope, predictionenginepython.InPlayScope)
	}
	if payload.BatchSize != DefaultBatchSize {
		t.Fatalf("batch_size = %d, want %d", payload.BatchSize, DefaultBatchSize)
	}
}

// The tier is a deployment decision. Stamping it at run time rather than at
// enqueue time means changing it takes effect on the next tick instead of
// after every already-queued job has drained.
func TestTheConfiguredTierReachesTheRunner(t *testing.T) {
	runner := &fakeRunner{summaries: []predictionenginepython.Summary{summary(1, 1)}}
	job := Job{Runner: runner, Tier: "B", Now: func() time.Time { return now }}

	if err := job.Run(context.Background(), jobs.Job{ID: 7}); err != nil {
		t.Fatalf("run: %v", err)
	}

	var payload struct {
		Tier string `json:"tier"`
	}
	if err := json.Unmarshal(runner.received[0].Payload, &payload); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if payload.Tier != "B" {
		t.Fatalf("tier = %q, want B", payload.Tier)
	}
}

func TestEnqueueIsANoOpWithoutAQueue(t *testing.T) {
	if err := Enqueue(context.Background(), nil, now, DefaultTick); err != nil {
		t.Fatalf("expected a nil enqueuer to be tolerated, got %v", err)
	}
}

func TestARunnerIsRequired(t *testing.T) {
	if err := (Job{}).Run(context.Background(), jobs.Job{}); err == nil {
		t.Fatal("expected a missing runner to be an error")
	}
}
