package ingestion

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCatalogRepositoryPreservesLeagueCountryCodeAndResolvesByName(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE countries (code TEXT PRIMARY KEY, name TEXT NOT NULL);",
		"CREATE TABLE sports (id INTEGER PRIMARY KEY AUTOINCREMENT, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL);",
		"CREATE TABLE leagues (id INTEGER PRIMARY KEY AUTOINCREMENT, sport_id INTEGER NOT NULL, country_code TEXT, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL, priority_score INTEGER NOT NULL DEFAULT 0);",
		"CREATE TABLE teams (id INTEGER PRIMARY KEY AUTOINCREMENT, country_code TEXT, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL);",
		"INSERT INTO countries (code, name) VALUES ('GB', 'England');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	repo := NewCatalogRepository(db)
	leagueID, err := repo.UpsertLeague(context.Background(), LeagueRecord{
		SportSlug:   "football",
		SportName:   "Football",
		CountryCode: "GB",
		Slug:        "league-39-premier-league",
		Name:        "Premier League",
	})
	if err != nil {
		t.Fatalf("UpsertLeague returned error: %v", err)
	}
	if leagueID <= 0 {
		t.Fatalf("league id = %d, want positive id", leagueID)
	}

	if _, err := repo.UpsertLeague(context.Background(), LeagueRecord{
		SportSlug: "football",
		SportName: "Football",
		Slug:      "league-39-premier-league",
		Name:      "Premier League",
	}); err != nil {
		t.Fatalf("UpsertLeague blank country returned error: %v", err)
	}

	if _, err := repo.UpsertLeague(context.Background(), LeagueRecord{
		SportSlug:   "football",
		SportName:   "Football",
		CountryName: "England",
		Slug:        "league-40-fa-cup",
		Name:        "FA Cup",
	}); err != nil {
		t.Fatalf("UpsertLeague country-name fallback returned error: %v", err)
	}

	var preservedCode string
	if err := db.QueryRowContext(context.Background(), `SELECT country_code FROM leagues WHERE slug = 'league-39-premier-league'`).Scan(&preservedCode); err != nil {
		t.Fatalf("scan preserved league: %v", err)
	}
	if preservedCode != "GB" {
		t.Fatalf("preserved country code = %q, want GB", preservedCode)
	}

	var resolvedCode string
	if err := db.QueryRowContext(context.Background(), `SELECT country_code FROM leagues WHERE slug = 'league-40-fa-cup'`).Scan(&resolvedCode); err != nil {
		t.Fatalf("scan resolved league: %v", err)
	}
	if resolvedCode != "GB" {
		t.Fatalf("resolved country code = %q, want GB", resolvedCode)
	}
}

