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
