package ingestion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

type recordingEnqueuer struct {
	queued []jobs.NewJob
	err    error
}

func (r *recordingEnqueuer) Enqueue(_ context.Context, job jobs.NewJob) (int64, error) {
	if r.err != nil {
		return 0, r.err
	}
	r.queued = append(r.queued, job)
	return int64(len(r.queued)), nil
}

func reconcileJobWith(ids []int64, enqueuer *recordingEnqueuer, maxBatches int) ResultsReconcileJob {
	return NewResultsReconcileJob(ResultsReconcileJobOptions{
		Stale:      func(context.Context, int) ([]int64, error) { return ids, nil },
		Jobs:       enqueuer,
		MaxBatches: maxBatches,
		Now:        func() time.Time { return time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC) },
	})
}

func TestReconcileBatchesTwentyFixturesPerProviderRequest(t *testing.T) {
	// 4.1's rule: never call the provider once per fixture. Fifty stranded
	// fixtures must be three requests, not fifty -- that ratio is what makes
	// clearing a multi-thousand backlog affordable at all.
	ids := make([]int64, 50)
	for i := range ids {
		ids[i] = int64(1000 + i)
	}
	enqueuer := &recordingEnqueuer{}
	if err := reconcileJobWith(ids, enqueuer, 25).Run(context.Background(), jobs.Job{ID: 1}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(enqueuer.queued) != 3 {
		t.Fatalf("enqueued %d batches for 50 fixtures, want 3", len(enqueuer.queued))
	}
	total := 0
	for _, job := range enqueuer.queued {
		var payload fixturesJobPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			t.Fatalf("payload: %v", err)
		}
		if len(payload.IDs) > MaxFixtureIDsPerRequest {
			t.Fatalf("batch of %d exceeds the provider cap of %d", len(payload.IDs), MaxFixtureIDsPerRequest)
		}
		total += len(payload.IDs)
	}
	if total != len(ids) {
		t.Fatalf("requested %d fixtures, want all %d -- a dropped fixture stays stranded silently", total, len(ids))
	}
}

