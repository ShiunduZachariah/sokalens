package snapshots

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func intp(value int) *int { return &value }

// --- change detection ------------------------------------------------------

func TestHashIgnoresCaptureTime(t *testing.T) {
	// Hashing the capture time would make every snapshot unique and defeat the
	// whole mechanism -- every poll would append a row.
	base := LiveSnapshot{FixtureID: 1, Provider: "api-football", Status: "2H", ElapsedMinute: intp(63), HomeGoals: intp(1), AwayGoals: intp(1)}
	first := base
	first.CapturedAt = time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	second := base
	second.CapturedAt = first.CapturedAt.Add(90 * time.Second)

	if first.Hash() != second.Hash() {
		t.Fatal("the same observation at two times produced different hashes")
	}
}

func TestHashChangesWithEveryObservedField(t *testing.T) {
	base := LiveSnapshot{FixtureID: 1, Provider: "api-football", Status: "2H", Period: "2H", ElapsedMinute: intp(63), ElapsedExtra: intp(0), HomeGoals: intp(1), AwayGoals: intp(1), HomeRedCards: intp(0), AwayRedCards: intp(0)}
	for name, mutate := range map[string]func(*LiveSnapshot){
		"status":     func(s *LiveSnapshot) { s.Status = "FT" },
		"period":     func(s *LiveSnapshot) { s.Period = "ET" },
		"elapsed":    func(s *LiveSnapshot) { s.ElapsedMinute = intp(64) },
		"extra":      func(s *LiveSnapshot) { s.ElapsedExtra = intp(2) },
		"home goals": func(s *LiveSnapshot) { s.HomeGoals = intp(2) },
		"away goals": func(s *LiveSnapshot) { s.AwayGoals = intp(2) },
		"home reds":  func(s *LiveSnapshot) { s.HomeRedCards = intp(1) },
		"away reds":  func(s *LiveSnapshot) { s.AwayRedCards = intp(1) },
		"fixture":    func(s *LiveSnapshot) { s.FixtureID = 2 },
		"provider":   func(s *LiveSnapshot) { s.Provider = "football-data" },
	} {
		changed := base
		mutate(&changed)
		if changed.Hash() == base.Hash() {
			t.Errorf("a change to %s did not change the hash", name)
		}
	}
}

// A feed that stops reporting a value has told us something. Encoding nil as
// zero would hide it: "we no longer know the score" would look identical to
// "the score is nil-nil".
func TestHashDistinguishesUnknownFromZero(t *testing.T) {
	unknown := LiveSnapshot{FixtureID: 1, Provider: "p", Status: "1H"}
	zero := LiveSnapshot{FixtureID: 1, Provider: "p", Status: "1H", HomeGoals: intp(0), AwayGoals: intp(0)}
	if unknown.Hash() == zero.Hash() {
		t.Fatal("nil and zero hashed identically")
	}
}

// --- append ----------------------------------------------------------------

func snapshotDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// SQLite has no declarative partitioning, and it does not need it: the
	// writes are plain inserts into the parent, which is exactly what Postgres
	// routes. The partition mechanics are covered by the migration verifier
	// against real PostgreSQL.
	if _, err := db.Exec(`CREATE TABLE fixture_live_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		fixture_id INTEGER NOT NULL,
		provider TEXT NOT NULL,
		captured_at TIMESTAMP NOT NULL,
		provider_timestamp TIMESTAMP,
		status TEXT NOT NULL,
		period TEXT,
		elapsed_minute INTEGER,
		elapsed_extra INTEGER,
		home_goals INTEGER,
		away_goals INTEGER,
		home_red_cards INTEGER,
		away_red_cards INTEGER,
		response_hash TEXT NOT NULL)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func rowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM fixture_live_snapshots`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	return count
}

func TestAppendSkipsAnUnchangedObservation(t *testing.T) {
	db := snapshotDB(t)
	repository := NewRepository(db)
	at := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	snapshot := LiveSnapshot{FixtureID: 42, Provider: "api-football", CapturedAt: at, Status: "2H", ElapsedMinute: intp(63), HomeGoals: intp(1), AwayGoals: intp(1)}

	written, err := repository.Append(context.Background(), snapshot)
	if err != nil || !written {
		t.Fatalf("first append written=%v err=%v", written, err)
	}
	// Three more polls, nothing changed on the pitch.
	for offset := 1; offset <= 3; offset++ {
		repeat := snapshot
		repeat.CapturedAt = at.Add(time.Duration(offset) * 30 * time.Second)
		written, err := repository.Append(context.Background(), repeat)
		if err != nil || written {
			t.Fatalf("repeat %d written=%v err=%v", offset, written, err)
		}
	}
	if got := rowCount(t, db); got != 1 {
		t.Fatalf("rows = %d, want 1", got)
	}
}

