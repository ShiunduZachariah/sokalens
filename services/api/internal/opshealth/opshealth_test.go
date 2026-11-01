package opshealth

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// The worker generates this key from ProviderJobKey("api-football", "live").
// This module cannot import that package -- it is internal to another module --
// so both sides pin the same literal instead. A worker-side test asserts its
// generated key equals this string; this asserts the constant does. Changing
// the key on either side fails the other side's test.
func TestLiveFixturesJobKeyMatchesTheWorkersGeneratedKey(t *testing.T) {
	if LiveFixturesJobKey != "provider.api-football.live" {
		t.Fatalf("LiveFixturesJobKey = %q; the worker generates provider.api-football.live", LiveFixturesJobKey)
	}
}

func healthDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		job_key TEXT NOT NULL,
		status TEXT NOT NULL,
		completed_at TIMESTAMP)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func addJob(t *testing.T, db *sql.DB, jobKey, status string, completedAt any) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO jobs (job_key, status, completed_at) VALUES (?, ?, ?)`, jobKey, status, completedAt); err != nil {
		t.Fatalf("insert job: %v", err)
	}
}

func TestSnapshotReportsTheAgeOfTheLastLivePollAndTheLastJob(t *testing.T) {
	db := healthDB(t)
	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	addJob(t, db, LiveFixturesJobKey, "completed", now.Add(-45*time.Second))
	addJob(t, db, LiveFixturesJobKey, "completed", now.Add(-10*time.Minute))
	addJob(t, db, "predictions.generate-upcoming", "completed", now.Add(-20*time.Second))

	snapshot := NewRepository(db).WithClock(func() time.Time { return now }).Snapshot(context.Background())

	if snapshot.LastLivePoll.Seconds == nil || *snapshot.LastLivePoll.Seconds != 45 {
		t.Fatalf("last live poll = %+v, want 45s", snapshot.LastLivePoll)
	}
	// Any completed job stands in for a worker heartbeat, so the most recent
	// one wins regardless of which job it was.
	if snapshot.LastWorkerJob.Seconds == nil || *snapshot.LastWorkerJob.Seconds != 20 {
		t.Fatalf("last worker job = %+v, want 20s", snapshot.LastWorkerJob)
	}
}

func TestNeverObservedIsDistinctFromLongAgo(t *testing.T) {
	// On a fresh deployment there has been no live poll yet. Reporting that as
	// an enormous age would look like an outage on first boot.
	db := healthDB(t)
	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	addJob(t, db, "predictions.generate-upcoming", "completed", now.Add(-30*time.Second))

	snapshot := NewRepository(db).WithClock(func() time.Time { return now }).Snapshot(context.Background())

	if snapshot.LastLivePoll.At != nil || snapshot.LastLivePoll.Seconds != nil {
		t.Fatalf("an unobserved live poll reported an age: %+v", snapshot.LastLivePoll)
	}
	if snapshot.LastWorkerJob.Seconds == nil {
		t.Fatal("a completed job should still be reported")
	}
}

func TestOnlyCompletedJobsCount(t *testing.T) {
	// A failed or still-running job is not evidence of progress. Counting it
	// would report a healthy worker during exactly the outage worth catching.
	db := healthDB(t)
	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	addJob(t, db, LiveFixturesJobKey, "failed", now.Add(-5*time.Second))
	addJob(t, db, LiveFixturesJobKey, "running", nil)
	addJob(t, db, LiveFixturesJobKey, "completed", now.Add(-90*time.Second))

	snapshot := NewRepository(db).WithClock(func() time.Time { return now }).Snapshot(context.Background())

	if snapshot.LastLivePoll.Seconds == nil || *snapshot.LastLivePoll.Seconds != 90 {
		t.Fatalf("last live poll = %+v, want the completed one at 90s", snapshot.LastLivePoll)
	}
}

func TestQueriesAreBoundedToTheLookbackWindow(t *testing.T) {
	// Without the bound these are unbounded max() scans of the whole jobs
	// table on an unauthenticated endpoint. Anything older than the window is
	// so far past every threshold that its exact age carries no information.
	db := healthDB(t)
	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	addJob(t, db, LiveFixturesJobKey, "completed", now.Add(-48*time.Hour))

	snapshot := NewRepository(db).WithClock(func() time.Time { return now }).Snapshot(context.Background())

	if snapshot.LastLivePoll.At != nil {
		t.Fatalf("a job outside the lookback window was reported: %+v", snapshot.LastLivePoll)
	}
}

func TestClockSkewDoesNotProduceANegativeAge(t *testing.T) {
	db := healthDB(t)
	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	addJob(t, db, LiveFixturesJobKey, "completed", now.Add(3*time.Second))

	snapshot := NewRepository(db).WithClock(func() time.Time { return now }).Snapshot(context.Background())

	if snapshot.LastLivePoll.Seconds == nil || *snapshot.LastLivePoll.Seconds != 0 {
		t.Fatalf("last live poll = %+v, want a clamped 0", snapshot.LastLivePoll)
	}
}

func TestSnapshotSurvivesAMissingDatabase(t *testing.T) {
	// The endpoint's job is to answer. A nil or broken database is reported as
	// an absence of ages, never as a panic or a hang.
	snapshot := (&Repository{}).Snapshot(context.Background())
	if snapshot.LastLivePoll.At != nil || snapshot.LastWorkerJob.At != nil {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

type countingSource struct {
	calls    int
	snapshot Snapshot
}

func (c *countingSource) Snapshot(context.Context) Snapshot {
	c.calls++
	return c.snapshot
}

func TestCachedBoundsDatabaseLoadFromAnUnauthenticatedEndpoint(t *testing.T) {
	seconds := int64(12)
	source := &countingSource{snapshot: Snapshot{LastLivePoll: Age{Seconds: &seconds}}}
	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	cached := &Cached{Source: source, TTL: 10 * time.Second, Now: func() time.Time { return now }}

	for range 50 {
		if got := cached.Snapshot(context.Background()); got.LastLivePoll.Seconds == nil {
			t.Fatal("cached snapshot lost its content")
		}
	}
	if source.calls != 1 {
		t.Fatalf("50 requests cost %d queries, want 1", source.calls)
	}

	now = now.Add(11 * time.Second)
	cached.Snapshot(context.Background())
	if source.calls != 2 {
		t.Fatalf("the cache did not expire: %d queries", source.calls)
	}
}

func TestCachedWithoutASourceIsInert(t *testing.T) {
	if got := (&Cached{}).Snapshot(context.Background()); got.LastLivePoll.At != nil {
		t.Fatalf("snapshot = %+v", got)
	}
}

type stubCheck struct {
	name  string
	err   error
	calls *int
}

func (s stubCheck) Name() string { return s.name }

func (s stubCheck) Check() error {
	if s.calls != nil {
		*s.calls++
	}
	return s.err
}

func TestSnapshotRecordsDependencyReachability(t *testing.T) {
	db := healthDB(t)
	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	addJob(t, db, LiveFixturesJobKey, "completed", now.Add(-30*time.Second))

	snapshot := NewRepository(db,
		stubCheck{name: DatabaseDependency},
		stubCheck{name: "redis", err: errors.New("dial tcp 10.0.0.9:6379: connection refused")},
	).WithClock(func() time.Time { return now }).Snapshot(context.Background())

	if snapshot.Dependencies[DatabaseDependency] != true || snapshot.Dependencies["redis"] != false {
		t.Fatalf("dependencies = %v", snapshot.Dependencies)
	}
	// Redis being down does not stop the database answering.
	if snapshot.LastLivePoll.Seconds == nil {
		t.Fatalf("freshness was skipped for an unrelated dependency: %+v", snapshot)
	}
}

func TestFreshnessIsSkippedWhenTheDatabaseIsUnreachable(t *testing.T) {
	// Two more two-second timeouts would only arrive at the same conclusion
	// four seconds later, on the endpoint whose whole job is to answer.
	db := healthDB(t)
	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	addJob(t, db, LiveFixturesJobKey, "completed", now.Add(-30*time.Second))

	snapshot := NewRepository(db, stubCheck{name: DatabaseDependency, err: errors.New("down")}).
		WithClock(func() time.Time { return now }).Snapshot(context.Background())

	if snapshot.Dependencies[DatabaseDependency] != false {
		t.Fatalf("dependencies = %v", snapshot.Dependencies)
	}
	if snapshot.LastLivePoll.At != nil {
		t.Fatalf("freshness was read with the database down: %+v", snapshot.LastLivePoll)
	}
}

// The probes are the expensive part of this endpoint -- a database ping and a
// socket dial, each with its own timeout. They live behind the same cache as
// the queries, because an uncached probe on an unauthenticated endpoint is the
// free database lever the cache exists to prevent.
func TestCachedCoversTheDependencyProbesToo(t *testing.T) {
	probes := 0
	db := healthDB(t)
	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	cached := &Cached{
		Source: NewRepository(db, stubCheck{name: DatabaseDependency, calls: &probes}).
			WithClock(func() time.Time { return now }),
		TTL: 10 * time.Second,
		Now: func() time.Time { return now },
	}

	for range 50 {
		cached.Snapshot(context.Background())
	}
	if probes != 1 {
		t.Fatalf("50 requests cost %d dependency probes, want 1", probes)
	}
}