func TestReconcileRespectsItsPerRunCeiling(t *testing.T) {
	// The backlog must drain over several runs rather than in one burst that
	// spends the day's provider budget in a minute.
	ids := make([]int64, 400)
	for i := range ids {
		ids[i] = int64(2000 + i)
	}
	enqueuer := &recordingEnqueuer{}
	if err := reconcileJobWith(ids, enqueuer, 5).Run(context.Background(), jobs.Job{ID: 1}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(enqueuer.queued) != 5 {
		t.Fatalf("enqueued %d batches, want the 5 the ceiling allows", len(enqueuer.queued))
	}
}

func TestReconcileKeysOnTheBatchSoRepeatRunsDoNotDuplicate(t *testing.T) {
	// A fixture whose result still has not arrived is selected again next run,
	// by design. Keying the job on the run would queue a second identical
	// provider request every hour for the same stranded fixtures.
	ids := []int64{7, 3, 11}
	first := &recordingEnqueuer{}
	second := &recordingEnqueuer{}
	if err := reconcileJobWith(ids, first, 25).Run(context.Background(), jobs.Job{ID: 1}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := reconcileJobWith(ids, second, 25).Run(context.Background(), jobs.Job{ID: 2}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if first.queued[0].IdempotencyKey != second.queued[0].IdempotencyKey {
		t.Fatalf("keys differ across runs: %q vs %q", first.queued[0].IdempotencyKey, second.queued[0].IdempotencyKey)
	}
	// Sorted, so the same set in a different order is the same request.
	if !strings.HasSuffix(first.queued[0].IdempotencyKey, "3-7-11") {
		t.Fatalf("key = %q, want the sorted id set", first.queued[0].IdempotencyKey)
	}
}

func TestReconcileEnqueuesNothingWhenNothingIsStranded(t *testing.T) {
	enqueuer := &recordingEnqueuer{}
	if err := reconcileJobWith(nil, enqueuer, 25).Run(context.Background(), jobs.Job{ID: 1}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(enqueuer.queued) != 0 {
		t.Fatalf("enqueued %d jobs with nothing stranded", len(enqueuer.queued))
	}
}

func TestReconcileQueryHasNoUpperBoundOnAge(t *testing.T) {
	// The absence of an age ceiling is the fix, not an oversight. The date
	// window reaches three days back; anything older was orphaned permanently,
	// and a ceiling here would recreate that same edge one step further out.
	// Production held fixtures from nine months earlier when this was written.
	sql := reconcileStaleSQL
	if strings.Contains(sql, "kickoff_at >") {
		t.Fatal("the stale query has a lower bound on kickoff; old fixtures would stay orphaned")
	}
	if !strings.Contains(sql, "kickoff_at < now()") {
		t.Fatal("the stale query must only consider matches already played")
	}
	for _, terminal := range []string{"finished", "cancelled", "canceled", "postponed", "abandoned", "awarded", "walkover"} {
		if !strings.Contains(sql, "'"+terminal+"'") {
			t.Fatalf("%q is terminal but the query would still fetch it forever", terminal)
		}
	}
}

func TestReconcileStaggersBatchesSoTheyDoNotFightTheExclusiveLock(t *testing.T) {
	// The fixtures job holds pipeline:api-football:fixtures, so only one batch
	// runs at a time regardless. Queuing them all at the same instant does not
	// make them faster; it makes them contend, and each retries against a held
	// lock until its attempts run out. On the first production run 23 of 25
	// batches failed exactly that way.
	ids := make([]int64, 100)
	for i := range ids {
		ids[i] = int64(3000 + i)
	}
	enqueuer := &recordingEnqueuer{}
	if err := reconcileJobWith(ids, enqueuer, 25).Run(context.Background(), jobs.Job{ID: 1}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(enqueuer.queued) != 5 {
		t.Fatalf("enqueued %d batches for 100 fixtures, want 5", len(enqueuer.queued))
	}
	for i := 1; i < len(enqueuer.queued); i++ {
		gap := enqueuer.queued[i].RunAfter.Sub(enqueuer.queued[i-1].RunAfter)
		if gap < reconcileBatchSpacing {
			t.Fatalf("batches %d and %d are %s apart; they will contend for the lock", i-1, i, gap)
		}
	}
	// And the whole run must still fit inside the hourly cadence, or a run
	// would still be draining when the next one starts selecting.
	span := enqueuer.queued[len(enqueuer.queued)-1].RunAfter.Sub(enqueuer.queued[0].RunAfter)
	if maxSpan := time.Duration(defaultReconcileBatches) * reconcileBatchSpacing; maxSpan >= time.Hour {
		t.Fatalf("a full run spans %s, which does not fit the hourly cadence", maxSpan)
	}
	_ = span
}

func TestReconcileRunFitsInsideItsSchedule(t *testing.T) {
	// The three numbers that set the drain rate have to stay consistent with
	// each other. A full run must finish before the next one is scheduled,
	// or runs overlap and both select the same stranded fixtures.
	span := time.Duration(defaultReconcileBatches) * reconcileBatchSpacing
	if span >= time.Hour {
		t.Fatalf("a full run spans %s against an hourly schedule; runs would overlap", span)
	}
	// And the run has to be worth making: at 20 fixtures a batch it should
	// clear a multi-thousand backlog in a small number of runs, not fifty.
	perRun := defaultReconcileBatches * MaxFixtureIDsPerRequest
	if perRun < 1000 {
		t.Fatalf("a run covers only %d fixtures; the backlog it exists to clear is thousands", perRun)
	}
}

func TestReconcileOverridesFallBackRatherThanRefusing(t *testing.T) {
	// These are 3am dials. A typo must not stop the worker draining.
	t.Setenv("RECONCILE_MAX_BATCHES", "not-a-number")
	if got := envInt("RECONCILE_MAX_BATCHES", 150, 1, 1000); got != 150 {
		t.Fatalf("bad override used: %d", got)
	}
	t.Setenv("RECONCILE_MAX_BATCHES", "99999")
	if got := envInt("RECONCILE_MAX_BATCHES", 150, 1, 1000); got != 150 {
		t.Fatalf("out-of-range override used: %d", got)
	}
	t.Setenv("RECONCILE_MAX_BATCHES", "40")
	if got := envInt("RECONCILE_MAX_BATCHES", 150, 1, 1000); got != 40 {
		t.Fatalf("valid override ignored: %d", got)
	}
}