func TestAppendWritesWhenTheMatchMoves(t *testing.T) {
	db := snapshotDB(t)
	repository := NewRepository(db)
	at := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	snapshot := LiveSnapshot{FixtureID: 42, Provider: "api-football", CapturedAt: at, Status: "2H", ElapsedMinute: intp(63), HomeGoals: intp(1), AwayGoals: intp(1)}
	if _, err := repository.Append(context.Background(), snapshot); err != nil {
		t.Fatalf("first append: %v", err)
	}

	scored := snapshot
	scored.CapturedAt = at.Add(time.Minute)
	scored.ElapsedMinute = intp(64)
	scored.HomeGoals = intp(2)
	written, err := repository.Append(context.Background(), scored)
	if err != nil || !written {
		t.Fatalf("goal written=%v err=%v", written, err)
	}
	if got := rowCount(t, db); got != 2 {
		t.Fatalf("rows = %d, want 2", got)
	}
}

// The skip compares against the newest row for that fixture, not the newest
// row overall. Two matches in play at once must not suppress each other.
func TestAppendComparesPerFixture(t *testing.T) {
	db := snapshotDB(t)
	repository := NewRepository(db)
	at := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	for _, fixtureID := range []int64{1, 2} {
		snapshot := LiveSnapshot{FixtureID: fixtureID, Provider: "api-football", CapturedAt: at, Status: "1H", ElapsedMinute: intp(10)}
		written, err := repository.Append(context.Background(), snapshot)
		if err != nil || !written {
			t.Fatalf("fixture %d written=%v err=%v", fixtureID, written, err)
		}
	}
	if got := rowCount(t, db); got != 2 {
		t.Fatalf("rows = %d, want 2", got)
	}
}

func TestAppendPersistsTheObservation(t *testing.T) {
	db := snapshotDB(t)
	at := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	providerAt := at.Add(-2 * time.Second)
	snapshot := LiveSnapshot{
		FixtureID: 7, Provider: "api-football", CapturedAt: at, ProviderTimestamp: &providerAt,
		Status: "2H", Period: "2H", ElapsedMinute: intp(63), HomeGoals: intp(1), AwayGoals: intp(0),
	}
	if _, err := NewRepository(db).Append(context.Background(), snapshot); err != nil {
		t.Fatalf("append: %v", err)
	}

	var (
		status  string
		elapsed int
		home    int
		reds    sql.NullInt64
		hash    string
	)
	if err := db.QueryRow(`SELECT status, elapsed_minute, home_goals, home_red_cards, response_hash FROM fixture_live_snapshots WHERE fixture_id = 7`).
		Scan(&status, &elapsed, &home, &reds, &hash); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "2H" || elapsed != 63 || home != 1 {
		t.Fatalf("row = %s %d %d", status, elapsed, home)
	}
	// Red cards are not carried by the bulk live endpoint. NULL says "not
	// observed"; 0 would claim there were none.
	if reds.Valid {
		t.Fatalf("home_red_cards = %v, want NULL", reds)
	}
	if hash != snapshot.Hash() {
		t.Fatalf("stored hash does not match the snapshot")
	}
}

func TestAppendRejectsAnUnusableSnapshot(t *testing.T) {
	db := snapshotDB(t)
	repository := NewRepository(db)
	at := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	for name, snapshot := range map[string]LiveSnapshot{
		"no fixture": {Provider: "p", CapturedAt: at, Status: "2H"},
		"no status":  {FixtureID: 1, Provider: "p", CapturedAt: at},
		"no time":    {FixtureID: 1, Provider: "p", Status: "2H"},
	} {
		if _, err := repository.Append(context.Background(), snapshot); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if got := rowCount(t, db); got != 0 {
		t.Fatalf("rows = %d, want 0", got)
	}
	if _, err := (Repository{}).Append(context.Background(), LiveSnapshot{FixtureID: 1, Provider: "p", CapturedAt: at, Status: "2H"}); err == nil {
		t.Error("a nil DB was accepted")
	}
}

// --- partitions ------------------------------------------------------------

type recordingExecer struct {
	statements []string
	err        error
}

func (r *recordingExecer) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	r.statements = append(r.statements, query)
	return nil, r.err
}

