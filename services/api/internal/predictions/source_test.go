package predictions

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/httpserver"
	_ "modernc.org/sqlite"
)

func TestPredictionWindowHonorsDateDayAndTimezone(t *testing.T) {
	source := NewSource(nil)
	source.now = func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }

	today, _ := source.predictionWindow(httpserver.PredictionFilter{})
	if got := today.Format("2006-01-02"); got != "2026-05-14" {
		t.Fatalf("default window start = %s, want 2026-05-14", got)
	}

	tomorrow, _ := source.predictionWindow(httpserver.PredictionFilter{Day: "tomorrow"})
	if got := tomorrow.Format("2006-01-02"); got != "2026-05-15" {
		t.Fatalf("tomorrow window start = %s, want 2026-05-15", got)
	}

	yesterday, _ := source.predictionWindow(httpserver.PredictionFilter{Day: "yesterday"})
	if got := yesterday.Format("2006-01-02"); got != "2026-05-13" {
		t.Fatalf("yesterday window start = %s, want 2026-05-13", got)
	}

	explicit, _ := source.predictionWindow(httpserver.PredictionFilter{Date: "2026-05-20"})
	if got := explicit.Format("2006-01-02"); got != "2026-05-20" {
		t.Fatalf("explicit date window start = %s, want 2026-05-20", got)
	}

	if end := explicit.AddDate(0, 0, 1); end.Format("2006-01-02") != "2026-05-21" {
		t.Fatalf("explicit date window end = %s, want 2026-05-21", end.Format("2006-01-02"))
	}
}

func TestSourceListTodayPredictionsReadsCurrentEnginePredictions(t *testing.T) {
	db := openEnginePredictionTestDB(t)
	for _, stmt := range []string{
		"INSERT INTO leagues (id, name, country_code) VALUES (1, 'Premier League', 'GB');",
		"INSERT INTO teams (id, name) VALUES (10, 'Arsenal'), (11, 'Chelsea'), (12, 'Liverpool'), (13, 'Everton'), (14, 'Leeds'), (15, 'Burnley');",
		"INSERT INTO fixtures (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES (100, 1, 10, 11, '2026-05-14T18:00:00Z', 'upcoming'), (101, 1, 12, 13, '2026-05-14T20:00:00Z', 'upcoming'), (102, 1, 14, 15, '2026-05-14T19:00:00Z', 'upcoming'), (103, 1, 12, 10, '2026-05-13T20:00:00Z', 'finished');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet, lifecycle_stage, data_completeness, features_used, explanation, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active, model_version) VALUES " +
			"('uuid-1', 1, 100, '1x2', 'home', 0.55, 0.55, 'low', 0, 'PRE_MATCH', 0.80, '{}', 'old', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1')," +
			"('uuid-2', 2, 101, '1x2', 'home', 0.62, 0.675, 'medium', 1, 'PRE_MATCH', 0.82, '{}', 'best', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1')," +
			"('uuid-3', 3, 102, '1x2', 'away', 0.61, 0.612, 'low', 0, 'PRE_MATCH', 0.76, '{}', 'later', '2026-05-14T11:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1')," +
			"('uuid-4', 4, 100, '1x2', 'draw', 0.30, 0.70, 'medium', 0, 'PRE_MATCH', 0.74, '{}', 'superseded', '2026-05-14T09:00:00Z', '2026-05-14T10:00:00Z', 'FULL', 1, 0, 'python-engine-v1')," +
			"('uuid-5', 5, 103, '1x2', 'away', 0.61, 0.99, 'high', 0, 'PRE_MATCH', 0.76, '{}', 'stale', '2026-05-13T11:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1')," +
			"('uuid-post-kickoff', 6, 101, '1x2', 'away', 0.80, 0.95, 'strong', 1, 'PRE_MATCH', 0.95, '{}', 'historical replay', '2026-05-14T21:00:00Z', '2026-05-14T21:00:00Z', 'FULL', 1, 0, 'python-engine-v1')," +
			"('uuid-goals', 7, 102, 'total_goals', 'over_2.5', 0.66, 0.70, 'medium', 0, 'PRE_MATCH', 0.82, '{}', 'goals', '2026-05-14T11:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec fixture: %v", err)
		}
	}

	minimumConfidence := 60.0
	source := NewSource(db)
	source.now = func() time.Time {
		return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	}
	predictions, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{
		MarketKey:         "1x2",
		MinimumConfidence: &minimumConfidence,
	})
	if err != nil {
		t.Fatalf("ListTodayPredictions returned error: %v", err)
	}

	if len(predictions) != 2 {
		t.Fatalf("predictions = %d, want 2", len(predictions))
	}
	if predictions[0].ID != 3 || predictions[1].ID != 2 {
		t.Fatalf("prediction order = [%d %d], want [3 2] (kickoff ascending)", predictions[0].ID, predictions[1].ID)
	}
	if predictions[0].Country != "GB" {
		t.Fatalf("prediction country = %q, want GB", predictions[0].Country)
	}
	if predictions[0].Confidence != 61.2 {
		if math.Abs(predictions[0].Confidence-61.2) > 0.001 {
			t.Fatalf("confidence = %.2f, want 61.20", predictions[0].Confidence)
		}
	}
	if predictions[0].PublishedVersion != 1 {
		t.Fatalf("published version = %d, want 1", predictions[0].PublishedVersion)
	}
	if len(predictions[0].Markets) != 2 || predictions[0].Markets[1].MarketKey != "total_goals" {
		t.Fatalf("prediction markets = %+v, want 1x2 and total_goals", predictions[0].Markets)
	}
	if got := predictions[0].UpdatedAt.Format(time.RFC3339); got != "2026-05-14T10:05:00Z" {
		t.Fatalf("updated at = %q", got)
	}
	if got := predictions[0].LastEvaluatedAt.Format(time.RFC3339); got != "2026-05-14T10:04:00Z" {
		t.Fatalf("last evaluated at = %q", got)
	}

	limited, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{
		MarketKey:         "1x2",
		MinimumConfidence: &minimumConfidence,
		Limit:             1,
	})
	if err != nil {
		t.Fatalf("ListTodayPredictions with limit returned error: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != 3 {
		t.Fatalf("limited predictions = %+v, want only prediction 3 (earliest kickoff)", limited)
	}
	bestBets, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{MinimumConfidence: &minimumConfidence, BestBetsOnly: true})
	if err != nil || len(bestBets) != 1 || bestBets[0].ID != 2 {
		t.Fatalf("best bets with post-kickoff replay = %+v, %v; want final pre-match prediction 2", bestBets, err)
	}
}

