package matches

import (
	"context"
	"database/sql"
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/httpserver"

	_ "modernc.org/sqlite"
)

// The tracker state is computed once by the engine and read here verbatim. The
// web app and the Flutter app both take it from this payload, so if the read
// were to derive it -- or silently substitute a default -- the two clients
// could show the same pick as on track and off track at the same time.
//
// The distinction this test pins down is between a pick the engine has not
// judged yet (no row: the field is absent) and a pick the engine judged as
// undecidable ("unknown": the field is present and says so). Collapsing those
// two into one value loses the difference between "we have not looked" and
// "we looked and the match cannot decide it either way".
func TestMatchPredictionsReadTheStoredTrackerStateAndNeverInventOne(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE leagues (id INTEGER PRIMARY KEY, name TEXT NOT NULL, country_code TEXT, priority_score INTEGER NOT NULL DEFAULT 0);",
		"CREATE TABLE teams (id INTEGER PRIMARY KEY, name TEXT NOT NULL, logo_url TEXT, slug TEXT);",
		"CREATE TABLE matches (id INTEGER PRIMARY KEY, league_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TEXT NOT NULL, status TEXT NOT NULL, provider_status_raw TEXT, provider_status_label TEXT, status_elapsed INTEGER, venue_name TEXT, venue_city TEXT, home_score INTEGER, away_score INTEGER, half_time_home_score INTEGER, half_time_away_score INTEGER, live_state_at TEXT);",
		"CREATE TABLE engine_predictions (id TEXT PRIMARY KEY, public_id INTEGER NOT NULL UNIQUE, fixture_id INTEGER NOT NULL, market TEXT NOT NULL, prediction TEXT NOT NULL, probability REAL NOT NULL, confidence REAL NOT NULL, generated_at TEXT NOT NULL, superseded_at TEXT, gate_tier TEXT NOT NULL DEFAULT 'FULL', horizon_eligible INTEGER NOT NULL DEFAULT 1, embargo_active INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT '2026-09-20T12:00:00Z', last_evaluated_at TEXT NOT NULL DEFAULT '2026-09-20T12:00:00Z');",
		"CREATE TABLE prediction_results (prediction_id TEXT PRIMARY KEY, settlement_status TEXT NOT NULL, is_correct INTEGER, actual TEXT, void_reason TEXT);",
		"CREATE TABLE effective_model_evaluation_facts (prediction_id TEXT PRIMARY KEY, final_outcome TEXT, settlement_classification TEXT, exclusion_reason TEXT);",
		"CREATE TABLE prediction_tracker_state (prediction_id TEXT PRIMARY KEY, state TEXT NOT NULL, home_goals INTEGER, away_goals INTEGER, match_minute INTEGER, updated_at TEXT);",
		"INSERT INTO leagues (id, name) VALUES (1, 'Premier League');",
		"INSERT INTO teams (id, name) VALUES (10, 'Brentford'), (11, 'Fulham');",
		"INSERT INTO matches (id, league_id, home_team_id, away_team_id, kickoff_at, status, home_score, away_score, status_elapsed) VALUES (4411, 1, 10, 11, '2026-09-20T14:00:00Z', 'live', 2, 0, 63);",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, generated_at) VALUES ('uuid-won', 901, 4411, 'total_goals', 'over_1.5', 0.61, 0.58, '2026-09-20T12:00:00Z');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, generated_at) VALUES ('uuid-unknown', 902, 4411, 'total_corners', 'over_8.5', 0.55, 0.52, '2026-09-20T12:00:00Z');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, generated_at) VALUES ('uuid-unjudged', 903, 4411, 'btts', 'yes', 0.50, 0.50, '2026-09-20T12:00:00Z');",
		// Two goals in: over 1.5 can no longer lose, and the engine said so.
		"INSERT INTO prediction_tracker_state (prediction_id, state, home_goals, away_goals, match_minute) VALUES ('uuid-won', 'won', 2, 0, 63);",
		// Corners are not in the live feed for this league, so the engine
		// reached a judgement it could not decide -- which is itself a fact
		// the clients render, not an absence.
		"INSERT INTO prediction_tracker_state (prediction_id, state, home_goals, away_goals, match_minute) VALUES ('uuid-unknown', 'unknown', 2, 0, 63);",
		// uuid-unjudged deliberately has no tracker row.
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	predictions, err := NewSource(db).matchPredictions(context.Background(), 4411)
	if err != nil {
		t.Fatalf("matchPredictions returned error: %v", err)
	}
	if len(predictions) != 3 {
		t.Fatalf("predictions = %d, want 3", len(predictions))
	}

	byMarket := map[string]httpserver.MatchPredictionSummary{}
	for _, prediction := range predictions {
		byMarket[prediction.MarketKey] = prediction
	}

	for market, want := range map[string]string{
		"total_goals":   "won",
		"total_corners": "unknown",
		"btts":          "",
	} {
		if got := byMarket[market].TrackerState; got != want {
			t.Fatalf("%s tracker state = %q, want %q", market, got, want)
		}
	}

	// The tracker is not the settlement. Nothing wrote a prediction_results
	// row here, so the graded outcome is still "pending" even though the
	// tracker has already called the pick won. The two fields answer
	// different questions -- "how is it going" versus "what did it finish as"
	// -- and a tracker verdict must never be promoted into a graded result.
	if outcome := byMarket["total_goals"].Outcome; outcome != "pending" {
		t.Fatalf("graded outcome = %q, want pending: the tracker verdict must not settle a pick", outcome)
	}
}

