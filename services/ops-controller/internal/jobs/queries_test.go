package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The read seam is exercised with a fake Queryer, matching how the write path
// is tested in repository_test.go: no driver, no live database.

func assignQueryScan(dest []any, values []any) error {
	if len(dest) != len(values) {
		return fmt.Errorf("scan arity: got %d dest for %d values", len(dest), len(values))
	}
	for i, value := range values {
		switch target := dest[i].(type) {
		case *string:
			*target = value.(string)
		case *int64:
			*target = value.(int64)
		case *int:
			*target = value.(int)
		case *bool:
			*target = value.(bool)
		case *time.Time:
			*target = value.(time.Time)
		default:
			return fmt.Errorf("unsupported scan dest %T", dest[i])
		}
	}
	return nil
}

type queryRows struct {
	values [][]any
	index  int
	err    error
	closed int
}

func (r *queryRows) Next() bool {
	if r.err != nil || r.index >= len(r.values) {
		return false
	}
	r.index++
	return true
}
func (r *queryRows) Scan(dest ...any) error { return assignQueryScan(dest, r.values[r.index-1]) }
func (r *queryRows) Err() error             { return r.err }
func (r *queryRows) Close() error           { r.closed++; return nil }

type queryDB struct {
	rows    []*queryRows
	err     error
	queries []string
	args    [][]any
	index   int
}

func (d *queryDB) QueryContext(_ context.Context, query string, args ...any) (Rows, error) {
	d.queries = append(d.queries, query)
	d.args = append(d.args, args)
	if d.err != nil {
		return nil, d.err
	}
	rows := d.rows[d.index]
	d.index++
	return rows, nil
}

var queryClock = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

func queryNow() time.Time { return queryClock }

func newQueryQueries(t *testing.T, db Queryer) *Queries {
	t.Helper()
	queries, err := NewQueries(db, queryNow)
	if err != nil {
		t.Fatal(err)
	}
	return queries
}

