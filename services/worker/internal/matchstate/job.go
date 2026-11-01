package matchstate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

// Job runs one reaper sweep.
type Job struct {
	Store      Store
	Reconciler Reconciler
	Resolver   ResultResolver
	Logger     *slog.Logger
	Now        func() time.Time
	Limit      int
}

func (j Job) Run(ctx context.Context, _ jobs.Job) error {
	_, err := Reaper{Store: j.Store, Reconciler: j.Reconciler, Resolver: j.Resolver, Logger: j.Logger, Now: j.Now, Limit: j.Limit}.Sweep(ctx)
	return err
}

// Enqueuer is the job queue, as the reconciler needs it.
type Enqueuer interface {
	Enqueue(context.Context, jobs.NewJob) (int64, error)
}

// DateReconciler asks the fixtures ingestion job for a fresh look at one day.
//
// It reuses the date-scoped branch that already exists rather than adding a
// per-fixture one. Two reasons, and the second is the load-bearing one:
//
//   - The provider adapter has no `ids=` branch to call (in-play architecture
//     4.1 flags this as unverified; it is absent). Adding one is a separate
//     piece of work with its own batching and fingerprint rules.
//   - Stuck matches cluster. A provider hiccup during a Saturday afternoon
//     strands the whole slate, so one request per date is not a compromise
//     against per-fixture calls -- it is strictly cheaper than the dozens the
//     architecture's own design would have spent.
type DateReconciler struct {
	Jobs     Enqueuer
	JobKey   string
	Queue    string
	Priority int
	Now      func() time.Time
}

func (d DateReconciler) ReconcileDate(ctx context.Context, day time.Time) error {
	if d.Jobs == nil {
		return nil
	}
	if d.JobKey == "" || d.Queue == "" {
		return fmt.Errorf("date reconciler needs a job key and queue")
	}
	now := time.Now().UTC()
	if d.Now != nil {
		now = d.Now().UTC()
	}
	date := day.UTC().Format(time.DateOnly)
	payload, err := json.Marshal(map[string]string{"date": date})
	if err != nil {
		return err
	}
	priority := d.Priority
	if priority <= 0 {
		// Above backfill, below live polling. A reconciliation is corrective
		// work, not the live path -- it must not compete with the poll that
		// would have made it unnecessary.
		priority = 20
	}
	_, err = d.Jobs.Enqueue(ctx, jobs.NewJob{
		JobKey:   d.JobKey,
		Queue:    d.Queue,
		Priority: priority,
		Payload:  payload,
		// At most one reconciliation per date per wall-clock hour. The bucket
		// is the current hour, not the fixture's: keying it on the fixture
		// date alone would dedupe forever and the day would never be looked at
		// again, while no key at all would re-enqueue the same date every two
		// minutes for hours and spend quota on a day already being refreshed.
		IdempotencyKey:       "matchstate:reconcile:" + date + ":" + now.Format("2006010215"),
		DeduplicateCompleted: true,
		RunAfter:             now,
	})
	return err
}

// ResultReconciler resolves specific matches by asking for their results.
//
// It enqueues the results-reconciliation planner rather than building an
// `ids=` request itself. That planner already owns the parts that are easy to
// get wrong: the join from canonical match id to provider fixture id, the
// twenty-per-request batching the provider caps at, the spacing that keeps
// batches off each other's exclusive lock, and an idempotency key derived from
// the batch so a repeated run does not queue the same request twice.
//
// Duplicating any of that here would be a second implementation of the same
// rules, and the two would drift.
type ResultReconciler struct {
	Jobs     Enqueuer
	JobKey   string
	Queue    string
	Priority int
	Now      func() time.Time
}

// ResolveMatches asks for the results of matches that just stopped claiming to
// be in play.
//
// The match ids are not passed through: the planner selects every fixture that
// is past kickoff without a result, which necessarily includes the ones just
// marked. Passing a list would mean a second selection path with its own
// horizon, and the whole point of this call is urgency, not precision -- it
// pulls the hourly sweep forward to now so "unknown" lasts a minute rather
// than three days.
func (r ResultReconciler) ResolveMatches(ctx context.Context, matchIDs []int64) error {
	if r.Jobs == nil || len(matchIDs) == 0 {
		return nil
	}
	if r.JobKey == "" || r.Queue == "" {
		return fmt.Errorf("result reconciler needs a job key and queue")
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	priority := r.Priority
	if priority <= 0 {
		priority = 44
	}
	// Keyed to a fifteen-minute bucket, and the width is a quota decision.
	//
	// The sweep runs every two minutes, so a per-minute key would let this
	// trigger the planner thirty times an hour. Each planner run enqueues up to
	// RECONCILE_MAX_BATCHES provider requests, and a fixture whose result has
	// still not arrived is re-selected by every run -- so a narrow key does not
	// resolve anything faster, it just asks the provider the same unanswerable
	// question thirty times and spends the day's budget doing it.
	//
	// Fifteen minutes bounds the pull-forward to four extra runs an hour while
	// still cutting the wait for a freshly reaped match from up to an hour to
	// at most fifteen minutes. The hourly schedule is unchanged underneath.
	bucket := now.Truncate(15 * time.Minute)
	key := "matchstate:resolve-unknown:" + bucket.Format("20060102T1504Z")
	_, err := r.Jobs.Enqueue(ctx, jobs.NewJob{
		JobKey:               r.JobKey,
		Queue:                r.Queue,
		Priority:             priority,
		Payload:              []byte(`{}`),
		IdempotencyKey:       key,
		DeduplicateCompleted: true,
		RunAfter:             now,
	})
	return err
}