func TestCatalogRepositoryMirrorsProviderMatchesIntoEngineFixtures(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE countries (code TEXT PRIMARY KEY, name TEXT NOT NULL);",
		"CREATE TABLE sports (id INTEGER PRIMARY KEY AUTOINCREMENT, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL);",
		"CREATE TABLE leagues (id INTEGER PRIMARY KEY AUTOINCREMENT, sport_id INTEGER NOT NULL, country_code TEXT, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL, priority_score INTEGER NOT NULL DEFAULT 0);",
		"CREATE TABLE teams (id INTEGER PRIMARY KEY AUTOINCREMENT, country_code TEXT, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL);",
		"CREATE TABLE matches (id INTEGER PRIMARY KEY AUTOINCREMENT, league_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TIMESTAMP NOT NULL, status TEXT NOT NULL, provider_status_raw TEXT, provider_status_label TEXT, status_elapsed INTEGER, venue_name TEXT, venue_city TEXT, home_score INTEGER, away_score INTEGER, half_time_home_score INTEGER, half_time_away_score INTEGER, live_state_at TIMESTAMP, UNIQUE (league_id, home_team_id, away_team_id, kickoff_at));",
		"CREATE TABLE fixture_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL, revoked_at TEXT);",
		"CREATE TABLE fixtures (id INTEGER PRIMARY KEY, league_id INTEGER NOT NULL, season_id INTEGER NOT NULL DEFAULT 2026, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TIMESTAMP NOT NULL, status TEXT NOT NULL, venue_name TEXT, home_goals_ft INTEGER, away_goals_ft INTEGER, home_goals_ht INTEGER, away_goals_ht INTEGER, source_match_id INTEGER, source TEXT NOT NULL DEFAULT 'matches_backfill', finished_at TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP);",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	repo := NewCatalogRepository(db)
	kickoff := time.Date(2026, 7, 3, 18, 0, 0, 0, time.UTC)
	match := MatchRecord{
		LeagueID:          39,
		SeasonID:          2025,
		HomeTeamID:        10,
		AwayTeamID:        20,
		KickoffAt:         kickoff,
		Status:            "upcoming",
		RawStatus:         "NS",
		RawStatusLabel:    "Not Started",
		HalfTimeHomeScore: intPtr(1),
		HalfTimeAwayScore: intPtr(0),
	}

	matchID, err := repo.InsertMatch(context.Background(), match)
	if err != nil {
		t.Fatalf("InsertMatch returned error: %v", err)
	}

	var fixtureStatus string
	var fixtureSeason int
	var fixtureKickoff time.Time
	if err := db.QueryRowContext(context.Background(), `SELECT status, season_id, kickoff_at FROM fixtures WHERE id = ?`, matchID).Scan(&fixtureStatus, &fixtureSeason, &fixtureKickoff); err != nil {
		t.Fatalf("scan mirrored fixture: %v", err)
	}
	if fixtureStatus != "upcoming" {
		t.Fatalf("fixture status = %q, want upcoming", fixtureStatus)
	}
	if !fixtureKickoff.Equal(kickoff) {
		t.Fatalf("fixture kickoff = %s, want %s", fixtureKickoff, kickoff)
	}
	if fixtureSeason != 2025 {
		t.Fatalf("fixture season = %d, want provider season start 2025", fixtureSeason)
	}

	duplicate := match
	duplicate.Status = "finished"
	duplicate.HomeScore = intPtr(2)
	duplicate.AwayScore = intPtr(1)
	duplicateID, err := repo.InsertMatch(context.Background(), duplicate)
	if err != nil {
		t.Fatalf("duplicate InsertMatch returned error: %v", err)
	}
	if duplicateID != matchID {
		t.Fatalf("duplicate match id = %d, want canonical id %d", duplicateID, matchID)
	}
	var matchCount int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM matches`).Scan(&matchCount); err != nil {
		t.Fatalf("count canonical matches: %v", err)
	}
	if matchCount != 1 {
		t.Fatalf("canonical match count = %d, want 1", matchCount)
	}
	var canonicalStatus string
	var canonicalHomeGoals int
	if err := db.QueryRowContext(context.Background(), `SELECT status, home_goals_ft FROM fixtures WHERE id = ?`, matchID).Scan(&canonicalStatus, &canonicalHomeGoals); err != nil {
		t.Fatalf("scan deduplicated fixture: %v", err)
	}
	if canonicalStatus != "finished" || canonicalHomeGoals != 2 {
		t.Fatalf("deduplicated fixture = status %q home goals %d, want finished/2", canonicalStatus, canonicalHomeGoals)
	}
	staleUpcoming := match
	staleUpcoming.Status = "upcoming"
	staleUpcoming.HomeScore = intPtr(0)
	staleUpcoming.AwayScore = intPtr(0)
	if _, err := repo.InsertMatch(context.Background(), staleUpcoming); err != nil {
		t.Fatalf("stale upcoming InsertMatch returned error: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT status, home_goals_ft FROM fixtures WHERE id = ?`, matchID).Scan(&canonicalStatus, &canonicalHomeGoals); err != nil {
		t.Fatalf("scan fixture after stale update: %v", err)
	}
	if canonicalStatus != "finished" || canonicalHomeGoals != 2 {
		t.Fatalf("stale update regressed fixture to status %q home goals %d", canonicalStatus, canonicalHomeGoals)
	}

	match.Status = "finished"
	match.HomeScore = intPtr(2)
	match.AwayScore = intPtr(1)
	if err := repo.UpdateMatch(context.Background(), matchID, match); err != nil {
		t.Fatalf("UpdateMatch returned error: %v", err)
	}

	var homeGoals int
	if err := db.QueryRowContext(context.Background(), `SELECT home_goals_ft FROM fixtures WHERE id = ?`, matchID).Scan(&homeGoals); err != nil {
		t.Fatalf("scan updated fixture: %v", err)
	}
	if homeGoals != 2 {
		t.Fatalf("fixture home goals = %d, want 2", homeGoals)
	}

	staleMapped := match
	staleMapped.Status = "upcoming"
	staleMapped.HomeScore = intPtr(0)
	staleMapped.AwayScore = intPtr(0)
	if err := repo.UpdateMatch(context.Background(), matchID, staleMapped); err != nil {
		t.Fatalf("stale mapped UpdateMatch returned error: %v", err)
	}
	var preservedStatus string
	var preservedHomeGoals sql.NullInt64
	if err := db.QueryRowContext(context.Background(), `SELECT status, home_goals_ft FROM fixtures WHERE id = ?`, matchID).Scan(&preservedStatus, &preservedHomeGoals); err != nil {
		t.Fatalf("scan fixture after stale mapped update: %v", err)
	}
	if preservedStatus != "finished" || !preservedHomeGoals.Valid || preservedHomeGoals.Int64 != 2 {
		t.Fatalf("stale mapped update regressed fixture to status %q home goals %v", preservedStatus, preservedHomeGoals)
	}
}