// The list must read the SAME stored verdict the match page reads.
//
// This is the whole point of storing it. Two queries that each derived "on
// track" from a score and a market key would agree in testing and diverge in
// production -- over 1.5 at 2-0 is won to one implementation and still on
// track to another -- and a reader who sees the list and then opens the match
// would watch the platform contradict itself.
//
// So the assertion is not "the list has a tracker" but "the list and the match
// page return the same word for the same pick, from one row".
func TestTheMatchListReadsTheSameTrackerVerdictAsTheMatchPage(t *testing.T) {
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
		"CREATE TABLE engine_predictions (id TEXT PRIMARY KEY, public_id INTEGER NOT NULL UNIQUE, fixture_id INTEGER NOT NULL, market TEXT NOT NULL, prediction TEXT NOT NULL, probability REAL NOT NULL, confidence REAL NOT NULL, generated_at TEXT NOT NULL, superseded_at TEXT, gate_tier TEXT NOT NULL DEFAULT 'FULL', horizon_eligible INTEGER NOT NULL DEFAULT 1, embargo_active INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT '2026-09-20T12:00:00Z', last_evaluated_at TEXT NOT NULL DEFAULT '2026-09-20T12:00:00Z');",
		"CREATE TABLE prediction_tracker_state (prediction_id TEXT PRIMARY KEY, state TEXT NOT NULL, home_goals INTEGER, away_goals INTEGER, match_minute INTEGER, updated_at TEXT);",
		"CREATE TABLE prediction_results (prediction_id TEXT PRIMARY KEY, settlement_status TEXT NOT NULL, is_correct INTEGER, actual TEXT, void_reason TEXT);",
		"CREATE TABLE effective_model_evaluation_facts (prediction_id TEXT PRIMARY KEY, final_outcome TEXT, settlement_classification TEXT, exclusion_reason TEXT);",
		"CREATE TABLE prediction_generation_failures (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, model_id INTEGER NOT NULL, reason_code TEXT NOT NULL, detail TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);",
		"INSERT INTO leagues (id, name, country_code) VALUES (1, 'Premier League', 'GB');",
		"INSERT INTO teams (id, name) VALUES (10, 'Brentford'), (11, 'Fulham');",
		"INSERT INTO matches (id, league_id, home_team_id, away_team_id, kickoff_at, status, home_score, away_score, status_elapsed, live_state_at) VALUES (4411, 1, 10, 11, '2026-09-20T14:00:00Z', 'live', 1, 0, 63, '2026-09-20T15:03:00Z');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, generated_at) VALUES ('uuid-1x2', 901, 4411, '1x2', 'home', 0.58, 0.55, '2026-09-20T12:00:00Z');",
		"INSERT INTO prediction_tracker_state (prediction_id, state, home_goals, away_goals, match_minute) VALUES ('uuid-1x2', 'on_track', 1, 0, 63);",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	source := NewSource(db)
	listed, err := source.ListMatches(context.Background(), Query{
		Date:               "2026-09-20",
		Timezone:           "UTC",
		Limit:              10,
		IncludeUnavailable: true,
	})
	if err != nil {
		t.Fatalf("ListMatches returned error: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d matches, want 1", len(listed))
	}
	if listed[0].Prediction == nil {
		t.Fatal("the listed match has no headline prediction")
	}
	if got := listed[0].Prediction.TrackerState; got != "on_track" {
		t.Fatalf("list tracker state = %q, want on_track", got)
	}

	detail, err := source.GetMatch(context.Background(), 4411)
	if err != nil {
		t.Fatalf("GetMatch returned error: %v", err)
	}
	if detail.Prediction == nil {
		t.Fatal("the match page has no headline prediction")
	}
	if detail.Prediction.TrackerState != listed[0].Prediction.TrackerState {
		t.Fatalf("list says %q and the match page says %q for the same pick",
			listed[0].Prediction.TrackerState, detail.Prediction.TrackerState)
	}
}