func TestSourceGetPredictionReadsEnginePredictionDetail(t *testing.T) {
	db := openEnginePredictionTestDB(t)
	for _, stmt := range []string{
		"INSERT INTO leagues (id, name) VALUES (1, 'Premier League');",
		"INSERT INTO teams (id, name) VALUES (10, 'Arsenal'), (11, 'Chelsea');",
		"INSERT INTO fixtures (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES (100, 1, 10, 11, '2026-05-14T18:00:00Z', 'finished');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet, lifecycle_stage, data_completeness, features_used, explanation, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active, model_version) VALUES " +
			"('uuid-1', 7, 100, '1x2', 'home', 0.62, 0.68, 'medium', 1, 'PRE_MATCH', 0.82, '{\"ranking_score\":0.72}', 'Home has the stronger recent signal.', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1');",
		"INSERT INTO prediction_results (prediction_id, fixture_id, market, predicted, actual, is_correct, settlement_status, probability, confidence, confidence_tier, lifecycle_stage_at_settlement, league_id, settled_at) VALUES ('uuid-1', 100, '1x2', 'home', 'home', 1, 'settled', 0.62, 0.68, 'medium', 'PRE_MATCH', 1, '2026-05-15T10:00:00Z');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec fixture: %v", err)
		}
	}

	detail, err := NewSource(db).GetPrediction(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetPrediction returned error: %v", err)
	}

	if detail.ID != 7 || detail.MatchID != 100 || detail.Selection != "home" {
		t.Fatalf("detail = %+v, want public_id 7 fixture 100 home", detail)
	}
	if detail.Confidence != 68.0 {
		t.Fatalf("confidence = %.2f, want 68.00", detail.Confidence)
	}
	if detail.Probabilities["home"] != 0.62 {
		t.Fatalf("probabilities = %+v, want home 0.62", detail.Probabilities)
	}
	if detail.InputSnapshotHash != "python-engine-v1" {
		t.Fatalf("input snapshot hash = %q, want model version", detail.InputSnapshotHash)
	}
	if detail.Outcome != "won" {
		t.Fatalf("outcome = %q, want won", detail.Outcome)
	}
	if detail.UpdatedAt.Format(time.RFC3339) != "2026-05-14T10:05:00Z" || detail.LastEvaluatedAt.Format(time.RFC3339) != "2026-05-14T10:04:00Z" {
		t.Fatalf("freshness = updated %s evaluated %s", detail.UpdatedAt, detail.LastEvaluatedAt)
	}
	if _, err := db.ExecContext(context.Background(), "INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet, lifecycle_stage, data_completeness, features_used, explanation, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active, model_version) VALUES ('uuid-replay', 8, 100, '1x2', 'away', 0.80, 0.90, 'strong', 1, 'PRE_MATCH', 0.9, '{}', 'replay', '2026-05-14T19:00:00Z', '2026-05-14T19:00:00Z', 'FULL', 1, 0, 'v2')"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSource(db).GetPrediction(context.Background(), 8); err != httpserver.ErrPredictionNotFound {
		t.Fatalf("post-kickoff replay error = %v, want not found", err)
	}
	if final, err := NewSource(db).GetPrediction(context.Background(), 7); err != nil || final.ID != 7 {
		t.Fatalf("final pre-match detail = %+v, %v", final, err)
	}
	cutoffSource := NewSource(db).ConfigurePublicResultDetailWindow(24 * time.Hour)
	cutoffSource.now = func() time.Time { return time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC) }
	if _, err := cutoffSource.GetPrediction(context.Background(), 7); err != httpserver.ErrPredictionNotFound {
		t.Fatalf("old settled prediction error = %v, want not found", err)
	}
}

func TestSourceImmediatelySelectsNewestCanonicalPrediction(t *testing.T) {
	db := openEnginePredictionTestDB(t)
	for _, stmt := range []string{
		"INSERT INTO leagues (id, name) VALUES (1, 'Premier League');",
		"INSERT INTO teams (id, name) VALUES (10, 'Arsenal'), (11, 'Chelsea');",
		"INSERT INTO fixtures (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES (100, 1, 10, 11, '2026-05-14T18:00:00Z', 'upcoming');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet, lifecycle_stage, data_completeness, features_used, explanation, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active, model_version) VALUES ('uuid-old', 10, 100, '1x2', 'home', 0.58, 0.60, 'medium', 1, 'PRE_MATCH', 0.8, '{}', 'old', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'v1');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	source := NewSource(db)
	source.now = func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }
	before, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{})
	if err != nil || len(before) != 1 || before[0].ID != 10 {
		t.Fatalf("before write = %+v, %v", before, err)
	}

	// The persistence transaction supersedes the previous current row before
	// inserting its replacement, preserving the partial unique-index contract.
	if _, err := db.ExecContext(context.Background(), "UPDATE engine_predictions SET superseded_at = '2026-05-14T11:00:00Z' WHERE id = 'uuid-old'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), "INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet, lifecycle_stage, data_completeness, features_used, explanation, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active, model_version) VALUES ('uuid-new', 11, 100, '1x2', 'away', 0.66, 0.72, 'medium', 1, 'PRE_MATCH', 0.9, '{}', 'new', '2026-05-14T11:00:00Z', NULL, 'FULL', 1, 0, 'v2')"); err != nil {
		t.Fatal(err)
	}
	after, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{})
	if err != nil || len(after) != 1 || after[0].ID != 11 || after[0].PublishedVersion != 2 || after[0].Selection != "away" {
		t.Fatalf("after write = %+v, %v", after, err)
	}
	if _, err := source.GetPrediction(context.Background(), 10); err != httpserver.ErrPredictionNotFound {
		t.Fatalf("old detail error = %v, want not found", err)
	}
	saved, err := source.GetSavedPrediction(context.Background(), 10)
	if err != nil || saved.ID != 10 || saved.Selection != "home" {
		t.Fatalf("saved superseded prediction = %+v, %v; want original saved selection", saved, err)
	}
}

