// Package snapshots owns the append-only live match history (in-play
// architecture §5) and the partition maintenance it depends on.
package snapshots

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"
)

// PartitionedTables is the set the maintainer keeps ahead.
//
// A list rather than a constant because §5.1 specifies three append-only
// tables and only one had a writer when this was written. Adding another is an
// entry here plus a migration, not a rewrite -- which is the point of
// maintaining partitions generically instead of per table.
//
// fixture_events belongs here for the reason its migration states and cannot
// enforce: 000070 pre-creates three months and then defers to this job. Left
// out, inserts succeed until the clock crosses into the first month nobody
// created, and then every live event is lost during a match -- the one time
// the data cannot be re-fetched, because the provider embeds events in the
// live response and never serves them again.
// inplay_snapshots belongs here for the same reason as the other two: 000079
// pre-creates three months and then defers to this job. Left out, inserts
// succeed until the clock crosses into the first month nobody created, and then
// every model snapshot is lost mid-match -- and unlike a prediction, a snapshot
// cannot be recomputed later, because the live state it described is gone.
// fixture_statistics_snapshots belongs here for the same reason again: 000081
// pre-creates three months and defers to this job. Its loss mode is the
// quietest of the four, which makes it the most dangerous -- the in-play pass
// reads the latest statistics capture to condition corner and card markets, so
// a missing partition does not stop the model, it just silently starves it and
// the markets keep publishing on stale counts.
var PartitionedTables = []string{"fixture_live_snapshots", "fixture_events", "inplay_snapshots", "fixture_statistics_snapshots"}

// LeadMonths is how far ahead partitions are created.
//
// §5.1 asks for thirty days of lead. Three months is deliberately more
// generous: the cost is three empty tables, and the failure it prevents is
// every insert on the table erroring the moment the clock crosses into a month
// nobody created. That failure lands during a fixture window, which is exactly
// when the data is irreplaceable.
const LeadMonths = 3

// safeTableName guards the only place this package interpolates an identifier
// into SQL. The names come from PartitionedTables, not from input, but a
// package that builds DDL by string concatenation should not rely on that
// staying true.
var safeTableName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Execer is the database surface partition maintenance needs.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Maintainer creates the monthly partitions the snapshot tables need.
//
// It runs daily and is idempotent: every statement is CREATE TABLE IF NOT
// EXISTS, so a run that overlaps another, or repeats after a failure, is a
// no-op rather than an error.
type Maintainer struct {
	// DB is narrowed to the one method this needs. A maintainer that can only
	// execute statements cannot grow a query path by accident, and it makes
	// the generated DDL assertable without a PostgreSQL container -- SQLite,
	// which the rest of these tests use, has no declarative partitioning.
	DB     Execer
	Tables []string
	Lead   int
	Logger *slog.Logger
	Now    func() time.Time
}

func (m Maintainer) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m Maintainer) logger() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

func (m Maintainer) tables() []string {
	if len(m.Tables) > 0 {
		return m.Tables
	}
	return PartitionedTables
}

func (m Maintainer) lead() int {
	if m.Lead > 0 {
		return m.Lead
	}
	return LeadMonths
}

// Ensure creates every partition the configured lead requires.
//
// It returns the number created. Zero on a healthy system is the normal
// result, because yesterday's run already made them -- which is why a caller
// should alert on the error, not on the count.
func (m Maintainer) Ensure(ctx context.Context) (int, error) {
	if m.DB == nil {
		return 0, errors.New("snapshot partition maintainer DB is nil")
	}
	created := 0
	monthStart := startOfMonth(m.now())
	for _, table := range m.tables() {
		if !safeTableName.MatchString(table) {
			return created, fmt.Errorf("unsafe partition parent name %q", table)
		}
		for offset := 0; offset <= m.lead(); offset++ {
			start := monthStart.AddDate(0, offset, 0)
			end := start.AddDate(0, 1, 0)
			name := PartitionName(table, start)
			statement := fmt.Sprintf(
				`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
				name, table, start.Format(time.RFC3339), end.Format(time.RFC3339),
			)
			if _, err := m.DB.ExecContext(ctx, statement); err != nil {
				// Returned rather than logged and skipped. A partition that
				// does not exist is not a degraded feature: every insert into
				// that month fails, so this has to reach the alerting path.
				return created, fmt.Errorf("create partition %s: %w", name, err)
			}
			created++
		}
	}
	m.logger().Info("snapshot partitions ensured",
		"tables", len(m.tables()),
		"months_ahead", m.lead(),
		"statements", created,
	)
	return created, nil
}

// PartitionName is the child table name for one month.
func PartitionName(table string, start time.Time) string {
	return fmt.Sprintf("%s_%s", table, start.UTC().Format("2006_01"))
}

func startOfMonth(at time.Time) time.Time {
	at = at.UTC()
	return time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
}