func TestQueueDepthAggregatesWaitingLeasedAndDeadLetters(t *testing.T) {
	oldest := queryClock.Add(-90 * time.Minute)
	db := &queryDB{rows: []*queryRows{{values: [][]any{
		{"ingest", int64(12), int64(3), int64(2), oldest, oldest, 900},
		{"default", int64(1), int64(0), int64(0), queryClock.Add(-time.Minute), queryClock.Add(-time.Minute), 100},
	}}}}
	queries := newQueryQueries(t, db)

	snapshot, err := queries.QueueDepth(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TotalWaiting != 13 || snapshot.TotalLeased != 3 || snapshot.TotalDeadLettered != 2 {
		t.Fatalf("totals not aggregated: %+v", snapshot)
	}
	if snapshot.Queues[0].OldestWaitingAge != 90*time.Minute {
		t.Fatalf("oldest waiting age=%s want 1h30m", snapshot.Queues[0].OldestWaitingAge)
	}
	if snapshot.OldestWaitingAge != 90*time.Minute {
		t.Fatalf("snapshot must carry the worst queue age: %s", snapshot.OldestWaitingAge)
	}
	if snapshot.Queues[0].MaxWaitingPriority != 900 {
		t.Fatalf("raw priority must be reported unmapped: %+v", snapshot.Queues[0])
	}
	if snapshot.Truncated {
		t.Fatalf("two queues under a limit of ten must not be truncated")
	}
	if db.rows[0].closed != 1 {
		t.Fatalf("rows closed %d times, want 1", db.rows[0].closed)
	}
}

func TestQueueDepthEmptyQueuesReturnZeroSnapshot(t *testing.T) {
	db := &queryDB{rows: []*queryRows{{}}}
	queries := newQueryQueries(t, db)
	snapshot, err := queries.QueueDepth(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Queues) != 0 || snapshot.TotalWaiting != 0 || snapshot.OldestWaitingAge != 0 {
		t.Fatalf("empty queue table must yield a zero snapshot: %+v", snapshot)
	}
	if snapshot.Truncated {
		t.Fatalf("an empty result is not truncated")
	}
	if snapshot.DeadLetterWindow != DefaultDeadLetterWindow {
		t.Fatalf("dead-letter window must be reported: %s", snapshot.DeadLetterWindow)
	}
}

func TestQueueDepthZeroWaitingReportsNoAgeOrTimestamps(t *testing.T) {
	// The SQL COALESCEs MIN(...) to the epoch when nothing is waiting; that
	// sentinel must never surface as a 56-year-old job.
	epoch := time.Unix(0, 0).UTC()
	db := &queryDB{rows: []*queryRows{{values: [][]any{
		{"idle", int64(0), int64(4), int64(0), epoch, epoch, 0},
	}}}}
	queries := newQueryQueries(t, db)
	snapshot, err := queries.QueueDepth(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	depth := snapshot.Queues[0]
	if depth.OldestWaitingAge != 0 || !depth.OldestEnqueuedAt.IsZero() || !depth.OldestAvailableAt.IsZero() {
		t.Fatalf("no waiting jobs must mean no age: %+v", depth)
	}
}

func TestQueueDepthBoundsDeadLetterWindowAndQueueCount(t *testing.T) {
	db := &queryDB{rows: []*queryRows{{}}}
	queries := newQueryQueries(t, db)
	if _, err := queries.QueueDepth(context.Background(), MaxQueueLimit*10); err != nil {
		t.Fatal(err)
	}
	since := db.args[0][0].(time.Time)
	if !since.Equal(queryClock.Add(-DefaultDeadLetterWindow)) {
		t.Fatalf("dead-letter lower bound=%s want=%s", since, queryClock.Add(-DefaultDeadLetterWindow))
	}
	if got := db.args[0][1].(int); got != MaxQueueLimit {
		t.Fatalf("queue limit=%d want clamped to %d", got, MaxQueueLimit)
	}
	if !strings.Contains(db.queries[0], "LIMIT $2") {
		t.Fatalf("queue count is not bounded by a parameterised LIMIT: %s", db.queries[0])
	}
}

func TestQueueDepthTruncationIsReportedWhenLimitIsReached(t *testing.T) {
	rows := &queryRows{}
	for i := 0; i < 3; i++ {
		rows.values = append(rows.values, []any{
			fmt.Sprintf("q%d", i), int64(1), int64(0), int64(0), queryClock, queryClock, 100,
		})
	}
	queries := newQueryQueries(t, &queryDB{rows: []*queryRows{rows}})
	snapshot, err := queries.QueueDepth(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Truncated {
		t.Fatalf("filling the limit must report truncation: %+v", snapshot)
	}
}

func TestProcessingRateComputesThroughputAndFailureRate(t *testing.T) {
	db := &queryDB{rows: []*queryRows{{values: [][]any{
		{"ingest", int64(90), int64(9), int64(1), int64(4)},
		{"default", int64(30), int64(0), int64(0), int64(0)},
	}}}}
	queries := newQueryQueries(t, db)

	snapshot, err := queries.ProcessingRate(context.Background(), time.Hour, 10)
	if err != nil {
		t.Fatal(err)
	}
	ingest := snapshot.Queues[0]
	if ingest.Completed != 100 {
		t.Fatalf("completed must exclude cancelled: %+v", ingest)
	}
	if ingest.FailureRate != 0.1 {
		t.Fatalf("failure rate=%v want 0.1", ingest.FailureRate)
	}
	if ingest.PerMinute != 100.0/60.0 {
		t.Fatalf("per-minute=%v", ingest.PerMinute)
	}
	if snapshot.TotalCompleted != 130 || snapshot.TotalCancelled != 4 {
		t.Fatalf("totals not aggregated: %+v", snapshot)
	}
	if snapshot.FailureRate != 10.0/130.0 {
		t.Fatalf("aggregate failure rate=%v", snapshot.FailureRate)
	}
	if !snapshot.Since.Equal(queryClock.Add(-time.Hour)) || !snapshot.AsOf.Equal(queryClock) {
		t.Fatalf("window boundaries: %+v", snapshot)
	}
}

func TestProcessingRateEmptyWindowHasZeroRatesNotDivisionByZero(t *testing.T) {
	db := &queryDB{rows: []*queryRows{{}}}
	queries := newQueryQueries(t, db)
	snapshot, err := queries.ProcessingRate(context.Background(), time.Hour, 10)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.FailureRate != 0 || snapshot.PerMinute != 0 || len(snapshot.Queues) != 0 {
		t.Fatalf("no completions must be zero rates: %+v", snapshot)
	}
}

func TestProcessingRateClampsWindow(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		window time.Duration
		want   time.Duration
	}{
		{"zero uses default", 0, DefaultProcessingWindow},
		{"negative uses default", -time.Hour, DefaultProcessingWindow},
		{"tiny is raised", time.Second, MinProcessingWindow},
		{"huge is clamped", 365 * 24 * time.Hour, MaxProcessingWindow},
		{"in range is honoured", 15 * time.Minute, 15 * time.Minute},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := &queryDB{rows: []*queryRows{{}}}
			queries := newQueryQueries(t, db)
			snapshot, err := queries.ProcessingRate(context.Background(), testCase.window, 10)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Window != testCase.want {
				t.Fatalf("window=%s want=%s", snapshot.Window, testCase.want)
			}
			since := db.args[0][0].(time.Time)
			if !since.Equal(queryClock.Add(-testCase.want)) {
				t.Fatalf("since=%s want=%s", since, queryClock.Add(-testCase.want))
			}
		})
	}
}