func TestCatalogRepositoryRecordsFixtureCompletionOnce(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	for _, stmt := range []string{
		"CREATE TABLE matches (id INTEGER PRIMARY KEY AUTOINCREMENT, league_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TIMESTAMP NOT NULL, status TEXT NOT NULL, provider_status_raw TEXT, provider_status_label TEXT, status_elapsed INTEGER, venue_name TEXT, venue_city TEXT, home_score INTEGER, away_score INTEGER, half_time_home_score INTEGER, half_time_away_score INTEGER, live_state_at TIMESTAMP, UNIQUE (league_id, home_team_id, away_team_id, kickoff_at));",
		"CREATE TABLE fixture_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL, revoked_at TEXT);",
		"CREATE TABLE fixtures (id INTEGER PRIMARY KEY, league_id INTEGER NOT NULL, season_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TIMESTAMP NOT NULL, status TEXT NOT NULL, venue_name TEXT, home_goals_ft INTEGER, away_goals_ft INTEGER, home_goals_ht INTEGER, away_goals_ht INTEGER, source_match_id INTEGER, source TEXT NOT NULL, finished_at TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP);",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}
	repo := NewCatalogRepository(db)
	kickoff := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	match := MatchRecord{LeagueID: 39, SeasonID: 2026, HomeTeamID: 1, AwayTeamID: 2, KickoffAt: kickoff, Status: "upcoming"}
	matchID, err := repo.InsertMatch(context.Background(), match)
	if err != nil {
		t.Fatalf("insert upcoming match: %v", err)
	}
	var before sql.NullTime
	if err := db.QueryRow(`SELECT finished_at FROM fixtures WHERE id = ?`, matchID).Scan(&before); err != nil || before.Valid {
		t.Fatalf("upcoming finished_at=%v err=%v, want NULL", before, err)
	}

	match.Status = "finished"
	if _, err := repo.InsertMatch(context.Background(), match); err != nil {
		t.Fatalf("finish match: %v", err)
	}
	var first time.Time
	if err := db.QueryRow(`SELECT finished_at FROM fixtures WHERE id = ?`, matchID).Scan(&first); err != nil {
		t.Fatalf("read first completion: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	match.Status = "ft"
	if _, err := repo.InsertMatch(context.Background(), match); err != nil {
		t.Fatalf("repeat terminal ingestion: %v", err)
	}
	var repeated time.Time
	if err := db.QueryRow(`SELECT finished_at FROM fixtures WHERE id = ?`, matchID).Scan(&repeated); err != nil {
		t.Fatalf("read repeated completion: %v", err)
	}
	if !repeated.Equal(first) {
		t.Fatalf("finished_at changed from %s to %s", first, repeated)
	}
	match.Status = "upcoming"
	if _, err := repo.InsertMatch(context.Background(), match); err != nil {
		t.Fatalf("status correction: %v", err)
	}
	var corrected time.Time
	if err := db.QueryRow(`SELECT finished_at FROM fixtures WHERE id = ?`, matchID).Scan(&corrected); err != nil || !corrected.Equal(first) {
		t.Fatalf("status correction rewrote finished_at=%s err=%v, want %s", corrected, err, first)
	}
}

func TestCatalogRepositoryReconcilesMappedMatchOntoExistingNaturalIdentity(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE matches (id INTEGER PRIMARY KEY AUTOINCREMENT, league_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TIMESTAMP NOT NULL, status TEXT NOT NULL, provider_status_raw TEXT, provider_status_label TEXT, status_elapsed INTEGER, venue_name TEXT, venue_city TEXT, home_score INTEGER, away_score INTEGER, half_time_home_score INTEGER, half_time_away_score INTEGER, live_state_at TIMESTAMP, UNIQUE (league_id, home_team_id, away_team_id, kickoff_at));",
		"CREATE TABLE fixture_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL, revoked_at TEXT);",
		"CREATE TABLE fixtures (id INTEGER PRIMARY KEY, league_id INTEGER NOT NULL, season_id INTEGER NOT NULL DEFAULT 2026, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TIMESTAMP NOT NULL, status TEXT NOT NULL, venue_name TEXT, home_goals_ft INTEGER, away_goals_ft INTEGER, home_goals_ht INTEGER, away_goals_ht INTEGER, source_match_id INTEGER, source TEXT NOT NULL DEFAULT 'matches_backfill', finished_at TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP);",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	repo := NewCatalogRepository(db)
	kickoff := time.Date(2026, 7, 19, 19, 0, 0, 0, time.UTC)
	oldMappedID, err := repo.InsertMatch(context.Background(), MatchRecord{
		LeagueID:          90,
		SeasonID:          2026,
		HomeTeamID:        901,
		AwayTeamID:        902,
		KickoffAt:         kickoff,
		Status:            "finished",
		RawStatus:         "FT",
		RawStatusLabel:    "Match Finished",
		HomeScore:         intPtr(2),
		AwayScore:         intPtr(0),
		HalfTimeHomeScore: intPtr(1),
		HalfTimeAwayScore: intPtr(0),
	})
	if err != nil {
		t.Fatalf("insert old mapped match: %v", err)
	}
	targetID, err := repo.InsertMatch(context.Background(), MatchRecord{
		LeagueID:   71,
		SeasonID:   2026,
		HomeTeamID: 701,
		AwayTeamID: 702,
		KickoffAt:  kickoff,
		Status:     "upcoming",
		RawStatus:  "TIMED",
	})
	if err != nil {
		t.Fatalf("insert natural target match: %v", err)
	}

	convergedID, err := repo.ReconcileMatch(context.Background(), oldMappedID, MatchRecord{
		LeagueID:   71,
		SeasonID:   2026,
		HomeTeamID: 701,
		AwayTeamID: 702,
		KickoffAt:  kickoff,
		Status:     "upcoming",
		RawStatus:  "TIMED",
	})
	if err != nil {
		t.Fatalf("ReconcileMatch returned error: %v", err)
	}
	if convergedID != targetID {
		t.Fatalf("converged id = %d, want existing natural target %d", convergedID, targetID)
	}

	var status string
	var rawStatus string
	var homeScore int
	var awayScore int
	if err := db.QueryRowContext(context.Background(), `
SELECT status, provider_status_raw, home_score, away_score
FROM matches
WHERE id = ?`, targetID).Scan(&status, &rawStatus, &homeScore, &awayScore); err != nil {
		t.Fatalf("scan converged match: %v", err)
	}
	if status != "finished" || rawStatus != "FT" || homeScore != 2 || awayScore != 0 {
		t.Fatalf("converged match = %q/%q %d-%d, want preserved terminal FT 2-0", status, rawStatus, homeScore, awayScore)
	}

	var fixtureStatus string
	var fixtureHomeScore int
	if err := db.QueryRowContext(context.Background(), `SELECT status, home_goals_ft FROM fixtures WHERE id = ?`, targetID).Scan(&fixtureStatus, &fixtureHomeScore); err != nil {
		t.Fatalf("scan converged fixture: %v", err)
	}
	if fixtureStatus != "finished" || fixtureHomeScore != 2 {
		t.Fatalf("converged fixture = %q/%d, want finished/2", fixtureStatus, fixtureHomeScore)
	}
}

func TestCatalogRepositoryReconcilesFixtureProjectionInBoundedBatches(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE matches (id INTEGER PRIMARY KEY AUTOINCREMENT, league_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TIMESTAMP NOT NULL, status TEXT NOT NULL, venue_name TEXT, home_score INTEGER, away_score INTEGER, half_time_home_score INTEGER, half_time_away_score INTEGER);",
		"CREATE TABLE fixture_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL, revoked_at TEXT);",
		"CREATE TABLE fixtures (id INTEGER PRIMARY KEY, league_id INTEGER NOT NULL, season_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TIMESTAMP NOT NULL, status TEXT NOT NULL, venue_name TEXT, home_goals_ft INTEGER, away_goals_ft INTEGER, home_goals_ht INTEGER, away_goals_ht INTEGER, source_match_id INTEGER, source TEXT NOT NULL, finished_at TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP);",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema/data: %v", err)
		}
	}
	kickoffOne := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	kickoffTwo := time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC)
	kickoffThree := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO matches (id, league_id, home_team_id, away_team_id, kickoff_at, status, venue_name, home_score, away_score, half_time_home_score, half_time_away_score) VALUES (?, 10, 101, 102, ?, 'finished', 'Ground A', 2, 1, 1, 0)`, []any{1, kickoffOne}},
		{`INSERT INTO matches (id, league_id, home_team_id, away_team_id, kickoff_at, status, venue_name, home_score, away_score, half_time_home_score, half_time_away_score) VALUES (?, 20, 201, 202, ?, 'finished', 'Ground B', 3, 0, 2, 0)`, []any{2, kickoffTwo}},
		{`INSERT INTO matches (id, league_id, home_team_id, away_team_id, kickoff_at, status, venue_name, home_score, away_score, half_time_home_score, half_time_away_score) VALUES (?, 30, 301, 302, ?, 'upcoming', 'Ground C', NULL, NULL, NULL, NULL)`, []any{3, kickoffThree}},
		{`INSERT INTO fixtures (id, league_id, season_id, home_team_id, away_team_id, kickoff_at, status, venue_name, home_goals_ft, away_goals_ft, home_goals_ht, away_goals_ht, source_match_id, source) VALUES (?, 20, 2026, 201, 202, ?, 'upcoming', NULL, NULL, NULL, NULL, NULL, 2, 'matches_backfill')`, []any{2, kickoffTwo}},
		{`INSERT INTO fixtures (id, league_id, season_id, home_team_id, away_team_id, kickoff_at, status, venue_name, home_goals_ft, away_goals_ft, home_goals_ht, away_goals_ht, source_match_id, source) VALUES (?, 30, 2026, 301, 302, ?, 'upcoming', NULL, NULL, NULL, NULL, NULL, 3, 'provider_ingestion')`, []any{3, kickoffThree}},
	} {
		if _, err := db.ExecContext(context.Background(), statement.query, statement.args...); err != nil {
			t.Fatalf("insert reconciliation data: %v", err)
		}
	}

	repo := NewCatalogRepository(db)
	processed, err := repo.ReconcileFixtureProjection(context.Background(), 1)
	if err != nil {
		t.Fatalf("first reconciliation returned error: %v", err)
	}
	if processed != 1 {
		t.Fatalf("first reconciliation processed %d rows, want 1", processed)
	}

	var missingStatus string
	var missingHomeGoals int
	var missingSource string
	if err := db.QueryRowContext(context.Background(), `SELECT status, home_goals_ft, source FROM fixtures WHERE id = 1`).Scan(&missingStatus, &missingHomeGoals, &missingSource); err != nil {
		t.Fatalf("scan inserted projection: %v", err)
	}
	if missingStatus != "finished" || missingHomeGoals != 2 || missingSource != "matches_reconciliation" {
		t.Fatalf("inserted projection = %q/%d/%q, want finished/2/matches_reconciliation", missingStatus, missingHomeGoals, missingSource)
	}

	processed, err = repo.ReconcileFixtureProjection(context.Background(), 10)
	if err != nil {
		t.Fatalf("second reconciliation returned error: %v", err)
	}
	if processed != 2 {
		t.Fatalf("second reconciliation processed %d rows, want 2", processed)
	}

	var repairedStatus string
	var repairedHomeGoals int
	var repairedSource string
	if err := db.QueryRowContext(context.Background(), `SELECT status, home_goals_ft, source FROM fixtures WHERE id = 2`).Scan(&repairedStatus, &repairedHomeGoals, &repairedSource); err != nil {
		t.Fatalf("scan repaired projection: %v", err)
	}
	if repairedStatus != "finished" || repairedHomeGoals != 3 || repairedSource != "matches_backfill" {
		t.Fatalf("repaired projection = %q/%d/%q, want finished/3/preserved matches_backfill", repairedStatus, repairedHomeGoals, repairedSource)
	}
	var repairedVenue string
	if err := db.QueryRowContext(context.Background(), `SELECT venue_name FROM fixtures WHERE id = 3`).Scan(&repairedVenue); err != nil {
		t.Fatalf("scan venue-only repair: %v", err)
	}
	if repairedVenue != "Ground C" {
		t.Fatalf("repaired venue = %q, want Ground C", repairedVenue)
	}

	processed, err = repo.ReconcileFixtureProjection(context.Background(), 10)
	if err != nil {
		t.Fatalf("idempotent reconciliation returned error: %v", err)
	}
	if processed != 0 {
		t.Fatalf("idempotent reconciliation processed %d rows, want 0", processed)
	}
	if _, err := repo.ReconcileFixtureProjection(context.Background(), 0); err == nil {
		t.Fatal("zero reconciliation limit returned nil error")
	}
	if _, err := repo.ReconcileFixtureProjection(context.Background(), MaxFixtureProjectionReconcileBatch+1); err == nil {
		t.Fatal("oversized reconciliation limit returned nil error")
	}
}

func TestCatalogRepositoryPreservesTeamCountryCode(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE countries (code TEXT PRIMARY KEY, name TEXT NOT NULL);",
		"CREATE TABLE sports (id INTEGER PRIMARY KEY AUTOINCREMENT, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL);",
		"CREATE TABLE leagues (id INTEGER PRIMARY KEY AUTOINCREMENT, sport_id INTEGER NOT NULL, country_code TEXT, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL, priority_score INTEGER NOT NULL DEFAULT 0);",
		"CREATE TABLE teams (id INTEGER PRIMARY KEY AUTOINCREMENT, country_code TEXT, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL);",
		"INSERT INTO countries (code, name) VALUES ('KE', 'Kenya');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	repo := NewCatalogRepository(db)
	if _, err := repo.UpsertTeam(context.Background(), TeamRecord{
		CountryCode: "KE",
		Slug:        "team-1-gor-mahia",
		Name:        "Gor Mahia",
	}); err != nil {
		t.Fatalf("UpsertTeam returned error: %v", err)
	}
	if _, err := repo.UpsertTeam(context.Background(), TeamRecord{
		Slug: "team-1-gor-mahia",
		Name: "Gor Mahia",
	}); err != nil {
		t.Fatalf("UpsertTeam blank country returned error: %v", err)
	}

	var countryCode string
	if err := db.QueryRowContext(context.Background(), `SELECT country_code FROM teams WHERE slug = 'team-1-gor-mahia'`).Scan(&countryCode); err != nil {
		t.Fatalf("scan team: %v", err)
	}
	if countryCode != "KE" {
		t.Fatalf("team country code = %q, want KE", countryCode)
	}
}

func TestCatalogRepositoryTeamExistsRejectsDeletedMappingTarget(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE teams (id INTEGER PRIMARY KEY, slug TEXT UNIQUE, name TEXT)"); err != nil {
		t.Fatalf("create teams: %v", err)
	}
	if _, err := db.Exec("INSERT INTO teams (id, slug, name) VALUES (42, 'team-sweden-konyaspor', 'Konyaspor')"); err != nil {
		t.Fatalf("insert team: %v", err)
	}
	repo := NewCatalogRepository(db)
	for _, tc := range []struct {
		id   int64
		want bool
	}{{42, true}, {36363, false}} {
		got, err := repo.TeamExists(context.Background(), tc.id)
		if err != nil {
			t.Fatalf("TeamExists(%d): %v", tc.id, err)
		}
		if got != tc.want {
			t.Fatalf("TeamExists(%d) = %t, want %t", tc.id, got, tc.want)
		}
	}
}

func TestCatalogRepositoryMapsUnitedKingdomSubdivisionsToGB(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE countries (code TEXT PRIMARY KEY, name TEXT NOT NULL);",
		"CREATE TABLE sports (id INTEGER PRIMARY KEY AUTOINCREMENT, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL);",
		"CREATE TABLE leagues (id INTEGER PRIMARY KEY AUTOINCREMENT, sport_id INTEGER NOT NULL, country_code TEXT, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL, priority_score INTEGER NOT NULL DEFAULT 0);",
		"CREATE TABLE teams (id INTEGER PRIMARY KEY AUTOINCREMENT, country_code TEXT, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL);",
		"INSERT INTO countries (code, name) VALUES ('GB', 'United-Kingdom');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	repo := NewCatalogRepository(db)
	if _, err := repo.UpsertLeague(context.Background(), LeagueRecord{
		SportSlug:   "football",
		SportName:   "Football",
		CountryName: "England",
		Slug:        "league-41-efl-cup",
		Name:        "EFL Cup",
	}); err != nil {
		t.Fatalf("UpsertLeague subdivision fallback returned error: %v", err)
	}

	var countryCode string
	if err := db.QueryRowContext(context.Background(), `SELECT country_code FROM leagues WHERE slug = 'league-41-efl-cup'`).Scan(&countryCode); err != nil {
		t.Fatalf("scan league: %v", err)
	}
	if countryCode != "GB" {
		t.Fatalf("league country code = %q, want GB", countryCode)
	}
}

func TestCatalogRepositoryFetchMatchIdentity(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE matches (id INTEGER PRIMARY KEY AUTOINCREMENT, league_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TIMESTAMP NOT NULL, status TEXT NOT NULL, provider_status_raw TEXT, provider_status_label TEXT, status_elapsed INTEGER, venue_name TEXT, venue_city TEXT, home_score INTEGER, away_score INTEGER, half_time_home_score INTEGER, half_time_away_score INTEGER, live_state_at TIMESTAMP, UNIQUE (league_id, home_team_id, away_team_id, kickoff_at));",
		"CREATE TABLE fixture_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL, revoked_at TEXT);",
		"CREATE TABLE fixtures (id INTEGER PRIMARY KEY, league_id INTEGER NOT NULL, season_id INTEGER NOT NULL DEFAULT 2026, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TIMESTAMP NOT NULL, status TEXT NOT NULL, venue_name TEXT, home_goals_ft INTEGER, away_goals_ft INTEGER, home_goals_ht INTEGER, away_goals_ht INTEGER, source_match_id INTEGER, source TEXT NOT NULL DEFAULT 'matches_backfill', finished_at TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP);",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	repo := NewCatalogRepository(db)
	kickoff := time.Date(2026, 9, 15, 20, 0, 0, 0, time.UTC)
	matchID, err := repo.InsertMatch(context.Background(), MatchRecord{
		LeagueID:   39,
		SeasonID:   2026,
		HomeTeamID: 101,
		AwayTeamID: 202,
		KickoffAt:  kickoff,
		Status:     "upcoming",
		RawStatus:  "NS",
	})
	if err != nil {
		t.Fatalf("InsertMatch returned error: %v", err)
	}

	identity, err := repo.FetchMatchIdentity(context.Background(), matchID)
	if err != nil {
		t.Fatalf("FetchMatchIdentity returned error: %v", err)
	}
	if identity.LeagueID != 39 || identity.HomeTeamID != 101 || identity.AwayTeamID != 202 {
		t.Fatalf("identity = %+v, want league 39 home 101 away 202", identity)
	}
	if !identity.KickoffAt.UTC().Equal(kickoff) {
		t.Fatalf("identity kickoff = %s, want %s", identity.KickoffAt.UTC(), kickoff)
	}
	if identity.Status != "upcoming" {
		t.Fatalf("identity status = %q, want upcoming", identity.Status)
	}

	if _, err := repo.FetchMatchIdentity(context.Background(), 99999); err == nil {
		t.Fatal("FetchMatchIdentity for missing match returned nil error")
	}
	if _, err := repo.FetchMatchIdentity(context.Background(), 0); err == nil {
		t.Fatal("FetchMatchIdentity with zero id returned nil error")
	}
	if _, err := (CatalogRepository{}).FetchMatchIdentity(context.Background(), 1); err == nil {
		t.Fatal("FetchMatchIdentity with nil db returned nil error")
	}
}
func TestCatalogRepositoryFindSuspectedDuplicate(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE leagues (id INTEGER PRIMARY KEY, name TEXT NOT NULL);",
		"CREATE TABLE teams (id INTEGER PRIMARY KEY, name TEXT NOT NULL);",
		"CREATE TABLE matches (id INTEGER PRIMARY KEY AUTOINCREMENT, league_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TIMESTAMP NOT NULL, status TEXT NOT NULL, provider_status_raw TEXT, provider_status_label TEXT, status_elapsed INTEGER, venue_name TEXT, venue_city TEXT, home_score INTEGER, away_score INTEGER, half_time_home_score INTEGER, half_time_away_score INTEGER, live_state_at TIMESTAMP, UNIQUE (league_id, home_team_id, away_team_id, kickoff_at));",
		"CREATE TABLE fixture_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL, revoked_at TEXT);",
		"CREATE TABLE fixtures (id INTEGER PRIMARY KEY, league_id INTEGER NOT NULL, season_id INTEGER NOT NULL DEFAULT 2026, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TIMESTAMP NOT NULL, status TEXT NOT NULL, venue_name TEXT, home_goals_ft INTEGER, away_goals_ft INTEGER, home_goals_ht INTEGER, away_goals_ht INTEGER, source_match_id INTEGER, source TEXT NOT NULL DEFAULT 'matches_backfill', finished_at TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP);",
		"INSERT INTO leagues (id, name) VALUES (39, 'Premier League'), (40, 'Premier League');",
		"INSERT INTO teams (id, name) VALUES (101, 'Arsenal'), (202, 'Chelsea'), (303, 'Arsenal'), (404, 'Chelsea'), (999, 'Liverpool');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	repo := NewCatalogRepository(db)
	kickoff := time.Date(2026, 9, 15, 20, 0, 0, 0, time.UTC)
	firstID, err := repo.InsertMatch(context.Background(), MatchRecord{
		LeagueID: 39, SeasonID: 2026, HomeTeamID: 101, AwayTeamID: 202, KickoffAt: kickoff, Status: "upcoming",
	})
	if err != nil {
		t.Fatalf("InsertMatch returned error: %v", err)
	}

	later := MatchRecord{LeagueID: 39, SeasonID: 2026, HomeTeamID: 101, AwayTeamID: 202, KickoffAt: kickoff.Add(30 * time.Minute), Status: "upcoming"}
	if id, found, err := repo.FindSuspectedDuplicate(context.Background(), later, time.Hour); err != nil || !found || id <= 0 {
		t.Fatalf("FindSuspectedDuplicate = (%d, %v, %v), want found", id, found, err)
	}

	if id, found, err := repo.FindExactCanonicalFixture(context.Background(), "Premier League", "Arsenal FC", "Chelsea", kickoff, 0); err != nil || !found || id <= 0 {
		t.Fatalf("FindExactCanonicalFixture = (%d, %v, %v), want normalized exact fixture", id, found, err)
	}
	if _, found, err := repo.FindExactCanonicalFixture(context.Background(), "Premier League", "Chelsea", "Arsenal", kickoff, 0); err != nil || found {
		t.Fatalf("FindExactCanonicalFixture (swapped) = (%v, %v), want no match", found, err)
	}
	if _, found, err := repo.FindExactCanonicalFixture(context.Background(), "Premier League", "Arsenal", "Chelsea", kickoff.Add(time.Second), 0); err != nil || found {
		t.Fatalf("FindExactCanonicalFixture (changed kickoff) = (%v, %v), want no match", found, err)
	}
	secondID, err := repo.InsertMatch(context.Background(), MatchRecord{LeagueID: 40, SeasonID: 2026, HomeTeamID: 303, AwayTeamID: 404, KickoffAt: kickoff, Status: "upcoming"})
	if err != nil {
		t.Fatalf("insert ambiguous duplicate: %v", err)
	}
	if _, _, err := repo.FindExactCanonicalFixture(context.Background(), "Premier League", "Arsenal", "Chelsea", kickoff, 0); !errors.Is(err, ErrAmbiguousExactCanonicalFixture) {
		t.Fatalf("FindExactCanonicalFixture error = %v, want reviewed identity ambiguity", err)
	}
	if _, err := db.ExecContext(context.Background(), "UPDATE matches SET status = 'cancelled' WHERE id = ?", secondID); err != nil {
		t.Fatalf("cancel duplicate: %v", err)
	}
	if _, found, err := repo.FindExactCanonicalFixture(context.Background(), "Premier League", "Arsenal", "Chelsea", kickoff, 0); err != nil || !found {
		t.Fatalf("FindExactCanonicalFixture with cancelled candidate = (%v, %v), want active candidate", found, err)
	}
	if _, err := db.ExecContext(context.Background(), "UPDATE matches SET status = 'upcoming' WHERE id = ?", secondID); err != nil {
		t.Fatalf("restore active canonical candidate: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), "INSERT INTO fixture_identity_aliases (old_id, canonical_id) VALUES (?, ?)", firstID, secondID); err != nil {
		t.Fatalf("record reviewed alias: %v", err)
	}
	if id, found, err := repo.FindExactCanonicalFixture(context.Background(), "Premier League", "Arsenal", "Chelsea", kickoff, 0); err != nil || !found || id != secondID {
		t.Fatalf("FindExactCanonicalFixture after alias = (%d, %v, %v), want canonical %d", id, found, err, secondID)
	}
	if id, found, err := repo.FindSuspectedDuplicate(context.Background(), later, time.Hour); err != nil || found {
		t.Fatalf("FindSuspectedDuplicate after alias = (%d, %v, %v), want no retired candidate", id, found, err)
	}

	far := MatchRecord{LeagueID: 39, SeasonID: 2026, HomeTeamID: 101, AwayTeamID: 202, KickoffAt: kickoff.Add(4 * time.Hour), Status: "upcoming"}
	if _, found, err := repo.FindSuspectedDuplicate(context.Background(), far, time.Hour); err != nil || found {
		t.Fatalf("FindSuspectedDuplicate (far) = (%v, %v), want not found", found, err)
	}

	other := MatchRecord{LeagueID: 39, SeasonID: 2026, HomeTeamID: 101, AwayTeamID: 999, KickoffAt: kickoff, Status: "upcoming"}
	if _, found, err := repo.FindSuspectedDuplicate(context.Background(), other, time.Hour); err != nil || found {
		t.Fatalf("FindSuspectedDuplicate (other) = (%v, %v), want not found", found, err)
	}

	if _, _, err := (CatalogRepository{}).FindSuspectedDuplicate(context.Background(), later, time.Hour); err == nil {
		t.Fatal("FindSuspectedDuplicate with nil db returned nil error")
	}
	if _, _, err := repo.FindSuspectedDuplicate(context.Background(), later, 0); err == nil {
		t.Fatal("FindSuspectedDuplicate with zero window returned nil error")
	}
	if _, _, err := (CatalogRepository{}).FindExactCanonicalFixture(context.Background(), "Premier League", "Arsenal", "Chelsea", kickoff, 0); err == nil {
		t.Fatal("FindExactCanonicalFixture with nil db returned nil error")
	}
}

func intPtr(value int) *int {
	return &value
}
