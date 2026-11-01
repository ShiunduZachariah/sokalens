package seo

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestFixtureSitemapListsOnlyFixturesNearKickoff(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE matches (id INTEGER PRIMARY KEY, kickoff_at TEXT NOT NULL);",
		"CREATE TABLE fixture_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL, revoked_at TEXT);",
		"CREATE TABLE team_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL, revoked_at TEXT);",
		"CREATE TABLE seo_urls (path TEXT PRIMARY KEY, entity_type TEXT NOT NULL, entity_id INTEGER, indexable BOOLEAN NOT NULL, last_modified TEXT);",
		"INSERT INTO matches (id, kickoff_at) VALUES " +
			"(1, '2026-09-21T12:00:00Z')," + // 3 days ago: still indexable, no longer advertised
			"(2, '2026-09-23T12:00:00Z')," + // yesterday: result searches
			"(3, '2026-09-24T18:00:00Z')," + // tonight
			"(4, '2026-09-28T12:00:00Z')," + // four days ahead
			"(5, '2026-10-01T12:00:00Z')," + // seven days ahead: too early
			"(6, '2026-09-24T20:00:00Z');", // tonight, but noindex
		"INSERT INTO seo_urls (path, entity_type, entity_id, indexable) VALUES " +
			"('/predictions/l/a-vs-b-1', 'fixture', 1, 1)," +
			"('/predictions/l/a-vs-b-2', 'fixture', 2, 1)," +
			"('/predictions/l/a-vs-b-3', 'fixture', 3, 1)," +
			"('/predictions/l/a-vs-b-4', 'fixture', 4, 1)," +
			"('/predictions/l/a-vs-b-5', 'fixture', 5, 1)," +
			"('/predictions/l/a-vs-b-6', 'fixture', 6, 0)," +
			"('/teams/team-a', 'team', 10, 1);",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	source := NewSource(db)
	source.now = func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC) }

	urls, err := source.List(context.Background(), "fixture", 0, 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var got []string
	for _, url := range urls {
		got = append(got, url.Path)
	}
	want := []string{"/predictions/l/a-vs-b-2", "/predictions/l/a-vs-b-3", "/predictions/l/a-vs-b-4"}
	if len(got) != len(want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paths = %v, want %v", got, want)
		}
	}

	// The sitemap index sizes its chunks from Count, so it must apply the
	// same band or the index would announce chunks that come back empty.
	total, err := source.Count(context.Background(), "fixture")
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if total != len(want) {
		t.Fatalf("Count = %d, want %d", total, len(want))
	}

	// Stable entity types are not windowed.
	teams, err := source.List(context.Background(), "team", 0, 100)
	if err != nil {
		t.Fatalf("List team: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("team urls = %d, want 1", len(teams))
	}
	if _, err := db.Exec("INSERT INTO fixture_identity_aliases (old_id, canonical_id) VALUES (3, 4)"); err != nil {
		t.Fatalf("insert fixture alias: %v", err)
	}
	if _, err := db.Exec("INSERT INTO team_identity_aliases (old_id, canonical_id) VALUES (10, 11)"); err != nil {
		t.Fatalf("insert team alias: %v", err)
	}
	filtered, err := source.List(context.Background(), "fixture", 0, 100)
	if err != nil || len(filtered) != 2 {
		t.Fatalf("fixture sitemap after alias = %+v, %v, want 2", filtered, err)
	}
	teamCount, err := source.Count(context.Background(), "team")
	if err != nil || teamCount != 0 {
		t.Fatalf("team sitemap count after alias = %d, %v, want 0", teamCount, err)
	}
	if _, err := db.Exec("UPDATE fixture_identity_aliases SET revoked_at = '2026-09-24T12:00:00Z' WHERE old_id = 3"); err != nil {
		t.Fatalf("revoke fixture alias: %v", err)
	}
	if total, err := source.Count(context.Background(), "fixture"); err != nil || total != 3 {
		t.Fatalf("fixture sitemap count after revocation = %d, %v, want 3", total, err)
	}
}
