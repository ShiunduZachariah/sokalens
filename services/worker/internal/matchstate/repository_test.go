package matchstate

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func repositoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE matches (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		league_id INTEGER NOT NULL DEFAULT 1,
		home_team_id INTEGER NOT NULL DEFAULT 1,
		away_team_id INTEGER NOT NULL DEFAULT 2,
		kickoff_at TIMESTAMP NOT NULL,
		status TEXT NOT NULL,
		status_elapsed INTEGER,
		home_score INTEGER,
		away_score INTEGER,
		live_state_at TIMESTAMP)`); err != nil {
		t.Fatalf("exec schema: %v", err)
	}
	// The engine projection shares the match primary key, and the catalogue
	// reads its status rather than the match's.
	if _, err := db.Exec(`CREATE TABLE fixtures (
		id INTEGER PRIMARY KEY,
		status TEXT NOT NULL,
		updated_at TIMESTAMP)`); err != nil {
		t.Fatalf("exec fixtures schema: %v", err)
	}
	return db
}

func projectFixture(t *testing.T, db *sql.DB, id int64, status string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO fixtures (id, status) VALUES (?, ?)`, id, status); err != nil {
		t.Fatalf("insert fixture projection: %v", err)
	}
}

func fixtureStatus(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM fixtures WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatalf("read fixture projection: %v", err)
	}
	return status
}

func insertMatch(t *testing.T, db *sql.DB, status string, kickoffAt time.Time, liveStateAt any, elapsed int, home, away int) int64 {
	t.Helper()
	result, err := db.Exec(
		`INSERT INTO matches (kickoff_at, status, status_elapsed, home_score, away_score, live_state_at) VALUES (?, ?, ?, ?, ?, ?)`,
		kickoffAt.UTC(), status, elapsed, home, away, liveStateAt)
	if err != nil {
		t.Fatalf("insert match: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	return id
}

func TestStuckMatchesSelectsOverdueAndIgnoresHealthyAndTerminal(t *testing.T) {
	db := repositoryDB(t)
	now := time.Date(2026, 9, 6, 16, 0, 0, 0, time.UTC)

	overdue := insertMatch(t, db, "second_half", now.Add(-80*time.Minute), now.Add(-20*time.Minute), 55, 1, 1)
	neverStamped := insertMatch(t, db, "second_half", now.Add(-4*time.Hour), nil, 55, 0, 0)
	healthy := insertMatch(t, db, "second_half", now.Add(-70*time.Minute), now.Add(-20*time.Second), 62, 2, 0)
	finished := insertMatch(t, db, "finished", now.Add(-5*time.Hour), now.Add(-3*time.Hour), 90, 2, 1)
	upcoming := insertMatch(t, db, "upcoming", now.Add(2*time.Hour), nil, 0, 0, 0)

	rows, err := NewRepository(db).StuckMatches(context.Background(), now, 50)
	if err != nil {
		t.Fatalf("StuckMatches returned error: %v", err)
	}
	found := map[int64]StuckMatch{}
	for _, row := range rows {
		found[row.MatchID] = row
	}
	if _, ok := found[overdue]; !ok {
		t.Error("an overdue live match was not selected")
	}
	if _, ok := found[neverStamped]; !ok {
		t.Error("a live match that was never stamped was not selected")
	}
	if _, ok := found[healthy]; ok {
		t.Error("a freshly polled match was selected")
	}
	if _, ok := found[finished]; ok {
		t.Error("a finished match was selected; terminal states cannot go stale")
	}
	if _, ok := found[upcoming]; ok {
		t.Error("an upcoming match was selected")
	}
	if got := found[overdue].State; got != StateSecondHalf {
		t.Errorf("state = %q, want second_half", got)
	}
	if found[neverStamped].LiveStateAt != (time.Time{}) {
		t.Error("a NULL stamp must read as zero, not as a real timestamp")
	}
}

func TestStuckMatchesReturnsTheMostOverdueFirst(t *testing.T) {
	db := repositoryDB(t)
	now := time.Date(2026, 9, 6, 16, 0, 0, 0, time.UTC)
	recent := insertMatch(t, db, "second_half", now.Add(-80*time.Minute), now.Add(-6*time.Minute), 55, 0, 0)
	older := insertMatch(t, db, "second_half", now.Add(-90*time.Minute), now.Add(-40*time.Minute), 55, 0, 0)

	rows, err := NewRepository(db).StuckMatches(context.Background(), now, 50)
	if err != nil {
		t.Fatalf("StuckMatches returned error: %v", err)
	}
	if len(rows) != 2 || rows[0].MatchID != older || rows[1].MatchID != recent {
		t.Fatalf("rows = %+v, want %d before %d", rows, older, recent)
	}
}

func TestMarkUnknownLeavesTheScoreAndTheFreshnessRecordAlone(t *testing.T) {
	db := repositoryDB(t)
	now := time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)
	stamped := now.Add(-3 * time.Hour)
	id := insertMatch(t, db, "second_half", now.Add(-4*time.Hour), stamped, 55, 2, 1)

	changed, err := NewRepository(db).MarkUnknown(context.Background(), id, StateSecondHalf, now)
	if err != nil || !changed {
		t.Fatalf("MarkUnknown changed=%v err=%v", changed, err)
	}

	var (
		status   string
		elapsed  int
		home     int
		away     int
		liveTime sql.NullTime
	)
	if err := db.QueryRow(`SELECT status, status_elapsed, home_score, away_score, live_state_at FROM matches WHERE id = ?`, id).
		Scan(&status, &elapsed, &home, &away, &liveTime); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != string(StateUnknown) {
		t.Errorf("status = %q, want unknown", status)
	}
	// The last observed score is real information and stays. The reaper says
	// "this row is no longer describing a match in play", not "here is a new
	// result" -- inventing one would settle predictions against a scoreline no
	// provider confirmed.
	if home != 2 || away != 1 || elapsed != 55 {
		t.Errorf("score/elapsed were altered: %d-%d at %d", home, away, elapsed)
	}
	// An administrative write must not masquerade as a provider observation.
	if !liveTime.Valid || !liveTime.Time.UTC().Equal(stamped) {
		t.Errorf("live_state_at = %v, want it untouched at %v", liveTime, stamped)
	}
}

func TestMarkUnknownLosesTheRaceToARealObservation(t *testing.T) {
	db := repositoryDB(t)
	now := time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)
	id := insertMatch(t, db, "second_half", now.Add(-4*time.Hour), now.Add(-3*time.Hour), 55, 2, 1)

	// A provider observation lands between the reaper's read and its write.
	if _, err := db.Exec(`UPDATE matches SET status = 'finished' WHERE id = ?`, id); err != nil {
		t.Fatalf("simulate concurrent update: %v", err)
	}

	changed, err := NewRepository(db).MarkUnknown(context.Background(), id, StateSecondHalf, now)
	if err != nil {
		t.Fatalf("MarkUnknown returned error: %v", err)
	}
	if changed {
		t.Fatal("the reaper overwrote a fresher truth")
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM matches WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "finished" {
		t.Fatalf("status = %q, want the real result to survive", status)
	}
}

func TestMarkUnknownRefusesToOverwriteATerminalState(t *testing.T) {
	db := repositoryDB(t)
	now := time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)
	id := insertMatch(t, db, "finished", now.Add(-4*time.Hour), now.Add(-3*time.Hour), 90, 2, 1)

	if _, err := NewRepository(db).MarkUnknown(context.Background(), id, StateFinished, now); err == nil {
		t.Fatal("expected the transition to be refused before any SQL ran")
	}
}