func TestSourceBestBetsUsesLatestVisibleBestBetOnly(t *testing.T) {
	db := openEnginePredictionTestDB(t)
	for _, stmt := range []string{
		"INSERT INTO leagues (id, name) VALUES (1, 'Premier League');",
		"INSERT INTO teams (id, name) VALUES (10, 'Arsenal'), (11, 'Chelsea'), (12, 'Liverpool'), (13, 'Everton'), (14, 'Leeds'), (15, 'Burnley');",
		"INSERT INTO fixtures (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES (100, 1, 10, 11, '2026-05-14T18:00:00Z', 'upcoming'), (101, 1, 12, 13, '2026-05-14T19:00:00Z', 'upcoming'), (102, 1, 14, 15, '2026-05-14T11:00:00Z', 'upcoming');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet, lifecycle_stage, data_completeness, features_used, explanation, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active, model_version) VALUES " +
			"('uuid-old-best', 20, 100, '1x2', 'home', 0.62, 0.70, 'medium', 1, 'PRE_MATCH', 0.8, '{}', '', '2026-05-14T10:00:00Z', '2026-05-14T11:00:00Z', 'FULL', 1, 0, 'v1')," +
			"('uuid-new-not-best', 21, 100, '1x2', 'away', 0.58, 0.66, 'medium', 0, 'PRE_MATCH', 0.8, '{}', '', '2026-05-14T11:00:00Z', NULL, 'FULL', 1, 0, 'v2')," +
			"('uuid-visible-best', 22, 101, '1x2', 'home', 0.65, 0.74, 'medium', 1, 'PRE_MATCH', 0.8, '{}', '', '2026-05-14T11:00:00Z', NULL, 'FULL', 1, 0, 'v2')," +
			"('uuid-started-best', 24, 102, '1x2', 'home', 0.70, 0.82, 'strong', 1, 'PRE_MATCH', 0.9, '{}', '', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'v1');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	source := NewSource(db)
	source.now = func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }
	items, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{BestBetsOnly: true})
	if err != nil || len(items) != 1 || items[0].ID != 22 {
		t.Fatalf("best bets = %+v, %v", items, err)
	}

	if _, err := db.ExecContext(context.Background(), "UPDATE engine_predictions SET superseded_at = '2026-05-14T11:30:00Z' WHERE id = 'uuid-visible-best'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), "INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet, lifecycle_stage, data_completeness, features_used, explanation, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active, model_version) VALUES ('uuid-embargoed', 23, 101, '1x2', 'away', 0.70, 0.80, 'strong', 1, 'PRE_MATCH', 0.9, '{}', '', '2026-05-14T11:30:00Z', NULL, 'FULL', 1, 1, 'v3')"); err != nil {
		t.Fatal(err)
	}
	items, err = source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{BestBetsOnly: true})
	if err != nil || len(items) != 0 {
		t.Fatalf("embargoed canonical best bets = %+v, %v", items, err)
	}
}

func TestSourceHidesCanonicalPredictionsThatAreNotPubliclyEligible(t *testing.T) {
	db := openEnginePredictionTestDB(t)
	for _, stmt := range []string{
		"INSERT INTO leagues (id, name) VALUES (1, 'Premier League');",
		"INSERT INTO teams (id, name) VALUES (10, 'Arsenal'), (11, 'Chelsea');",
		"INSERT INTO fixtures (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES (200, 1, 10, 11, '2026-05-14T18:00:00Z', 'upcoming'), (201, 1, 10, 11, '2026-05-14T19:00:00Z', 'upcoming'), (202, 1, 10, 11, '2026-05-14T20:00:00Z', 'upcoming');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet, lifecycle_stage, data_completeness, features_used, explanation, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active, model_version) VALUES " +
			"('uuid-skip', 30, 200, '1x2', 'home', 0.60, 0.70, 'medium', 0, 'PRE_MATCH', 0.8, '{}', '', '2026-05-14T10:00:00Z', NULL, 'SKIP', 1, 0, 'v1')," +
			"('uuid-outside-horizon', 31, 201, '1x2', 'home', 0.60, 0.70, 'medium', 0, 'PRE_MATCH', 0.8, '{}', '', '2026-05-14T10:00:00Z', NULL, 'FULL', 0, 0, 'v1')," +
			"('uuid-embargoed', 32, 202, '1x2', 'home', 0.60, 0.70, 'medium', 0, 'PRE_MATCH', 0.8, '{}', '', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 1, 'v1');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	source := NewSource(db)
	source.now = func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }
	items, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{})
	if err != nil || len(items) != 0 {
		t.Fatalf("non-public predictions = %+v, %v", items, err)
	}
	for _, id := range []int64{30, 31, 32} {
		if _, err := source.GetPrediction(context.Background(), id); err != httpserver.ErrPredictionNotFound {
			t.Fatalf("prediction %d error = %v, want not found", id, err)
		}
	}
}

