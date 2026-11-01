DROP INDEX IF EXISTS idx_provider_prediction_benchmarks_match;
DROP TABLE IF EXISTS provider_prediction_benchmarks;

DROP INDEX IF EXISTS idx_predictions_current_model_market;
DROP INDEX IF EXISTS idx_prediction_versions_status;
DROP INDEX IF EXISTS idx_prediction_candidates_unique_snapshot;

DROP INDEX IF EXISTS idx_prediction_feature_snapshots_lookup;
DROP TABLE IF EXISTS prediction_feature_snapshots;

ALTER TABLE matches
  DROP COLUMN IF EXISTS settled_at,
  DROP COLUMN IF EXISTS away_throw_ins,
  DROP COLUMN IF EXISTS home_throw_ins,
  DROP COLUMN IF EXISTS away_fouls,
  DROP COLUMN IF EXISTS home_fouls,
  DROP COLUMN IF EXISTS away_cards,
  DROP COLUMN IF EXISTS home_cards,
  DROP COLUMN IF EXISTS away_corners,
  DROP COLUMN IF EXISTS home_corners,
  DROP COLUMN IF EXISTS half_time_away_score,
  DROP COLUMN IF EXISTS half_time_home_score,
  DROP COLUMN IF EXISTS away_score,
  DROP COLUMN IF EXISTS home_score;