func TestRepositoryRequiresADatabase(t *testing.T) {
	if _, err := (Repository{}).StuckMatches(context.Background(), time.Now(), 10); err == nil {
		t.Error("StuckMatches accepted a nil DB")
	}
	if _, err := (Repository{}).MarkUnknown(context.Background(), 1, StateLive, time.Now()); err == nil {
		t.Error("MarkUnknown accepted a nil DB")
	}
}

// The catalogue reads fixtures.status and treats anything not finished-like as
// upcoming. Ending the stuck clock in `matches` while leaving the projection
// saying `second_half` would fix the live surface and leave the same stranded
// match sitting in the catalogue forever -- half a fix presented as a whole one.
func TestMarkUnknownAlsoClearsTheEngineProjection(t *testing.T) {
	db := repositoryDB(t)
	now := time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)
	id := insertMatch(t, db, "second_half", now.Add(-4*time.Hour), now.Add(-3*time.Hour), 55, 2, 1)
	projectFixture(t, db, id, "second_half")

	changed, err := NewRepository(db).MarkUnknown(context.Background(), id, StateSecondHalf, now)
	if err != nil || !changed {
		t.Fatalf("MarkUnknown changed=%v err=%v", changed, err)
	}
	if got := fixtureStatus(t, db, id); got != string(StateUnknown) {
		t.Fatalf("projection status = %q, want unknown", got)
	}
}

func TestALostRaceLeavesTheProjectionAlone(t *testing.T) {
	// A real observation won, so the projection will follow that observation
	// rather than this sweep.
	db := repositoryDB(t)
	now := time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)
	id := insertMatch(t, db, "finished", now.Add(-4*time.Hour), now.Add(-3*time.Hour), 90, 2, 1)
	projectFixture(t, db, id, "finished")

	changed, err := NewRepository(db).MarkUnknown(context.Background(), id, StateSecondHalf, now)
	if err != nil || changed {
		t.Fatalf("MarkUnknown changed=%v err=%v", changed, err)
	}
	if got := fixtureStatus(t, db, id); got != "finished" {
		t.Fatalf("projection status = %q, want the real result preserved", got)
	}
}

