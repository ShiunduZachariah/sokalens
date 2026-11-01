package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Direction string

const (
	DirectionUp   Direction = "up"
	DirectionDown Direction = "down"
)

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type Runner struct {
	DB        *sql.DB
	Directory string
}

func (r Runner) Up(ctx context.Context) error {
	if r.DB == nil {
		return errors.New("migration DB is nil")
	}
	if err := ensureSchemaMigrations(ctx, r.DB); err != nil {
		return err
	}

	files, err := List(r.Directory, DirectionUp)
	if err != nil {
		return err
	}
	for _, file := range files {
		version := migrationVersion(file, DirectionUp)
		applied, err := isApplied(ctx, r.DB, version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if err := execFileInTx(ctx, r.DB, file, version); err != nil {
			return err
		}
	}
	return nil
}

func (r Runner) Down(ctx context.Context) error {
	if r.DB == nil {
		return errors.New("migration DB is nil")
	}
	if err := ensureSchemaMigrations(ctx, r.DB); err != nil {
		return err
	}

	files, err := List(r.Directory, DirectionDown)
	if err != nil {
		return err
	}
	for _, file := range files {
		version := migrationVersion(file, DirectionDown)
		applied, err := isApplied(ctx, r.DB, version)
		if err != nil {
			return err
		}
		if !applied {
			continue
		}
		if err := execDownFileInTx(ctx, r.DB, file, version); err != nil {
			return err
		}
	}
	return nil
}

func List(dir string, direction Direction) ([]string, error) {
	if dir == "" {
		return nil, errors.New("migration directory is empty")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	suffix := "." + string(direction) + ".sql"
	files := []string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		files = append(files, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(files)
	if direction == DirectionDown {
		for i, j := 0, len(files)-1; i < j; i, j = i+1, j-1 {
			files[i], files[j] = files[j], files[i]
		}
	}
	return files, nil
}

func Seed(ctx context.Context, db *sql.DB, path string) error {
	if db == nil {
		return errors.New("seed DB is nil")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, string(body))
	return err
}

func VerifyFoundationSeed(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("verify seed DB is nil")
	}
	checks := []struct {
		name  string
		query string
		min   int
	}{
		{name: "countries", query: "SELECT COUNT(*) FROM countries", min: 3},
		{name: "sports", query: "SELECT COUNT(*) FROM sports", min: 1},
		{name: "leagues", query: "SELECT COUNT(*) FROM leagues", min: 1},
		{name: "teams", query: "SELECT COUNT(*) FROM teams", min: 2},
		{name: "matches", query: "SELECT COUNT(*) FROM matches", min: 1},
		{name: "odds_markets", query: "SELECT COUNT(*) FROM odds_markets", min: 16},
		{name: "odds_snapshots", query: "SELECT COUNT(*) FROM odds_snapshots", min: 1},
		{name: "prediction_models", query: "SELECT COUNT(*) FROM prediction_models", min: 1},
		{name: "predictions", query: "SELECT COUNT(*) FROM predictions", min: 1},
		{name: "prediction_versions", query: "SELECT COUNT(*) FROM prediction_versions", min: 1},
		{name: "prediction_outcomes", query: "SELECT COUNT(*) FROM prediction_outcomes", min: 1},
		{name: "prediction_feature_snapshots", query: "SELECT COUNT(*) FROM prediction_feature_snapshots", min: 1},
		{name: "provider_prediction_benchmarks", query: "SELECT COUNT(*) FROM provider_prediction_benchmarks", min: 0},
		{name: "users", query: "SELECT COUNT(*) FROM users", min: 2},
		{name: "feature_flags", query: "SELECT COUNT(*) FROM feature_flags", min: 9},
		{name: "app_settings", query: "SELECT COUNT(*) FROM app_settings", min: 1},
		{name: "subscription_entitlements", query: "SELECT COUNT(*) FROM subscription_entitlements", min: 5},
		{name: "provider_rate_limits", query: "SELECT COUNT(*) FROM provider_rate_limits", min: 1},
		{name: "providers", query: "SELECT COUNT(*) FROM providers", min: 1},
		{name: "provider_endpoint_specs", query: "SELECT COUNT(*) FROM provider_endpoint_specs", min: 6},
		{name: "provider_feature_sources", query: "SELECT COUNT(*) FROM provider_feature_sources", min: 8},
		{name: "prediction_market_support", query: "SELECT COUNT(*) FROM prediction_market_support", min: 11},
		{name: "ad_campaigns", query: "SELECT COUNT(*) FROM ad_campaigns", min: 1},
	}
	for _, check := range checks {
		var count int
		if err := db.QueryRowContext(ctx, check.query).Scan(&count); err != nil {
			return fmt.Errorf("verify seed %s: %w", check.name, err)
		}
		if count < check.min {
			return fmt.Errorf("verify seed %s: got %d rows, want at least %d", check.name, count, check.min)
		}
	}
	requiredRows := []struct {
		name  string
		query string
	}{
		{name: "multi_match_builder market", query: "SELECT COUNT(*) FROM odds_markets WHERE market_key = 'multi_match_builder'"},
		{name: "free_entity_favorites entitlement", query: "SELECT COUNT(*) FROM subscription_entitlements WHERE entitlement_key = 'free_entity_favorites'"},
		{name: "free_basic_match_notifications entitlement", query: "SELECT COUNT(*) FROM subscription_entitlements WHERE entitlement_key = 'free_basic_match_notifications'"},
		{name: "pro_insight_watchlist entitlement", query: "SELECT COUNT(*) FROM subscription_entitlements WHERE entitlement_key = 'pro_insight_watchlist'"},
		{name: "pro_insight_alerts entitlement", query: "SELECT COUNT(*) FROM subscription_entitlements WHERE entitlement_key = 'pro_insight_alerts'"},
		{name: "pro_advanced_multi_match_builder entitlement", query: "SELECT COUNT(*) FROM subscription_entitlements WHERE entitlement_key = 'pro_advanced_multi_match_builder'"},
		{name: "api-football provider rate limit", query: "SELECT COUNT(*) FROM provider_rate_limits WHERE provider = 'api-football'"},
		{name: "api-football provider record", query: "SELECT COUNT(*) FROM providers WHERE provider = 'api-football'"},
		{name: "api-football countries endpoint", query: "SELECT COUNT(*) FROM provider_endpoint_specs WHERE provider = 'api-football' AND endpoint_key = 'countries'"},
		{name: "api-football fixtures statistics endpoint", query: "SELECT COUNT(*) FROM provider_endpoint_specs WHERE provider = 'api-football' AND endpoint_key = 'fixtures.statistics'"},
		{name: "corners feature source", query: "SELECT COUNT(*) FROM provider_feature_sources WHERE provider = 'api-football' AND feature_key = 'corners'"},
		{name: "1x2 market support", query: "SELECT COUNT(*) FROM prediction_market_support WHERE market_key = '1x2' AND enabled = true"},
		{name: "double chance market support", query: "SELECT COUNT(*) FROM prediction_market_support WHERE market_key = 'double_chance' AND enabled = true"},
		{name: "btts market support", query: "SELECT COUNT(*) FROM prediction_market_support WHERE market_key = 'btts' AND enabled = true"},
		{name: "over under 2.5 market support", query: "SELECT COUNT(*) FROM prediction_market_support WHERE market_key = 'ou_2_5' AND enabled = true"},
		{name: "over under 0.5 market support", query: "SELECT COUNT(*) FROM prediction_market_support WHERE market_key = 'ou_0_5' AND enabled = true"},
		{name: "over under 1.5 market support", query: "SELECT COUNT(*) FROM prediction_market_support WHERE market_key = 'ou_1_5' AND enabled = true"},
		{name: "over under 3.5 market support", query: "SELECT COUNT(*) FROM prediction_market_support WHERE market_key = 'ou_3_5' AND enabled = true"},
		{name: "over under 4.5 market support", query: "SELECT COUNT(*) FROM prediction_market_support WHERE market_key = 'ou_4_5' AND enabled = true"},
		{name: "over under 5.5 market support", query: "SELECT COUNT(*) FROM prediction_market_support WHERE market_key = 'ou_5_5' AND enabled = true"},
		{name: "team over under goals market support", query: "SELECT COUNT(*) FROM prediction_market_support WHERE market_key = 'team_ou_goals' AND enabled = true"},
		{name: "half-time full-time market support", query: "SELECT COUNT(*) FROM prediction_market_support WHERE market_key = 'ht_ft' AND enabled = true"},
		{name: "corners market support", query: "SELECT COUNT(*) FROM prediction_market_support WHERE market_key = 'corners' AND enabled = true"},
		{name: "cards market support", query: "SELECT COUNT(*) FROM prediction_market_support WHERE market_key = 'cards' AND enabled = true"},
		{name: "fouls market support", query: "SELECT COUNT(*) FROM prediction_market_support WHERE market_key = 'fouls' AND enabled = true"},
		{name: "throw-ins market support", query: "SELECT COUNT(*) FROM prediction_market_support WHERE market_key = 'throw_ins' AND enabled = true"},
	}
	for _, row := range requiredRows {
		var count int
		if err := db.QueryRowContext(ctx, row.query).Scan(&count); err != nil {
			return fmt.Errorf("verify seed %s: %w", row.name, err)
		}
		if count == 0 {
			return fmt.Errorf("verify seed %s: missing required row", row.name)
		}
	}
	return nil
}

// CompactPredictionStorage reclaims disk after the guarded prediction-version
// cleanup. VACUUM FULL takes an exclusive table lock, so callers must schedule
// this operation during a maintenance window rather than normal API startup.
func CompactPredictionStorage(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("compact prediction DB is nil")
	}
	if _, err := db.ExecContext(ctx, `VACUUM (FULL, ANALYZE) engine_predictions`); err != nil {
		return fmt.Errorf("compact engine_predictions: %w", err)
	}
	return nil
}

func VerifyPredictionEngineContract(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("verify prediction engine contract DB is nil")
	}
	for _, table := range predictionEngineContractTables() {
		query := "SELECT " + strings.Join(table.columns, ", ") + " FROM " + table.name + " LIMIT 0"
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("verify prediction engine contract %s: %w", table.name, err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("verify prediction engine contract %s: %w", table.name, err)
		}
	}
	return nil
}

