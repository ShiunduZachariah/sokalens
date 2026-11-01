package jobs

import (
	"context"
	"database/sql"
	"time"
)

// Read-only queue projections backing the operator read commands.
//
// Everything in this file is a bounded SELECT. Nothing here claims, leases,
// enqueues, or mutates a job, and nothing formats for a transport: callers
// receive typed structs.
//
// Note on priority: ops_jobs.priority is a raw integer where larger values are
// claimed first (migration 000040 says so explicitly). It is NOT a P-level.
// These reads never reinterpret it; PriorityValue in types.go remains the only
// sanctioned P-class conversion boundary, and it is one-way.

// Queryer is a read-only, non-transactional seam over *sql.DB, matching the
// database/sql-compatible DB/Tx seam the write path already uses.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (Rows, error)
}

// Rows is the bounded multi-row cursor contract; *sql.Rows satisfies it.
type Rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

// SQLQueryer adapts *sql.DB to Queryer.
type SQLQueryer struct{ DB *sql.DB }

func (q SQLQueryer) QueryContext(ctx context.Context, query string, args ...any) (Rows, error) {
	if q.DB == nil {
		return nil, ErrUnavailable
	}
	return q.DB.QueryContext(ctx, query, args...)
}

const (
	// DefaultQueueLimit and MaxQueueLimit bound the number of distinct queues
	// a single read will return. Queue names are operator-defined, so the read
	// is bounded rather than trusting the cardinality to stay small.
	DefaultQueueLimit = 25
	MaxQueueLimit     = 50

	// DefaultDeadLetterWindow bounds how far back dead-lettered jobs are
	// counted. ops_jobs keeps terminal rows, and there is no partial index on
	// dead_letter, so an all-time count would be an unbounded historical scan.
	DefaultDeadLetterWindow = 7 * 24 * time.Hour

	// MinProcessingWindow and MaxProcessingWindow bound the throughput window.
	MinProcessingWindow     = time.Minute
	MaxProcessingWindow     = 7 * 24 * time.Hour
	DefaultProcessingWindow = time.Hour
)

// QueueDepth is per-queue backlog. Waiting counts claimable work (pending and
// retry); Leased counts work a worker currently holds.
type QueueDepth struct {
	Queue        string
	Waiting      int64
	Leased       int64
	DeadLettered int64
	// OldestAvailableAt is the earliest available_at among waiting jobs, and
	// OldestEnqueuedAt the earliest created_at. Both are zero when Waiting is
	// zero.
	OldestAvailableAt time.Time
	OldestEnqueuedAt  time.Time
	// OldestWaitingAge is AsOf minus OldestEnqueuedAt: how long the oldest
	// waiting job has been in the queue. Zero when Waiting is zero.
	OldestWaitingAge time.Duration
	// MaxWaitingPriority is the raw priority integer of the most-claimable
	// waiting job. Larger is claimed first. It is not a P-level.
	MaxWaitingPriority int
}

// QueueDepthSnapshot backs /queues and the /status queue line.
type QueueDepthSnapshot struct {
	AsOf              time.Time
	Queues            []QueueDepth
	TotalWaiting      int64
	TotalLeased       int64
	TotalDeadLettered int64
	DeadLetterWindow  time.Duration
	// OldestWaitingAge is the largest per-queue age in the snapshot.
	OldestWaitingAge time.Duration
	// Truncated reports that the queue limit was reached and more distinct
	// queues exist than were returned.
	Truncated bool
}

// QueueThroughput is per-queue completion counts over a window.
type QueueThroughput struct {
	Queue        string
	Succeeded    int64
	Failed       int64
	DeadLettered int64
	Cancelled    int64
	// Completed is Succeeded + Failed + DeadLettered. Cancelled work was never
	// attempted to completion, so it is reported but excluded from throughput
	// and from the failure-rate denominator.
	Completed int64
	// FailureRate is (Failed + DeadLettered) / Completed in [0, 1]; zero when
	// Completed is zero.
	FailureRate float64
	// PerMinute is Completed divided by the window in minutes.
	PerMinute float64
}

