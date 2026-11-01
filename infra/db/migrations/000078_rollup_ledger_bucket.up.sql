-- Let a reversed settlement be un-counted from published accuracy (§14).
--
-- 000077 added `needs_recount`, which marks a prediction whose settlement moved
-- after it had already been folded into `prediction_accuracy_daily`. Acting on
-- that mark needs three things the ledger does not currently record, and two of
-- them cannot be recovered later:
--
--   * WHICH BUCKET the prediction was counted into. The rollup derives it from
--     `prediction_results.settled_at` and the prediction's market, league, tier
--     and model version -- but `aggregate_and_prune` DELETES the
--     `prediction_results` row after aggregating it, and the default raw
--     retention is two days. A reversal arriving after that has no row to read.
--   * WHAT IT WAS COUNTED AS. The aggregate holds only counters, so nothing says
--     whether this prediction contributed to `correct`, `incorrect`, `voided` or
--     `pending`. Without it a recount cannot decrement the right column.
--   * That the bucket must not move. `resettle_fixture` updates `settled_at`
--     when it writes a change, so deriving the date at recount time could land
--     on a different day from the one the rollup actually incremented, and the
--     decrement would corrupt a bucket it never touched.
--
-- So the ledger records where it counted, at the moment it counts. It is the
-- record of what was counted; it should say where.
--
-- All columns are nullable: rows written before this migration cannot be
-- reconstructed, and the recount reports them as unrecountable rather than
-- guessing a bucket and corrupting a real one.
ALTER TABLE prediction_result_rollup_ledger
  ADD COLUMN IF NOT EXISTS aggregate_date DATE,
  ADD COLUMN IF NOT EXISTS market TEXT,
  ADD COLUMN IF NOT EXISTS league_id BIGINT,
  ADD COLUMN IF NOT EXISTS confidence_tier TEXT,
  ADD COLUMN IF NOT EXISTS model_version TEXT,
  ADD COLUMN IF NOT EXISTS counted_as TEXT;

-- Matches the four counter columns on prediction_accuracy_daily. NULL means
-- either a pre-migration row or one the rollup recorded as not included in
-- accuracy at all.
ALTER TABLE prediction_result_rollup_ledger
  DROP CONSTRAINT IF EXISTS prediction_result_rollup_ledger_counted_as_check;

ALTER TABLE prediction_result_rollup_ledger
  ADD CONSTRAINT prediction_result_rollup_ledger_counted_as_check
  CHECK (counted_as IS NULL OR counted_as IN ('correct', 'incorrect', 'voided', 'pending'));

COMMENT ON COLUMN prediction_result_rollup_ledger.aggregate_date IS
  'The prediction_accuracy_daily bucket this prediction was counted into, '
  'recorded at rollup time because prediction_results is pruned afterwards and '
  'settled_at can move when a settlement is revised.';

COMMENT ON COLUMN prediction_result_rollup_ledger.counted_as IS
  'Which prediction_accuracy_daily counter this prediction incremented: '
  'correct, incorrect, voided or pending. NULL for rows written before the '
  'column existed, or for predictions not included in accuracy.';

-- The recount sweep reads flagged rows only, so a partial index keeps it off a
-- table that is otherwise one row per settled prediction for ever.
CREATE INDEX IF NOT EXISTS idx_prediction_result_rollup_ledger_recount_bucket
  ON prediction_result_rollup_ledger (aggregate_date, market, league_id)
  WHERE needs_recount;
