DROP INDEX IF EXISTS idx_engine_predictions_public_id;

ALTER TABLE engine_predictions
  DROP COLUMN IF EXISTS public_id;

DROP SEQUENCE IF EXISTS engine_predictions_public_id_seq;