func TestSourceGetHitRateReportAggregatesEnginePredictionResults(t *testing.T) {
	db := openEnginePredictionTestDB(t)
	for _, stmt := range []string{
		"INSERT INTO leagues (id, name) VALUES (1, 'Premier League');",
		"INSERT INTO teams (id, name) VALUES (10, 'Arsenal'), (11, 'Chelsea'), (12, 'Liverpool'), (13, 'Everton');",
		"INSERT INTO fixtures (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES (100, 1, 10, 11, '2026-05-14T18:00:00Z', 'finished'), (101, 1, 12, 13, '2026-05-14T20:00:00Z', 'finished'), (102, 1, 10, 13, '2026-05-14T22:00:00Z', 'finished');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet, lifecycle_stage, data_completeness, features_used, explanation, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active, model_version) VALUES " +
			"('uuid-1', 1, 100, '1x2', 'home', 0.62, 0.68, 'medium', 1, 'PRE_MATCH', 0.82, '{}', '', '2026-05-14T10:00:00Z', '2026-05-14T17:00:00Z', 'FULL', 1, 0, 'python-engine-v1')," +
			"('uuid-2', 2, 101, '1x2', 'away', 0.58, 0.59, 'low', 0, 'PRE_MATCH', 0.70, '{}', '', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1')," +
			"('uuid-3', 3, 102, 'btts', 'yes', 0.55, 0.56, 'low', 0, 'PRE_MATCH', 0.73, '{}', '', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1')," +
			"('uuid-4', 4, 102, 'draw_no_bet', 'home', 0.55, 0.56, 'low', 0, 'PRE_MATCH', 0.73, '{}', '', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1')," +
			"('uuid-1-final', 5, 100, '1x2', 'away', 0.58, 0.64, 'medium', 0, 'PRE_MATCH', 0.82, '{}', '', '2026-05-14T17:00:00Z', '2026-05-14T19:00:00Z', 'FULL', 1, 0, 'python-engine-v1')," +
			"('uuid-1-replay', 6, 100, '1x2', 'home', 0.80, 0.90, 'strong', 1, 'PRE_MATCH', 0.95, '{}', '', '2026-05-14T19:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1');",
		"INSERT INTO prediction_results (prediction_id, fixture_id, market, predicted, actual, is_correct, settlement_status, probability, confidence, confidence_tier, lifecycle_stage_at_settlement, league_id, settled_at) VALUES " +
			"('uuid-1', 100, '1x2', 'home', 'home', 1, 'settled', 0.62, 0.68, 'medium', 'PRE_MATCH', 1, '2026-05-15T10:00:00Z')," +
			"('uuid-2', 101, '1x2', 'away', 'home', 0, 'settled', 0.58, 0.59, 'low', 'PRE_MATCH', 1, '2026-05-15T10:00:00Z')," +
			"('uuid-3', 102, 'btts', 'yes', 'yes', 1, 'settled', 0.55, 0.56, 'low', 'PRE_MATCH', 1, '2026-05-15T10:00:00Z')," +
			"('uuid-4', 102, 'draw_no_bet', 'home', 'draw', NULL, 'void', 0.55, 0.56, 'low', 'PRE_MATCH', 1, '2026-05-15T10:00:00Z')," +
			"('uuid-1-final', 100, '1x2', 'away', 'home', 0, 'settled', 0.58, 0.64, 'medium', 'PRE_MATCH', 1, '2026-05-15T10:00:00Z')," +
			"('uuid-1-replay', 100, '1x2', 'home', 'home', 1, 'settled', 0.80, 0.90, 'strong', 'PRE_MATCH', 1, '2026-05-15T10:00:00Z');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec fixture: %v", err)
		}
	}

	report, err := NewSource(db).GetHitRateReport(context.Background())
	if err != nil {
		t.Fatalf("GetHitRateReport returned error: %v", err)
	}
	if report.Settled != 4 || report.Won != 1 || report.Lost != 2 || report.Void != 1 {
		t.Fatalf("report = %+v, want one final pre-match result per fixture-market", report)
	}
	if report.Denominator != 3 {
		t.Fatalf("denominator = %d, want 3 (won 1 + lost 2)", report.Denominator)
	}
	if report.HitRate != 33.33 {
		t.Fatalf("hit rate = %.2f, want 33.33", report.HitRate)
	}
	if len(report.Markets) != 3 || report.Markets[0].MarketKey != "1x2" || report.Markets[0].HitRate != 0 || report.Markets[0].Settled != 2 || report.Markets[2].Void != 1 {
		t.Fatalf("markets = %+v, want two final 1x2 losses and draw_no_bet void", report.Markets)
	}
	if report.Markets[0].Denominator != 2 || report.Markets[1].Denominator != 1 || report.Markets[2].Denominator != 0 {
		t.Fatalf("market denominators = [%d %d %d], want [2 1 0] (won + lost per market)",
			report.Markets[0].Denominator, report.Markets[1].Denominator, report.Markets[2].Denominator)
	}

	beforePrune := report
	if _, err := db.ExecContext(context.Background(), "INSERT INTO prediction_accuracy_daily (aggregate_date, market, league_id, confidence_tier, model_version, total_predictions, correct, incorrect, voided, pending, first_settled_at, last_settled_at, aggregated_at) VALUES ('2026-05-15', '1x2', 1, 'medium', 'python-engine-v1', 2, 0, 2, 0, 0, '2026-05-15T10:00:00Z', '2026-05-15T10:00:00Z', '2026-05-16T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), "DELETE FROM prediction_results WHERE market = '1x2'"); err != nil {
		t.Fatal(err)
	}
	afterPrune, err := NewSource(db).GetHitRateReport(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterPrune, beforePrune) {
		t.Fatalf("report changed after aggregate/prune: before=%+v after=%+v", beforePrune, afterPrune)
	}
}

