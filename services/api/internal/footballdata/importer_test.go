package footballdata

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestImportSeasonPopulatesCanonicalStatisticsAndIsIdempotent(t *testing.T) {
	db := openImportTestDB(t)
	createImportTestSchema(t, db)

	root := t.TempDir()
	csvDir := filepath.Join(root, "raw", "main", "England", "2025-2026")
	if err := os.MkdirAll(csvDir, 0o755); err != nil {
		t.Fatalf("mkdir csv dir: %v", err)
	}
	csvPath := filepath.Join(csvDir, "E0.csv")
	body := "\ufeffDiv,Date,Time,HomeTeam,AwayTeam,FTHG,FTAG,FTR,HTHG,HTAG,HTR,Referee,HS,AS,HST,AST,HF,AF,HC,AC,HY,AY,HR,AR,PSCH,PSCD,PSCA\n" +
		"E0,15/08/2025,20:00,Liverpool,Bournemouth,4,2,H,1,0,H,A Taylor,19,10,10,3,7,10,6,7,1,2,0,0,1.29,6.55,9.75\n"
	if err := os.WriteFile(csvPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	otherSeasonDir := filepath.Join(root, "raw", "main", "England", "2024-2025")
	if err := os.MkdirAll(otherSeasonDir, 0o755); err != nil {
		t.Fatalf("mkdir other season csv dir: %v", err)
	}
	otherSeasonPath := filepath.Join(otherSeasonDir, "E0.csv")
	otherSeasonBody := "Div,Date,Time,HomeTeam,AwayTeam,FTHG,FTAG,FTR\n" +
		"E0,16/08/2024,20:00,Arsenal,Wolves,2,0,H\n"
	if err := os.WriteFile(otherSeasonPath, []byte(otherSeasonBody), 0o644); err != nil {
		t.Fatalf("write other season csv: %v", err)
	}

	first, err := Import(context.Background(), db, Options{Path: root, Season: "2025-2026"})
	if err != nil {
		t.Fatalf("first import returned error: %v", err)
	}
	second, err := Import(context.Background(), db, Options{Path: root, Season: "2025-2026"})
	if err != nil {
		t.Fatalf("second import returned error: %v", err)
	}

	if first.ImportedMatches != 1 {
		t.Fatalf("first imported matches = %d, want 1", first.ImportedMatches)
	}
	if first.ParsedFiles != 1 {
		t.Fatalf("first parsed files = %d, want 1", first.ParsedFiles)
	}
	if second.ImportedMatches != 1 {
		t.Fatalf("second imported matches = %d, want 1", second.ImportedMatches)
	}
	assertCount(t, db, "matches", 1)
	assertCount(t, db, "fixtures", 1)
	assertCount(t, db, "fixture_statistics", 2)
	assertCount(t, db, "odds", 3)
	assertCount(t, db, "provider_entity_maps", 1)

	var referee string
	var homeGoalsFT, awayGoalsFT, homeGoalsHT, awayGoalsHT int
	if err := db.QueryRow(`
SELECT referee, home_goals_ft, away_goals_ft, home_goals_ht, away_goals_ht
FROM fixtures
LIMIT 1`).Scan(&referee, &homeGoalsFT, &awayGoalsFT, &homeGoalsHT, &awayGoalsHT); err != nil {
		t.Fatalf("query fixture: %v", err)
	}
	if referee != "A Taylor" || homeGoalsFT != 4 || awayGoalsFT != 2 || homeGoalsHT != 1 || awayGoalsHT != 0 {
		t.Fatalf("fixture = %q %d-%d ht %d-%d, want A Taylor 4-2 ht 1-0", referee, homeGoalsFT, awayGoalsFT, homeGoalsHT, awayGoalsHT)
	}

	var shotsTotal, shotsOnTarget, corners, fouls, yellowCards, redCards, cardPoints int
	if err := db.QueryRow(`
SELECT fs.shots_total, fs.shots_on_target, fs.corners, fs.fouls, fs.yellow_cards, fs.red_cards, fs.card_points
FROM fixture_statistics fs
JOIN teams t ON t.id = fs.team_id
WHERE t.name = 'Liverpool'`).Scan(&shotsTotal, &shotsOnTarget, &corners, &fouls, &yellowCards, &redCards, &cardPoints); err != nil {
		t.Fatalf("query home stats: %v", err)
	}
	if shotsTotal != 19 || shotsOnTarget != 10 || corners != 6 || fouls != 7 || yellowCards != 1 || redCards != 0 || cardPoints != 1 {
		t.Fatalf("home stats = shots %d/%d corners %d fouls %d cards %d/%d points %d", shotsTotal, shotsOnTarget, corners, fouls, yellowCards, redCards, cardPoints)
	}

	var avgYellow, avgRed, avgFouls float64
	if err := db.QueryRow(`
SELECT avg_yellow_cards, avg_red_cards, avg_fouls
FROM referee_stats
WHERE name = 'A Taylor'`).Scan(&avgYellow, &avgRed, &avgFouls); err != nil {
		t.Fatalf("query referee stats: %v", err)
	}
	if avgYellow != 3 || avgRed != 0 || avgFouls != 17 {
		t.Fatalf("referee averages = %.2f %.2f %.2f, want 3 0 17", avgYellow, avgRed, avgFouls)
	}

	var hasStatistics, hasOdds bool
	if err := db.QueryRow(`
SELECT has_statistics, has_odds
FROM league_coverage
LIMIT 1`).Scan(&hasStatistics, &hasOdds); err != nil {
		t.Fatalf("query coverage: %v", err)
	}
	if !hasStatistics || !hasOdds {
		t.Fatalf("coverage has_statistics=%v has_odds=%v, want true true", hasStatistics, hasOdds)
	}
}

func TestImportReusesExistingLeagueAndTeamsByCountryAndName(t *testing.T) {
	db := openImportTestDB(t)
	createImportTestSchema(t, db)

	if _, err := db.ExecContext(context.Background(), `
INSERT INTO sports (id, slug, name) VALUES (10, 'football', 'Football');
INSERT INTO leagues (id, sport_id, country_code, slug, name) VALUES (1183, 10, 'NO', 'league-103-eliteserien', 'Eliteserien');
INSERT INTO teams (id, country_code, slug, name) VALUES
  (13819, NULL, 'team-2149-fredrikstad', 'Fredrikstad'),
  (13821, NULL, 'team-321-lillestrom', 'Lillestrom');`); err != nil {
		t.Fatalf("seed existing entities: %v", err)
	}

	root := t.TempDir()
	csvPath := filepath.Join(root, "NOR.csv")
	body := "Country,League,Season,Date,Time,Home,Away,HG,AG,Res,PSCH,PSCD,PSCA\n" +
		"Norway,Eliteserien,2026,20/05/2026,17:00,Fredrikstad,Lillestrom,2,1,H,2.10,3.40,3.60\n"
	if err := os.WriteFile(csvPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	report, err := Import(context.Background(), db, Options{Path: csvPath, Season: "2026"})
	if err != nil {
		t.Fatalf("import returned error: %v", err)
	}

	if report.ImportedMatches != 1 {
		t.Fatalf("imported matches = %d, want 1", report.ImportedMatches)
	}

	var leagueID, homeTeamID, awayTeamID int64
	if err := db.QueryRow(`
SELECT league_id, home_team_id, away_team_id
FROM fixtures
WHERE source = 'football_data_import'
LIMIT 1`).Scan(&leagueID, &homeTeamID, &awayTeamID); err != nil {
		t.Fatalf("query imported fixture: %v", err)
	}
	if leagueID != 1183 || homeTeamID != 13819 || awayTeamID != 13821 {
		t.Fatalf("fixture mapped to league/team IDs %d/%d/%d, want 1183/13819/13821", leagueID, homeTeamID, awayTeamID)
	}
}

func assertCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func openImportTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func createImportTestSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`CREATE TABLE providers (provider TEXT PRIMARY KEY, display_name TEXT, base_url TEXT, enabled BOOLEAN DEFAULT true, created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE countries (code TEXT PRIMARY KEY, name TEXT, commercial_focus BOOLEAN DEFAULT false, created_at TEXT DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE sports (id INTEGER PRIMARY KEY AUTOINCREMENT, slug TEXT UNIQUE, name TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE leagues (id INTEGER PRIMARY KEY AUTOINCREMENT, sport_id INTEGER, country_code TEXT, slug TEXT UNIQUE, name TEXT, priority_score INTEGER DEFAULT 0, created_at TEXT DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE teams (id INTEGER PRIMARY KEY AUTOINCREMENT, country_code TEXT, slug TEXT UNIQUE, name TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE matches (id INTEGER PRIMARY KEY AUTOINCREMENT, league_id INTEGER, home_team_id INTEGER, away_team_id INTEGER, kickoff_at TEXT, status TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP, home_score INTEGER, away_score INTEGER, half_time_home_score INTEGER, half_time_away_score INTEGER, provider_status_raw TEXT, provider_status_label TEXT, status_elapsed INTEGER, venue_name TEXT, venue_city TEXT);`,
		`CREATE TABLE fixtures (id INTEGER PRIMARY KEY, league_id INTEGER, season_id INTEGER, home_team_id INTEGER, away_team_id INTEGER, kickoff_at TEXT, status TEXT, venue_name TEXT, referee TEXT, round TEXT, home_goals_ft INTEGER, away_goals_ft INTEGER, home_goals_ht INTEGER, away_goals_ht INTEGER, source_match_id INTEGER, source TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE fixture_statistics (fixture_id INTEGER, team_id INTEGER, shots_on_target INTEGER, shots_total INTEGER, shots_blocked INTEGER, shots_inside_box INTEGER, shots_outside_box INTEGER, possession_pct REAL, corners INTEGER, fouls INTEGER, offsides INTEGER, yellow_cards INTEGER, red_cards INTEGER, goalkeeper_saves INTEGER, passes_total INTEGER, passes_accurate INTEGER, pass_accuracy REAL, xg REAL, card_points INTEGER, source TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (fixture_id, team_id));`,
		`CREATE TABLE odds (id INTEGER PRIMARY KEY AUTOINCREMENT, fixture_id INTEGER, bookmaker TEXT, market TEXT, outcome TEXT, value REAL, implied_prob REAL, fetched_at TEXT, source_snapshot_id INTEGER, created_at TEXT DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE team_season_stats (team_id INTEGER, league_id INTEGER, season_id INTEGER, played_home INTEGER DEFAULT 0, wins_home INTEGER DEFAULT 0, draws_home INTEGER DEFAULT 0, losses_home INTEGER DEFAULT 0, goals_for_home INTEGER DEFAULT 0, goals_against_home INTEGER DEFAULT 0, clean_sheets_home INTEGER DEFAULT 0, failed_to_score_home INTEGER DEFAULT 0, played_away INTEGER DEFAULT 0, wins_away INTEGER DEFAULT 0, draws_away INTEGER DEFAULT 0, losses_away INTEGER DEFAULT 0, goals_for_away INTEGER DEFAULT 0, goals_against_away INTEGER DEFAULT 0, clean_sheets_away INTEGER DEFAULT 0, failed_to_score_away INTEGER DEFAULT 0, source TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP, updated_at TEXT DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (team_id, league_id, season_id));`,
		`CREATE TABLE head_to_head (id INTEGER PRIMARY KEY AUTOINCREMENT, home_team_id INTEGER, away_team_id INTEGER, fixture_id INTEGER UNIQUE, played_at TEXT, home_goals INTEGER, away_goals INTEGER, venue TEXT, source TEXT, created_at TEXT DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE standings (league_id INTEGER, season_id INTEGER, team_id INTEGER, rank INTEGER, points INTEGER, played INTEGER, wins INTEGER, draws INTEGER, losses INTEGER, goals_for INTEGER, goals_against INTEGER, goal_diff INTEGER, form TEXT, source TEXT, updated_at TEXT DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (league_id, season_id, team_id));`,
		`CREATE TABLE referee_stats (name TEXT PRIMARY KEY, matches_officiated INTEGER, avg_yellow_cards REAL, avg_red_cards REAL, avg_fouls REAL, source TEXT, updated_at TEXT DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE league_coverage (league_id INTEGER, season_id INTEGER, has_statistics BOOLEAN, has_xg BOOLEAN, has_injuries BOOLEAN, has_odds BOOLEAN, has_lineups BOOLEAN, source TEXT, updated_at TEXT DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (league_id, season_id));`,
		`CREATE TABLE provider_entity_maps (id INTEGER PRIMARY KEY AUTOINCREMENT, provider TEXT, entity_type TEXT, provider_entity_id TEXT, provider_entity_key TEXT, canonical_entity_type TEXT, canonical_entity_id TEXT, match_status TEXT, confidence REAL, source_payload_id INTEGER, metadata TEXT DEFAULT '{}', first_seen_at TEXT DEFAULT CURRENT_TIMESTAMP, last_seen_at TEXT DEFAULT CURRENT_TIMESTAMP, retired_at TEXT);`,
		`CREATE UNIQUE INDEX idx_provider_entity_maps_active_external_id ON provider_entity_maps (provider, entity_type, provider_entity_id) WHERE retired_at IS NULL;`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("exec schema statement %q: %v", statement, err)
		}
	}
}
