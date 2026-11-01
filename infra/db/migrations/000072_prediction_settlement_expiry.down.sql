-- Dropping void_reason loses the distinction between a market-rule void and a
-- never-resolved one. The void rows themselves survive; only the explanation
-- goes, which is the reversible half.
DROP INDEX IF EXISTS idx_prediction_results_void_reason;

ALTER TABLE prediction_results
  DROP COLUMN IF EXISTS void_reason;
