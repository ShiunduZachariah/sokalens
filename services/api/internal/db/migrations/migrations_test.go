package migrations

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestListUpMigrationsReturnsSortedUpFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "000002_second.up.sql", "SELECT 2;")
	writeFile(t, dir, "000001_first.up.sql", "SELECT 1;")
	writeFile(t, dir, "000001_first.down.sql", "DROP TABLE first;")

	files, err := List(dir, DirectionUp)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}

	want := []string{"000001_first.up.sql", "000002_second.up.sql"}
	if len(files) != len(want) {
		t.Fatalf("len(files) = %d, want %d", len(files), len(want))
	}
	for i := range want {
		if filepath.Base(files[i]) != want[i] {
			t.Fatalf("files[%d] = %q, want %q", i, filepath.Base(files[i]), want[i])
		}
	}
}

func TestRunnerAppliesMigrationsOnce(t *testing.T) {
	db := openSQLiteLikeTestDB(t)
	dir := t.TempDir()
	writeFile(t, dir, "000001_foundation.up.sql", "CREATE TABLE sample (id INTEGER PRIMARY KEY);")

	runner := Runner{DB: db, Directory: dir}
	if err := runner.Up(context.Background()); err != nil {
		t.Fatalf("first Up returned error: %v", err)
	}
	if err := runner.Up(context.Background()); err != nil {
		t.Fatalf("second Up returned error: %v", err)
	}

	var count int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", "000001_foundation").Scan(&count); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if count != 1 {
		t.Fatalf("migration count = %d, want 1", count)
	}
}

func TestRunnerDownAppliesDownMigrationsInReverseOrder(t *testing.T) {
	db := openSQLiteLikeTestDB(t)
	dir := t.TempDir()
	writeFile(t, dir, "000001_first.up.sql", "CREATE TABLE first_table (id INTEGER PRIMARY KEY);")
	writeFile(t, dir, "000001_first.down.sql", "DROP TABLE first_table;")
	writeFile(t, dir, "000002_second.up.sql", "CREATE TABLE second_table (id INTEGER PRIMARY KEY);")
	writeFile(t, dir, "000002_second.down.sql", "DROP TABLE second_table;")

	runner := Runner{DB: db, Directory: dir}
	if err := runner.Up(context.Background()); err != nil {
		t.Fatalf("Up returned error: %v", err)
	}
	if err := runner.Down(context.Background()); err != nil {
		t.Fatalf("Down returned error: %v", err)
	}

	for _, table := range []string{"first_table", "second_table"} {
		var name string
		err := db.QueryRowContext(context.Background(), "SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&name)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("table %s still exists or query failed with %v", table, err)
		}
	}

	var count int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if count != 0 {
		t.Fatalf("schema migration count = %d, want 0", count)
	}
}

