DROP INDEX IF EXISTS idx_prediction_results_status_settled;

ALTER TABLE prediction_results
  DROP CONSTRAINT IF EXISTS prediction_results_settlement_status_check;

DELETE FROM prediction_results
WHERE actual IS NULL
   OR is_correct IS NULL
   OR settlement_status <> 'settled';

ALTER TABLE prediction_results
  ALTER COLUMN actual SET NOT NULL,
  ALTER COLUMN is_correct SET NOT NULL,
  DROP COLUMN IF EXISTS settlement_status;
