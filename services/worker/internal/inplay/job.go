// Package inplay drives the in-play prediction cadence (architecture v2 7.6).
//
// 7.6 is "event-driven first, timer as backstop", and this package is both
// halves of that sentence:
//
//   - Event-driven. The fixtures ingest writes a live snapshot for every match
//     it sees in play, and enqueues this job in the same pass. A goal
//     therefore reaches the engine on the next poll rather than on the next
//     tick.
//   - Timer as backstop. After a run that saw at least one live fixture, the
//     job re-enqueues itself one tick later. While matches are live the loop
//     keeps itself alive; when the last one ends the loop stops on its own and
//     the next live snapshot starts it again.
//
// WHY THE LOOP IS SELF-SUSTAINING RATHER THAN SCHEDULED. A fixed schedule
// entry would fire every tick of every day, including the many hours with no
// football at all, and the interval it needs (finer than the 90-second
// snapshot freshness limit) is an order of magnitude below anything else the
// planner runs. A loop that only exists while matches do costs nothing when
// nothing is live and cannot drift from the ingest that feeds it.
//
// WHAT THIS PACKAGE DOES NOT DECIDE. Whether an individual fixture is
// regenerated is not decided here. `inplay_model.should_regenerate` owns that,
// because it is the thing that knows whether the score, the cards or the
// period changed since the last published row. This job's only cadence
// decision is how often to ask.
package inplay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/predictionenginepython"
)

const (
	// JobKey is the in-play generation job. Distinct from
	// predictions.generate.upcoming on purpose: the two write different
	// lifecycle stages and must be separately observable, retryable and
	// disableable.
	JobKey = "predictions.generate.in_play"

	// Queue is the existing predictions queue. In-play work is prediction
	// work; a queue of its own would need its own processor, its own
	// concurrency budget and its own failure story for no gain.
	Queue = "predictions"

	// Priority sits above pre-match generation. A pre-match batch can be
	// minutes late without anyone noticing; an in-play prediction that is
	// minutes late is describing a match that has moved on.
	Priority = 3
)

const (
	// DefaultTick is how often the backstop asks while matches are live.
	//
	// Finer than both 7.6 intervals (2 and 5 minutes) deliberately: the tick
	// is not the regeneration rate, it is how quickly an event that already
	// happened can be noticed. It is also below the 90-second snapshot
	// freshness limit, so a fixture cannot go stale purely because the loop
	// was between ticks.
	DefaultTick = 30 * time.Second

	// DefaultBatchSize bounds one run. The candidate query already restricts
	// to fixtures with a snapshot inside the freshness window, so this is a
	// ceiling on a pathological day rather than a routine limit -- a busy
	// Saturday peaks near 60 concurrent matches.
	DefaultBatchSize = 200
)

// Enqueuer is the slice of the job repository this package needs. The
// signature matches ingestion's own enqueuer so the ingest can pass its
// dependency straight through rather than adapting it.
type Enqueuer interface {
	Enqueue(context.Context, jobs.NewJob) (int64, error)
}

// Runner executes the in-play scope and reports what it did. Satisfied by
// predictionenginepython.JobHandler built with NewInPlayJobHandler.
type Runner interface {
	RunWithSummaries(context.Context, jobs.Job) ([]predictionenginepython.Summary, error)
}

// Job is the worker handler for JobKey.
type Job struct {
	Runner Runner
	Jobs   Enqueuer
	Tick   time.Duration
	Tier   string
	Logger *slog.Logger
	Now    func() time.Time
}

// Run executes one in-play pass and schedules the next one if it is still
// needed.
//
// The reschedule is deliberately AFTER the run and deliberately conditional on
// the run having seen live fixtures. Rescheduling first would keep the loop
// alive through a persistently failing engine; rescheduling unconditionally
// would keep it alive through the night.
func (j Job) Run(ctx context.Context, job jobs.Job) error {
	if j.Runner == nil {
		return errors.New("in-play job requires a runner")
	}
	summaries, err := j.Runner.RunWithSummaries(ctx, j.payloadFor(job))
	if err != nil {
		return err
	}
	live := 0
	for _, summary := range summaries {
		live += summary.FixturesProcessed
		j.log(ctx, job, summary)
	}
	if live == 0 {
		return nil
	}
	return j.scheduleNext(ctx)
}

// Enqueue starts or extends the loop. The fixtures ingest calls it whenever it
// records live snapshots; it is safe to call on every poll because the
// idempotency key buckets by tick.
func Enqueue(ctx context.Context, enqueuer Enqueuer, now time.Time, tick time.Duration) error {
	if enqueuer == nil {
		return nil
	}
	_, err := enqueuer.Enqueue(ctx, NewJob(now, tick, now.UTC()))
	return err
}

// NewJob builds one tick of the loop.
//
// The idempotency key is the tick bucket, which is what makes the two triggers
// compose rather than multiply: the ingest may enqueue a dozen times in a
// bucket while the previous run also re-enqueues into it, and they collapse
// onto a single job.
func NewJob(now time.Time, tick time.Duration, runAfter time.Time) jobs.NewJob {
	if tick <= 0 {
		tick = DefaultTick
	}
	bucket := runAfter.UTC().Truncate(tick).Format("20060102T150405Z")
	return jobs.NewJob{
		JobKey:               JobKey,
		Queue:                Queue,
		Priority:             Priority,
		Payload:              []byte(`{"scope":"` + predictionenginepython.InPlayScope + `","batch_size":` + strconv.Itoa(DefaultBatchSize) + `}`),
		IdempotencyKey:       "schedule:predictions:in-play:" + bucket,
		DeduplicateCompleted: true,
		RunAfter:             runAfter.UTC(),
	}
}

func (j Job) scheduleNext(ctx context.Context) error {
	if j.Jobs == nil {
		return nil
	}
	tick := j.tick()
	next := j.now().UTC().Add(tick)
	if _, err := j.Jobs.Enqueue(ctx, NewJob(j.now(), tick, next)); err != nil {
		return fmt.Errorf("enqueue next in-play tick: %w", err)
	}
	return nil
}

// payloadFor stamps the configured tier onto the job the runner receives.
//
// Done here rather than at enqueue time so the tier is a deployment decision
// that takes effect on the next tick, not a value frozen into every queued job
// by whichever process happened to enqueue it.
func (j Job) payloadFor(job jobs.Job) jobs.Job {
	tier := strings.TrimSpace(j.Tier)
	if tier == "" {
		return job
	}
	job.Payload = []byte(`{"scope":"` + predictionenginepython.InPlayScope +
		`","batch_size":` + strconv.Itoa(DefaultBatchSize) +
		`,"tier":"` + tier + `"}`)
	return job
}

func (j Job) log(ctx context.Context, job jobs.Job, summary predictionenginepython.Summary) {
	logger := j.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.InfoContext(ctx, "in-play prediction run",
		"job_id", job.ID,
		"job_key", job.JobKey,
		"fixtures_processed", summary.FixturesProcessed,
		"fixtures_published", summary.FixturesPublished,
		"fixtures_suppressed", summary.FixturesSuppressed,
		"predictions_generated", summary.PredictionsGenerated,
		"predictions_written", summary.PredictionsWritten,
		// 14: a suppression is an outcome with a reason, and the reason is
		// the operational signal. Logged even when it is empty, so its
		// absence is informative too.
		"suppressed_by_reason", summary.SuppressedByReason,
		"tier", summary.Tier,
	)
}

func (j Job) tick() time.Duration {
	if j.Tick > 0 {
		return j.Tick
	}
	return DefaultTick
}

func (j Job) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}
