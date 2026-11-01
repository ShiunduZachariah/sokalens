package ingestion

import (
	"context"
	"errors"
	"strings"
	"time"
)

// The request ledger (architecture v2 §12.1). Every provider request is rolled
// up into a per-minute bucket keyed by priority class and endpoint group, on
// the same call that writes the raw request log.
//
// The rollup exists because the raw log is pruned and the dashboard questions
// are aggregate ones. Keeping the counters here means the answers survive the
// log's retention window, and a burn-down chart reads a few hundred rows
// instead of scanning a day of requests.

// Endpoint groups. These are the buckets the dashboard slices by, so they are
// deliberately coarse: the question is "what is fixtures costing", not "what is
// fixtures?date=2026-09-08 costing".
const (
	EndpointGroupLive      = "live"
	EndpointGroupFixtures  = "fixtures"
	EndpointGroupOdds      = "odds"
	EndpointGroupReference = "reference"
	EndpointGroupBackfill  = "backfill"
	EndpointGroupOther     = "other"
)

// classifyEndpointGroup maps one request to its dashboard bucket, from the
// job's reason first and the endpoint only as a fallback.
//
// The reason comes first because the endpoint cannot answer the question. Every
// fixture-shaped job -- the live poll, the results reconcile, the date sweeps,
// the batched in-play statistics poll, the league-season backfill -- calls the
// provider's `fixtures` endpoint, so the endpoint string is "fixtures" for all
// of them. Classifying on it collapsed five different kinds of spend into one
// bucket.
//
// This was not a theoretical gap. Measured in production on 2026-10-16: the
// live poll had made 361 requests and backfill 70, and the dashboard read
// `worker_quota_today_endpoint_live 0` and `..._backfill 0`, with 1,194
// against `fixtures`. An operator reading that would conclude live polling
// costs nothing -- the one number the burn-down chart exists to show.
//
// The previous version expected endpoints like "fixtures.live" and
// "fixtures.backfill", and its test asserted on exactly those strings. No
// adapter has ever written them, so the test passed on inputs that do not
// occur while the real inputs were misfiled. The reason field, which the
// request log already carries, is what the scheduler actually sets.
//
// Anything unrecognised lands in "other" rather than being dropped: spend
// nobody classified is exactly the spend worth noticing, and discarding it
// would make the ledger disagree with the raw log.
func classifyEndpointGroup(reason, endpoint string) string {
	if group, ok := groupForReason(reason); ok {
		return group
	}
	e := strings.ToLower(strings.TrimSpace(endpoint))
	if e == "" {
		return EndpointGroupOther
	}
	// Order still matters for the fallback, in case an adapter ever does
	// record a qualified endpoint.
	switch {
	case strings.Contains(e, "live"):
		return EndpointGroupLive
	case strings.Contains(e, "backfill"):
		return EndpointGroupBackfill
	case strings.Contains(e, "odds"):
		return EndpointGroupOdds
	case strings.Contains(e, "fixture"):
		return EndpointGroupFixtures
	case strings.Contains(e, "country"), strings.Contains(e, "countries"),
		strings.Contains(e, "league"), strings.Contains(e, "team"),
		strings.Contains(e, "venue"), strings.Contains(e, "player"),
		strings.Contains(e, "season"), strings.Contains(e, "standing"):
		return EndpointGroupReference
	default:
		return EndpointGroupOther
	}
}

// groupForReason maps the scheduler's own reason for a request onto a bucket.
//
// The vocabulary is the set observed in production, and the mapping is
// deliberately explicit rather than a substring match: "reconcile" and
// "reference" both contain "re", and a reason nobody has mapped should fall
// through to the endpoint and then to "other" rather than land somewhere
// plausible-looking by accident.
//
// The second return value distinguishes "not a reason I know" from "a reason
// that means other", because only the first should fall back to the endpoint.
func groupForReason(reason string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "live":
		return EndpointGroupLive, true
	case "backfill", "team_history":
		return EndpointGroupBackfill, true
	case "odds":
		return EndpointGroupOdds, true
	case "reference", "countries", "leagues", "teams", "players":
		return EndpointGroupReference, true
	// Fixture data by another route: the hourly results reconcile, the
	// date-scoped pre-match sweeps, the per-fixture statistics fetch and the
	// batched in-play statistics poll. All of them read fixtures.
	case "reconcile", "prematch", "statistics", "inplay_statistics":
		return EndpointGroupFixtures, true
	// Reconciling our view of the quota with the provider's. It reads
	// /status, which is not fixture data and not reference data either.
	case "quota_sync":
		return EndpointGroupOther, true
	default:
		return "", false
	}
}

// classifyPriority maps the numeric job priority the scheduler assigns onto the
// P0..P5 class vocabulary the quota planner reasons in.
//
// The two are separate scales that nobody had to reconcile until now: the
// scheduler orders work with small-is-urgent integers, the planner allocates
// budget by class. The bands below are the scheduler's own assignments read
// back -- live polling is enqueued at 10, reference at 20-25, fixtures at 30,
// odds at 35, backfill lower still. A call site that knows its class should set
// RequestLog.PriorityClass explicitly rather than rely on this.
func classifyPriority(priority int) string {
	switch {
	case priority <= 0:
		// Unset. Not guessed as P0: an unlabelled request is not evidence of
		// live-critical spend, and calling it P0 would corrupt the one number
		// the alerts page cares about most.
		return "unknown"
	case priority <= 15:
		return "P0"
	case priority <= 28:
		return "P1"
	case priority <= 32:
		return "P2"
	case priority <= 40:
		return "P3"
	case priority <= 60:
		return "P4"
	default:
		return "P5"
	}
}