func TestEnsureCreatesThisMonthAndTheLead(t *testing.T) {
	execer := &recordingExecer{}
	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	// One table named explicitly: this case is about the month window, and
	// pinning it here keeps the count from moving every time a table joins
	// PartitionedTables. TestDefaultTablesCoverEveryPartitionedTable is what
	// guards the set itself.
	created, err := Maintainer{DB: execer, Tables: []string{"fixture_live_snapshots"}, Now: func() time.Time { return now }}.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	// The current month plus three ahead.
	if created != 4 || len(execer.statements) != 4 {
		t.Fatalf("created = %d, statements = %d", created, len(execer.statements))
	}
	for _, want := range []string{"fixture_live_snapshots_2026_09", "fixture_live_snapshots_2026_10", "fixture_live_snapshots_2026_11", "fixture_live_snapshots_2026_12"} {
		found := false
		for _, statement := range execer.statements {
			if strings.Contains(statement, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no statement created %s", want)
		}
	}
	// Idempotent: a re-run, an overlapping run, or a retry after failure must
	// be a no-op rather than an error.
	for _, statement := range execer.statements {
		if !strings.Contains(statement, "CREATE TABLE IF NOT EXISTS") {
			t.Errorf("statement is not idempotent: %s", statement)
		}
	}
}

// Every table the migrations partition must be in the default set, or its
// partitions stop at whatever window its migration pre-created and inserts
// begin failing the moment the clock crosses that edge -- during a live match,
// which is exactly when the data is irreplaceable. fixture_events is here
// because 000070 creates three months and then defers to this job.
func TestDefaultTablesCoverEveryPartitionedTable(t *testing.T) {
	execer := &recordingExecer{}
	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	if _, err := (Maintainer{DB: execer, Now: func() time.Time { return now }}).Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	joined := strings.Join(execer.statements, "\n")
	for _, table := range []string{"fixture_live_snapshots", "fixture_events"} {
		if !strings.Contains(joined, PartitionName(table, now)) {
			t.Errorf("default tables do not maintain %s: no statement created %s", table, PartitionName(table, now))
		}
	}
}

func TestEnsureRollsOverTheYear(t *testing.T) {
	execer := &recordingExecer{}
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	if _, err := (Maintainer{DB: execer, Now: func() time.Time { return now }}).Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	joined := strings.Join(execer.statements, "\n")
	for _, want := range []string{"fixture_live_snapshots_2026_12", "fixture_live_snapshots_2027_01", "fixture_live_snapshots_2027_02"} {
		if !strings.Contains(joined, want) {
			t.Errorf("year rollover missed %s", want)
		}
	}
}

// A missing partition is not a degraded feature: every insert into that month
// fails. The error has to reach the alerting path rather than be logged and
// skipped.
func TestEnsureReturnsTheError(t *testing.T) {
	execer := &recordingExecer{err: errors.New("permission denied")}
	if _, err := (Maintainer{DB: execer}).Ensure(context.Background()); err == nil {
		t.Fatal("a failed partition creation was swallowed")
	}
	if _, err := (Maintainer{}).Ensure(context.Background()); err == nil {
		t.Fatal("a nil DB was accepted")
	}
}

func TestEnsureRefusesAnUnsafeParentName(t *testing.T) {
	// The names come from a package-level list, not from input. The guard is
	// there so that stays true of a future caller too.
	execer := &recordingExecer{}
	if _, err := (Maintainer{DB: execer, Tables: []string{"snapshots; DROP TABLE matches"}}).Ensure(context.Background()); err == nil {
		t.Fatal("an unsafe parent name was accepted")
	}
	if len(execer.statements) != 0 {
		t.Fatalf("statements ran for an unsafe name: %v", execer.statements)
	}
}

func TestPartitionNameIsTheMonth(t *testing.T) {
	got := PartitionName("fixture_live_snapshots", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if got != "fixture_live_snapshots_2027_01" {
		t.Fatalf("PartitionName = %q", got)
	}
}

// The change-detection lookup is bounded to a day, and that bound is partition
// pruning rather than a correctness filter: without a predicate on the
// partition key, Postgres consults every monthly partition for the fixture, and
// that set grows for as long as the table is retained.
//
// The bound is observable, so it is worth pinning. A day is comfortably longer
// than any match plus stoppages, so the previous snapshot of a fixture in play
// right now is always inside it; an identical row from before the window is
// treated as absent and written, which is the correct outcome for a fixture
// nobody has observed in a day.
func TestChangeDetectionOnlyLooksBackOneDay(t *testing.T) {
	db := snapshotDB(t)
	repository := NewRepository(db)
	at := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	snapshot := LiveSnapshot{FixtureID: 42, Provider: "api-football", CapturedAt: at, Status: "2H", ElapsedMinute: intp(63), HomeGoals: intp(1), AwayGoals: intp(1)}

	if _, err := repository.Append(context.Background(), snapshot); err != nil {
		t.Fatalf("first append: %v", err)
	}

	// Inside the window: identical observation, suppressed.
	inside := snapshot
	inside.CapturedAt = at.Add(23 * time.Hour)
	written, err := repository.Append(context.Background(), inside)
	if err != nil || written {
		t.Fatalf("inside the window written=%v err=%v", written, err)
	}

	// Outside it: the earlier row is no longer consulted, so this is written.
	outside := snapshot
	outside.CapturedAt = at.Add(25 * time.Hour)
	written, err = repository.Append(context.Background(), outside)
	if err != nil || !written {
		t.Fatalf("outside the window written=%v err=%v", written, err)
	}
	if got := rowCount(t, db); got != 2 {
		t.Fatalf("rows = %d, want 2", got)
	}
}