func TestQueueReadsRespectCancellationAndNilDatabase(t *testing.T) {
	if _, err := NewQueries(nil, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil database err=%v", err)
	}
	db := &queryDB{}
	queries := newQueryQueries(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := queries.QueueDepth(ctx, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("QueueDepth err=%v", err)
	}
	if _, err := queries.ProcessingRate(ctx, time.Hour, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("ProcessingRate err=%v", err)
	}
	if len(db.queries) != 0 {
		t.Fatalf("a cancelled context must issue no query: %v", db.queries)
	}
}

func TestQueueReadsSurfaceQueryErrors(t *testing.T) {
	boom := errors.New("connection reset")
	queries := newQueryQueries(t, &queryDB{err: boom})
	if _, err := queries.QueueDepth(context.Background(), 5); !errors.Is(err, boom) {
		t.Fatalf("QueueDepth err=%v", err)
	}
	queries = newQueryQueries(t, &queryDB{err: boom})
	if _, err := queries.ProcessingRate(context.Background(), time.Hour, 5); !errors.Is(err, boom) {
		t.Fatalf("ProcessingRate err=%v", err)
	}
}

func TestQueueReadsAreSelectOnlyBoundedAndParameterised(t *testing.T) {
	for name, query := range map[string]string{
		"queueDepth":     queueDepthSQL,
		"processingRate": processingRateSQL,
	} {
		upper := strings.ToUpper(query)
		for _, forbidden := range []string{"INSERT ", "UPDATE ", "DELETE ", "ALTER ", "DROP ", "FOR UPDATE", "SKIP LOCKED"} {
			if strings.Contains(upper, forbidden) {
				t.Fatalf("%s must be read-only, found %q", name, forbidden)
			}
		}
		if !strings.Contains(query, "LIMIT $") {
			t.Fatalf("%s must be bounded by a parameterised LIMIT", name)
		}
		if strings.Contains(query, "%s") || strings.Contains(query, "%d") {
			t.Fatalf("%s looks string-built rather than parameterised", name)
		}
	}
}

func TestQueueReadsNeverReinterpretRawPriorityAsAPLevel(t *testing.T) {
	// Guards the migration 000040 warning: larger raw values are claimed
	// first, which is the reverse of P0 < P1 < P2. The read must not map.
	db := &queryDB{rows: []*queryRows{{values: [][]any{
		{"ingest", int64(1), int64(0), int64(0), queryClock, queryClock, 900},
	}}}}
	queries := newQueryQueries(t, db)
	snapshot, err := queries.QueueDepth(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	p0, err := PriorityValue(PriorityP0)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Queues[0].MaxWaitingPriority != p0 {
		t.Fatalf("raw priority must pass through unchanged: got %d want %d",
			snapshot.Queues[0].MaxWaitingPriority, p0)
	}
}
