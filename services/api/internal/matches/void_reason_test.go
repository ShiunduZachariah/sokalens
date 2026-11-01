package matches

import (
	"context"
	"database/sql"
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/httpserver"

	_ "modernc.org/sqlite"
)

// A prediction voided because no result ever arrived must reach the page with
// its reason attached. "Void" on its own is barely better than the "Pending"
// it replaces -- both leave the reader guessing whether the site is broken --
// and a void produced by a market rule (a drawn draw-no-bet) is a graded
// outcome that must not borrow the same explanation.
func TestMatchPredictionsCarryTheVoidReasonOnlyWhenOneWasRecorded(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE leagues (id INTEGER PRIMARY KEY, name TEXT NOT NULL, country_code TEXT, priority_score INTEGER NOT NULL DEFAULT 0);",
		"CREATE TABLE teams (id INTEGER PRIMARY KEY, name TEXT NOT NULL, logo_url TEXT, slug TEXT);",
		"CREATE TABLE matches (id INTEGER PRIMARY KEY, league_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TEXT NOT NULL, status TEXT NOT NULL, provider_status_raw TEXT, provider_status_label TEXT, status_elapsed INTEGER, venue_name TEXT, venue_city TEXT, home_score INTEGER, away_score INTEGER, half_time_home_score INTEGER, half_time_away_score INTEGER, live_state_at TEXT);",
		"CREATE TABLE engine_predictions (id TEXT PRIMARY KEY, public_id INTEGER NOT NULL UNIQUE, fixture_id INTEGER NOT NULL, market TEXT NOT NULL, prediction TEXT NOT NULL, probability REAL NOT NULL, confidence REAL NOT NULL, generated_at TEXT NOT NULL, superseded_at TEXT, gate_tier TEXT NOT NULL DEFAULT 'FULL', horizon_eligible INTEGER NOT NULL DEFAULT 1, embargo_active INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT '2026-05-14T10:05:00Z', last_evaluated_at TEXT NOT NULL DEFAULT '2026-05-14T10:04:00Z');",
		"CREATE TABLE prediction_results (prediction_id TEXT PRIMARY KEY, settlement_status TEXT NOT NULL, is_correct INTEGER, actual TEXT, void_reason TEXT);",
		"CREATE TABLE effective_model_evaluation_facts (prediction_id TEXT PRIMARY KEY, final_outcome TEXT, settlement_classification TEXT, exclusion_reason TEXT);",
		"CREATE TABLE prediction_tracker_state (prediction_id TEXT PRIMARY KEY, state TEXT NOT NULL, home_goals INTEGER, away_goals INTEGER, match_minute INTEGER, updated_at TEXT);",
		"INSERT INTO leagues (id, name) VALUES (1, 'Major League Soccer');",
		"INSERT INTO teams (id, name) VALUES (10, 'Inter Miami'), (11, 'San Diego');",
		"INSERT INTO matches (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES (3735781, 1, 10, 11, '2026-09-20T23:00:00Z', 'unknown');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, generated_at) VALUES ('uuid-1', 901, 3735781, 'total_goals', 'over_2.5', 0.61, 0.58, '2026-09-20T12:00:00Z');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, generated_at) VALUES ('uuid-2', 902, 3735781, 'draw_no_bet', 'home', 0.55, 0.52, '2026-09-20T12:00:00Z');",
		// Expired: no result ever arrived, so the page has to say so.
		"INSERT INTO prediction_results (prediction_id, settlement_status, is_correct, actual, void_reason) VALUES ('uuid-1', 'void', NULL, NULL, 'result_never_arrived');",
		// Graded void: the market rule voided it, and it needs no excuse.
		"INSERT INTO prediction_results (prediction_id, settlement_status, is_correct, actual, void_reason) VALUES ('uuid-2', 'void', NULL, 'draw', NULL);",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	predictions, err := NewSource(db).matchPredictions(context.Background(), 3735781)
	if err != nil {
		t.Fatalf("matchPredictions returned error: %v", err)
	}
	if len(predictions) != 2 {
		t.Fatalf("predictions = %d, want 2", len(predictions))
	}

	byMarket := map[string]httpserver.MatchPredictionSummary{}
	for _, prediction := range predictions {
		byMarket[prediction.MarketKey] = prediction
	}

	expired := byMarket["total_goals"]
	if expired.Outcome != "void" {
		t.Fatalf("expired outcome = %q, want void", expired.Outcome)
	}
	if expired.VoidReason != "result_never_arrived" {
		t.Fatalf("expired void reason = %q, want result_never_arrived", expired.VoidReason)
	}

	graded := byMarket["draw_no_bet"]
	if graded.Outcome != "void" {
		t.Fatalf("graded outcome = %q, want void", graded.Outcome)
	}
	if graded.VoidReason != "" {
		t.Fatalf("a market-rule void borrowed an expiry explanation: %q", graded.VoidReason)
	}
	// The mutable result row can be pruned independently of the compact model
	// fact and its latest revision. Only BTTS uses that retained fallback here;
	// missing evidence in another market must remain pending.
	for _, statement := range []string{
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, generated_at) VALUES ('uuid-btts', 903, 3735781, 'btts_total_goals', 'yes_over_0.5', 0.61, 0.58, '2026-09-20T12:00:00Z')",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, generated_at) VALUES ('uuid-other', 904, 3735781, 'total_corners', 'over_8.5', 0.61, 0.58, '2026-09-20T12:00:00Z')",
		"INSERT INTO effective_model_evaluation_facts (prediction_id, final_outcome, settlement_classification) VALUES ('uuid-btts', 'yes_over_0.5', 'correct'), ('uuid-other', 'over_8.5', 'incorrect')",
	} {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("insert retained fact fixture: %v", err)
		}
	}
	predictions, err = NewSource(db).matchPredictions(context.Background(), 3735781)
	if err != nil {
		t.Fatalf("matchPredictions with pruned result: %v", err)
	}
	for _, prediction := range predictions {
		switch prediction.MarketKey {
		case "btts_total_goals":
			if prediction.Outcome != "won" || prediction.Actual != "yes_over_0.5" {
				t.Fatalf("retained BTTS fact = %+v, want won/yes_over_0.5", prediction)
			}
		case "total_corners":
			if prediction.Outcome != "pending" || prediction.Actual != "" {
				t.Fatalf("other market invented retained result = %+v", prediction)
			}
		}
	}
	if _, err := db.ExecContext(context.Background(), "INSERT INTO prediction_results (prediction_id, settlement_status, is_correct, actual) VALUES ('uuid-btts', 'settled', 0, 'no_under_0.5')"); err != nil {
		t.Fatalf("restore mutable BTTS result: %v", err)
	}
	predictions, err = NewSource(db).matchPredictions(context.Background(), 3735781)
	if err != nil {
		t.Fatalf("matchPredictions with current result: %v", err)
	}
	for _, prediction := range predictions {
		if prediction.MarketKey == "btts_total_goals" && (prediction.Outcome != "lost" || prediction.Actual != "no_under_0.5") {
			t.Fatalf("current result should outrank retained fallback: %+v", prediction)
		}
	}
}
