DROP INDEX IF EXISTS idx_prediction_accuracy_daily_period;
DROP INDEX IF EXISTS idx_prediction_results_recent_settled;
DROP INDEX IF EXISTS idx_engine_predictions_current_best_bet_rank;
DROP INDEX IF EXISTS idx_engine_predictions_content_history;
DROP INDEX IF EXISTS idx_engine_predictions_one_current_market;

DROP TRIGGER IF EXISTS engine_predictions_idempotent_insert ON engine_predictions;
DROP FUNCTION IF EXISTS guard_engine_prediction_insert();

ALTER TABLE engine_predictions
  DROP CONSTRAINT IF EXISTS engine_predictions_content_hash_length,
  DROP COLUMN IF EXISTS updated_at,
  DROP COLUMN IF EXISTS last_evaluated_at,
  DROP COLUMN IF EXISTS content_hash;

DROP FUNCTION IF EXISTS engine_prediction_content_hash(
  BIGINT,
  TEXT,
  TEXT,
  NUMERIC,
  NUMERIC,
  TEXT,
  BOOLEAN,
  TEXT,
  NUMERIC,
  JSONB,
  TEXT,
  TEXT,
  BOOLEAN,
  BOOLEAN,
  TEXT
);

DROP TABLE IF EXISTS prediction_maintenance_audit;
DROP TABLE IF EXISTS prediction_accuracy_daily;

-- Duplicate rows removed by the up migration are data changes. Restore the
-- pre-migration pg_dump backup before applying this schema rollback if those
-- historical rows are needed.
