-- Safe only before v2 writer/backfill rollout. After governance data exists,
-- production rollback is a forward fix plus logical model deactivation.
ALTER TABLE engine_predictions
  DROP CONSTRAINT IF EXISTS engine_predictions_recommendation_reason_nonempty,
  DROP CONSTRAINT IF EXISTS engine_predictions_feature_version_nonempty,
  DROP CONSTRAINT IF EXISTS engine_predictions_stability_score_range,
  DROP CONSTRAINT IF EXISTS engine_predictions_ood_score_range,
  DROP CONSTRAINT IF EXISTS engine_predictions_model_agreement_range,
  DROP CONSTRAINT IF EXISTS engine_predictions_calibrated_probability_range,
  DROP CONSTRAINT IF EXISTS engine_predictions_raw_probability_range,
  DROP CONSTRAINT IF EXISTS engine_predictions_as_of_cutoff_order,
  DROP CONSTRAINT IF EXISTS engine_predictions_odds_snapshot_fk,
  DROP CONSTRAINT IF EXISTS engine_predictions_model_registry_fk;

ALTER TABLE engine_predictions
  DROP COLUMN IF EXISTS explanation_factors,
  DROP COLUMN IF EXISTS value_edge,
  DROP COLUMN IF EXISTS odds_snapshot_id,
  DROP COLUMN IF EXISTS recommendation_reason,
  DROP COLUMN IF EXISTS recommendation_score,
  DROP COLUMN IF EXISTS stability_score,
  DROP COLUMN IF EXISTS ood_score,
  DROP COLUMN IF EXISTS model_agreement_score,
  DROP COLUMN IF EXISTS calibrated_probability,
  DROP COLUMN IF EXISTS raw_probability,
  DROP COLUMN IF EXISTS feature_version,
  DROP COLUMN IF EXISTS data_cutoff_at,
  DROP COLUMN IF EXISTS as_of_timestamp,
  DROP COLUMN IF EXISTS model_registry_id;

DROP INDEX IF EXISTS idx_calibration_models_lookup;
DROP TABLE IF EXISTS calibration_models;
DROP INDEX IF EXISTS idx_market_metadata_active;
DROP TABLE IF EXISTS market_metadata;
DROP TRIGGER IF EXISTS model_registry_identity_immutable ON model_registry;
DROP FUNCTION IF EXISTS guard_model_registry_identity();
DROP INDEX IF EXISTS idx_model_registry_market_status_created;
DROP INDEX IF EXISTS idx_model_registry_one_production_per_market;
DROP TABLE IF EXISTS model_registry;