func TestSourceListsEverySettledMarketForLocalMatchDay(t *testing.T) {
	db := openEnginePredictionTestDB(t)
	for _, stmt := range []string{
		"INSERT INTO leagues (id, name, country_code) VALUES (1, 'Premier League', 'KE');",
		"INSERT INTO teams (id, name) VALUES (10, 'Arsenal'), (11, 'Chelsea');",
		"INSERT INTO fixtures (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES (100, 1, 10, 11, '2026-05-14T21:30:00Z', 'finished');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet, lifecycle_stage, data_completeness, features_used, explanation, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active, model_version) VALUES " +
			"('uuid-result', 9, 100, 'total_goals', 'over_2.5', 0.62, 0.68, 'medium', 0, 'PRE_MATCH', 0.82, '{}', '', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1');",
		"INSERT INTO prediction_results (prediction_id, fixture_id, market, predicted, actual, is_correct, settlement_status, probability, confidence, confidence_tier, lifecycle_stage_at_settlement, league_id, settled_at) VALUES ('uuid-result', 100, 'total_goals', 'over_2.5', 'over_2.5', 1, 'settled', 0.62, 0.68, 'medium', 'PRE_MATCH', 1, '2026-05-15T01:00:00Z');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	source := NewSource(db)
	source.now = func() time.Time { return time.Date(2026, 5, 15, 6, 0, 0, 0, time.UTC) }
	filter := httpserver.PredictionFilter{Date: "2026-05-15", Timezone: "Africa/Nairobi", Limit: 100}

	results, err := source.ListPredictionResults(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Outcome != "won" || results[0].Actual != "over_2.5" {
		t.Fatalf("results = %+v, want won over 2.5", results)
	}
	count, err := source.CountPredictionResults(context.Background(), filter)
	if err != nil || count != 1 {
		t.Fatalf("count = %d, err=%v", count, err)
	}
}

func TestSourceReadsCorrectedBTTSWhenRawResultWasPruned(t *testing.T) {
	db := openEnginePredictionTestDB(t)
	for _, stmt := range []string{
		"INSERT INTO leagues (id, name) VALUES (1, 'Premier League');",
		"INSERT INTO teams (id, name) VALUES (10, 'Arsenal'), (11, 'Chelsea');",
		"INSERT INTO fixtures (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES (100, 1, 10, 11, '2026-05-14T18:00:00Z', 'finished');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet, lifecycle_stage, data_completeness, features_used, explanation, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active, model_version) VALUES " +
			"('uuid-btts', 77, 100, 'btts_total_goals', 'yes_over_0.5', 0.62, 0.68, 'medium', 0, 'PRE_MATCH', 0.82, '{}', '', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1');",
		"INSERT INTO effective_model_evaluation_facts (prediction_id, final_outcome, settlement_classification) VALUES ('uuid-btts', 'yes_over_0.5', 'correct');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	source := NewSource(db)
	source.now = func() time.Time { return time.Date(2026, 5, 15, 6, 0, 0, 0, time.UTC) }
	filter := httpserver.PredictionFilter{Date: "2026-05-14", Timezone: "Africa/Nairobi"}
	detail, err := source.GetPrediction(context.Background(), 77)
	if err != nil || detail.Outcome != "won" {
		t.Fatalf("pruned BTTS detail = %+v, %v; want won", detail, err)
	}
	results, err := source.ListPredictionResults(context.Background(), filter)
	if err != nil || len(results) != 1 || results[0].Outcome != "won" || results[0].Actual != "yes_over_0.5" {
		t.Fatalf("pruned BTTS results = %+v, %v", results, err)
	}
	count, err := source.CountPredictionResults(context.Background(), filter)
	if err != nil || count != 1 {
		t.Fatalf("pruned BTTS count = %d, %v", count, err)
	}
	if _, err := db.ExecContext(context.Background(), "DELETE FROM effective_model_evaluation_facts WHERE prediction_id='uuid-btts'"); err != nil {
		t.Fatal(err)
	}
	detail, err = source.GetPrediction(context.Background(), 77)
	if err != nil || detail.Outcome != "pending" {
		t.Fatalf("missing evidence detail = %+v, %v; want pending", detail, err)
	}
	results, err = source.ListPredictionResults(context.Background(), filter)
	if err != nil || len(results) != 0 {
		t.Fatalf("missing evidence should remove result, got %+v, %v", results, err)
	}
}

func TestSourceListTodayProviderPredictionsIncludesCanonicalPrimitives(t *testing.T) {
	db := openEnginePredictionTestDB(t)
	for _, stmt := range []string{
		"INSERT INTO leagues (id, name) VALUES (1, 'Premier League');",
		"INSERT INTO teams (id, name) VALUES (10, 'Arsenal'), (11, 'Chelsea'), (12, 'Liverpool'), (13, 'Everton'), (14, 'Leeds'), (15, 'Burnley');",
		"INSERT INTO fixtures (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES (100, 1, 10, 11, '2026-05-14T18:00:00Z', 'upcoming'), (101, 1, 12, 13, '2026-05-14T17:00:00Z', 'upcoming'), (102, 1, 14, 15, '2026-05-14T19:00:00Z', 'upcoming');",
		"INSERT INTO api_predictions (fixture_id, predicted_winner, win_or_draw, under_over, percent_home, percent_draw, percent_away, advice, captured_at) VALUES " +
			"(100, 'Arsenal', 0, 'Over 2.5', 45.2, 28.1, 26.7, 'Arsenal strong at home.', '2026-05-14T09:30:00Z')," +
			"(101, 'Everton', 1, 'Under 2.5', 30.0, 25.0, 45.234, 'Everton likely to win.', '2026-05-14T08:00:00Z')," +
			"(102, 'Draw', 0, '', 33.3, 33.4, 33.3, '', '2026-05-14T07:00:00Z');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec fixture: %v", err)
		}
	}
	source := NewSource(db)
	source.now = func() time.Time {
		return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	}
	items, err := source.ListTodayProviderPredictions(context.Background(), httpserver.PredictionFilter{})
	if err != nil {
		t.Fatalf("ListTodayProviderPredictions returned error: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("provider items = %d, want 3", len(items))
	}
	total, err := source.CountTodayProviderPredictions(context.Background(), httpserver.PredictionFilter{})
	if err != nil {
		t.Fatalf("CountTodayProviderPredictions returned error: %v", err)
	}
	if total != 3 {
		t.Fatalf("provider total = %d, want 3", total)
	}
	// ORDER BY kickoff ASC: 101 (17:00), 100 (18:00), 102 (19:00).
	away := items[0]
	if away.MatchID != 101 || away.Selection != "Away" {
		t.Fatalf("items[0] = %+v, want fixture 101 selection Away", away)
	}
	if away.MarketKey != "1x2" {
		t.Fatalf("market key = %q, want 1x2", away.MarketKey)
	}
	if away.Confidence != 45.23 {
		t.Fatalf("confidence = %.2f, want 45.23 (rounded max percent)", away.Confidence)
	}
	if away.PublishedVersion != 0 {
		t.Fatalf("published version = %d, want 0 (provider items have no version lineage)", away.PublishedVersion)
	}
	if got := away.PublishedAt.Format(time.RFC3339); got != "2026-05-14T08:00:00Z" {
		t.Fatalf("published at = %q, want captured_at 2026-05-14T08:00:00Z", got)
	}
	if away.PredictedWinner != "Everton" || away.Advice != "Everton likely to win." || !away.WinOrDraw {
		t.Fatalf("existing provider fields changed: %+v", away)
	}

	home := items[1]
	if home.MatchID != 100 || home.Selection != "Home" || home.Confidence != 45.2 {
		t.Fatalf("items[1] = %+v, want fixture 100 selection Home confidence 45.2", home)
	}
	draw := items[2]
	if draw.MatchID != 102 || draw.Selection != "Draw" || draw.Confidence != 33.4 {
		t.Fatalf("items[2] = %+v, want fixture 102 selection Draw confidence 33.4", draw)
	}
}

func TestTodayProviderFallbackRequiresPublicEnginePrediction(t *testing.T) {
	db := openEnginePredictionTestDB(t)
	for _, stmt := range []string{
		"INSERT INTO leagues (id, name) VALUES (1, 'Premier League');",
		"INSERT INTO teams (id, name) VALUES (10, 'Arsenal'), (11, 'Chelsea');",
		"INSERT INTO fixtures (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES (100, 1, 10, 11, '2026-05-14T18:00:00Z', 'upcoming'), (101, 1, 10, 11, '2026-05-14T19:00:00Z', 'upcoming'), (102, 1, 10, 11, '2026-05-14T20:00:00Z', 'upcoming');",
		"INSERT INTO api_predictions (fixture_id, predicted_winner, captured_at) VALUES (100, 'Arsenal', '2026-05-14T09:00:00Z'), (101, 'Arsenal', '2026-05-14T09:00:00Z'), (102, 'Arsenal', '2026-05-14T09:00:00Z');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet, lifecycle_stage, data_completeness, features_used, explanation, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active, model_version) VALUES " +
			"('visible', 1, 100, '1x2', 'home', 0.6, 0.6, 'medium', 0, 'PRE_MATCH', 0.8, '{}', '', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'v1')," +
			"('embargoed', 2, 101, '1x2', 'home', 0.6, 0.6, 'medium', 0, 'PRE_MATCH', 0.8, '{}', '', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 1, 'v1')," +
			"('post-kickoff', 3, 102, '1x2', 'home', 0.6, 0.6, 'medium', 0, 'PRE_MATCH', 0.8, '{}', '', '2026-05-14T21:00:00Z', NULL, 'FULL', 1, 0, 'v1');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	source := NewSource(db)
	source.now = func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }
	items, err := source.ListTodayProviderPredictions(context.Background(), httpserver.PredictionFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].MatchID != 101 || items[1].MatchID != 102 {
		t.Fatalf("provider fallback fixtures = %+v, want hidden-engine fixtures 101, 102 only", items)
	}
	count, err := source.CountTodayProviderPredictions(context.Background(), httpserver.PredictionFilter{})
	if err != nil || count != 2 {
		t.Fatalf("provider fallback count = %d, err = %v; want 2", count, err)
	}
}

func TestProviderPrimitiveHelpersMatchCanonicalPrimitives(t *testing.T) {
	home := httpserver.ProviderPredictionSummary{
		HomeTeam: "Arsenal", AwayTeam: "Chelsea", PredictedWinner: "Arsenal",
		PercentHome: 45.2, PercentDraw: 28.1, PercentAway: 26.7,
	}
	httpserver.PopulateProviderPrimitives(&home)
	if home.Selection != "Home" {
		t.Fatalf("selection = %q, want Home when predicted winner is the home team", home.Selection)
	}
	caseInsensitive := home
	caseInsensitive.PredictedWinner = "arsenal"
	httpserver.PopulateProviderPrimitives(&caseInsensitive)
	if caseInsensitive.Selection != "Home" {
		t.Fatalf("selection = %q, want Home for case-insensitive team-name match", caseInsensitive.Selection)
	}
	away := httpserver.ProviderPredictionSummary{
		HomeTeam: "Arsenal", AwayTeam: "Chelsea", PredictedWinner: "Chelsea",
		PercentHome: 30.0, PercentDraw: 25.0, PercentAway: 45.234,
	}
	httpserver.PopulateProviderPrimitives(&away)
	if away.Selection != "Away" {
		t.Fatalf("selection = %q, want Away when predicted winner is the away team", away.Selection)
	}
	if away.Confidence != 45.23 {
		t.Fatalf("confidence = %.2f, want 45.23 (max percent rounded to two decimals)", away.Confidence)
	}
	draw := httpserver.ProviderPredictionSummary{
		HomeTeam: "Arsenal", AwayTeam: "Chelsea", PredictedWinner: "Draw",
		PercentHome: 33.3, PercentDraw: 33.4, PercentAway: 33.3,
	}
	populated := draw
	httpserver.PopulateProviderPrimitives(&populated)
	if populated.MarketKey != "1x2" || populated.PublishedVersion != 0 || populated.Selection != "Draw" || populated.Confidence != 33.4 {
		t.Fatalf("populated primitives = %+v, want 1x2 Draw confidence 33.4 version 0", populated)
	}
}

func TestCurrentPredictionQueriesUsePartialIndexPredicate(t *testing.T) {
	body, err := os.ReadFile("source.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	if strings.Count(source, "ep.superseded_at IS NULL") < 4 {
		t.Fatal("current prediction queries do not consistently use the partial-index predicate")
	}
	if strings.Contains(source, "FROM engine_predictions AS newer") {
		t.Fatal("current prediction queries must not scan prediction history through a correlated newer-row subquery")
	}
}

func TestPredictionDetailRetentionUsesConfirmedFinishBoundary(t *testing.T) {
	body, err := os.ReadFile("source.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	if !strings.Contains(source, "f.finished_at >= $3") || !strings.Contains(source, "f.finished_at IS NULL AND f.kickoff_at >= $4") {
		t.Fatal("prediction detail retention must use confirmed finish time with a conservative legacy fallback")
	}
}

func openEnginePredictionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, stmt := range []string{
		// priority_score mirrors production, where it is NOT NULL DEFAULT 0 and is
		// what the feed ranks competitions by.
		"CREATE TABLE leagues (id INTEGER PRIMARY KEY, name TEXT NOT NULL, country_code TEXT, priority_score INTEGER NOT NULL DEFAULT 0);",
		"CREATE TABLE teams (id INTEGER PRIMARY KEY, name TEXT NOT NULL, logo_url TEXT);",
		// home_goals_ft/away_goals_ft mirror production. Their absence here is
		// why a clause that reads them could not be tested at all.
		"CREATE TABLE fixtures (id INTEGER PRIMARY KEY, league_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TEXT NOT NULL, finished_at TEXT, status TEXT NOT NULL, home_goals_ft INTEGER, away_goals_ft INTEGER);",
		"CREATE TABLE engine_predictions (id TEXT PRIMARY KEY, public_id INTEGER NOT NULL UNIQUE, fixture_id INTEGER NOT NULL, market TEXT NOT NULL, prediction TEXT NOT NULL, probability REAL NOT NULL, confidence REAL NOT NULL, confidence_tier TEXT NOT NULL, is_best_bet INTEGER NOT NULL, lifecycle_stage TEXT NOT NULL, data_completeness REAL NOT NULL, features_used TEXT NOT NULL, explanation TEXT NOT NULL, generated_at TEXT NOT NULL, superseded_at TEXT, gate_tier TEXT NOT NULL, horizon_eligible INTEGER NOT NULL, embargo_active INTEGER NOT NULL, model_version TEXT NOT NULL, updated_at TEXT NOT NULL DEFAULT '2026-05-14T10:05:00Z', last_evaluated_at TEXT NOT NULL DEFAULT '2026-05-14T10:04:00Z');",
		"CREATE TABLE prediction_accuracy_daily (aggregate_date TEXT NOT NULL, market TEXT NOT NULL, league_id INTEGER NOT NULL, confidence_tier TEXT NOT NULL, model_version TEXT NOT NULL, total_predictions INTEGER NOT NULL, correct INTEGER NOT NULL, incorrect INTEGER NOT NULL, voided INTEGER NOT NULL, pending INTEGER NOT NULL, first_settled_at TEXT NOT NULL, last_settled_at TEXT NOT NULL, aggregated_at TEXT NOT NULL, PRIMARY KEY (aggregate_date, market, league_id, confidence_tier, model_version));",
		"CREATE TABLE prediction_results (prediction_id TEXT PRIMARY KEY, fixture_id INTEGER NOT NULL, market TEXT NOT NULL, predicted TEXT NOT NULL, actual TEXT, is_correct INTEGER, settlement_status TEXT NOT NULL, probability REAL NOT NULL, confidence REAL NOT NULL, confidence_tier TEXT NOT NULL, lifecycle_stage_at_settlement TEXT NOT NULL, league_id INTEGER NOT NULL, settled_at TEXT NOT NULL);",
		"CREATE TABLE effective_model_evaluation_facts (prediction_id TEXT PRIMARY KEY, final_outcome TEXT NOT NULL, settlement_classification TEXT NOT NULL);",
		"CREATE TABLE api_predictions (fixture_id INTEGER PRIMARY KEY, predicted_winner TEXT, win_or_draw INTEGER, under_over TEXT, percent_home REAL, percent_draw REAL, percent_away REAL, advice TEXT, captured_at TEXT NOT NULL);",
		"CREATE TABLE odds (id INTEGER PRIMARY KEY, fixture_id INTEGER NOT NULL, bookmaker TEXT NOT NULL DEFAULT '', market TEXT NOT NULL, outcome TEXT NOT NULL, value REAL NOT NULL, fetched_at TEXT NOT NULL);",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}
	return db
}

// The feed must lead with the competitions a reader came for.
//
// On 2026-09-12 the site published 11,031 predictions for the day and served
// the first hundred by kickoff time. Those hundred were Western Australia NPL,
// three Korean divisions and a Mexican under-21 league; the day's eight Premier
// League fixtures, all of them predicted and eligible, appeared nowhere. The
// fixtures kicked off at 14:00-19:00 UTC and several thousand lower-division
// fixtures simply kicked off earlier.
func TestSourceLeadsWithRankedCompetitions(t *testing.T) {
	db := openEnginePredictionTestDB(t)
	for _, stmt := range []string{
		// The minor league kicks off first, exactly as it did in production.
		"INSERT INTO leagues (id, name, country_code, priority_score) VALUES (1, 'Premier League', 'GB', 95), (2, 'Western Australia NPL', 'AU', 0);",
		"INSERT INTO teams (id, name) VALUES (10, 'Liverpool'), (11, 'Fulham'), (12, 'Perth'), (13, 'Joondalup');",
		"INSERT INTO fixtures (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES " +
			"(200, 2, 12, 13, '2026-05-14T13:00:00Z', 'upcoming')," +
			"(201, 1, 10, 11, '2026-05-14T18:00:00Z', 'upcoming');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet, lifecycle_stage, data_completeness, features_used, explanation, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active, model_version) VALUES " +
			"('uuid-minor', 1, 200, '1x2', 'home', 0.55, 0.55, 'low', 0, 'PRE_MATCH', 0.80, '{}', 'minor', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1')," +
			"('uuid-epl', 2, 201, '1x2', 'home', 0.61, 0.61, 'medium', 0, 'PRE_MATCH', 0.82, '{}', 'epl', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec fixture: %v", err)
		}
	}

	source := NewSource(db)
	source.now = func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }

	rows, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{MarketKey: "1x2"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected both fixtures, got %d", len(rows))
	}
	if rows[0].LeagueName != "Premier League" {
		t.Fatalf("the ranked competition must lead, got %q first (kickoff order would give %q)", rows[0].LeagueName, "Western Australia NPL")
	}
	if rows[1].LeagueName != "Western Australia NPL" {
		t.Fatalf("unranked competitions still follow, got %q", rows[1].LeagueName)
	}
}

