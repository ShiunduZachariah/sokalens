DROP INDEX IF EXISTS idx_engine_predictions_lifecycle_stability;

ALTER TABLE engine_predictions
  DROP CONSTRAINT IF EXISTS engine_predictions_lifecycle_first_version_neutral,
  DROP CONSTRAINT IF EXISTS engine_predictions_lifecycle_max_covers_delta,
  DROP CONSTRAINT IF EXISTS engine_predictions_lifecycle_metadata_complete,
  DROP CONSTRAINT IF EXISTS engine_predictions_lifecycle_previous_delta_pair,
  DROP CONSTRAINT IF EXISTS engine_predictions_lifecycle_stability_score_range,
  DROP CONSTRAINT IF EXISTS engine_predictions_max_lifecycle_shift_range,
  DROP CONSTRAINT IF EXISTS engine_predictions_probability_delta_range,
  DROP CONSTRAINT IF EXISTS engine_predictions_previous_probability_range;

ALTER TABLE engine_predictions
  DROP COLUMN IF EXISTS lifecycle_stability_score,
  DROP COLUMN IF EXISTS prediction_direction_changed,
  DROP COLUMN IF EXISTS max_lifecycle_probability_shift,
  DROP COLUMN IF EXISTS probability_delta,
  DROP COLUMN IF EXISTS previous_probability;