// A match with no projection row must not roll back the write that actually
// ends the stuck clock.
func TestMarkUnknownSucceedsWithoutAProjectionRow(t *testing.T) {
	db := repositoryDB(t)
	now := time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)
	id := insertMatch(t, db, "second_half", now.Add(-4*time.Hour), now.Add(-3*time.Hour), 55, 0, 0)

	changed, err := NewRepository(db).MarkUnknown(context.Background(), id, StateSecondHalf, now)
	if err != nil || !changed {
		t.Fatalf("MarkUnknown changed=%v err=%v", changed, err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM matches WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != string(StateUnknown) {
		t.Fatalf("status = %q, want unknown", status)
	}
}

// A projection failure rolls back the match update too.
//
// Letting the match stand as `unknown` while the projection kept saying
// `second_half` would recreate exactly the split state MarkUnknown exists to
// prevent -- the live surface fixed, the catalogue still stranded -- and it
// would do so silently. Failing whole means the next sweep tries again two
// minutes later.
func TestAProjectionFailureLeavesTheMatchUntouched(t *testing.T) {
	db := repositoryDB(t)
	now := time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)
	id := insertMatch(t, db, "second_half", now.Add(-4*time.Hour), now.Add(-3*time.Hour), 55, 2, 1)
	if _, err := db.Exec(`DROP TABLE fixtures`); err != nil {
		t.Fatalf("drop projection: %v", err)
	}

	changed, err := NewRepository(db).MarkUnknown(context.Background(), id, StateSecondHalf, now)
	if err == nil {
		t.Fatal("a broken projection reported success")
	}
	if changed {
		t.Fatal("a failed transaction reported a change")
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM matches WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "second_half" {
		t.Fatalf("status = %q; the match was updated while the projection was not", status)
	}
}

// A match still claiming it has not kicked off, long after it did.
//
// The sweep only ever looked at matches that had reached a live state, so a
// match live polling never saw at all stayed 'upcoming' for ever. On
// 2026-09-22 production held 3,207 of them, the oldest kicking off in December
// 2025, none with a score. They read as "starting soon" on the in-play board
// while the match was long over, and 680 were in the indexable sitemap being
// advertised as fixtures still to come.
func TestStuckMatchesSelectsUpcomingMatchesWhoseKickoffHasLongPassed(t *testing.T) {
	db := repositoryDB(t)
	now := time.Date(2026, 9, 6, 16, 0, 0, 0, time.UTC)

	played := insertMatch(t, db, "upcoming", now.Add(-6*time.Hour), nil, 0, 0, 0)

	rows, err := NewRepository(db).StuckMatches(context.Background(), now, 50)
	if err != nil {
		t.Fatalf("StuckMatches returned error: %v", err)
	}
	var found *StuckMatch
	for i := range rows {
		if rows[i].MatchID == played {
			found = &rows[i]
		}
	}
	if found == nil {
		t.Fatal("a match that kicked off six hours ago and never left 'upcoming' " +
			"was not selected; nothing else will ever correct it")
	}
	if found.State != StateUpcoming {
		t.Errorf("state = %q, want upcoming", found.State)
	}
}

// The clause that selects those must not reach fixtures that simply have not
// started yet.
//
// Every upcoming match has a NULL live_state_at -- that is what upcoming means
// -- so selecting on staleness rather than on the clock would match the entire
// forward schedule, and the sweep would mark every fixture on the calendar
// unknown. This is the guard on the single most destructive way to get the
// change wrong.
func TestStuckMatchesLeavesTheForwardScheduleAlone(t *testing.T) {
	db := repositoryDB(t)
	now := time.Date(2026, 9, 6, 16, 0, 0, 0, time.UTC)

	soon := insertMatch(t, db, "upcoming", now.Add(30*time.Minute), nil, 0, 0, 0)
	nextWeek := insertMatch(t, db, "upcoming", now.Add(7*24*time.Hour), nil, 0, 0, 0)
	// Inside the plausible-duration window: kicked off, could still be playing.
	justStarted := insertMatch(t, db, "upcoming", now.Add(-20*time.Minute), nil, 0, 0, 0)

	rows, err := NewRepository(db).StuckMatches(context.Background(), now, 50)
	if err != nil {
		t.Fatalf("StuckMatches returned error: %v", err)
	}
	for _, row := range rows {
		switch row.MatchID {
		case soon:
			t.Error("a match kicking off in 30 minutes was selected as overdue")
		case nextWeek:
			t.Error("a match kicking off next week was selected as overdue -- " +
				"this would mark the whole forward schedule unknown")
		case justStarted:
			t.Error("a match that kicked off 20 minutes ago was selected; " +
				"it could still be playing")
		}
	}
}