// Everything unranked keeps the old behaviour, so this is a change to the top
// of the feed rather than a new ordering for the whole catalogue.
func TestSourceStillOrdersUnrankedCompetitionsByKickoff(t *testing.T) {
	db := openEnginePredictionTestDB(t)
	for _, stmt := range []string{
		"INSERT INTO leagues (id, name, country_code, priority_score) VALUES (2, 'Western Australia NPL', 'AU', 0), (3, 'K League 2', 'KR', 0);",
		"INSERT INTO teams (id, name) VALUES (12, 'Perth'), (13, 'Joondalup'), (14, 'Seoul'), (15, 'Busan');",
		"INSERT INTO fixtures (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES " +
			"(300, 3, 14, 15, '2026-05-14T16:00:00Z', 'upcoming')," +
			"(301, 2, 12, 13, '2026-05-14T13:00:00Z', 'upcoming');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet, lifecycle_stage, data_completeness, features_used, explanation, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active, model_version) VALUES " +
			"('uuid-k', 1, 300, '1x2', 'home', 0.55, 0.55, 'low', 0, 'PRE_MATCH', 0.80, '{}', 'k', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1')," +
			"('uuid-w', 2, 301, '1x2', 'home', 0.61, 0.61, 'medium', 0, 'PRE_MATCH', 0.82, '{}', 'w', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec fixture: %v", err)
		}
	}

	source := NewSource(db)
	source.now = func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }

	rows, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{MarketKey: "1x2"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 || rows[0].LeagueName != "Western Australia NPL" {
		t.Fatalf("unranked competitions must stay in kickoff order, got %+v", rows)
	}
}

