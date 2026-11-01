package search

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// These are the queries QA reported as broken, run against the SQL that
// actually ships. They cannot be unit tested: the bug was in the shape of the
// statement, so anything that stubs the database out would have passed
// throughout.
//
// Opt-in, following services/api/internal/opshealth/postgres_test.go:
//
//	SEARCH_TEST_DATABASE_URL='postgres://sports:sports@127.0.0.1:5432/scratch?sslmode=disable' \
//	  go test ./services/api/internal/search/
//
// It creates and drops its own schema. Point it at a scratch database anyway.
func TestSearchRecallAgainstPostgres(t *testing.T) {
	url := os.Getenv("SEARCH_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set SEARCH_TEST_DATABASE_URL to run the recall cases against PostgreSQL")
	}
	const schema = "search_recall_test"

	admin, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer admin.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := admin.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() {
		_, _ = admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
	}()

	// search_path has to come from the connection string: database/sql hands
	// out pooled connections, so a `SET search_path` runs on one of them and
	// the next query lands on another.
	separator := "?"
	if strings.Contains(url, "?") {
		separator = "&"
	}
	db, err := sql.Open("pgx", url+separator+"options=-csearch_path%3D"+schema)
	if err != nil {
		t.Fatalf("open scoped database: %v", err)
	}
	defer db.Close()

	exec := func(statements ...string) {
		t.Helper()
		for _, statement := range statements {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Fatalf("exec %.60s: %v", statement, err)
			}
		}
	}
	exec(
		`CREATE TABLE countries (code text PRIMARY KEY, name text NOT NULL, flag_url text)`,
		`CREATE TABLE leagues (id bigint PRIMARY KEY, name text NOT NULL, slug text, country_code text, logo_url text, priority_score int)`,
		`CREATE TABLE teams (id bigint PRIMARY KEY, name text NOT NULL, slug text, country_code text, logo_url text)`,
		`CREATE TABLE players (id bigint PRIMARY KEY, name text NOT NULL, team_id bigint, photo_url text)`,
		`CREATE TABLE fixtures (id bigint PRIMARY KEY, home_team_id bigint, away_team_id bigint, league_id bigint, kickoff_at timestamptz, status text)`,
		`INSERT INTO countries VALUES ('GB','United Kingdom',NULL)`,
		`INSERT INTO leagues VALUES (1,'Premier League','premier-league','GB',NULL,100)`,
		`INSERT INTO teams VALUES
		   (10,'Manchester City','team-england-manchester-city','GB',NULL),
		   (11,'Sunderland','team-england-sunderland','GB',NULL),
		   (12,'Sunderland U21','team-england-sunderland-u21','GB',NULL),
		   (13,'Manchester United','team-england-manchester-united','GB',NULL),
		   (14,'Tottenham Hotspur','team-england-tottenham-hotspur','GB',NULL)`,
	)
	// The fixture the user was looking for: kicked off six hours ago, so it is
	// in the past but its detail page still resolves. And one that has not
	// kicked off yet, to prove nothing regressed.
	exec(
		`INSERT INTO fixtures VALUES (3735044,10,11,1,now() - interval '6 hours','unknown')`,
		`INSERT INTO fixtures VALUES (4014494,11,13,1,now() + interval '2 days','scheduled')`,
	)

	source := Source{db: db, schema: schema, now: time.Now}

	for _, testCase := range []struct {
		query    string
		wantName string
		wantType string
	}{
		// QA's verbatim query. Before this change: zero results, because no
		// single column contains "sunderland vs mancity".
		{"sunderland vs mancity", "Manchester City vs Sunderland", "match"},
		{"sunderland vs manchester city", "Manchester City vs Sunderland", "match"},
		{"mancity vs sunderland", "Manchester City vs Sunderland", "match"},
		{"man city sunderland", "Manchester City vs Sunderland", "match"},
		// The abbreviations on their own.
		{"mancity", "Manchester City", "team"},
		{"man city", "Manchester City", "team"},
		{"man utd", "Manchester United", "team"},
		{"spurs", "Tottenham Hotspur", "team"},
		// And the queries that already worked, so the rewrite is not a trade.
		{"sunderland", "Sunderland", "team"},
		{"manchester city", "Manchester City", "team"},
		{"premier league", "Premier League", "league"},
	} {
		t.Run(testCase.query, func(t *testing.T) {
			results, err := source.Search(ctx, testCase.query, 20)
			if err != nil {
				t.Fatalf("search %q: %v", testCase.query, err)
			}
			for _, result := range results {
				if result.Type == testCase.wantType && strings.EqualFold(result.Name, testCase.wantName) {
					return
				}
			}
			names := make([]string, 0, len(results))
			for _, result := range results {
				names = append(names, result.Type+":"+result.Name)
			}
			t.Fatalf("search %q returned %v, want a %s named %q",
				testCase.query, names, testCase.wantType, testCase.wantName)
		})
	}

	// A match must not disappear from search the minute it kicks off — that is
	// when people search for it hardest.
	t.Run("in-play match stays findable", func(t *testing.T) {
		results, err := source.Search(ctx, "manchester city", 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range results {
			if result.Type == "match" && result.ID == 3735044 {
				return
			}
		}
		t.Fatalf("a match that kicked off six hours ago is missing from search: %+v", results)
	})

	// The exact team must outrank its academy sides.
	t.Run("exact team name ranks first", func(t *testing.T) {
		results, err := source.Search(ctx, "sunderland", 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) == 0 || results[0].Name != "Sunderland" {
			t.Fatalf("first result = %+v, want the senior Sunderland side", results)
		}
	})

	// A query that genuinely matches nothing must still come back empty rather
	// than having the fallback passes drag in the whole catalogue.
	t.Run("nonsense query stays empty", func(t *testing.T) {
		results, err := source.Search(ctx, "qzxwvhjk", 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 0 {
			t.Fatalf("expected no results, got %+v", results)
		}
	})

	// The query is answered in up to three passes, and a query that matches
	// nothing runs all three. That is the shape most likely to get slowly
	// worse without anyone noticing, so it is measured against a catalogue the
	// size of the real one.
	t.Run("latency", func(t *testing.T) {
		exec(
			`INSERT INTO teams SELECT g, 'Team Number ' || g, 'team-country-' || g, 'GB', NULL
			   FROM generate_series(1000, 14000) g`,
			`INSERT INTO players SELECT g, 'Player Name ' || g, 1000, NULL
			   FROM generate_series(1, 6500) g`,
			"ANALYZE",
		)
		const budget = time.Second
		for _, query := range []string{"sunderland", "sunderland vs mancity", "qzxwvhjkmn pqrstu"} {
			// Warm the plan cache, then measure.
			if _, err := source.Search(ctx, query, 20); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			if _, err := source.Search(ctx, query, 20); err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(started)
			t.Logf("search %-24q %s", query, elapsed)
			if elapsed > budget {
				t.Errorf("search %q took %s, over the %s budget", query, elapsed, budget)
			}
		}
	})
}