type predictionEngineContractTable struct {
	name    string
	columns []string
}

func predictionEngineContractTables() []predictionEngineContractTable {
	return []predictionEngineContractTable{
		{
			name: "fixtures",
			columns: []string{
				"id", "league_id", "season_id", "home_team_id", "away_team_id", "kickoff_at", "status",
				"venue_name", "referee", "round", "home_goals_ft", "away_goals_ft", "home_goals_ht", "away_goals_ht",
			},
		},
		{
			name: "fixture_statistics",
			columns: []string{
				"fixture_id", "team_id", "shots_on_target", "shots_total", "shots_blocked", "shots_inside_box",
				"shots_outside_box", "possession_pct", "corners", "fouls", "offsides", "yellow_cards",
				"red_cards", "goalkeeper_saves", "passes_total", "passes_accurate", "pass_accuracy", "xg",
			},
		},
		{
			name: "team_season_stats",
			columns: []string{
				"team_id", "league_id", "season_id", "played_home", "wins_home", "draws_home", "losses_home",
				"goals_for_home", "goals_against_home", "clean_sheets_home", "failed_to_score_home",
				"played_away", "wins_away", "draws_away", "losses_away", "goals_for_away",
				"goals_against_away", "clean_sheets_away", "failed_to_score_away",
			},
		},
		{
			name:    "head_to_head",
			columns: []string{"home_team_id", "away_team_id", "fixture_id", "played_at", "home_goals", "away_goals", "venue"},
		},
		{
			name: "standings",
			columns: []string{
				"league_id", "season_id", "team_id", "rank", "points", "played", "wins", "draws", "losses",
				"goals_for", "goals_against", "goal_diff", "form",
			},
		},
		{
			name:    "player_availability",
			columns: []string{"fixture_id", "team_id", "player_name", "reason", "status"},
		},
		{
			name:    "odds",
			columns: []string{"fixture_id", "bookmaker", "market", "outcome", "value", "implied_prob", "fetched_at"},
		},
		{
			name:    "referee_stats",
			columns: []string{"name", "matches_officiated", "avg_yellow_cards", "avg_red_cards", "avg_fouls"},
		},
		{
			name:    "league_coverage",
			columns: []string{"league_id", "season_id", "has_statistics", "has_xg", "has_injuries", "has_odds", "has_lineups"},
		},
		{
			name: "api_predictions",
			columns: []string{
				"fixture_id", "predicted_winner", "win_or_draw", "under_over", "percent_home",
				"percent_draw", "percent_away", "advice",
			},
		},
		{
			name: "engine_predictions",
			columns: []string{
				"id", "public_id", "fixture_id", "market", "prediction", "probability", "confidence",
				"confidence_tier", "is_best_bet", "lifecycle_stage", "data_completeness", "features_used",
				"explanation", "generated_at", "superseded_at", "gate_tier", "horizon_eligible",
				"embargo_active", "model_version", "content_hash", "last_evaluated_at", "updated_at",
			},
		},
		{
			name: "prediction_results",
			columns: []string{
				"prediction_id", "fixture_id", "market", "predicted", "actual", "is_correct", "settlement_status",
				"probability", "confidence", "confidence_tier", "lifecycle_stage_at_settlement",
				"league_id", "settled_at",
			},
		},
		{
			name: "accuracy_snapshots",
			columns: []string{
				"snapshot_date", "scope", "scope_id", "total_predictions", "correct", "accuracy",
			},
		},
		{
			name: "prediction_accuracy_daily",
			columns: []string{
				"aggregate_date", "market", "league_id", "confidence_tier", "model_version",
				"total_predictions", "correct", "incorrect", "voided", "pending",
				"first_settled_at", "last_settled_at", "aggregated_at",
			},
		},
		{
			name:    "prediction_result_rollup_ledger",
			columns: []string{"prediction_id", "included_in_accuracy", "aggregated_at"},
		},
	}
}