// Ranking competitions must not simply move the burial somewhere else.
//
// The feed is one row per fixture-market and a Premier League fixture carries
// about seventeen markets, so once competitions were ranked the eight fixtures
// of a matchday produced 136 rows and filled every slot on page one. Verified
// against production: `limit=100` returned 100 rows, all Premier League, the
// first six of them the same fixture. The reader saw Aston Villa vs Nottingham
// Forest repeated down the screen and no other league at all.
func TestSourceDoesNotLetOneFixtureFillThePage(t *testing.T) {
	db := openEnginePredictionTestDB(t)
	stmts := []string{
		"INSERT INTO leagues (id, name, country_code, priority_score) VALUES (1, 'Premier League', 'GB', 95), (2, 'Western Australia NPL', 'AU', 0);",
		"INSERT INTO teams (id, name) VALUES (10, 'Liverpool'), (11, 'Fulham'), (12, 'Perth'), (13, 'Joondalup');",
		"INSERT INTO fixtures (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES " +
			"(200, 1, 10, 11, '2026-05-14T18:00:00Z', 'upcoming')," +
			"(201, 2, 12, 13, '2026-05-14T19:00:00Z', 'upcoming');",
	}

	// Eight markets on the ranked fixture, one on the unranked one.
	markets := []string{"1x2", "total_goals", "btts", "corners", "cards", "double_chance", "dnb", "halftime"}
	values := make([]string, 0, len(markets)+1)
	for i, market := range markets {
		values = append(values, fmt.Sprintf(
			"('uuid-epl-%d', %d, 200, '%s', 'home', 0.6, %f, 'medium', 0, 'PRE_MATCH', 0.8, '{}', 'epl', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1')",
			i, i+1, market, 0.90-float64(i)*0.05))
	}
	values = append(values, "('uuid-minor', 99, 201, '1x2', 'home', 0.6, 0.55, 'low', 0, 'PRE_MATCH', 0.8, '{}', 'minor', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1')")
	stmts = append(stmts, "INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet, lifecycle_stage, data_completeness, features_used, explanation, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active, model_version) VALUES "+strings.Join(values, ",")+";")

	for _, stmt := range stmts {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec fixture: %v", err)
		}
	}

	source := NewSource(db)
	source.now = func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }

	rows, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	perFixture := map[int64]int{}
	for _, row := range rows {
		perFixture[row.MatchID]++
	}
	if perFixture[200] > 3 {
		t.Fatalf("one fixture contributed %d rows to the unfiltered feed; eight markets must not become eight rows", perFixture[200])
	}
	if perFixture[201] == 0 {
		t.Fatal("the other league was pushed off the page entirely, which is the burial this cap exists to prevent")
	}
	// The cap keeps the strongest markets, not an arbitrary three.
	if rows[0].MarketKey != "1x2" {
		t.Fatalf("the highest-confidence market must survive the cap, got %q", rows[0].MarketKey)
	}
}