// ProcessingRateSnapshot backs the throughput half of /queues.
type ProcessingRateSnapshot struct {
	Window            time.Duration
	Since             time.Time
	AsOf              time.Time
	Queues            []QueueThroughput
	TotalSucceeded    int64
	TotalFailed       int64
	TotalDeadLettered int64
	TotalCancelled    int64
	TotalCompleted    int64
	FailureRate       float64
	PerMinute         float64
	Truncated         bool
}

const (
	queueDepthSQL = `SELECT queue_name,
       COUNT(*) FILTER (WHERE status IN ('pending', 'retry')),
       COUNT(*) FILTER (WHERE status = 'leased'),
       COUNT(*) FILTER (WHERE status = 'dead_letter'),
       COALESCE(MIN(available_at) FILTER (WHERE status IN ('pending', 'retry')), to_timestamp(0)),
       COALESCE(MIN(created_at) FILTER (WHERE status IN ('pending', 'retry')), to_timestamp(0)),
       COALESCE(MAX(priority) FILTER (WHERE status IN ('pending', 'retry')), 0)
FROM ops_jobs
WHERE status IN ('pending', 'retry', 'leased')
   OR (status = 'dead_letter' AND dead_lettered_at >= $1)
GROUP BY queue_name
ORDER BY COUNT(*) FILTER (WHERE status IN ('pending', 'retry')) DESC, queue_name
LIMIT $2`

	processingRateSQL = `SELECT queue_name,
       COUNT(*) FILTER (WHERE status = 'succeeded'),
       COUNT(*) FILTER (WHERE status = 'failed'),
       COUNT(*) FILTER (WHERE status = 'dead_letter'),
       COUNT(*) FILTER (WHERE status = 'cancelled')
FROM ops_jobs
WHERE completed_at >= $1
  AND completed_at < $2
  AND status IN ('succeeded', 'failed', 'dead_letter', 'cancelled')
GROUP BY queue_name
ORDER BY COUNT(*) DESC, queue_name
LIMIT $3`
)

// Queries is the read-only queue projection API. It never writes.
type Queries struct {
	db  Queryer
	now func() time.Time
}

// NewQueries returns the read-only queue query API. now may be nil, in which
// case time.Now is used; tests inject a fixed clock.
func NewQueries(db Queryer, now func() time.Time) (*Queries, error) {
	if db == nil {
		return nil, ErrUnavailable
	}
	if now == nil {
		now = time.Now
	}
	return &Queries{db: db, now: now}, nil
}

func boundQueueLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultQueueLimit
	case limit > MaxQueueLimit:
		return MaxQueueLimit
	default:
		return limit
	}
}

func boundProcessingWindow(window time.Duration) time.Duration {
	switch {
	case window <= 0:
		return DefaultProcessingWindow
	case window < MinProcessingWindow:
		return MinProcessingWindow
	case window > MaxProcessingWindow:
		return MaxProcessingWindow
	default:
		return window
	}
}

