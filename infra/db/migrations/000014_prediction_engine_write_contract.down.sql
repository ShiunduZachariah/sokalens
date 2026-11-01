DROP INDEX IF EXISTS idx_fixture_prediction_horizon_eligible;
DROP INDEX IF EXISTS idx_fixture_prediction_horizon_fixture_checked;
DROP TABLE IF EXISTS fixture_prediction_horizon;

DROP INDEX IF EXISTS idx_acca_selections_generated;
DROP TABLE IF EXISTS acca_selections;

DROP INDEX IF EXISTS idx_accuracy_snapshots_scope_date;
DROP TABLE IF EXISTS accuracy_snapshots;

DROP INDEX IF EXISTS idx_prediction_results_league_settled;
DROP INDEX IF EXISTS idx_prediction_results_market_settled;
DROP TABLE IF EXISTS prediction_results;

DROP INDEX IF EXISTS idx_engine_predictions_market_current;
DROP INDEX IF EXISTS idx_engine_predictions_best_bets;
DROP INDEX IF EXISTS idx_engine_predictions_fixture_current;
DROP TABLE IF EXISTS engine_predictions;
