package snapshots

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/dbtest"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Everything else in this package is tested against SQLite, which has no
// declarative partitioning at all. So the one thing that is genuinely
// PostgreSQL-specific -- whether an insert finds a partition -- has no coverage
// there, and it is also the failure this design is most exposed to: a row whose
// captured_at falls outside every partition is rejected outright.
//
// Opt-in, following services/api/internal/acca/generate_db_test.go:
//
//	SNAPSHOTS_TEST_DATABASE_URL='postgres://sports:sports@127.0.0.1:5432/scratch?sslmode=disable' \
//	  go test ./services/worker/internal/snapshots/
//
// It creates and drops its own schema. Point it at a scratch database anyway.
func TestSnapshotsAgainstPostgres(t *testing.T) {
	url := dbtest.URLOrSkip(t, "SNAPSHOTS_TEST_DATABASE_URL", "run the snapshot tests against PostgreSQL")
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	// One connection only, as services/api/internal/fixturepolicy's PostgreSQL
	// test does and for the same reason: the scratch schema is selected with
	// SET search_path, which is per-session, so a pool could hand a later
	// statement a connection that never saw it. That connection's path is
	// "$user", public -- and CI now points this at a migrated database, where
	// public holds the real tables this test creates by those names. Proven
	// load-bearing: forcing a new session per statement makes the first CREATE
	// TABLE fail with `relation "matches" already exists`.
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const schema = "snapshots_partition_test"
	exec := func(statements ...string) {
		t.Helper()
		for _, statement := range statements {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Fatalf("exec %.60s: %v", strings.ReplaceAll(statement, "\n", " "), err)
			}
		}
	}
	exec("DROP SCHEMA IF EXISTS "+schema+" CASCADE", "CREATE SCHEMA "+schema, "SET search_path TO "+schema)
	// Deferred rather than t.Cleanup, which would run after `defer db.Close()`
	// and find a closed handle.
	defer func() {
		if _, err := db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE"); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()

	// The parent only. Partitions come from the maintainer below, which is the
	// point: this proves the two halves fit together.
	exec(`CREATE TABLE fixture_live_snapshots (
		id BIGSERIAL,
		fixture_id BIGINT NOT NULL,
		provider TEXT NOT NULL,
		captured_at TIMESTAMPTZ NOT NULL,
		provider_timestamp TIMESTAMPTZ,
		status TEXT NOT NULL,
		period TEXT,
		elapsed_minute SMALLINT,
		elapsed_extra SMALLINT,
		home_goals SMALLINT,
		away_goals SMALLINT,
		home_red_cards SMALLINT,
		away_red_cards SMALLINT,
		response_hash TEXT NOT NULL,
		PRIMARY KEY (id, captured_at)
	) PARTITION BY RANGE (captured_at)`)
	exec(`CREATE INDEX ON fixture_live_snapshots (fixture_id, captured_at DESC)`)

	// fixture_events, the second table the maintainer keeps ahead. Migration
	// 000070 pre-creates three months and then defers to this job: if the table
	// is not in PartitionedTables, inserts succeed until the clock crosses into
	// the first month nobody created, and then every live event is lost during
	// a match. Only the columns the partition key and a test insert need.
	exec(`CREATE TABLE fixture_events (
		id BIGSERIAL,
		fixture_id BIGINT NOT NULL,
		provider TEXT NOT NULL,
		observed_at TIMESTAMPTZ NOT NULL,
		elapsed_minute SMALLINT NOT NULL,
		elapsed_extra SMALLINT,
		event_type TEXT NOT NULL,
		event_detail TEXT NOT NULL,
		identity_key TEXT NOT NULL,
		is_retracted BOOLEAN NOT NULL DEFAULT false,
		PRIMARY KEY (id, observed_at)
	) PARTITION BY RANGE (observed_at)`)

	// inplay_snapshots (000079) and fixture_statistics_snapshots (000081) joined
	// PartitionedTables after this test was written, and because the test only
	// ran when someone exported SNAPSHOTS_TEST_DATABASE_URL by hand, nothing
	// noticed that it still created two parents out of four -- Ensure failed on
	// the first table it had never heard of. The assertion below is written
	// against len(PartitionedTables), so the parents are what has to keep up:
	// every table in that list needs one here. Partition key and primary key
	// only; nothing inserts into these two, the point is that Ensure can create
	// their children.
	exec(`CREATE TABLE inplay_snapshots (
		id BIGSERIAL,
		fixture_id BIGINT NOT NULL,
		captured_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (id, captured_at)
	) PARTITION BY RANGE (captured_at)`)
	exec(`CREATE TABLE fixture_statistics_snapshots (
		fixture_id BIGINT NOT NULL,
		team_id BIGINT NOT NULL,
		captured_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (fixture_id, team_id, captured_at)
	) PARTITION BY RANGE (captured_at)`)

	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	repository := NewRepository(db)
	snapshot := LiveSnapshot{
		FixtureID: 4242, Provider: "api-football", CapturedAt: now,
		Status: "2H", Period: "2H", ElapsedMinute: intp(63), HomeGoals: intp(1), AwayGoals: intp(0),
	}

	// A partitioned table with no matching child rejects every insert. This is
	// the failure the maintainer exists to prevent, so it is worth seeing once.
	if _, err := repository.Append(ctx, snapshot); err == nil {
		t.Fatal("an insert with no matching partition unexpectedly succeeded")
	} else if !strings.Contains(err.Error(), "no partition") {
		t.Fatalf("unexpected error before partitions exist: %v", err)
	}

	created, err := Maintainer{DB: db, Now: func() time.Time { return now }}.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// One month plus the lead, for each table in the default set.
	if want := (LeadMonths + 1) * len(PartitionedTables); created != want {
		t.Fatalf("created %d partitions, want %d", created, want)
	}
	// Idempotent: every statement is CREATE TABLE IF NOT EXISTS, so a second
	// run against a real server must not error.
	if _, err := (Maintainer{DB: db, Now: func() time.Time { return now }}).Ensure(ctx); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}

	written, err := repository.Append(ctx, snapshot)
	if err != nil || !written {
		t.Fatalf("append after partitions exist written=%v err=%v", written, err)
	}
	// Unchanged observation, later capture time: suppressed.
	repeat := snapshot
	repeat.CapturedAt = now.Add(30 * time.Second)
	written, err = repository.Append(ctx, repeat)
	if err != nil || written {
		t.Fatalf("repeat written=%v err=%v", written, err)
	}
	// A goal: written.
	scored := snapshot
	scored.CapturedAt = now.Add(time.Minute)
	scored.HomeGoals = intp(2)
	written, err = repository.Append(ctx, scored)
	if err != nil || !written {
		t.Fatalf("goal written=%v err=%v", written, err)
	}

	// The rows landed in the month's partition, not in the parent.
	partition := PartitionName("fixture_live_snapshots", now)
	var inPartition int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+partition).Scan(&inPartition); err != nil {
		t.Fatalf("count in %s: %v", partition, err)
	}
	if inPartition != 2 {
		t.Fatalf("%s holds %d rows, want 2", partition, inPartition)
	}

	// Red cards stay NULL: the bulk live endpoint does not carry them, and 0
	// would claim there were none.
	var reds sql.NullInt64
	if err := db.QueryRowContext(ctx,
		`SELECT home_red_cards FROM fixture_live_snapshots WHERE fixture_id = $1 ORDER BY captured_at LIMIT 1`, 4242).
		Scan(&reds); err != nil {
		t.Fatalf("read red cards: %v", err)
	}
	if reds.Valid {
		t.Errorf("home_red_cards = %v, want NULL", reds)
	}

	// The same proof for fixture_events: a partition the maintainer created,
	// and a row that lands in it. Without this table in PartitionedTables the
	// insert below fails with "no partition of relation ... found".
	if _, err := db.ExecContext(ctx, `
INSERT INTO fixture_events
  (fixture_id, provider, observed_at, elapsed_minute, event_type, event_detail, identity_key)
VALUES ($1, 'api-football', $2, 63, 'Goal', 'Normal Goal', 'test-identity')`, 4242, now); err != nil {
		t.Fatalf("insert event after partitions exist: %v", err)
	}
	eventPartition := PartitionName("fixture_events", now)
	var eventsInPartition int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+eventPartition).Scan(&eventsInPartition); err != nil {
		t.Fatalf("count in %s: %v", eventPartition, err)
	}
	if eventsInPartition != 1 {
		t.Fatalf("%s holds %d rows, want 1", eventPartition, eventsInPartition)
	}

	// The change-detection lookup prunes partitions rather than consulting all
	// of them. Asserted on the plan, because the behaviour is identical either
	// way and only the cost differs -- nothing would fail if this regressed.
	rows, err := db.QueryContext(ctx, `
EXPLAIN (FORMAT TEXT)
SELECT response_hash FROM fixture_live_snapshots
WHERE fixture_id = $1 AND captured_at >= $2
ORDER BY captured_at DESC LIMIT 1`, 4242, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	t.Logf("change-detection plan:\n%s", plan.String())
	if !strings.Contains(plan.String(), "Index") {
		t.Errorf("the change-detection lookup is not using an index:\n%s", plan.String())
	}
}