// ledgerErrorCount reports whether a logged request should count against the
// error counter. A request that never reached the provider still consumed a
// scheduling slot but no quota, so status 0 with an error category counts;
// a clean 2xx does not.
func ledgerErrorCount(statusCode int, errorCategory string) int {
	if strings.TrimSpace(errorCategory) != "" {
		return 1
	}
	if statusCode == 0 {
		return 0
	}
	if statusCode < 200 || statusCode >= 300 {
		return 1
	}
	return 0
}

// minuteBucket truncates to the minute in UTC.
//
// Computed in Go rather than with date_trunc so the statement stays portable:
// the unit tests run this against SQLite while production runs PostgreSQL, and
// a dialect-specific function here would mean the ledger is only ever exercised
// in one of them.
func minuteBucket(at time.Time) time.Time {
	return at.UTC().Truncate(time.Minute)
}

// ledgerEntry is the rollup coordinate a single request contributes to.
type ledgerEntry struct {
	Provider      string
	MinuteBucket  time.Time
	PriorityClass string
	EndpointGroup string
}

func ledgerEntryFor(log RequestLog) ledgerEntry {
	class := strings.TrimSpace(log.PriorityClass)
	if class == "" {
		class = classifyPriority(log.Priority)
	}
	group := strings.TrimSpace(log.EndpointGroup)
	if group == "" {
		group = classifyEndpointGroup(log.Reason, log.Endpoint)
	}
	return ledgerEntry{
		Provider:      log.Provider,
		MinuteBucket:  minuteBucket(log.RequestedAt),
		PriorityClass: class,
		EndpointGroup: group,
	}
}

const upsertQuotaMinuteSQL = `
INSERT INTO provider_quota_minute (
  provider, minute_bucket, priority_class, endpoint_group,
  request_count, error_count, bytes_total, rows_written
) VALUES ($1, $2, $3, $4, 1, $5, $6, 0)
ON CONFLICT (provider, minute_bucket, priority_class, endpoint_group)
DO UPDATE SET
  request_count = provider_quota_minute.request_count + 1,
  error_count   = provider_quota_minute.error_count + excluded.error_count,
  bytes_total   = provider_quota_minute.bytes_total + excluded.bytes_total`

// recordRequest adds one request to its minute bucket.
func (r RequestLogRepository) recordRequest(ctx context.Context, log RequestLog) error {
	if r.db == nil {
		return errors.New("request log DB is nil")
	}
	entry := ledgerEntryFor(log)
	bytes := log.ResponseBytes
	if bytes < 0 {
		bytes = 0
	}
	_, err := r.db.ExecContext(ctx, upsertQuotaMinuteSQL,
		entry.Provider,
		entry.MinuteBucket,
		entry.PriorityClass,
		entry.EndpointGroup,
		ledgerErrorCount(log.StatusCode, log.ErrorCategory),
		bytes,
	)
	return err
}

const addRowsWrittenSQL = `
INSERT INTO provider_quota_minute (
  provider, minute_bucket, priority_class, endpoint_group,
  request_count, error_count, bytes_total, rows_written
) VALUES ($1, $2, $3, $4, 0, 0, 0, $5)
ON CONFLICT (provider, minute_bucket, priority_class, endpoint_group)
DO UPDATE SET rows_written = provider_quota_minute.rows_written + excluded.rows_written`

// AddRowsWritten attributes yield to the bucket the request landed in.
//
// Separate from recordRequest because the row count is not known when the
// request completes -- it is known when the ingesting job finishes writing.
// Callers pass the same RequestLog they logged, so the two agree on the bucket
// without the caller having to know how classification works.
//
// A zero or negative count is a no-op rather than an error: a response that
// changed nothing is a normal outcome, and inserting an empty bucket for it
// would make "no requests" and "requests that yielded nothing" look alike.
func (r RequestLogRepository) AddRowsWritten(ctx context.Context, log RequestLog, rows int) error {
	if rows <= 0 {
		return nil
	}
	if r.db == nil {
		return errors.New("request log DB is nil")
	}
	// Insert defaults a missing timestamp to now() on its own copy, so a caller
	// that omitted it would have its request counted in the current minute and
	// its yield counted in year one. Refuse rather than write the orphan: the
	// caller passes the same RequestLog to both, and the only way these disagree
	// is a caller that never set the field at all.
	if log.RequestedAt.IsZero() {
		return errors.New("cannot attribute yield: RequestLog has no RequestedAt")
	}
	entry := ledgerEntryFor(log)
	_, err := r.db.ExecContext(ctx, addRowsWrittenSQL,
		entry.Provider,
		entry.MinuteBucket,
		entry.PriorityClass,
		entry.EndpointGroup,
		rows,
	)
	return err
}

// yieldRecorder is the optional half of requestLogger. It is a separate
// interface, rather than another method on requestLogger, so that the fakes in
// the existing job tests keep compiling: a logger that cannot record yield is a
// logger that records no yield, which is exactly what those tests assert about.
type yieldRecorder interface {
	AddRowsWritten(context.Context, RequestLog, int) error
}

// recordYield attributes rows written to the request that produced them, when
// the configured logger can record it.
//
// Yield is reporting, not ingestion: a failure here must not fail a job that
// has already written its rows successfully, because that would roll the job
// back and refetch on retry -- spending quota to fix a number on a chart. The
// error is returned for the caller to log, never to propagate.
func recordYield(ctx context.Context, logger requestLogger, log RequestLog, rows int) error {
	recorder, ok := logger.(yieldRecorder)
	if !ok || rows <= 0 {
		return nil
	}
	return recorder.AddRowsWritten(ctx, log, rows)
}
