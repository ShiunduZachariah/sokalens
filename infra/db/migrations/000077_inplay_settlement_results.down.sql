-- Reverses 000077. The widened constraints go back to their pre-in-play form,
-- which will fail if any in-play settlement row exists -- correctly, because
-- narrowing a constraint under live data must not silently drop rows. Settle or
-- delete those rows first.
DROP INDEX IF EXISTS idx_prediction_result_rollup_ledger_needs_recount;

ALTER TABLE prediction_result_rollup_ledger
  DROP COLUMN IF EXISTS needs_recount;

DROP INDEX IF EXISTS idx_prediction_result_revisions_recent;
DROP INDEX IF EXISTS idx_prediction_result_revisions_prediction;
DROP TABLE IF EXISTS prediction_result_revisions;

DROP INDEX IF EXISTS idx_prediction_results_settled_by_event_keys;

ALTER TABLE prediction_results
  DROP COLUMN IF EXISTS settled_by_event_keys;

ALTER TABLE prediction_results
  DROP CONSTRAINT IF EXISTS prediction_results_settlement_status_check;

ALTER TABLE prediction_results
  ADD CONSTRAINT prediction_results_settlement_status_check
  CHECK (settlement_status IN ('settled', 'void', 'unsettled_data_unavailable'));

ALTER TABLE prediction_results
  DROP CONSTRAINT IF EXISTS prediction_results_probability_check;

ALTER TABLE prediction_results
  ADD CONSTRAINT prediction_results_probability_check
  CHECK (probability >= 0.01 AND probability <= 0.99);

ALTER TABLE engine_predictions
  DROP CONSTRAINT IF EXISTS engine_predictions_probability_check;

ALTER TABLE engine_predictions
  ADD CONSTRAINT engine_predictions_probability_check
  CHECK (probability >= 0.01 AND probability <= 0.99);

ALTER TABLE prediction_results
  DROP CONSTRAINT IF EXISTS prediction_results_lifecycle_stage_at_settlement_check;

ALTER TABLE prediction_results
  ADD CONSTRAINT prediction_results_lifecycle_stage_at_settlement_check
  CHECK (lifecycle_stage_at_settlement IN ('EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED'));