// A pick nothing has judged reads empty in Go, which the serialiser sends as
// null. Empty here is "no row", not a verdict.
func TestAnUnjudgedHeadlinePickReadsEmptyOnTheList(t *testing.T) {
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
		"CREATE TABLE engine_predictions (id TEXT PRIMARY KEY, public_id INTEGER NOT NULL UNIQUE, fixture_id INTEGER NOT NULL, market TEXT NOT NULL, prediction TEXT NOT NULL, probability REAL NOT NULL, confidence REAL NOT NULL, generated_at TEXT NOT NULL, superseded_at TEXT, gate_tier TEXT NOT NULL DEFAULT 'FULL', horizon_eligible INTEGER NOT NULL DEFAULT 1, embargo_active INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT '2026-09-20T12:00:00Z', last_evaluated_at TEXT NOT NULL DEFAULT '2026-09-20T12:00:00Z');",
		"CREATE TABLE prediction_tracker_state (prediction_id TEXT PRIMARY KEY, state TEXT NOT NULL, home_goals INTEGER, away_goals INTEGER, match_minute INTEGER, updated_at TEXT);",
		"CREATE TABLE prediction_results (prediction_id TEXT PRIMARY KEY, settlement_status TEXT NOT NULL, is_correct INTEGER, actual TEXT, void_reason TEXT);",
		"CREATE TABLE effective_model_evaluation_facts (prediction_id TEXT PRIMARY KEY, final_outcome TEXT, settlement_classification TEXT, exclusion_reason TEXT);",
		"CREATE TABLE prediction_generation_failures (id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, model_id INTEGER NOT NULL, reason_code TEXT NOT NULL, detail TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);",
		"INSERT INTO leagues (id, name, country_code) VALUES (1, 'Premier League', 'GB');",
		"INSERT INTO teams (id, name) VALUES (10, 'Brentford'), (11, 'Fulham');",
		"INSERT INTO matches (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES (4412, 1, 10, 11, '2026-09-20T20:00:00Z', 'upcoming');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, generated_at) VALUES ('uuid-upcoming', 902, 4412, '1x2', 'away', 0.52, 0.50, '2026-09-20T12:00:00Z');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	listed, err := NewSource(db).ListMatches(context.Background(), Query{
		Date:               "2026-09-20",
		Timezone:           "UTC",
		Limit:              10,
		IncludeUnavailable: true,
	})
	if err != nil {
		t.Fatalf("ListMatches returned error: %v", err)
	}
	if len(listed) != 1 || listed[0].Prediction == nil {
		t.Fatalf("listed %d matches with a prediction", len(listed))
	}
	if got := listed[0].Prediction.TrackerState; got != "" {
		t.Fatalf("tracker state = %q, want empty for a pick nothing has judged", got)
	}
}
