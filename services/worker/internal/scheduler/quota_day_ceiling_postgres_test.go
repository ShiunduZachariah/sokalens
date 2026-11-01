package scheduler

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/dbtest"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// backfillSlotsLeftToday is PostgreSQL-only SQL reached through a *sql.DB, so
// the unit tests around the ceiling arithmetic cannot execute it. Everything
// that could be wrong with it -- the UTC day boundary, the two LIKE patterns,
// the job_key bind -- fails at runtime, not at compile time.
//
// Run with SCHEDULER_TEST_DATABASE_URL pointed at a disposable database that
// has infra/db/migrations applied:
//
//	SCHEDULER_TEST_DATABASE_URL='postgres://sports:sports@127.0.0.1:5433/scratch?sslmode=disable' \
//	  go test ./services/worker/internal/scheduler/
//
// It copies the real jobs table into its own schema, so the column types under
// test are the migrated ones rather than a hand-written approximation.
func TestBackfillSlotsLeftTodayAgainstPostgres(t *testing.T) {
	// Through dbtest rather than a local t.Skip, so CI cannot pass this test
	// by not running it. With WORKER_REQUIRE_DB_TESTS set, as the worker job
	// sets it, a missing DSN fails here instead of quietly shrinking the
	// suite -- which is the failure this whole gating exercise is about.
	url := dbtest.URLOrSkip(t, "SCHEDULER_TEST_DATABASE_URL", "run the backfill day ceiling against PostgreSQL")
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	// search_path and TimeZone are per-connection, and the query under test
	// runs on whichever connection the pool hands out.
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const schema = "scheduler_quota_ceiling_test"
	exec := func(statements ...string) {
		t.Helper()
		for _, statement := range statements {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Fatalf("exec %.70s: %v", strings.ReplaceAll(statement, "\n", " "), err)
			}
		}
	}
	requireMigratedTable(ctx, t, db, "jobs")

	exec("DROP SCHEMA IF EXISTS "+schema+" CASCADE", "CREATE SCHEMA "+schema,
		"SET search_path TO "+schema+", public")
	// Deferred rather than t.Cleanup: cleanups run after `defer db.Close()`,
	// which would leave the schema behind on a closed handle.
	defer func() {
		if _, err := db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE"); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()

	// A deliberately non-UTC session zone, and one whose local calendar day
	// begins before UTC midnight at every hour of the day. Under the local-time
	// boundary this query must not use, the pre-midnight row below is counted
	// and the assertion fails.
	exec("SET TimeZone TO 'Pacific/Kiritimati'")
	exec("CREATE TABLE jobs (LIKE public.jobs INCLUDING ALL)")

	// The boundary is the instant the query compares against, so assert
	// directly that it is UTC midnight rather than inferring it from counts.
	var boundary, trueUTCMidnight time.Time
	if err := db.QueryRowContext(ctx, `
SELECT date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC',
       (now() AT TIME ZONE 'UTC')::date::timestamp AT TIME ZONE 'UTC'`).
		Scan(&boundary, &trueUTCMidnight); err != nil {
		t.Fatalf("read day boundary: %v", err)
	}
	if !boundary.Equal(trueUTCMidnight) {
		t.Fatalf("day boundary is not UTC midnight: got %s, want %s",
			boundary.UTC(), trueUTCMidnight.UTC())
	}
	if hour, minute, second := boundary.UTC().Clock(); hour != 0 || minute != 0 || second != 0 {
		t.Fatalf("day boundary %s is not midnight in UTC", boundary.UTC())
	}

	exec(`
INSERT INTO jobs (job_key, queue, priority, payload, idempotency_key, created_at) VALUES
  -- Counted: a league-season fixture backfill enqueued today.
  ('provider.fixtures.backfill', 'provider-fixtures', 40, '{}',
   'schedule:provider:api-football:backfill:39:2025:20261016T000000Z',
   date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + interval '1 hour'),
  -- Counted: a statistics-demand job, matched by the second LIKE pattern.
  ('provider.inplay.statistics', 'provider-stats', 30, '{}',
   'schedule:provider:api-football:stats-demand:12345',
   date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + interval '2 hours'),
  -- Counted: team history, matched on job_key with no idempotency key at all.
  ('backfill.team_history', 'provider-fixtures', 40, '{}', NULL,
   date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + interval '3 hours'),
  -- Counted: exactly on the boundary, which is inclusive.
  ('provider.fixtures.backfill', 'provider-fixtures', 40, '{}',
   'schedule:provider:api-football:backfill:41:2025:boundary',
   date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'),
  -- Not counted: a scheduled job that spends no discretionary budget.
  ('provider.fixtures.today', 'provider-fixtures', 20, '{}',
   'schedule:provider:api-football:today:20261016',
   date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + interval '4 hours'),
  -- Not counted: the previous day's backfill, one second before the boundary.
  ('provider.fixtures.backfill', 'provider-fixtures', 40, '{}',
   'schedule:provider:api-football:backfill:40:2025:stale',
   date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - interval '1 second'),
  -- Not counted: another provider's backfill key.
  ('provider.fixtures.backfill', 'provider-fixtures', 40, '{}',
   'schedule:provider:sportmonks:backfill:7:2025:bucket',
   date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + interval '5 hours')`)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Four jobs enqueued at two requests each is eight spent of twenty, so six
	// whole jobs remain. The arithmetic has its own unit test; this ceiling is
	// chosen so that any miscount of the four changes the result.
	opts := QuotaOptions{DB: db, BackfillMaxRequestsPerDay: 20}
	if got := backfillSlotsLeftToday(ctx, opts, time.Now(), logger); got != 6 {
		t.Fatalf("backfillSlotsLeftToday = %d, want 6; the query counted %d jobs today, not 4",
			got, (20-got*requestsPerBackfillJob)/requestsPerBackfillJob)
	}

	// No ceiling configured must stay unbounded.
	if got := backfillSlotsLeftToday(ctx, QuotaOptions{DB: db}, time.Now(), logger); got != -1 {
		t.Fatalf("backfillSlotsLeftToday with no ceiling = %d, want -1", got)
	}

	// Fail closed: a broken query must pause backfill rather than hand out a
	// full allowance. Dropping the table is the cheapest real failure -- and
	// public has to leave the search_path first, or the query resolves against
	// the real public.jobs, finds it empty, and reports a full allowance.
	exec("SET search_path TO "+schema, "DROP TABLE jobs")
	if got := backfillSlotsLeftToday(ctx, opts, time.Now(), logger); got != 0 {
		t.Fatalf("backfillSlotsLeftToday on a failing query = %d, want 0 (fail closed)", got)
	}
}

// requireMigratedTable skips rather than fails when the target database has not
// had infra/db/migrations applied: the test copies the real table definition,
// so without it there is nothing to copy and nothing would be verified.
func requireMigratedTable(ctx context.Context, t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var exists bool
	if err := db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_tables WHERE schemaname = 'public' AND tablename = $1)`,
		table).Scan(&exists); err != nil {
		t.Fatalf("check for public.%s: %v", table, err)
	}
	if !exists {
		t.Skipf("public.%s is missing; apply infra/db/migrations to the test database", table)
	}
}
