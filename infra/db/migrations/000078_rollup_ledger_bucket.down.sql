-- Reverses 000078. Dropping these columns loses the ability to un-count a
-- reversed settlement whose raw prediction_results row has already been pruned;
-- that information is not recoverable from anywhere else.
DROP INDEX IF EXISTS idx_prediction_result_rollup_ledger_recount_bucket;

ALTER TABLE prediction_result_rollup_ledger
  DROP CONSTRAINT IF EXISTS prediction_result_rollup_ledger_counted_as_check;

ALTER TABLE prediction_result_rollup_ledger
  DROP COLUMN IF EXISTS counted_as,
  DROP COLUMN IF EXISTS model_version,
  DROP COLUMN IF EXISTS confidence_tier,
  DROP COLUMN IF EXISTS league_id,
  DROP COLUMN IF EXISTS market,
  DROP COLUMN IF EXISTS aggregate_date;
