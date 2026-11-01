package fixturepolicy

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// The defect this guards against was invisible to text assertions: the clause
// parsed, ran, and returned the wrong rows. Only executing it against
// PostgreSQL proves which fixtures survive it.
//
// DATABASE_URL is set for the whole `api` CI job, so this runs there. Locally
// it skips unless you point it at a throwaway database. In CI
// FIXTUREPOLICY_REQUIRE_DB_TESTS makes a missing database a failure: a skipped
// database test is not a passing one, and a suite that silently shrinks is how
// this defect survived in the first place.
func TestUnresolvedEarlierFixtureClauseAgainstPostgres(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		const message = "set DATABASE_URL to run the unresolved-fixture clause against PostgreSQL"
		if os.Getenv("FIXTUREPOLICY_REQUIRE_DB_TESTS") != "" {
			t.Fatal(message + " (FIXTUREPOLICY_REQUIRE_DB_TESTS is set, so a skip is a failure)")
		}
		t.Skip(message)
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	// One connection only: the scratch schema is selected with SET
	// search_path, which is per-session. A pool would hand later statements a
	// connection that never saw it.
	db.SetMaxOpenConns(1)

	schema := fmt.Sprintf("fixturepolicy_test_%d", time.Now().UnixNano())
	if _, err := db.Exec(fmt.Sprintf(`CREATE SCHEMA %q`, schema)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema)); err != nil {
			t.Logf("drop schema: %v", err)
		}
		db.Close()
	})
	if _, err := db.Exec(fmt.Sprintf(`SET search_path TO %q`, schema)); err != nil {
		t.Fatalf("set search_path: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE fixtures (
  id BIGINT PRIMARY KEY,
  home_team_id BIGINT NOT NULL,
  away_team_id BIGINT NOT NULL,
  kickoff_at TIMESTAMPTZ NOT NULL,
  status TEXT,
  home_goals_ft INT,
  away_goals_ft INT
)`); err != nil {
		t.Fatalf("create fixtures: %v", err)
	}

	insert := func(id, home, away int64, kickoffOffset time.Duration, status string) {
		t.Helper()
		if _, err := db.Exec(
			`INSERT INTO fixtures (id, home_team_id, away_team_id, kickoff_at, status)
			 VALUES ($1, $2, $3, now() + $4::interval, $5)`,
			id, home, away, fmt.Sprintf("%d seconds", int64(kickoffOffset.Seconds())), status,
		); err != nil {
			t.Fatalf("insert fixture %d: %v", id, err)
		}
	}

	const hour = time.Hour

	// A match played four days ago whose status never advanced. This is the
	// production case: Sunderland vs AZ Alkmaar, still `unknown`, which hid
	// Manchester City vs Sunderland.
	insert(1, 2, 3, -96*hour, "unknown")
	insert(100, 1, 2, 7*hour, "unknown")

	// Same mechanism with the other stale value, `upcoming` in the past. It
	// was the single largest blocker in production.
	insert(2, 6, 4, -96*hour, "upcoming")
	insert(200, 4, 5, 48*hour, "upcoming")

	// A fixture that has not kicked off is not awaiting a result. Suppressing
	// on it meant only a team's next match was ever eligible.
	insert(3, 7, 18, 48*hour, "upcoming")
	insert(300, 7, 8, 120*hour, "upcoming")

	// The guard that must survive: a match in flight right now.
	insert(4, 9, 11, -2*hour, "live")
	insert(400, 9, 10, 3*hour, "upcoming")

	// Decided without being played is still decided.
	insert(5, 12, 14, -3*hour, "awarded")
	insert(500, 12, 13, 3*hour, "upcoming")

	rows, err := db.Query(`
SELECT f.id
FROM fixtures AS f
WHERE f.kickoff_at >= now()` + UnresolvedEarlierFixtureClause("f", time.Now()) + `
ORDER BY f.id`)
	if err != nil {
		t.Fatalf("eligibility query: %v", err)
	}
	defer rows.Close()
	eligible := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		eligible[id] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	for _, want := range []struct {
		id     int64
		reason string
	}{
		{100, "a match played four days ago and still marked `unknown` must not hide a later fixture"},
		{200, "a match played four days ago and still marked `upcoming` must not hide a later fixture"},
		{300, "a team's second fixture in the horizon must stay eligible"},
		{500, "an awarded result is resolved"},
	} {
		if !eligible[want.id] {
			t.Errorf("fixture %d was suppressed: %s", want.id, want.reason)
		}
	}
	if eligible[400] {
		t.Error("fixture 400 must stay suppressed while its team's match is still in play")
	}
}
