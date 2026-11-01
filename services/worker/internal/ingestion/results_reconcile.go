package ingestion

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
)

// Results reconciliation: fetch the result of a match that has been played and
// never got one.
//
// The scheduled fixture refresh walks dates from -3 to +7 days. A fixture whose
// result never arrived inside that window is never asked about again, and it
// sits at `upcoming` or `unknown` forever. On 2026-09-21 production held 2,912
// such fixtures, the oldest kicked off on 2025-12-18 -- nine months of matches
// that were played and whose results nobody ever fetched.
//
// That is not a cosmetic backlog. A past fixture with no result suppressed both
// its teams from prediction for a fortnight, so one unfetched result in
// December was still deleting prediction cards in September. The date-window
// refresh cannot fix this by being widened: any window has an edge, and a
// fixture that slips it is orphaned by exactly the same mechanism one day
// further out. This asks by fixture id instead, so age stops mattering.
//
// It is deliberately cheap. Twenty fixtures per provider request via the `ids=`
// branch (4.1's rule: never call the provider once per fixture), a bounded
// number of batches per run, and it reuses the ordinary fixtures ingest path
// rather than a second copy of parsing and normalisation.

// ResultsReconcileJobKey identifies the planner job.
func ResultsReconcileJobKey() string {
	return ProviderJobKey(apifootball.ProviderName, "results.reconcile")
}

// reconcileGrace is how long after kickoff a match is left alone.
//
// Long enough to cover a full match plus extra time, penalties and the feed lag
// behind them, so this never competes with the live poll for a match still
// being played. Shorter than that and reconciliation would fetch fixtures the
// live path already owns.
const reconcileGrace = 6 * time.Hour

// defaultReconcileBatches bounds one run.
//
// Three numbers set the drain rate and only one of them is quota:
//
//	batches x MaxFixtureIDsPerRequest = fixtures per run
//	batches x reconcileBatchSpacing   = how long a run takes
//	the schedule (hourly)             = how often it repeats
//
// 25 batches was sized for a 2,912 backlog before the exclusive lock was
// understood, and it drains 500 fixtures an hour at best -- ten hours for the
// backlog, far longer once live polling takes the same lock. 150 covers 3,000
// fixtures a run and clears the whole backlog in two.
//
// Quota is not the constraint and never was: 150 requests an hour is 3,600 a
// day, under five percent of the 75,000 plan. The constraint is the lock the
// fixtures pipeline holds, which is why raising this is only safe now that
// contention defers instead of failing -- an over-large run parks its excess
// and drains next cycle rather than destroying it.
//
// RECONCILE_MAX_BATCHES overrides it, so the rate can be tuned on the host
// without a deploy.
const defaultReconcileBatches = 150

// reconcileBatchSpacing staggers the batches this job enqueues.
//
// The fixtures job holds an exclusive lock, pipeline:api-football:fixtures, so
// only one batch can run at a time whatever we queue. Enqueuing all 25 at once
// does not make them faster, it makes 24 of them contend: each retries against
// a held lock and exhausts its attempts before the lock frees. That is not
// theoretical -- it is what happened on the first production run, where 23 of
// 25 batches failed with "job already running for lock".
//
// Spacing them matches the queue to the lock that already serialises it.
//
// 45 seconds was chosen when contention still killed jobs, so the spacing had
// to guarantee no collision at all. It no longer does: a contended job defers
// and keeps its attempt, so the cost of an occasional collision is one
// rescheduling rather than lost work. Ten seconds keeps the queue from
// thrashing while letting 150 batches finish inside the hour they are
// scheduled in.
//
// RECONCILE_BATCH_SPACING overrides it.
const reconcileBatchSpacing = 10 * time.Second

// reconcileStaleSQL selects played fixtures that never got a result.
const reconcileStaleSQL = `
SELECT pem.provider_entity_id::bigint
FROM matches m
JOIN provider_entity_maps pem
  ON pem.provider = $1
 AND pem.entity_type = 'match'
 AND pem.canonical_entity_type = 'match'
 AND pem.canonical_entity_id = m.id::text
 AND pem.retired_at IS NULL
WHERE m.kickoff_at < now() - ($2 * interval '1 hour')
  AND m.status NOT IN (
    'finished', 'cancelled', 'canceled', 'postponed',
    'abandoned', 'awarded', 'walkover'
  )
  AND pem.provider_entity_id ~ '^[0-9]+$'
ORDER BY m.kickoff_at DESC
LIMIT $3`

// ResultsReconcilePlanner finds played fixtures that never got a result.
type ResultsReconcilePlanner struct {
	db interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}
}

func NewResultsReconcilePlanner(db *sql.DB) ResultsReconcilePlanner {
	return ResultsReconcilePlanner{db: db}
}

