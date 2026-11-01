DROP INDEX IF EXISTS idx_prediction_outcomes_result;
DROP TABLE IF EXISTS prediction_outcomes;

DROP INDEX IF EXISTS idx_prediction_versions_prediction;
DROP TABLE IF EXISTS prediction_versions;

DROP INDEX IF EXISTS idx_prediction_candidates_status;
DROP INDEX IF EXISTS idx_prediction_candidates_match_market;
DROP TABLE IF EXISTS prediction_candidates;

DROP INDEX IF EXISTS idx_prediction_models_model_key_version;

DROP TABLE IF EXISTS prediction_market_support;
