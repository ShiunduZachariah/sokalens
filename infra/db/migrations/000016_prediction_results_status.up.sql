ALTER TABLE prediction_results
  ADD COLUMN IF NOT EXISTS settlement_status TEXT NOT NULL DEFAULT 'settled';

UPDATE prediction_results
SET settlement_status = 'settled'
WHERE settlement_status IS NULL;

ALTER TABLE prediction_results
  ALTER COLUMN actual DROP NOT NULL,
  ALTER COLUMN is_correct DROP NOT NULL;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'prediction_results_settlement_status_check'
  ) THEN
    ALTER TABLE prediction_results
      ADD CONSTRAINT prediction_results_settlement_status_check
      CHECK (settlement_status IN ('settled', 'void', 'unsettled_data_unavailable'));
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_prediction_results_status_settled
  ON prediction_results (settlement_status, settled_at DESC);