// Stale returns provider fixture ids for matches played without a result.
//
// Newest first. A result from last night is what somebody is looking at now; a
// fixture from December has already done its damage and can wait a run. It also
// means a backlog drains from the end that matters rather than from 2025.
//
// There is deliberately no lower bound on age. The absence of one is the fix:
// an age ceiling here would recreate the orphaning window this exists to close.
func (p ResultsReconcilePlanner) Stale(ctx context.Context, provider string, limit int) ([]int64, error) {
	if p.db == nil {
		return nil, errors.New("results reconcile planner DB is nil")
	}
	if limit <= 0 || limit > 2000 {
		limit = defaultReconcileBatches * MaxFixtureIDsPerRequest
	}
	rows, err := p.db.QueryContext(ctx, reconcileStaleSQL, provider, reconcileGrace.Hours(), limit)

	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ResultsReconcileJobOptions wires the planner job.
type ResultsReconcileJobOptions struct {
	// Stale returns provider fixture ids for played matches with no result.
	// A function rather than the planner struct so the batching, the ceiling
	// and the idempotency key can be tested without a database -- they are
	// the parts that decide what the provider is asked for.
	Stale      func(ctx context.Context, limit int) ([]int64, error)
	Jobs       providerPredictionsEnqueuer
	MaxBatches int
	Now        func() time.Time
}

type ResultsReconcileJob struct {
	opts ResultsReconcileJobOptions
}

func NewResultsReconcileJob(opts ResultsReconcileJobOptions) ResultsReconcileJob {
	return ResultsReconcileJob{opts: opts}
}

// Run enqueues `ids=` fixture refreshes for played fixtures with no result.
//
// It enqueues rather than fetching: the fixtures job already parses, maps and
// normalises a provider fixture, and a second implementation of that here would
// be a second place for the status vocabulary to drift.
func (j ResultsReconcileJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Stale == nil {
		return errors.New("results reconcile job needs a stale-fixture source")
	}
	if j.opts.Jobs == nil {
		return errors.New("results reconcile job enqueuer is required")
	}
	now := time.Now().UTC
	if j.opts.Now != nil {
		now = j.opts.Now
	}
	maxBatches := j.opts.MaxBatches
	if maxBatches <= 0 {
		maxBatches = envInt("RECONCILE_MAX_BATCHES", defaultReconcileBatches, 1, 1000)
	}
	spacing := envDuration("RECONCILE_BATCH_SPACING", reconcileBatchSpacing, time.Second, 5*time.Minute)

	ids, err := j.opts.Stale(ctx, maxBatches*MaxFixtureIDsPerRequest)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		slog.Info("results reconcile found nothing to fetch", "provider", apifootball.ProviderName, "job_id", job.ID)
		return nil
	}

	at := now()
	batches := 0
	for start := 0; start < len(ids) && batches < maxBatches; start += MaxFixtureIDsPerRequest {
		end := start + MaxFixtureIDsPerRequest
		if end > len(ids) {
			end = len(ids)
		}
		batch := normalizeFixtureIDs(ids[start:end])
		if len(batch) == 0 {
			continue
		}
		payload, err := json.Marshal(fixturesJobPayload{IDs: batch})
		if err != nil {
			return err
		}
		// The idempotency key carries the batch, not the run: two runs that
		// select the same stranded fixtures must not queue the same request
		// twice, and a fixture whose result still has not arrived is selected
		// again on the next run by design.
		key := "reconcile:api-football:results:" + fixtureIDsParam(batch)
		if _, err := j.opts.Jobs.Enqueue(ctx, jobs.NewJob{
			JobKey:               FixturesJobKey(),
			Queue:                "provider-fixtures",
			Priority:             45,
			Payload:              payload,
			IdempotencyKey:       key,
			DeduplicateCompleted: false,
			RunAfter:             at.Add(time.Duration(batches) * spacing),
		}); err != nil {
			return err
		}
		batches++
	}

	slog.Info("results reconcile enqueued targeted refreshes",
		"provider", apifootball.ProviderName,
		"job_id", job.ID,
		"stranded_found", len(ids),
		"batches_enqueued", batches,
		"fixtures_requested", batches*MaxFixtureIDsPerRequest,
	)
	return nil
}

// envInt reads a bounded integer override, falling back silently.
//
// Read here rather than threaded through config because these two are
// operational dials: the right value depends on how deep the backlog is and
// how busy the fixtures lock is on the day, and both are things an operator
// learns at 3am rather than at build time. A bad value falls back to the
// default rather than refusing to start -- a worker that will not boot is a
// worse outcome than one draining at the wrong rate.
func envInt(name string, fallback, min, max int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < min || parsed > max {
		slog.Warn("ignoring out-of-range override", "name", name, "value", raw, "using", fallback)
		return fallback
	}
	return parsed
}

func envDuration(name string, fallback, min, max time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < min || parsed > max {
		slog.Warn("ignoring out-of-range override", "name", name, "value", raw, "using", fallback)
		return fallback
	}
	return parsed
}
