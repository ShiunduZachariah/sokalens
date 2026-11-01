package matches

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSourceListMatchesFiltersByDateTimezoneAndStatus(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE leagues (id INTEGER PRIMARY KEY, name TEXT NOT NULL, country_code TEXT, slug TEXT, priority_score INTEGER NOT NULL DEFAULT 0);",
		"CREATE TABLE teams (id INTEGER PRIMARY KEY, name TEXT NOT NULL, logo_url TEXT, slug TEXT);",
		"CREATE TABLE matches (id INTEGER PRIMARY KEY, league_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TEXT NOT NULL, status TEXT NOT NULL, provider_status_raw TEXT, provider_status_label TEXT, status_elapsed INTEGER, venue_name TEXT, venue_city TEXT, home_score INTEGER, away_score INTEGER, half_time_home_score INTEGER, half_time_away_score INTEGER, live_state_at TEXT);",
		"CREATE TABLE fixtures (id INTEGER PRIMARY KEY, finished_at TEXT);",
		"CREATE TABLE fixture_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL, revoked_at TEXT);",
		"CREATE TABLE odds_markets (id INTEGER PRIMARY KEY, market_key TEXT NOT NULL);",
		"CREATE TABLE odds_snapshots (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, provider TEXT NOT NULL, payload TEXT NOT NULL, captured_at TEXT NOT NULL);",
		"CREATE TABLE engine_predictions (id TEXT PRIMARY KEY, public_id INTEGER NOT NULL UNIQUE, fixture_id INTEGER NOT NULL, market TEXT NOT NULL, prediction TEXT NOT NULL, probability REAL NOT NULL, confidence REAL NOT NULL, generated_at TEXT NOT NULL, superseded_at TEXT, gate_tier TEXT NOT NULL DEFAULT 'FULL', horizon_eligible INTEGER NOT NULL DEFAULT 1, embargo_active INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT '2026-05-14T10:05:00Z', last_evaluated_at TEXT NOT NULL DEFAULT '2026-05-14T10:04:00Z');",
		"CREATE TABLE prediction_results (prediction_id TEXT PRIMARY KEY, settlement_status TEXT NOT NULL, is_correct INTEGER, actual TEXT, void_reason TEXT);",
		"CREATE TABLE effective_model_evaluation_facts (prediction_id TEXT PRIMARY KEY, final_outcome TEXT, settlement_classification TEXT, exclusion_reason TEXT);",
		"CREATE TABLE prediction_tracker_state (prediction_id TEXT PRIMARY KEY, state TEXT NOT NULL, home_goals INTEGER, away_goals INTEGER, match_minute INTEGER, updated_at TEXT);",
		"CREATE TABLE api_predictions (fixture_id INTEGER, predicted_winner TEXT, under_over TEXT, percent_home REAL, percent_draw REAL, percent_away REAL, captured_at TEXT NOT NULL);",
		"CREATE TABLE data_backfill_requests (target_fixture_id INTEGER, feature_group TEXT, status TEXT);",
		"CREATE TABLE prediction_feature_snapshots (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, model_id INTEGER NOT NULL, feature_key TEXT NOT NULL, snapshot_hash TEXT NOT NULL, payload TEXT NOT NULL, created_at TEXT NOT NULL);",
		"CREATE TABLE prediction_generation_failures (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, model_id INTEGER NOT NULL, reason_code TEXT NOT NULL, detail TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);",
		"INSERT INTO leagues (id, name, country_code) VALUES (1, 'Premier League', 'GB');",
		"INSERT INTO teams (id, name) VALUES (10, 'Arsenal'), (11, 'Chelsea'), (12, 'Liverpool'), (13, 'Everton');",
		"INSERT INTO odds_markets (id, market_key) VALUES (1, '1x2');",
		"INSERT INTO matches (id, league_id, home_team_id, away_team_id, kickoff_at, status, provider_status_raw, provider_status_label, status_elapsed, venue_name, venue_city, home_score, away_score, half_time_home_score, half_time_away_score) VALUES " +
			"(100, 1, 10, 11, '2026-05-13T18:00:00Z', 'halftime', 'HT', 'Halftime', 45, 'Emirates Stadium', 'London', 1, 0, 1, 0)," +
			"(101, 1, 12, 13, '2026-05-12T21:00:00Z', 'finished', 'FT', 'Match Finished', 90, 'Anfield', 'Liverpool', 2, 1, 1, 1)," +
			"(102, 1, 10, 12, '2026-05-14T16:00:00Z', 'upcoming', 'NS', 'Not Started', NULL, 'Emirates Stadium', 'London', NULL, NULL, NULL, NULL)," +
			"(104, 1, 10, 11, '2026-05-11T21:00:00Z', 'finished', 'FT', 'Match Finished', 90, 'Emirates Stadium', 'London', 1, 1, 0, 1);",
		"INSERT INTO odds_snapshots (id, match_id, market_id, provider, payload, captured_at) VALUES (500, 100, 1, 'api-football', '{\"prices\":[{\"label\":\"Home\",\"value\":\"1.80\",\"trend\":\"flat\"},{\"label\":\"Draw\",\"value\":\"3.40\",\"trend\":\"flat\"},{\"label\":\"Away\",\"value\":\"4.40\",\"trend\":\"flat\"}]}', '2026-05-13T17:00:00Z');",
		"INSERT INTO prediction_feature_snapshots (id, match_id, market_id, model_id, feature_key, snapshot_hash, payload, created_at) VALUES (700, 100, 1, 1, 'odds_signal', 'hash-100', '{}', '2026-05-13T17:01:00Z');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, generated_at, superseded_at) VALUES ('uuid-900', 900, 100, '1x2', 'home', 0.63, 0.635, '2026-05-13T17:02:00Z', NULL);",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	source := NewSource(db)
	source.now = func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }
	query := Query{
		Date:     "2026-05-13",
		Timezone: "Africa/Nairobi",
		Statuses: []string{"halftime"},
		Limit:    20,
	}

	matches, err := source.ListMatches(context.Background(), query)
	if err != nil {
		t.Fatalf("ListMatches returned error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	match := matches[0]
	if match.ID != 100 || match.Status != "halftime" || match.RawStatus != "HT" {
		t.Fatalf("match = %+v, want halftime fixture 100", match)
	}
	if match.Country != "GB" {
		t.Fatalf("match country = %q, want GB", match.Country)
	}
	if match.PredictionState != "published" || match.Prediction == nil {
		t.Fatalf("prediction state = %q prediction = %+v, want published prediction", match.PredictionState, match.Prediction)
	}
	if len(match.Odds) != 3 {
		t.Fatalf("odds = %+v, want 3 prices", match.Odds)
	}

	previousDay, err := source.ListMatches(context.Background(), Query{Date: "2026-05-13", Timezone: "Africa/Nairobi", Statuses: []string{"finished"}, IncludeUnavailable: true, ResultDetailWindow: 24 * time.Hour})
	if err != nil || len(previousDay) != 1 || previousDay[0].ID != 101 {
		t.Fatalf("previous-day results = %+v, %v", previousDay, err)
	}
	oldWithoutCutoff, err := source.ListMatches(context.Background(), Query{Date: "2026-05-12", Timezone: "Africa/Nairobi", Statuses: []string{"finished"}, IncludeUnavailable: true})
	if err != nil || len(oldWithoutCutoff) != 1 || oldWithoutCutoff[0].ID != 104 {
		t.Fatalf("unrestricted old results = %+v, %v", oldWithoutCutoff, err)
	}
	oldWithCutoff, err := source.ListMatches(context.Background(), Query{Date: "2026-05-12", Timezone: "Africa/Nairobi", Statuses: []string{"finished"}, IncludeUnavailable: true, ResultDetailWindow: 24 * time.Hour})
	if err != nil || len(oldWithCutoff) != 0 {
		t.Fatalf("cut-off old results = %+v, %v", oldWithCutoff, err)
	}
	// Any canonical fixture — including old settled matches — must resolve so
	// league/team "recent results" links never 404.
	if _, err := source.GetMatch(context.Background(), 104); err != nil {
		t.Fatalf("old settled match detail should resolve: %v", err)
	}
	if _, err := source.GetMatch(context.Background(), 101); err != nil {
		t.Fatalf("previous-day match detail error = %v", err)
	}
	// A reviewed identity alias removes the retired identity from public lists
	// without moving its original match or prediction rows. A direct lookup
	// instead resolves to the canonical public fixture.
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO matches (id, league_id, home_team_id, away_team_id, kickoff_at, status, home_score, away_score)
SELECT 105, league_id, home_team_id, away_team_id, kickoff_at, status, home_score, away_score
FROM matches WHERE id = 104`); err != nil {
		t.Fatalf("insert canonical fixture: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), "INSERT INTO fixture_identity_aliases (old_id, canonical_id) VALUES (104, 105)"); err != nil {
		t.Fatalf("insert reviewed alias: %v", err)
	}
	listed, err := source.ListMatches(context.Background(), Query{Date: "2026-05-12", Timezone: "Africa/Nairobi", Statuses: []string{"finished"}, IncludeUnavailable: true})
	if err != nil || len(listed) != 1 || listed[0].ID != 105 {
		t.Fatalf("aliased match list = %+v, %v, want only canonical 105", listed, err)
	}
	count, err := source.CountMatches(context.Background(), Query{Date: "2026-05-12", Timezone: "Africa/Nairobi", Statuses: []string{"finished"}, IncludeUnavailable: true})
	if err != nil || count != 1 {
		t.Fatalf("aliased match count = %d, %v, want 1", count, err)
	}
	resolved, err := source.GetMatch(context.Background(), 104)
	if err != nil || resolved.ID != 105 {
		t.Fatalf("old URL lookup = %+v, %v, want canonical 105", resolved, err)
	}
	if _, err := db.ExecContext(context.Background(), "UPDATE fixture_identity_aliases SET revoked_at = '2026-05-15T00:00:00Z' WHERE old_id = 104"); err != nil {
		t.Fatalf("revoke alias: %v", err)
	}
	restored, err := source.GetMatch(context.Background(), 104)
	if err != nil || restored.ID != 104 {
		t.Fatalf("revoked alias lookup = %+v, %v, want original 104", restored, err)
	}
}

func TestSourceListMatchesAppliesLimitAfterSelectedDayRange(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE leagues (id INTEGER PRIMARY KEY, name TEXT NOT NULL, country_code TEXT, slug TEXT, priority_score INTEGER NOT NULL DEFAULT 0);",
		"CREATE TABLE teams (id INTEGER PRIMARY KEY, name TEXT NOT NULL, logo_url TEXT, slug TEXT);",
		"CREATE TABLE matches (id INTEGER PRIMARY KEY, league_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TEXT NOT NULL, status TEXT NOT NULL, provider_status_raw TEXT, provider_status_label TEXT, status_elapsed INTEGER, venue_name TEXT, venue_city TEXT, home_score INTEGER, away_score INTEGER, half_time_home_score INTEGER, half_time_away_score INTEGER, live_state_at TEXT);",
		"CREATE TABLE fixtures (id INTEGER PRIMARY KEY, finished_at TEXT);",
		"CREATE TABLE fixture_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL, revoked_at TEXT);",
		"CREATE TABLE odds_markets (id INTEGER PRIMARY KEY, market_key TEXT NOT NULL);",
		"CREATE TABLE odds_snapshots (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, provider TEXT NOT NULL, payload TEXT NOT NULL, captured_at TEXT NOT NULL);",
		"CREATE TABLE engine_predictions (id TEXT PRIMARY KEY, public_id INTEGER NOT NULL UNIQUE, fixture_id INTEGER NOT NULL, market TEXT NOT NULL, prediction TEXT NOT NULL, probability REAL NOT NULL, confidence REAL NOT NULL, generated_at TEXT NOT NULL, superseded_at TEXT, gate_tier TEXT NOT NULL DEFAULT 'FULL', horizon_eligible INTEGER NOT NULL DEFAULT 1, embargo_active INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT '2026-05-14T10:05:00Z', last_evaluated_at TEXT NOT NULL DEFAULT '2026-05-14T10:04:00Z');",
		"CREATE TABLE prediction_tracker_state (prediction_id TEXT PRIMARY KEY, state TEXT NOT NULL, home_goals INTEGER, away_goals INTEGER, match_minute INTEGER, updated_at TEXT);",
		"CREATE TABLE prediction_feature_snapshots (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, model_id INTEGER NOT NULL, feature_key TEXT NOT NULL, snapshot_hash TEXT NOT NULL, payload TEXT NOT NULL, created_at TEXT NOT NULL);",
		"CREATE TABLE prediction_generation_failures (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, model_id INTEGER NOT NULL, reason_code TEXT NOT NULL, detail TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);",
		"INSERT INTO leagues (id, name) VALUES (1, 'Premier League');",
		"INSERT INTO teams (id, name) VALUES (10, 'Arsenal'), (11, 'Chelsea'), (12, 'Liverpool'), (13, 'Everton');",
		"INSERT INTO matches (id, league_id, home_team_id, away_team_id, kickoff_at, status, provider_status_raw, provider_status_label) VALUES " +
			"(100, 1, 10, 11, '2026-05-13T18:00:00Z', 'finished', 'FT', 'Match Finished')," +
			"(101, 1, 12, 13, '2026-05-14T18:00:00Z', 'upcoming', 'NS', 'Not Started');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	matches, err := NewSource(db).ListMatches(context.Background(), Query{
		Date:               "2026-05-14",
		Timezone:           "Africa/Nairobi",
		Limit:              1,
		IncludeUnavailable: true,
	})
	if err != nil {
		t.Fatalf("ListMatches returned error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	if matches[0].ID != 101 {
		t.Fatalf("match id = %d, want 101", matches[0].ID)
	}
}

func TestSourceListMatchesAppliesLimitAfterStatusFiltering(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE leagues (id INTEGER PRIMARY KEY, name TEXT NOT NULL, country_code TEXT, slug TEXT, priority_score INTEGER NOT NULL DEFAULT 0);",
		"CREATE TABLE teams (id INTEGER PRIMARY KEY, name TEXT NOT NULL, logo_url TEXT, slug TEXT);",
		"CREATE TABLE matches (id INTEGER PRIMARY KEY, league_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TEXT NOT NULL, status TEXT NOT NULL, provider_status_raw TEXT, provider_status_label TEXT, status_elapsed INTEGER, venue_name TEXT, venue_city TEXT, home_score INTEGER, away_score INTEGER, half_time_home_score INTEGER, half_time_away_score INTEGER, live_state_at TEXT);",
		"CREATE TABLE fixtures (id INTEGER PRIMARY KEY, finished_at TEXT);",
		"CREATE TABLE fixture_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL, revoked_at TEXT);",
		"CREATE TABLE odds_markets (id INTEGER PRIMARY KEY, market_key TEXT NOT NULL);",
		"CREATE TABLE odds_snapshots (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, provider TEXT NOT NULL, payload TEXT NOT NULL, captured_at TEXT NOT NULL);",
		"CREATE TABLE engine_predictions (id TEXT PRIMARY KEY, public_id INTEGER NOT NULL UNIQUE, fixture_id INTEGER NOT NULL, market TEXT NOT NULL, prediction TEXT NOT NULL, probability REAL NOT NULL, confidence REAL NOT NULL, generated_at TEXT NOT NULL, superseded_at TEXT, gate_tier TEXT NOT NULL DEFAULT 'FULL', horizon_eligible INTEGER NOT NULL DEFAULT 1, embargo_active INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT '2026-05-14T10:05:00Z', last_evaluated_at TEXT NOT NULL DEFAULT '2026-05-14T10:04:00Z');",
		"CREATE TABLE prediction_tracker_state (prediction_id TEXT PRIMARY KEY, state TEXT NOT NULL, home_goals INTEGER, away_goals INTEGER, match_minute INTEGER, updated_at TEXT);",
		"CREATE TABLE prediction_feature_snapshots (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, model_id INTEGER NOT NULL, feature_key TEXT NOT NULL, snapshot_hash TEXT NOT NULL, payload TEXT NOT NULL, created_at TEXT NOT NULL);",
		"CREATE TABLE prediction_generation_failures (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, model_id INTEGER NOT NULL, reason_code TEXT NOT NULL, detail TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);",
		"INSERT INTO leagues (id, name) VALUES (1, 'Premier League');",
		"INSERT INTO teams (id, name) VALUES (10, 'Arsenal'), (11, 'Chelsea'), (12, 'Liverpool'), (13, 'Everton');",
		"INSERT INTO matches (id, league_id, home_team_id, away_team_id, kickoff_at, status, provider_status_raw, provider_status_label) VALUES " +
			"(100, 1, 10, 11, '2026-05-13T21:00:00Z', 'finished', 'FT', 'Match Finished')," +
			"(101, 1, 12, 13, '2026-05-14T18:00:00Z', 'upcoming', 'NS', 'Not Started');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	matches, err := NewSource(db).ListMatches(context.Background(), Query{
		Date:               "2026-05-14",
		Timezone:           "Africa/Nairobi",
		Statuses:           []string{"upcoming"},
		Limit:              1,
		IncludeUnavailable: true,
	})
	if err != nil {
		t.Fatalf("ListMatches returned error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	if matches[0].ID != 101 {
		t.Fatalf("match id = %d, want 101", matches[0].ID)
	}
}

func TestSourceListMatchesReturnsActionableRowsByDefault(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE leagues (id INTEGER PRIMARY KEY, name TEXT NOT NULL, country_code TEXT, slug TEXT, priority_score INTEGER NOT NULL DEFAULT 0);",
		"CREATE TABLE teams (id INTEGER PRIMARY KEY, name TEXT NOT NULL, logo_url TEXT, slug TEXT);",
		"CREATE TABLE matches (id INTEGER PRIMARY KEY, league_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TEXT NOT NULL, status TEXT NOT NULL, provider_status_raw TEXT, provider_status_label TEXT, status_elapsed INTEGER, venue_name TEXT, venue_city TEXT, home_score INTEGER, away_score INTEGER, half_time_home_score INTEGER, half_time_away_score INTEGER, live_state_at TEXT);",
		"CREATE TABLE fixtures (id INTEGER PRIMARY KEY, finished_at TEXT);",
		"CREATE TABLE fixture_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL, revoked_at TEXT);",
		"CREATE TABLE odds_markets (id INTEGER PRIMARY KEY, market_key TEXT NOT NULL);",
		"CREATE TABLE odds_snapshots (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, provider TEXT NOT NULL, payload TEXT NOT NULL, captured_at TEXT NOT NULL);",
		"CREATE TABLE engine_predictions (id TEXT PRIMARY KEY, public_id INTEGER NOT NULL UNIQUE, fixture_id INTEGER NOT NULL, market TEXT NOT NULL, prediction TEXT NOT NULL, probability REAL NOT NULL, confidence REAL NOT NULL, generated_at TEXT NOT NULL, superseded_at TEXT, gate_tier TEXT NOT NULL DEFAULT 'FULL', horizon_eligible INTEGER NOT NULL DEFAULT 1, embargo_active INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT '2026-05-14T10:05:00Z', last_evaluated_at TEXT NOT NULL DEFAULT '2026-05-14T10:04:00Z');",
		"CREATE TABLE prediction_tracker_state (prediction_id TEXT PRIMARY KEY, state TEXT NOT NULL, home_goals INTEGER, away_goals INTEGER, match_minute INTEGER, updated_at TEXT);",
		"CREATE TABLE prediction_generation_failures (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, model_id INTEGER NOT NULL, reason_code TEXT NOT NULL, detail TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);",
		"INSERT INTO leagues (id, name) VALUES (1, 'Premier League');",
		"INSERT INTO teams (id, name) VALUES (10, 'Arsenal'), (11, 'Chelsea'), (12, 'Liverpool'), (13, 'Everton'), (14, 'Spurs'), (15, 'Villa'), (16, 'Newcastle'), (17, 'Leeds'), (18, 'Brentford'), (19, 'Fulham');",
		"INSERT INTO odds_markets (id, market_key) VALUES (1, '1x2');",
		"INSERT INTO matches (id, league_id, home_team_id, away_team_id, kickoff_at, status, provider_status_raw, provider_status_label, venue_name, venue_city) VALUES " +
			"(200, 1, 10, 11, '2026-05-13T12:00:00Z', 'upcoming', 'NS', 'Not Started', 'Bridge', 'London')," +
			"(201, 1, 12, 13, '2026-05-13T15:00:00Z', 'upcoming', 'NS', 'Not Started', 'Anfield', 'Liverpool')," +
			"(202, 1, 14, 15, '2026-05-13T18:00:00Z', 'upcoming', 'NS', 'Not Started', 'Spurs Stadium', 'London')," +
			"(203, 1, 16, 17, '2026-05-13T20:00:00Z', 'upcoming', 'NS', 'Not Started', 'St James', 'Newcastle');",
		"INSERT INTO matches (id, league_id, home_team_id, away_team_id, kickoff_at, status, provider_status_raw, provider_status_label, home_score, away_score) VALUES " +
			"(300, 1, 16, 18, '2026-05-01T12:00:00Z', 'finished', 'FT', 'Match Finished', 1, 0)," +
			"(301, 1, 19, 16, '2026-05-02T12:00:00Z', 'finished', 'FT', 'Match Finished', 0, 2)," +
			"(302, 1, 16, 19, '2026-05-03T12:00:00Z', 'finished', 'FT', 'Match Finished', 2, 2)," +
			"(303, 1, 18, 16, '2026-05-04T12:00:00Z', 'finished', 'FT', 'Match Finished', 1, 3)," +
			"(304, 1, 16, 18, '2026-05-05T12:00:00Z', 'finished', 'FT', 'Match Finished', 2, 0)," +
			"(305, 1, 17, 18, '2026-05-01T15:00:00Z', 'finished', 'FT', 'Match Finished', 1, 1)," +
			"(306, 1, 19, 17, '2026-05-02T15:00:00Z', 'finished', 'FT', 'Match Finished', 0, 1)," +
			"(307, 1, 17, 19, '2026-05-03T15:00:00Z', 'finished', 'FT', 'Match Finished', 2, 1)," +
			"(308, 1, 18, 17, '2026-05-04T15:00:00Z', 'finished', 'FT', 'Match Finished', 1, 2)," +
			"(309, 1, 17, 18, '2026-05-05T15:00:00Z', 'finished', 'FT', 'Match Finished', 3, 1);",
		"INSERT INTO odds_snapshots (id, match_id, market_id, provider, payload, captured_at) VALUES (501, 201, 1, 'api-football', '{\"prices\":[{\"label\":\"Home\",\"value\":\"2.10\",\"trend\":\"flat\"},{\"label\":\"Draw\",\"value\":\"3.10\",\"trend\":\"flat\"},{\"label\":\"Away\",\"value\":\"3.60\",\"trend\":\"flat\"}]}', '2026-05-13T11:00:00Z');",
		"INSERT INTO prediction_generation_failures (id, match_id, market_id, model_id, reason_code, detail, created_at, updated_at) VALUES (801, 202, 1, 1, 'failed_generation', 'generation crashed', '2026-05-13T11:10:00Z', '2026-05-13T11:10:00Z');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	matches, err := NewSource(db).ListMatches(context.Background(), Query{
		Date:     "2026-05-13",
		Timezone: "UTC",
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("ListMatches returned error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	if matches[0].ID != 203 || matches[0].PredictionState != "pending" {
		t.Fatalf("match = %+v, want no-odds history-backed pending match 203", matches[0])
	}
	if len(matches[0].Odds) != 0 {
		t.Fatalf("match odds = %+v, want no odds", matches[0].Odds)
	}
}

func TestSourceListMatchesCanIncludeUnavailableRowsForQA(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE leagues (id INTEGER PRIMARY KEY, name TEXT NOT NULL, country_code TEXT, slug TEXT, priority_score INTEGER NOT NULL DEFAULT 0);",
		"CREATE TABLE teams (id INTEGER PRIMARY KEY, name TEXT NOT NULL, logo_url TEXT, slug TEXT);",
		"CREATE TABLE matches (id INTEGER PRIMARY KEY, league_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TEXT NOT NULL, status TEXT NOT NULL, provider_status_raw TEXT, provider_status_label TEXT, status_elapsed INTEGER, venue_name TEXT, venue_city TEXT, home_score INTEGER, away_score INTEGER, half_time_home_score INTEGER, half_time_away_score INTEGER, live_state_at TEXT);",
		"CREATE TABLE fixtures (id INTEGER PRIMARY KEY, finished_at TEXT);",
		"CREATE TABLE fixture_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL, revoked_at TEXT);",
		"CREATE TABLE odds_markets (id INTEGER PRIMARY KEY, market_key TEXT NOT NULL);",
		"CREATE TABLE odds_snapshots (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, provider TEXT NOT NULL, payload TEXT NOT NULL, captured_at TEXT NOT NULL);",
		"CREATE TABLE engine_predictions (id TEXT PRIMARY KEY, public_id INTEGER NOT NULL UNIQUE, fixture_id INTEGER NOT NULL, market TEXT NOT NULL, prediction TEXT NOT NULL, probability REAL NOT NULL, confidence REAL NOT NULL, generated_at TEXT NOT NULL, superseded_at TEXT, gate_tier TEXT NOT NULL DEFAULT 'FULL', horizon_eligible INTEGER NOT NULL DEFAULT 1, embargo_active INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT '2026-05-14T10:05:00Z', last_evaluated_at TEXT NOT NULL DEFAULT '2026-05-14T10:04:00Z');",
		"CREATE TABLE prediction_tracker_state (prediction_id TEXT PRIMARY KEY, state TEXT NOT NULL, home_goals INTEGER, away_goals INTEGER, match_minute INTEGER, updated_at TEXT);",
		"CREATE TABLE prediction_generation_failures (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, model_id INTEGER NOT NULL, reason_code TEXT NOT NULL, detail TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);",
		"INSERT INTO leagues (id, name) VALUES (1, 'Premier League');",
		"INSERT INTO teams (id, name) VALUES (10, 'Arsenal'), (11, 'Chelsea');",
		"INSERT INTO odds_markets (id, market_key) VALUES (1, '1x2');",
		"INSERT INTO matches (id, league_id, home_team_id, away_team_id, kickoff_at, status, provider_status_raw, provider_status_label) VALUES " +
			"(200, 1, 10, 11, '2026-05-13T12:00:00Z', 'upcoming', 'NS', 'Not Started');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	matches, err := NewSource(db).ListMatches(context.Background(), Query{
		Date:               "2026-05-13",
		Timezone:           "UTC",
		Limit:              10,
		IncludeUnavailable: true,
	})
	if err != nil {
		t.Fatalf("ListMatches returned error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	if matches[0].PredictionState != "insufficient_history" {
		t.Fatalf("prediction state = %q, want insufficient_history", matches[0].PredictionState)
	}
}

func TestSourceGetMatchReadsFixtureDetailByID(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE leagues (id INTEGER PRIMARY KEY, name TEXT NOT NULL, country_code TEXT, slug TEXT, priority_score INTEGER NOT NULL DEFAULT 0);",
		"CREATE TABLE teams (id INTEGER PRIMARY KEY, name TEXT NOT NULL, logo_url TEXT, slug TEXT);",
		"CREATE TABLE matches (id INTEGER PRIMARY KEY, league_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TEXT NOT NULL, status TEXT NOT NULL, provider_status_raw TEXT, provider_status_label TEXT, status_elapsed INTEGER, venue_name TEXT, venue_city TEXT, home_score INTEGER, away_score INTEGER, half_time_home_score INTEGER, half_time_away_score INTEGER, live_state_at TEXT);",
		"CREATE TABLE fixtures (id INTEGER PRIMARY KEY, finished_at TEXT);",
		"CREATE TABLE fixture_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL, revoked_at TEXT);",
		"CREATE TABLE odds_markets (id INTEGER PRIMARY KEY, market_key TEXT NOT NULL);",
		"CREATE TABLE odds_snapshots (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, provider TEXT NOT NULL, payload TEXT NOT NULL, captured_at TEXT NOT NULL);",
		"CREATE TABLE engine_predictions (id TEXT PRIMARY KEY, public_id INTEGER NOT NULL UNIQUE, fixture_id INTEGER NOT NULL, market TEXT NOT NULL, prediction TEXT NOT NULL, probability REAL NOT NULL, confidence REAL NOT NULL, generated_at TEXT NOT NULL, superseded_at TEXT, gate_tier TEXT NOT NULL DEFAULT 'FULL', horizon_eligible INTEGER NOT NULL DEFAULT 1, embargo_active INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT '2026-05-14T10:05:00Z', last_evaluated_at TEXT NOT NULL DEFAULT '2026-05-14T10:04:00Z');",
		"CREATE TABLE api_predictions (fixture_id INTEGER, predicted_winner TEXT, under_over TEXT, percent_home REAL, percent_draw REAL, percent_away REAL, captured_at TEXT NOT NULL);",
		"CREATE TABLE prediction_results (prediction_id TEXT PRIMARY KEY, settlement_status TEXT NOT NULL, is_correct INTEGER, actual TEXT, void_reason TEXT);",
		"CREATE TABLE effective_model_evaluation_facts (prediction_id TEXT PRIMARY KEY, final_outcome TEXT, settlement_classification TEXT, exclusion_reason TEXT);",
		"CREATE TABLE prediction_tracker_state (prediction_id TEXT PRIMARY KEY, state TEXT NOT NULL, home_goals INTEGER, away_goals INTEGER, match_minute INTEGER, updated_at TEXT);",
		"CREATE TABLE data_backfill_requests (target_fixture_id INTEGER, feature_group TEXT, status TEXT);",
		"CREATE TABLE prediction_feature_snapshots (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, model_id INTEGER NOT NULL, feature_key TEXT NOT NULL, snapshot_hash TEXT NOT NULL, payload TEXT NOT NULL, created_at TEXT NOT NULL);",
		"CREATE TABLE prediction_generation_failures (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, model_id INTEGER NOT NULL, reason_code TEXT NOT NULL, detail TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);",
		"INSERT INTO leagues (id, name, slug) VALUES (1, 'Premier League', 'league-england-premier-league');",
		"INSERT INTO teams (id, name) VALUES (10, 'Arsenal'), (11, 'Chelsea');",
		"INSERT INTO odds_markets (id, market_key) VALUES (1, '1x2');",
		"INSERT INTO matches (id, league_id, home_team_id, away_team_id, kickoff_at, status, provider_status_raw, provider_status_label, status_elapsed, venue_name, venue_city, home_score, away_score, half_time_home_score, half_time_away_score) VALUES " +
			"(100, 1, 10, 11, '2025-05-18T15:00:00Z', 'finished', 'FT', 'Match Finished', 90, 'Emirates Stadium', 'London', 2, 1, 1, 0);",
		"INSERT INTO odds_snapshots (id, match_id, market_id, provider, payload, captured_at) VALUES (500, 100, 1, 'api-football', '{\"prices\":[{\"label\":\"Home\",\"value\":\"1.80\",\"trend\":\"flat\"}]}', '2025-05-18T14:00:00Z');",
		"INSERT INTO prediction_feature_snapshots (id, match_id, market_id, model_id, feature_key, snapshot_hash, payload, created_at) VALUES (700, 100, 1, 1, 'odds_signal', 'hash-100', '{}', '2025-05-18T14:01:00Z');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, generated_at, superseded_at) VALUES ('uuid-900', 900, 100, '1x2', 'home', 0.63, 0.635, '2025-05-18T14:02:00Z', NULL);",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	match, err := NewSource(db).GetMatch(context.Background(), 100)
	if err != nil {
		t.Fatalf("GetMatch returned error: %v", err)
	}
	if match.ID != 100 || match.HomeTeam != "Arsenal" || match.AwayTeam != "Chelsea" || match.Status != "finished" {
		t.Fatalf("match = %+v, want Arsenal v Chelsea finished fixture", match)
	}
	if match.HomeScore == nil || *match.HomeScore != 2 || match.AwayScore == nil || *match.AwayScore != 1 {
		t.Fatalf("score = %v-%v, want 2-1", match.HomeScore, match.AwayScore)
	}
	if match.LeagueSlug != "league-england-premier-league" {
		t.Fatalf("league slug = %q, want league-england-premier-league", match.LeagueSlug)
	}
	if match.Prediction == nil || match.Prediction.ID != 900 {
		t.Fatalf("prediction = %+v, want current prediction 900", match.Prediction)
	}
	if _, err := db.ExecContext(context.Background(), "INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active) VALUES ('uuid-901', 901, 100, '1x2', 'away', 0.70, 0.75, '2025-05-18T14:03:00Z', NULL, 'FULL', 1, 1)"); err != nil {
		t.Fatal(err)
	}
	match, err = NewSource(db).GetMatch(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if match.Prediction != nil {
		t.Fatalf("embargoed canonical prediction leaked through stale fallback: %+v", match.Prediction)
	}
	if _, err := db.ExecContext(context.Background(), "INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active) VALUES ('uuid-902', 902, 100, '1x2', 'draw', 0.64, 0.69, '2025-05-18T14:04:00Z', NULL, 'FULL', 1, 0)"); err != nil {
		t.Fatal(err)
	}
	match, err = NewSource(db).GetMatch(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if match.Prediction == nil || match.Prediction.ID != 902 || match.Prediction.PublishedVersion != 3 {
		t.Fatalf("new canonical prediction = %+v, want visible version 902/v3", match.Prediction)
	}
	if match.Prediction.UpdatedAt.Format(time.RFC3339) != "2026-05-14T10:05:00Z" || match.Prediction.LastEvaluatedAt.Format(time.RFC3339) != "2026-05-14T10:04:00Z" {
		t.Fatalf("prediction freshness = updated %s evaluated %s", match.Prediction.UpdatedAt, match.Prediction.LastEvaluatedAt)
	}
	if _, err := db.ExecContext(context.Background(), "UPDATE engine_predictions SET superseded_at = '2025-05-18T16:00:00Z' WHERE public_id = 902"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), "INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active) VALUES ('uuid-903', 903, 100, '1x2', 'away', 0.80, 0.90, '2025-05-18T16:00:00Z', NULL, 'FULL', 1, 0)"); err != nil {
		t.Fatal(err)
	}
	match, err = NewSource(db).GetMatch(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if match.Prediction == nil || match.Prediction.ID != 902 {
		t.Fatalf("post-kickoff replay replaced final pre-match prediction: %+v", match.Prediction)
	}
	if _, err := db.ExecContext(context.Background(), "DELETE FROM engine_predictions; INSERT INTO data_backfill_requests (target_fixture_id, feature_group, status) VALUES (100, 'results', 'pending')"); err != nil {
		t.Fatal(err)
	}
	match, err = NewSource(db).GetMatch(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if match.PredictionState != "pending" || match.PredictionStateReason != "backfill_pending" {
		t.Fatalf("pending backfill state = %q/%q", match.PredictionState, match.PredictionStateReason)
	}
	if _, err := db.ExecContext(context.Background(), "UPDATE data_backfill_requests SET status = 'confirmed_unavailable'"); err != nil {
		t.Fatal(err)
	}
	match, err = NewSource(db).GetMatch(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if match.PredictionState != "insufficient_history" || match.PredictionStateReason != "PROVIDER_CONFIRMED_NO_HISTORY" {
		t.Fatalf("terminal backfill state = %q/%q", match.PredictionState, match.PredictionStateReason)
	}
	if _, err := db.ExecContext(context.Background(), "DELETE FROM data_backfill_requests; INSERT INTO api_predictions (fixture_id, predicted_winner, under_over, percent_home, percent_draw, percent_away, captured_at) VALUES (100, 'Arsenal', 'Over 2.5', 55, 25, 20, '2025-05-18T14:05:00Z')"); err != nil {
		t.Fatal(err)
	}
	match, err = NewSource(db).GetMatch(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if match.Prediction == nil || len(match.Predictions) != 2 || match.Predictions[1].MarketKey != "total_goals" || match.Predictions[1].Selection != "over_2.5" {
		t.Fatalf("provider fallback detail = %+v, want canonical 1x2 and over_2.5", match.Predictions)
	}
	// An unpublished engine row is not a public prediction. Match detail must
	// still use the provider projection while the engine row is embargoed.
	if _, err := db.ExecContext(context.Background(), "INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active) VALUES ('hidden', 904, 100, '1x2', 'away', 0.80, 0.90, '2025-05-18T14:10:00Z', NULL, 'FULL', 1, 1)"); err != nil {
		t.Fatal(err)
	}
	match, err = NewSource(db).GetMatch(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if match.Prediction == nil || match.Prediction.Selection != "Home" || match.Prediction.ID != 0 {
		t.Fatalf("embargoed engine row hid provider fallback: %+v", match.Prediction)
	}
}

func TestNormalizeQueryDateUsesTimezone(t *testing.T) {
	date, err := normalizeQueryDate(Query{Day: "today", Timezone: "Africa/Nairobi"}, func() time.Time {
		return time.Date(2026, 5, 13, 1, 30, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("normalizeQueryDate returned error: %v", err)
	}
	if got := date.Format("2006-01-02"); got != "2026-05-13" {
		t.Fatalf("normalized date = %s, want 2026-05-13", got)
	}
}

func TestLoadTimezoneFallsBackForNairobi(t *testing.T) {
	location, err := loadTimezone("Africa/Nairobi")
	if err != nil {
		t.Fatalf("loadTimezone returned error: %v", err)
	}
	if _, offset := time.Date(2026, 5, 13, 12, 0, 0, 0, location).Zone(); offset != 3*60*60 {
		t.Fatalf("timezone offset = %d, want %d", offset, 3*60*60)
	}
}
