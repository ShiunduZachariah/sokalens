-- ENG-008: additive lifecycle-stability evidence. Existing rows stay NULL
-- because their preceding point-in-time prediction cannot be reconstructed
-- safely from the current database state.
ALTER TABLE engine_predictions
  ADD COLUMN previous_probability NUMERIC(8,6),
  ADD COLUMN probability_delta NUMERIC(9,6),
  ADD COLUMN max_lifecycle_probability_shift NUMERIC(8,6),
  ADD COLUMN prediction_direction_changed BOOLEAN,
  ADD COLUMN lifecycle_stability_score NUMERIC(8,6);

ALTER TABLE engine_predictions
  ADD CONSTRAINT engine_predictions_previous_probability_range
    CHECK (previous_probability IS NULL OR (previous_probability >= 0 AND previous_probability <= 1)),
  ADD CONSTRAINT engine_predictions_probability_delta_range
    CHECK (probability_delta IS NULL OR (probability_delta >= -1 AND probability_delta <= 1)),
  ADD CONSTRAINT engine_predictions_max_lifecycle_shift_range
    CHECK (max_lifecycle_probability_shift IS NULL OR
           (max_lifecycle_probability_shift >= 0 AND max_lifecycle_probability_shift <= 1)),
  ADD CONSTRAINT engine_predictions_lifecycle_stability_score_range
    CHECK (lifecycle_stability_score IS NULL OR
           (lifecycle_stability_score >= 0 AND lifecycle_stability_score <= 1)),
  ADD CONSTRAINT engine_predictions_lifecycle_previous_delta_pair
    CHECK ((previous_probability IS NULL) = (probability_delta IS NULL)),
  ADD CONSTRAINT engine_predictions_lifecycle_metadata_complete
    CHECK (
      (max_lifecycle_probability_shift IS NULL AND
       prediction_direction_changed IS NULL AND lifecycle_stability_score IS NULL AND
       previous_probability IS NULL AND probability_delta IS NULL)
      OR
      (max_lifecycle_probability_shift IS NOT NULL AND
       prediction_direction_changed IS NOT NULL AND lifecycle_stability_score IS NOT NULL)
    ),
  ADD CONSTRAINT engine_predictions_lifecycle_max_covers_delta
    CHECK (probability_delta IS NULL OR max_lifecycle_probability_shift >= abs(probability_delta)),
  ADD CONSTRAINT engine_predictions_lifecycle_first_version_neutral
    CHECK (previous_probability IS NOT NULL OR
           max_lifecycle_probability_shift IS NULL OR
           (max_lifecycle_probability_shift = 0 AND
            prediction_direction_changed = false));

CREATE INDEX idx_engine_predictions_lifecycle_stability
  ON engine_predictions (market, lifecycle_stage, lifecycle_stability_score, generated_at DESC)
  WHERE lifecycle_stability_score IS NOT NULL;

COMMENT ON COLUMN engine_predictions.previous_probability IS
  'Effective calibrated probability from the immediately preceding current fixture/market version.';
COMMENT ON COLUMN engine_predictions.probability_delta IS
  'Signed current effective probability minus previous_probability.';
COMMENT ON COLUMN engine_predictions.max_lifecycle_probability_shift IS
  'Largest absolute probability delta observed through this lifecycle version.';
COMMENT ON COLUMN engine_predictions.prediction_direction_changed IS
  'True when the leading selection differs from the immediately preceding version.';
COMMENT ON COLUMN engine_predictions.lifecycle_stability_score IS
  'ENG-008 stability evidence in [0,1]; distinct from legacy v2 stability_score. Not yet used for confidence.';
