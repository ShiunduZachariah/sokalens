package opshealth

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// The freshness queries run against PostgreSQL in production and SQLite in the
// rest of this package's tests. The gap matters more here than it looks,
// because of how this code fails: an unparseable timestamp returns an empty
// Age, not an error. So a driver returning something parseTimestamp does not
// recognise would make /healthz report "never observed" for a live poll running
// perfectly well -- sending an operator to look for an outage that is not
// happening, with nothing anywhere saying why.
//
// Opt-in, following services/api/internal/acca/generate_db_test.go:
//
//	OPSHEALTH_TEST_DATABASE_URL='postgres://sports:sports@127.0.0.1:5432/scratch?sslmode=disable' \
//	  go test ./services/api/internal/opshealth/
//
// It creates and drops its own schema. Point it at a scratch database anyway.
func TestSnapshotAgainstPostgres(t *testing.T) {
	url := os.Getenv("OPSHEALTH_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set OPSHEALTH_TEST_DATABASE_URL to run the freshness queries against PostgreSQL")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const schema = "opshealth_freshness_test"
	exec := func(statements ...string) {
		t.Helper()
		for _, statement := range statements {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Fatalf("exec %.60s: %v", strings.ReplaceAll(statement, "\n", " "), err)
			}
		}
	}
	exec("DROP SCHEMA IF EXISTS "+schema+" CASCADE", "CREATE SCHEMA "+schema, "SET search_path TO "+schema)
	// Deferred rather than t.Cleanup, which would run after `defer db.Close()`.
	defer func() {
		if _, err := db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE"); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()

	exec(`CREATE TABLE jobs (
		id BIGSERIAL PRIMARY KEY,
		job_key TEXT NOT NULL,
		status TEXT NOT NULL,
		completed_at TIMESTAMPTZ)`)

	now := time.Now().UTC()
	addJob := func(jobKey, status string, completedAt any) {
		t.Helper()
		if _, err := db.ExecContext(ctx,
			`INSERT INTO jobs (job_key, status, completed_at) VALUES ($1, $2, $3)`, jobKey, status, completedAt); err != nil {
			t.Fatalf("insert job: %v", err)
		}
	}
	addJob(LiveFixturesJobKey, "completed", now.Add(-45*time.Second))
	addJob(LiveFixturesJobKey, "completed", now.Add(-10*time.Minute))
	addJob(LiveFixturesJobKey, "failed", now.Add(-5*time.Second))
	addJob(LiveFixturesJobKey, "running", nil)
	addJob("predictions.generate-upcoming", "completed", now.Add(-20*time.Second))
	// Outside the lookback window.
	addJob(LiveFixturesJobKey, "completed", now.Add(-48*time.Hour))

	snapshot := NewRepository(db, stubCheck{name: DatabaseDependency}).
		WithClock(func() time.Time { return now }).Snapshot(ctx)

	if snapshot.Dependencies[DatabaseDependency] != true {
		t.Fatalf("dependencies = %v", snapshot.Dependencies)
	}
	// The load-bearing assertion. A nil age here would be indistinguishable
	// from "no poll has ever run", which is what a parse failure produces.
	if snapshot.LastLivePoll.Seconds == nil {
		t.Fatal("last live poll came back empty against PostgreSQL; a timestamp did not parse")
	}
	if got := *snapshot.LastLivePoll.Seconds; got != 45 {
		t.Errorf("last live poll = %ds, want 45 (only completed jobs, newest first)", got)
	}
	if snapshot.LastWorkerJob.Seconds == nil || *snapshot.LastWorkerJob.Seconds != 20 {
		t.Errorf("last worker job = %+v, want 20s", snapshot.LastWorkerJob)
	}
	if snapshot.LastLivePoll.At == nil || snapshot.LastLivePoll.At.Location() != time.UTC {
		t.Errorf("timestamp did not come back as UTC: %+v", snapshot.LastLivePoll.At)
	}

	// A database that answers but holds nothing must report absence, not zero:
	// on a fresh deployment there genuinely has been no live poll, and an age
	// of zero would read as "just happened".
	exec("DELETE FROM jobs")
	empty := NewRepository(db, stubCheck{name: DatabaseDependency}).
		WithClock(func() time.Time { return now }).Snapshot(ctx)
	if empty.LastLivePoll.At != nil || empty.LastLivePoll.Seconds != nil {
		t.Errorf("an empty jobs table reported an age: %+v", empty.LastLivePoll)
	}
	if empty.Dependencies[DatabaseDependency] != true {
		t.Error("an empty table was reported as an unreachable database")
	}
}
