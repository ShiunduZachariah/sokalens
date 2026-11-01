DROP VIEW IF EXISTS effective_model_evaluation_facts;
DROP INDEX IF EXISTS idx_prediction_result_revisions_correction_operation;
ALTER TABLE prediction_result_revisions
  DROP CONSTRAINT IF EXISTS prediction_result_revisions_correction_context_check,
  DROP COLUMN IF EXISTS correction_context,
  DROP COLUMN IF EXISTS correction_operation_id;

-- A rollback must fail instead of discarding provenance if raw parents have
-- already been retained away. Add the original constraints only when safe.
ALTER TABLE prediction_result_rollup_ledger
  ADD CONSTRAINT prediction_result_rollup_ledger_prediction_id_fkey
  FOREIGN KEY (prediction_id) REFERENCES engine_predictions(id) ON DELETE CASCADE;
ALTER TABLE prediction_result_revisions
  ADD CONSTRAINT prediction_result_revisions_prediction_id_fkey
  FOREIGN KEY (prediction_id) REFERENCES engine_predictions(id) ON DELETE CASCADE;
