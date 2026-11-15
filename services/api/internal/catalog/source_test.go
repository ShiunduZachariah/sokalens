package catalog

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestLookupTeamUsesOnlyExplicitActiveIdentityAlias(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE TABLE countries (code TEXT PRIMARY KEY, name TEXT, flag_url TEXT)",
		"CREATE TABLE teams (id INTEGER PRIMARY KEY, slug TEXT UNIQUE, name TEXT, country_code TEXT, logo_url TEXT)",
		"CREATE TABLE team_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER, revoked_at TEXT)",
		"INSERT INTO teams (id, slug, name, logo_url) VALUES (1, 'team-world-wales-u18', 'Wales U18', 'old.png'), (2, 'team-wales-wales-u18', 'Wales U18', 'new.png'), (3, 'another-wales-u18', 'Wales U18', 'other.png')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	source := NewSource(db)
	ctx := context.Background()
	before, err := source.lookupTeam(ctx, "team-world-wales-u18")
	if err != nil || before.ID != 1 || before.Slug != "team-world-wales-u18" {
		t.Fatalf("before alias = %+v, %v", before, err)
	}
	if _, err := db.Exec("INSERT INTO team_identity_aliases (old_id, canonical_id) VALUES (1, 2)"); err != nil {
		t.Fatal(err)
	}
	resolved, err := source.lookupTeam(ctx, "team-world-wales-u18")
	if err != nil || resolved.ID != 2 || resolved.Slug != "team-wales-wales-u18" || resolved.LogoURL != "new.png" {
		t.Fatalf("active alias = %+v, %v, want canonical team", resolved, err)
	}
	unrelated, err := source.lookupTeam(ctx, "another-wales-u18")
	if err != nil || unrelated.ID != 3 || unrelated.Slug != "another-wales-u18" {
		t.Fatalf("same-name team without alias = %+v, %v", unrelated, err)
	}
	if _, err := db.Exec("UPDATE team_identity_aliases SET revoked_at = '2026-10-06T12:00:00Z' WHERE old_id = 1"); err != nil {
		t.Fatal(err)
	}
	after, err := source.lookupTeam(ctx, "team-world-wales-u18")
	if err != nil || after.ID != 1 || after.Slug != "team-world-wales-u18" {
		t.Fatalf("revoked alias = %+v, %v, want original identity", after, err)
	}
}

// The team fixture queries resolve identity aliases once, then filter fixtures
// by plain equality so the home/away indexes apply. This pins the two halves:
// which ids are resolved, and that the rendered predicate selects exactly the
// fixtures of those ids on either side.
func TestTeamFixturesFollowOnlyActiveIdentityAliases(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE TABLE team_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER, revoked_at TEXT)",
		"CREATE TABLE fixtures (id INTEGER PRIMARY KEY, home_team_id INTEGER, away_team_id INTEGER)",
		// 2 is canonical. 1 is folded into it, 4 was folded and then revoked,
		// 3 and 5 are unrelated teams.
		"INSERT INTO team_identity_aliases (old_id, canonical_id, revoked_at) VALUES (1, 2, NULL), (4, 2, '2026-10-06T12:00:00Z'), (5, 3, NULL)",
		"INSERT INTO fixtures (id, home_team_id, away_team_id) VALUES (10, 2, 3), (11, 3, 2), (12, 1, 3), (13, 3, 1), (14, 4, 3), (15, 3, 5)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	source := NewSource(db)
	ctx := context.Background()

	fixturesFor := func(teamID int64) []int64 {
		t.Helper()
		ids, err := source.teamIdentityIDs(ctx, teamID)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := db.QueryContext(ctx, "SELECT f.id FROM fixtures f WHERE "+teamSidePredicate(len(ids))+" ORDER BY f.id", ids...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			got = append(got, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return got
	}

	equal := func(got, want []int64) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	// Canonical team: its own fixtures home and away, plus the active alias's,
	// but not the revoked alias's fixture 14.
	if got, want := fixturesFor(2), []int64{10, 11, 12, 13}; !equal(got, want) {
		t.Fatalf("canonical team fixtures = %v, want %v", got, want)
	}
	// A team with no alias must still find its own fixtures.
	if got, want := fixturesFor(4), []int64{14}; !equal(got, want) {
		t.Fatalf("team without alias fixtures = %v, want %v", got, want)
	}
	// An alias pointing at a different canonical team must not leak in.
	if got, want := fixturesFor(1), []int64{12, 13}; !equal(got, want) {
		t.Fatalf("old id queried directly = %v, want %v", got, want)
	}

	if got, want := teamSidePredicate(2), "(f.home_team_id IN ($1, $2) OR f.away_team_id IN ($1, $2))"; got != want {
		t.Fatalf("predicate = %q, want %q", got, want)
	}
}