func ensureSchemaMigrations(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version TEXT PRIMARY KEY,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
)`)
	return err
}

func isApplied(ctx context.Context, db *sql.DB, version string) (bool, error) {
	var count int
	query := "SELECT COUNT(*) FROM schema_migrations WHERE version = '" + quoteLiteral(version) + "'"
	if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func execFileInTx(ctx context.Context, db *sql.DB, file, version string) error {
	if !versionPattern.MatchString(version) {
		return fmt.Errorf("unsafe migration version %q", version)
	}
	body, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		return fmt.Errorf("apply %s: %w", filepath.Base(file), err)
	}
	insert := "INSERT INTO schema_migrations (version) VALUES ('" + quoteLiteral(version) + "')"
	if _, err := tx.ExecContext(ctx, insert); err != nil {
		return fmt.Errorf("record %s: %w", version, err)
	}
	return tx.Commit()
}

func execDownFileInTx(ctx context.Context, db *sql.DB, file, version string) error {
	if !versionPattern.MatchString(version) {
		return fmt.Errorf("unsafe migration version %q", version)
	}
	body, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		return fmt.Errorf("rollback %s: %w", filepath.Base(file), err)
	}
	deleteRecord := "DELETE FROM schema_migrations WHERE version = '" + quoteLiteral(version) + "'"
	if _, err := tx.ExecContext(ctx, deleteRecord); err != nil {
		return fmt.Errorf("forget %s: %w", version, err)
	}
	return tx.Commit()
}

func migrationVersion(file string, direction Direction) string {
	return strings.TrimSuffix(filepath.Base(file), "."+string(direction)+".sql")
}

func quoteLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}