// QueueDepth returns per-queue backlog with the oldest waiting job's age, plus
// dead-letter counts over DefaultDeadLetterWindow. An empty queue table yields
// a snapshot with no rows and zero totals, not an error.
func (q *Queries) QueueDepth(ctx context.Context, limit int) (QueueDepthSnapshot, error) {
	if q == nil || q.db == nil {
		return QueueDepthSnapshot{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return QueueDepthSnapshot{}, err
	}
	bounded := boundQueueLimit(limit)
	asOf := q.now().UTC()
	snapshot := QueueDepthSnapshot{
		AsOf:             asOf,
		Queues:           make([]QueueDepth, 0, bounded),
		DeadLetterWindow: DefaultDeadLetterWindow,
	}

	rows, err := q.db.QueryContext(ctx, queueDepthSQL, asOf.Add(-DefaultDeadLetterWindow), bounded)
	if err != nil {
		return QueueDepthSnapshot{}, err
	}
	defer rows.Close()

	for rows.Next() {
		var depth QueueDepth
		if err := rows.Scan(&depth.Queue, &depth.Waiting, &depth.Leased,
			&depth.DeadLettered, &depth.OldestAvailableAt, &depth.OldestEnqueuedAt,
			&depth.MaxWaitingPriority); err != nil {
			return QueueDepthSnapshot{}, err
		}
		if depth.Waiting == 0 {
			depth.OldestAvailableAt = time.Time{}
			depth.OldestEnqueuedAt = time.Time{}
		} else if age := asOf.Sub(depth.OldestEnqueuedAt); age > 0 {
			depth.OldestWaitingAge = age
		}
		snapshot.TotalWaiting += depth.Waiting
		snapshot.TotalLeased += depth.Leased
		snapshot.TotalDeadLettered += depth.DeadLettered
		if depth.OldestWaitingAge > snapshot.OldestWaitingAge {
			snapshot.OldestWaitingAge = depth.OldestWaitingAge
		}
		snapshot.Queues = append(snapshot.Queues, depth)
	}
	if err := rows.Err(); err != nil {
		return QueueDepthSnapshot{}, err
	}
	snapshot.Truncated = len(snapshot.Queues) == bounded
	return snapshot, nil
}

// ProcessingRate returns throughput and failure rate per queue over the given
// window, ending now. The window is clamped to [MinProcessingWindow,
// MaxProcessingWindow] so a caller cannot ask for an unbounded history scan.
func (q *Queries) ProcessingRate(ctx context.Context, window time.Duration, limit int) (ProcessingRateSnapshot, error) {
	if q == nil || q.db == nil {
		return ProcessingRateSnapshot{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return ProcessingRateSnapshot{}, err
	}
	bounded := boundQueueLimit(limit)
	window = boundProcessingWindow(window)
	asOf := q.now().UTC()
	since := asOf.Add(-window)
	snapshot := ProcessingRateSnapshot{
		Window: window,
		Since:  since,
		AsOf:   asOf,
		Queues: make([]QueueThroughput, 0, bounded),
	}

	rows, err := q.db.QueryContext(ctx, processingRateSQL, since, asOf, bounded)
	if err != nil {
		return ProcessingRateSnapshot{}, err
	}
	defer rows.Close()

	minutes := window.Minutes()
	for rows.Next() {
		var t QueueThroughput
		if err := rows.Scan(&t.Queue, &t.Succeeded, &t.Failed, &t.DeadLettered,
			&t.Cancelled); err != nil {
			return ProcessingRateSnapshot{}, err
		}
		t.Completed = t.Succeeded + t.Failed + t.DeadLettered
		t.FailureRate = ratio(t.Failed+t.DeadLettered, t.Completed)
		if minutes > 0 {
			t.PerMinute = float64(t.Completed) / minutes
		}
		snapshot.TotalSucceeded += t.Succeeded
		snapshot.TotalFailed += t.Failed
		snapshot.TotalDeadLettered += t.DeadLettered
		snapshot.TotalCancelled += t.Cancelled
		snapshot.TotalCompleted += t.Completed
		snapshot.Queues = append(snapshot.Queues, t)
	}
	if err := rows.Err(); err != nil {
		return ProcessingRateSnapshot{}, err
	}
	snapshot.FailureRate = ratio(snapshot.TotalFailed+snapshot.TotalDeadLettered, snapshot.TotalCompleted)
	if minutes > 0 {
		snapshot.PerMinute = float64(snapshot.TotalCompleted) / minutes
	}
	snapshot.Truncated = len(snapshot.Queues) == bounded
	return snapshot, nil
}

func ratio(numerator, denominator int64) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}