func TestSeedExecutesSQLFile(t *testing.T) {
	db := openSQLiteLikeTestDB(t)
	dir := t.TempDir()
	seedPath := filepath.Join(dir, "seed.sql")
	if err := os.WriteFile(seedPath, []byte("CREATE TABLE seeded (name TEXT); INSERT INTO seeded (name) VALUES ('football');"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	if err := Seed(context.Background(), db, seedPath); err != nil {
		t.Fatalf("Seed returned error: %v", err)
	}

	var name string
	if err := db.QueryRowContext(context.Background(), "SELECT name FROM seeded").Scan(&name); err != nil {
		t.Fatalf("query seeded: %v", err)
	}
	if name != "football" {
		t.Fatalf("name = %q, want football", name)
	}
}

func TestVerifyFoundationSeedChecksRequiredRows(t *testing.T) {
	db := openSQLiteLikeTestDB(t)
	for _, stmt := range foundationSeedTestSchema(true) {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	if err := VerifyFoundationSeed(context.Background(), db); err != nil {
		t.Fatalf("VerifyFoundationSeed returned error: %v", err)
	}
}

func TestVerifyFoundationSeedRequiresSubscriptionEntitlements(t *testing.T) {
	db := openSQLiteLikeTestDB(t)
	for _, stmt := range foundationSeedTestSchema(false) {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	err := VerifyFoundationSeed(context.Background(), db)
	if err == nil {
		t.Fatal("VerifyFoundationSeed returned nil error without subscription entitlements")
	}
	if !strings.Contains(err.Error(), "subscription_entitlements") {
		t.Fatalf("error = %v, want subscription_entitlements", err)
	}
}

func TestVerifyFoundationSeedRequiresMultiMatchBuilderMarket(t *testing.T) {
	db := openSQLiteLikeTestDB(t)
	for _, stmt := range foundationSeedTestSchema(true) {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}
	if _, err := db.ExecContext(context.Background(), "DELETE FROM odds_markets WHERE market_key = 'multi_match_builder'"); err != nil {
		t.Fatalf("delete multi_match_builder: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), "INSERT INTO odds_markets (market_key) VALUES ('accumulator_recommendation')"); err != nil {
		t.Fatalf("insert stale market: %v", err)
	}

	err := VerifyFoundationSeed(context.Background(), db)
	if err == nil {
		t.Fatal("VerifyFoundationSeed returned nil error without multi_match_builder market")
	}
	if !strings.Contains(err.Error(), "multi_match_builder") {
		t.Fatalf("error = %v, want multi_match_builder", err)
	}
}

func TestVerifyFoundationSeedRequiresProviderRateLimitState(t *testing.T) {
	db := openSQLiteLikeTestDB(t)
	for _, stmt := range foundationSeedTestSchemaWithoutProviderRateLimits() {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	err := VerifyFoundationSeed(context.Background(), db)
	if err == nil {
		t.Fatal("VerifyFoundationSeed returned nil error without provider rate limits")
	}
	if !strings.Contains(err.Error(), "provider_rate_limits") {
		t.Fatalf("error = %v, want provider_rate_limits", err)
	}
}

func TestVerifyFoundationSeedRequiresProviderEndpointSpecs(t *testing.T) {
	db := openSQLiteLikeTestDB(t)
	for _, stmt := range foundationSeedTestSchemaWithoutProviderEndpointSpecs() {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	err := VerifyFoundationSeed(context.Background(), db)
	if err == nil {
		t.Fatal("VerifyFoundationSeed returned nil error without provider endpoint specs")
	}
	if !strings.Contains(err.Error(), "provider_endpoint_specs") {
		t.Fatalf("error = %v, want provider_endpoint_specs", err)
	}
}

func TestVerifyFoundationSeedRequiresPredictionMarketSupport(t *testing.T) {
	db := openSQLiteLikeTestDB(t)
	for _, stmt := range foundationSeedTestSchemaWithoutPredictionMarketSupport() {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	err := VerifyFoundationSeed(context.Background(), db)
	if err == nil {
		t.Fatal("VerifyFoundationSeed returned nil error without prediction market support")
	}
	if !strings.Contains(err.Error(), "prediction_market_support") {
		t.Fatalf("error = %v, want prediction_market_support", err)
	}
}

func TestVerifyFoundationSeedRequiresPredictionWorkflowRows(t *testing.T) {
	db := openSQLiteLikeTestDB(t)
	for _, stmt := range foundationSeedTestSchemaWithoutPredictionWorkflowRows() {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	err := VerifyFoundationSeed(context.Background(), db)
	if err == nil {
		t.Fatal("VerifyFoundationSeed returned nil error without prediction workflow rows")
	}
	if !strings.Contains(err.Error(), "prediction_feature_snapshots") {
		t.Fatalf("error = %v, want prediction_feature_snapshots", err)
	}
}

func TestVerifyFoundationSeedRequiresPredictionBenchmarkTable(t *testing.T) {
	db := openSQLiteLikeTestDB(t)
	for _, stmt := range foundationSeedTestSchemaWithoutPredictionBenchmarks() {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	err := VerifyFoundationSeed(context.Background(), db)
	if err == nil {
		t.Fatal("VerifyFoundationSeed returned nil error without provider prediction benchmarks")
	}
	if !strings.Contains(err.Error(), "provider_prediction_benchmarks") {
		t.Fatalf("error = %v, want provider_prediction_benchmarks", err)
	}
}

func TestVerifyPredictionEngineContractRequiresSection31Tables(t *testing.T) {
	db := openSQLiteLikeTestDB(t)

	err := VerifyPredictionEngineContract(context.Background(), db)
	if err == nil {
		t.Fatal("VerifyPredictionEngineContract returned nil error without contract tables")
	}
	if !strings.Contains(err.Error(), "fixtures") {
		t.Fatalf("error = %v, want fixtures", err)
	}
}

func TestVerifyPredictionEngineContractPassesWithSection31Columns(t *testing.T) {
	db := openSQLiteLikeTestDB(t)
	for _, stmt := range predictionEngineContractTestSchema() {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec contract schema: %v", err)
		}
	}

	if err := VerifyPredictionEngineContract(context.Background(), db); err != nil {
		t.Fatalf("VerifyPredictionEngineContract returned error: %v", err)
	}
}

func TestVerifyPredictionEngineContractRequiresEnginePredictionPublicID(t *testing.T) {
	db := openSQLiteLikeTestDB(t)
	for _, stmt := range predictionEngineContractTestSchemaWithoutEnginePredictionPublicID() {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec contract schema: %v", err)
		}
	}

	err := VerifyPredictionEngineContract(context.Background(), db)
	if err == nil {
		t.Fatal("VerifyPredictionEngineContract returned nil error without engine_predictions.public_id")
	}
	if !strings.Contains(err.Error(), "engine_predictions") {
		t.Fatalf("error = %v, want engine_predictions", err)
	}
}

func TestVerifyFoundationSeedReportsMissingRows(t *testing.T) {
	db := openSQLiteLikeTestDB(t)
	if _, err := db.ExecContext(context.Background(), "CREATE TABLE countries (code TEXT);"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	if err := VerifyFoundationSeed(context.Background(), db); err == nil {
		t.Fatal("VerifyFoundationSeed returned nil error for missing seed data")
	}
}

func foundationSeedTestSchema(includeSubscriptionEntitlements bool) []string {
	schema := []string{
		"CREATE TABLE countries (code TEXT); INSERT INTO countries (code) VALUES ('KE'), ('GB'), ('US');",
		"CREATE TABLE sports (slug TEXT); INSERT INTO sports (slug) VALUES ('football');",
		"CREATE TABLE leagues (slug TEXT); INSERT INTO leagues (slug) VALUES ('kenyan-premier-league');",
		"CREATE TABLE teams (slug TEXT); INSERT INTO teams (slug) VALUES ('gor-mahia'), ('afc-leopards');",
		"CREATE TABLE matches (id INTEGER); INSERT INTO matches (id) VALUES (1);",
		"CREATE TABLE odds_markets (market_key TEXT); INSERT INTO odds_markets (market_key) VALUES ('1x2'), ('double_chance'), ('btts'), ('ou_2_5'), ('corners'), ('cards'), ('fouls'), ('throw_ins'), ('team_ou_goals'), ('ht_ft'), ('ou_0_5'), ('ou_1_5'), ('ou_3_5'), ('ou_4_5'), ('ou_5_5'), ('multi_match_builder');",
		"CREATE TABLE odds_snapshots (id INTEGER); INSERT INTO odds_snapshots (id) VALUES (1);",
		"CREATE TABLE prediction_models (id INTEGER); INSERT INTO prediction_models (id) VALUES (1);",
		"CREATE TABLE predictions (id INTEGER); INSERT INTO predictions (id) VALUES (1);",
		"CREATE TABLE prediction_versions (id INTEGER); INSERT INTO prediction_versions (id) VALUES (1);",
		"CREATE TABLE prediction_outcomes (id INTEGER); INSERT INTO prediction_outcomes (id) VALUES (1);",
		"CREATE TABLE users (id INTEGER); INSERT INTO users (id) VALUES (1), (2);",
		"CREATE TABLE feature_flags (feature_key TEXT); INSERT INTO feature_flags (feature_key) VALUES ('public_content'), ('predictions'), ('odds_display'), ('bookmaker_ctas'), ('affiliate_links'), ('paid_promotions'), ('gambling_ads'), ('subscriptions'), ('push_promotions');",
		"CREATE TABLE app_settings (key TEXT); INSERT INTO app_settings (key) VALUES ('platform.phase');",
	}
	if includeSubscriptionEntitlements {
		schema = append(schema, "CREATE TABLE subscription_entitlements (entitlement_key TEXT); INSERT INTO subscription_entitlements (entitlement_key) VALUES ('free_entity_favorites'), ('free_basic_match_notifications'), ('pro_insight_watchlist'), ('pro_insight_alerts'), ('pro_advanced_multi_match_builder');")
	}
	schema = append(schema, "CREATE TABLE provider_rate_limits (provider TEXT); INSERT INTO provider_rate_limits (provider) VALUES ('api-football');")
	schema = append(schema, "CREATE TABLE providers (provider TEXT); INSERT INTO providers (provider) VALUES ('api-football');")
	schema = append(schema, "CREATE TABLE provider_endpoint_specs (provider TEXT, endpoint_key TEXT); INSERT INTO provider_endpoint_specs (provider, endpoint_key) VALUES ('api-football', 'countries'), ('api-football', 'leagues'), ('api-football', 'fixtures'), ('api-football', 'fixtures.statistics'), ('api-football', 'odds.pre_match'), ('api-football', 'predictions');")
	schema = append(schema, "CREATE TABLE provider_feature_sources (provider TEXT, feature_key TEXT); INSERT INTO provider_feature_sources (provider, feature_key) VALUES ('api-football', 'corners'), ('api-football', 'cards'), ('api-football', 'fouls'), ('api-football', 'throw_ins'), ('api-football', 'team_over_under_goals'), ('api-football', 'half_time_full_time'), ('api-football', 'over_under_goals'), ('api-football', 'multi_match_builder');")
	schema = append(schema, "CREATE TABLE prediction_market_support (market_key TEXT, enabled BOOLEAN); INSERT INTO prediction_market_support (market_key, enabled) VALUES ('1x2', true), ('double_chance', true), ('btts', true), ('ou_0_5', true), ('ou_1_5', true), ('ou_2_5', true), ('ou_3_5', true), ('ou_4_5', true), ('ou_5_5', true), ('team_ou_goals', true), ('ht_ft', true), ('corners', true), ('cards', true), ('fouls', true), ('throw_ins', true), ('multi_match_builder', false);")
	schema = append(schema, "CREATE TABLE prediction_feature_snapshots (id INTEGER); INSERT INTO prediction_feature_snapshots (id) VALUES (1);")
	schema = append(schema, "CREATE TABLE provider_prediction_benchmarks (id INTEGER);")
	return append(schema, "CREATE TABLE ad_campaigns (campaign_key TEXT); INSERT INTO ad_campaigns (campaign_key) VALUES ('house-foundation');")
}

func foundationSeedTestSchemaWithoutProviderRateLimits() []string {
	schema := []string{
		"CREATE TABLE countries (code TEXT); INSERT INTO countries (code) VALUES ('KE'), ('GB'), ('US');",
		"CREATE TABLE sports (slug TEXT); INSERT INTO sports (slug) VALUES ('football');",
		"CREATE TABLE leagues (slug TEXT); INSERT INTO leagues (slug) VALUES ('kenyan-premier-league');",
		"CREATE TABLE teams (slug TEXT); INSERT INTO teams (slug) VALUES ('gor-mahia'), ('afc-leopards');",
		"CREATE TABLE matches (id INTEGER); INSERT INTO matches (id) VALUES (1);",
		"CREATE TABLE odds_markets (market_key TEXT); INSERT INTO odds_markets (market_key) VALUES ('1x2'), ('double_chance'), ('btts'), ('ou_2_5'), ('corners'), ('cards'), ('fouls'), ('throw_ins'), ('team_ou_goals'), ('ht_ft'), ('ou_0_5'), ('ou_1_5'), ('ou_3_5'), ('ou_4_5'), ('ou_5_5'), ('multi_match_builder');",
		"CREATE TABLE odds_snapshots (id INTEGER); INSERT INTO odds_snapshots (id) VALUES (1);",
		"CREATE TABLE prediction_models (id INTEGER); INSERT INTO prediction_models (id) VALUES (1);",
		"CREATE TABLE predictions (id INTEGER); INSERT INTO predictions (id) VALUES (1);",
		"CREATE TABLE prediction_versions (id INTEGER); INSERT INTO prediction_versions (id) VALUES (1);",
		"CREATE TABLE prediction_outcomes (id INTEGER); INSERT INTO prediction_outcomes (id) VALUES (1);",
		"CREATE TABLE prediction_feature_snapshots (id INTEGER); INSERT INTO prediction_feature_snapshots (id) VALUES (1);",
		"CREATE TABLE provider_prediction_benchmarks (id INTEGER);",
		"CREATE TABLE users (id INTEGER); INSERT INTO users (id) VALUES (1), (2);",
		"CREATE TABLE feature_flags (feature_key TEXT); INSERT INTO feature_flags (feature_key) VALUES ('public_content'), ('predictions'), ('odds_display'), ('bookmaker_ctas'), ('affiliate_links'), ('paid_promotions'), ('gambling_ads'), ('subscriptions'), ('push_promotions');",
		"CREATE TABLE app_settings (key TEXT); INSERT INTO app_settings (key) VALUES ('platform.phase');",
		"CREATE TABLE subscription_entitlements (entitlement_key TEXT); INSERT INTO subscription_entitlements (entitlement_key) VALUES ('free_entity_favorites'), ('free_basic_match_notifications'), ('pro_insight_watchlist'), ('pro_insight_alerts'), ('pro_advanced_multi_match_builder');",
		"CREATE TABLE ad_campaigns (campaign_key TEXT); INSERT INTO ad_campaigns (campaign_key) VALUES ('house-foundation');",
	}
	return schema
}

func foundationSeedTestSchemaWithoutProviderEndpointSpecs() []string {
	schema := foundationSeedTestSchema(true)
	filtered := make([]string, 0, len(schema))
	for _, stmt := range schema {
		if strings.Contains(stmt, "provider_endpoint_specs") || strings.Contains(stmt, "provider_feature_sources") {
			continue
		}
		filtered = append(filtered, stmt)
	}
	return filtered
}

func foundationSeedTestSchemaWithoutPredictionMarketSupport() []string {
	schema := foundationSeedTestSchema(true)
	filtered := make([]string, 0, len(schema))
	for _, stmt := range schema {
		if strings.Contains(stmt, "prediction_market_support") {
			continue
		}
		filtered = append(filtered, stmt)
	}
	return filtered
}

func foundationSeedTestSchemaWithoutPredictionWorkflowRows() []string {
	schema := foundationSeedTestSchema(true)
	filtered := make([]string, 0, len(schema))
	for _, stmt := range schema {
		if strings.Contains(stmt, "prediction_feature_snapshots") {
			continue
		}
		filtered = append(filtered, stmt)
	}
	return filtered
}

func foundationSeedTestSchemaWithoutPredictionBenchmarks() []string {
	schema := foundationSeedTestSchema(true)
	filtered := make([]string, 0, len(schema))
	for _, stmt := range schema {
		if strings.Contains(stmt, "provider_prediction_benchmarks") {
			continue
		}
		filtered = append(filtered, stmt)
	}
	return filtered
}

func predictionEngineContractTestSchema() []string {
	return []string{
		`CREATE TABLE fixtures (
			id INTEGER, league_id INTEGER, season_id INTEGER, home_team_id INTEGER, away_team_id INTEGER,
			kickoff_at TEXT, status TEXT, venue_name TEXT, referee TEXT, round TEXT,
			home_goals_ft INTEGER, away_goals_ft INTEGER, home_goals_ht INTEGER, away_goals_ht INTEGER
		);`,
		`CREATE TABLE fixture_statistics (
			fixture_id INTEGER, team_id INTEGER, shots_on_target INTEGER, shots_total INTEGER,
			shots_blocked INTEGER, shots_inside_box INTEGER, shots_outside_box INTEGER,
			possession_pct REAL, corners INTEGER, fouls INTEGER, offsides INTEGER,
			yellow_cards INTEGER, red_cards INTEGER, goalkeeper_saves INTEGER,
			passes_total INTEGER, passes_accurate INTEGER, pass_accuracy REAL, xg REAL
		);`,
		`CREATE TABLE team_season_stats (
			team_id INTEGER, league_id INTEGER, season_id INTEGER,
			played_home INTEGER, wins_home INTEGER, draws_home INTEGER, losses_home INTEGER,
			goals_for_home INTEGER, goals_against_home INTEGER, clean_sheets_home INTEGER, failed_to_score_home INTEGER,
			played_away INTEGER, wins_away INTEGER, draws_away INTEGER, losses_away INTEGER,
			goals_for_away INTEGER, goals_against_away INTEGER, clean_sheets_away INTEGER, failed_to_score_away INTEGER
		);`,
		`CREATE TABLE head_to_head (
			home_team_id INTEGER, away_team_id INTEGER, fixture_id INTEGER, played_at TEXT,
			home_goals INTEGER, away_goals INTEGER, venue TEXT
		);`,
		`CREATE TABLE standings (
			league_id INTEGER, season_id INTEGER, team_id INTEGER, rank INTEGER, points INTEGER,
			played INTEGER, wins INTEGER, draws INTEGER, losses INTEGER, goals_for INTEGER,
			goals_against INTEGER, goal_diff INTEGER, form TEXT
		);`,
		`CREATE TABLE player_availability (
			fixture_id INTEGER, team_id INTEGER, player_name TEXT, reason TEXT, status TEXT
		);`,
		`CREATE TABLE odds (
			fixture_id INTEGER, bookmaker TEXT, market TEXT, outcome TEXT, value REAL,
			implied_prob REAL, fetched_at TEXT
		);`,
		`CREATE TABLE referee_stats (
			name TEXT, matches_officiated INTEGER, avg_yellow_cards REAL, avg_red_cards REAL, avg_fouls REAL
		);`,
		`CREATE TABLE league_coverage (
			league_id INTEGER, season_id INTEGER, has_statistics INTEGER, has_xg INTEGER,
			has_injuries INTEGER, has_odds INTEGER, has_lineups INTEGER
		);`,
		`CREATE TABLE api_predictions (
			fixture_id INTEGER, predicted_winner TEXT, win_or_draw INTEGER, under_over TEXT,
			percent_home REAL, percent_draw REAL, percent_away REAL, advice TEXT
		);`,
		`CREATE TABLE engine_predictions (
			id TEXT, public_id INTEGER, fixture_id INTEGER, market TEXT, prediction TEXT,
			probability REAL, confidence REAL, confidence_tier TEXT, is_best_bet INTEGER,
			lifecycle_stage TEXT, data_completeness REAL, features_used TEXT, explanation TEXT,
			generated_at TEXT, superseded_at TEXT, gate_tier TEXT, horizon_eligible INTEGER,
			embargo_active INTEGER, model_version TEXT, content_hash TEXT,
			last_evaluated_at TEXT, updated_at TEXT
		);`,
		`CREATE TABLE prediction_results (
			prediction_id TEXT, fixture_id INTEGER, market TEXT, predicted TEXT, actual TEXT,
			is_correct INTEGER, settlement_status TEXT, probability REAL, confidence REAL, confidence_tier TEXT,
			lifecycle_stage_at_settlement TEXT, league_id INTEGER, settled_at TEXT
		);`,
		`CREATE TABLE accuracy_snapshots (
			snapshot_date TEXT, scope TEXT, scope_id TEXT, total_predictions INTEGER, correct INTEGER, accuracy REAL
		);`,
		`CREATE TABLE prediction_accuracy_daily (
			aggregate_date TEXT, market TEXT, league_id INTEGER, confidence_tier TEXT,
			model_version TEXT, total_predictions INTEGER, correct INTEGER, incorrect INTEGER,
			voided INTEGER, pending INTEGER, first_settled_at TEXT, last_settled_at TEXT, aggregated_at TEXT
		);`,
		`CREATE TABLE prediction_result_rollup_ledger (
			prediction_id TEXT, included_in_accuracy INTEGER, aggregated_at TEXT
		);`,
	}
}

func predictionEngineContractTestSchemaWithoutEnginePredictionPublicID() []string {
	schema := predictionEngineContractTestSchema()
	for index, stmt := range schema {
		if strings.Contains(stmt, "CREATE TABLE engine_predictions") {
			schema[index] = `CREATE TABLE engine_predictions (
				id TEXT, fixture_id INTEGER, market TEXT, prediction TEXT,
				probability REAL, confidence REAL, confidence_tier TEXT, is_best_bet INTEGER,
				lifecycle_stage TEXT, data_completeness REAL, features_used TEXT, explanation TEXT,
				generated_at TEXT, superseded_at TEXT, gate_tier TEXT, horizon_eligible INTEGER,
				embargo_active INTEGER, model_version TEXT, content_hash TEXT,
				last_evaluated_at TEXT, updated_at TEXT
			);`
		}
	}
	return schema
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func openSQLiteLikeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
