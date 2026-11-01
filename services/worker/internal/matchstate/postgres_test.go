package matchstate

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/dbtest"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// The reaper's SQL runs against PostgreSQL in production and against SQLite in
// every other test in this package. That gap is where dialect bugs live: the
// partial-index predicate, timestamptz round-tripping, and whether the
// compare-and-set actually sees the row it read.
//
// This closes it, opt-in, following the convention in
// services/api/internal/acca/generate_db_test.go:
//
//	MATCHSTATE_TEST_DATABASE_URL='postgres://sports:sports@127.0.0.1:5432/scratch?sslmode=disable' \
//	  go test ./services/worker/internal/matchstate/
//
// It creates and drops its own tables in a dedicated schema, so it never
// touches anything the rest of the database owns. Point it at a scratch
// database regardless -- a test that creates tables should never be aimed at
// production, whatever its safeguards.
func TestReaperAgainstPostgres(t *testing.T) {
	url := dbtest.URLOrSkip(t, "MATCHSTATE_TEST_DATABASE_URL", "run the reaper against PostgreSQL")
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

	const schema = "matchstate_reaper_test"
	exec := func(statements ...string) {
		t.Helper()
		for _, statement := range statements {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Fatalf("exec %.60s: %v", strings.ReplaceAll(statement, "\n", " "), err)
			}
		}
	}

	exec("DROP SCHEMA IF EXISTS "+schema+" CASCADE", "CREATE SCHEMA "+schema, "SET search_path TO "+schema)
	// Deferred rather than t.Cleanup: cleanups run after the test function
	// returns, which is after `defer db.Close()`, so the drop would find a
	// closed handle and leave the schema behind. Defers run last-in-first-out,
	// so registering this one after the close means it runs before it.
	defer func() {
		if _, err := db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE"); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()

	exec(`CREATE TABLE matches (
		id BIGSERIAL PRIMARY KEY,
		league_id BIGINT NOT NULL DEFAULT 1,
		home_team_id BIGINT NOT NULL DEFAULT 1,
		away_team_id BIGINT NOT NULL DEFAULT 2,
		kickoff_at TIMESTAMPTZ NOT NULL,
		status TEXT NOT NULL,
		status_elapsed INTEGER,
		home_score INTEGER,
		away_score INTEGER,
		live_state_at TIMESTAMPTZ)`)
	exec(`CREATE TABLE fixtures (
		id BIGINT PRIMARY KEY,
		status TEXT NOT NULL,
		updated_at TIMESTAMPTZ)`)
	// The same partial indexes migration 000061 creates. Without them the
	// query still works, so this is here to prove the predicate matches.
	exec(`CREATE INDEX idx_matches_non_terminal_kickoff ON matches (kickoff_at)
		WHERE status IN ('scheduled','upcoming','delayed','live','halftime','second_half','extra_time','penalties','break','interrupted','suspended')`)

	now := time.Now().UTC()
	insert := func(status string, kickoff time.Time, liveState any) int64 {
		t.Helper()
		var id int64
		if err := db.QueryRowContext(ctx,
			`INSERT INTO matches (kickoff_at, status, status_elapsed, home_score, away_score, live_state_at)
			 VALUES ($1, $2, 55, 2, 1, $3) RETURNING id`, kickoff, status, liveState).Scan(&id); err != nil {
			t.Fatalf("insert match: %v", err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO fixtures (id, status) VALUES ($1, $2)`, id, status); err != nil {
			t.Fatalf("insert projection: %v", err)
		}
		return id
	}

	stranded := insert("second_half", now.Add(-5*time.Hour), now.Add(-4*time.Hour))
	unstamped := insert("second_half", now.Add(-6*time.Hour), nil)
	healthy := insert("second_half", now.Add(-70*time.Minute), now.Add(-20*time.Second))
	finished := insert("finished", now.Add(-8*time.Hour), now.Add(-6*time.Hour))

	repository := NewRepository(db)
	stuck, err := repository.StuckMatches(ctx, now, 200)
	if err != nil {
		t.Fatalf("StuckMatches: %v", err)
	}
	found := map[int64]StuckMatch{}
	for _, row := range stuck {
		found[row.MatchID] = row
	}
	if _, ok := found[stranded]; !ok {
		t.Error("the stranded match was not selected")
	}
	if _, ok := found[unstamped]; !ok {
		t.Error("the match with no freshness stamp was not selected")
	}
	if _, ok := found[healthy]; ok {
		t.Error("a freshly polled match was selected")
	}
	if _, ok := found[finished]; ok {
		t.Error("a finished match was selected")
	}
	// timestamptz must survive the round trip, or Assess would judge on a zero
	// time and bound every match immediately.
	if row := found[stranded]; row.LiveStateAt.IsZero() {
		t.Error("live_state_at did not round-trip from PostgreSQL")
	}
	if row := found[unstamped]; !row.LiveStateAt.IsZero() {
		t.Error("a NULL live_state_at did not read back as zero")
	}

	result, err := Reaper{Store: repository, Now: func() time.Time { return now }}.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if result.MarkedUnknown != 2 {
		t.Fatalf("marked unknown = %d, want the two past the hard bound", result.MarkedUnknown)
	}

	assertStatus := func(table string, id int64, want string) {
		t.Helper()
		var got string
		if err := db.QueryRowContext(ctx, `SELECT status FROM `+table+` WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		if got != want {
			t.Errorf("%s.status for %d = %q, want %q", table, id, got, want)
		}
	}
	// Both rows move together, or the catalogue keeps showing a match the live
	// surface has already given up on.
	assertStatus("matches", stranded, string(StateUnknown))
	assertStatus("fixtures", stranded, string(StateUnknown))
	assertStatus("matches", healthy, "second_half")
	assertStatus("fixtures", healthy, "second_half")
	assertStatus("matches", finished, "finished")

	// The score is real information and survives: the reaper says "this row is
	// no longer describing a match in play", never "here is a result".
	var home, away, elapsed int
	if err := db.QueryRowContext(ctx,
		`SELECT home_score, away_score, status_elapsed FROM matches WHERE id = $1`, stranded).
		Scan(&home, &away, &elapsed); err != nil {
		t.Fatalf("read score: %v", err)
	}
	if home != 2 || away != 1 || elapsed != 55 {
		t.Errorf("the reaper altered the observed score: %d-%d at %d", home, away, elapsed)
	}

	// Compare-and-set: a real observation that landed first must win.
	if _, err := db.ExecContext(ctx, `UPDATE matches SET status = 'finished' WHERE id = $1`, unstamped); err != nil {
		t.Fatalf("simulate concurrent observation: %v", err)
	}
	changed, err := repository.MarkUnknown(ctx, unstamped, StateSecondHalf, now)
	if err != nil {
		t.Fatalf("MarkUnknown: %v", err)
	}
	if changed {
		t.Error("the reaper overwrote a fresher truth")
	}
	assertStatus("matches", unstamped, "finished")

	// And the partial index is actually used, not merely present. A partial
	// index whose predicate does not match its query is a silent miss:
	// everything works, just sequentially, forever.
	//
	// EXPLAIN returns one row per plan line, so this reads them all -- scanning
	// only the first would report "Limit (cost=...)" and prove nothing, which
	// is what the first version of this test did.
	rows, err := db.QueryContext(ctx, `
EXPLAIN (FORMAT TEXT)
SELECT id, status, kickoff_at, live_state_at FROM matches
WHERE status IN ('live','halftime','second_half','extra_time','penalties','break','interrupted','suspended')
  AND (live_state_at IS NULL OR live_state_at < $1 OR kickoff_at < $2)
ORDER BY COALESCE(live_state_at, kickoff_at) ASC LIMIT 200`,
		now.Add(-4*time.Minute), now.Add(-145*time.Minute))
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
	t.Logf("reaper plan:\n%s", plan.String())
	if !strings.Contains(plan.String(), "idx_matches_non_terminal_kickoff") {
		t.Errorf("the partial index was not used; the sweep would scan every match ever played:\n%s", plan.String())
	}
}