// A reader who asked for one market wants all of it.
func TestSourceDoesNotCapAFilteredMarket(t *testing.T) {
	db := openEnginePredictionTestDB(t)
	stmts := []string{
		"INSERT INTO leagues (id, name, country_code, priority_score) VALUES (1, 'Premier League', 'GB', 95);",
		"INSERT INTO teams (id, name) VALUES (10, 'Liverpool'), (11, 'Fulham'), (12, 'Arsenal'), (13, 'Chelsea'), (14, 'Spurs'), (15, 'Everton'), (16, 'Leeds'), (17, 'Burnley');",
		"INSERT INTO fixtures (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES " +
			"(300, 1, 10, 11, '2026-05-14T15:00:00Z', 'upcoming')," +
			"(301, 1, 12, 13, '2026-05-14T16:00:00Z', 'upcoming')," +
			"(302, 1, 14, 15, '2026-05-14T17:00:00Z', 'upcoming')," +
			"(303, 1, 16, 17, '2026-05-14T18:00:00Z', 'upcoming');",
	}
	values := make([]string, 0, 4)
	for i, fixture := range []int{300, 301, 302, 303} {
		values = append(values, fmt.Sprintf(
			"('uuid-c-%d', %d, %d, 'corners', 'over', 0.6, 0.7, 'medium', 0, 'PRE_MATCH', 0.8, '{}', 'c', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1')",
			i, i+1, fixture))
	}
	stmts = append(stmts, "INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet, lifecycle_stage, data_completeness, features_used, explanation, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active, model_version) VALUES "+strings.Join(values, ",")+";")

	for _, stmt := range stmts {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec fixture: %v", err)
		}
	}

	source := NewSource(db)
	source.now = func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }

	rows, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{MarketKey: "corners"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("a market filter must return every matching row, got %d of 4", len(rows))
	}
}

// A published prediction must not be hidden by a match that was played days
// ago and never reported a result.
//
// Reported from QA on 2026-09-20: "no prediction for Manchester City vs
// Sunderland". Sunderland had played AZ Alkmaar four days earlier and that row
// was still sitting at status `unknown`, so the eligibility clause treated
// Sunderland as having an unresolved match and suppressed every later fixture
// they appeared in. The same clause is why Brighton vs Arsenal had no
// prediction the day before, and why Manchester United kept losing theirs: one
// mechanism, not three incidents.
//
// The second fixture here is the other half of the defect. Both belong to
// Sunderland on the same day, and the old clause let a team hold only one
// eligible fixture at a time, so the later one disappeared even with nothing
// stale at all -- a future match cannot be "awaiting its result".
func TestSourceKeepsPredictionsForTeamsWithAStalePreviousFixture(t *testing.T) {
	db := openEnginePredictionTestDB(t)
	for _, stmt := range []string{
		"INSERT INTO leagues (id, name, country_code, priority_score) VALUES (1, 'Premier League', 'GB', 95);",
		"INSERT INTO teams (id, name) VALUES (10, 'Manchester City'), (11, 'Sunderland'), (12, 'AZ Alkmaar'), (13, 'Everton');",
		"INSERT INTO fixtures (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES " +
			"(400, 1, 11, 12, '2026-05-10T19:00:00Z', 'unknown')," +
			"(401, 1, 10, 11, '2026-05-14T13:00:00Z', 'upcoming')," +
			"(402, 1, 11, 13, '2026-05-14T19:00:00Z', 'upcoming');",
		"INSERT INTO engine_predictions (id, public_id, fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet, lifecycle_stage, data_completeness, features_used, explanation, generated_at, superseded_at, gate_tier, horizon_eligible, embargo_active, model_version) VALUES " +
			"('uuid-city', 1, 401, '1x2', 'home', 0.72, 0.72, 'high', 0, 'PRE_MATCH', 0.90, '{}', 'city', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1')," +
			"('uuid-sund', 2, 402, '1x2', 'home', 0.51, 0.51, 'low', 0, 'PRE_MATCH', 0.80, '{}', 'sund', '2026-05-14T10:00:00Z', NULL, 'FULL', 1, 0, 'python-engine-v1');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec fixture: %v", err)
		}
	}

	source := NewSource(db)
	source.now = func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }

	rows, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{MarketKey: "1x2"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	served := map[int64]bool{}
	for _, row := range rows {
		served[row.MatchID] = true
	}
	if !served[401] {
		t.Error("Manchester City vs Sunderland was hidden by a match played four days earlier that never reported a result")
	}
	if !served[402] {
		t.Error("a team's second fixture in the horizon was hidden by its own first fixture")
	}
}
