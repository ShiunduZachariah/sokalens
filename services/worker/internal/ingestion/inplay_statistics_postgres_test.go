package ingestion

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/dbtest"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// The rest of this package's coverage stubs the database out, which is right
// for the job's logic and useless for the two things that can only be wrong in
// PostgreSQL: whether an append lands in a partition, and whether the
// ON CONFLICT clause matches the primary key the migration actually created.
//
// A partitioned table with no child covering captured_at rejects every insert.
// An ON CONFLICT target that does not match a unique constraint is an error at
// execution, not at build. Both would first show during a live match.
//
// Opt-in, following services/worker/internal/snapshots/postgres_test.go:
//
//	INGESTION_TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/scratch?sslmode=disable' \
//	  go test ./services/worker/internal/ingestion/
//
// Point it at a database with infra/db/migrations applied. It writes only to
// fixture_statistics_snapshots, under fixture ids far outside real data.
func TestInPlayStatisticsAppendAgainstPostgres(t *testing.T) {
	url := dbtest.URLOrSkip(t, "INGESTION_TEST_DATABASE_URL", "run the in-play statistics writer against PostgreSQL")
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	// Registered before the row cleanup so it runs after it: t.Cleanup is
	// LIFO, and a deferred Close would fire first and leave the test rows
	// behind on a database the next run reuses.
	t.Cleanup(func() { db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const fixtureID = int64(999000001)
	const teamID = int64(999000002)
	// Its own context, not the test's: the test's is cancelled by a defer,
	// which fires before t.Cleanup and would cancel the delete.
	cleanup := func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelCleanup()
		if _, err := db.ExecContext(cleanupCtx, "DELETE FROM fixture_statistics_snapshots WHERE fixture_id = $1", fixtureID); err != nil {
			t.Errorf("clean up test rows: %v", err)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	repo := NewInPlayStatisticsRepository(db)
	corners := 6
	minute := 63
	capturedAt := time.Now().UTC().Truncate(time.Second)

	stored, err := repo.AppendSnapshot(ctx, fixtureID, teamID, capturedAt, &minute, StatRecord{Corners: &corners})
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	if !stored {
		t.Fatal("first append reported nothing stored")
	}

	// Re-observing the same cutoff is normal -- the provider's statistics
	// refresh about once a minute and the poll runs at that cadence, so two
	// passes routinely see the same numbers. It must be a no-op, not a
	// duplicate and not an error.
	stored, err = repo.AppendSnapshot(ctx, fixtureID, teamID, capturedAt, &minute, StatRecord{Corners: &corners})
	if err != nil {
		t.Fatalf("second append of the same cutoff: %v", err)
	}
	if stored {
		t.Fatal("re-observing one cutoff stored a second row")
	}

	// A later capture is a new point in the series, not an overwrite: the
	// whole reason this table exists is that fixture_statistics cannot say how
	// many corners had been taken when a market was published.
	later := capturedAt.Add(time.Minute)
	laterCorners := 8
	stored, err = repo.AppendSnapshot(ctx, fixtureID, teamID, later, &minute, StatRecord{Corners: &laterCorners})
	if err != nil {
		t.Fatalf("append a later capture: %v", err)
	}
	if !stored {
		t.Fatal("a later capture was treated as a duplicate")
	}

	var rows, distinctCorners int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*), count(DISTINCT corners) FROM fixture_statistics_snapshots WHERE fixture_id = $1",
		fixtureID,
	).Scan(&rows, &distinctCorners); err != nil {
		t.Fatalf("count stored rows: %v", err)
	}
	if rows != 2 || distinctCorners != 2 {
		t.Fatalf("stored %d rows with %d distinct corner counts, want 2 and 2", rows, distinctCorners)
	}

	// A zero capture time would put every observation of every match in one
	// primary key, so the series would hold one row per team for all time.
	if _, err := repo.AppendSnapshot(ctx, fixtureID, teamID, time.Time{}, &minute, StatRecord{}); err == nil {
		t.Fatal("a zero captured_at was accepted")
	}
}

// The live lookup is pure SQL over three tables and a regex; SQLite would not
// run it, and a typo in the join would return an empty live set -- a poll that
// looks healthy and covers nothing.
func TestLiveProviderFixtureIDsQueryAgainstPostgres(t *testing.T) {
	url := dbtest.URLOrSkip(t, "INGESTION_TEST_DATABASE_URL", "run the live fixture lookup against PostgreSQL")
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ids, err := NewLiveFixturesRepository(db).LiveProviderFixtureIDs(ctx, 50)
	if err != nil {
		t.Fatalf("LiveProviderFixtureIDs: %v", err)
	}
	// On an empty scratch database the answer is legitimately none; the point
	// of the case is that the statement executes against the real schema.
	for _, id := range ids {
		if id <= 0 {
			t.Fatalf("returned a non-positive provider fixture id: %d", id)
		}
	}
}
