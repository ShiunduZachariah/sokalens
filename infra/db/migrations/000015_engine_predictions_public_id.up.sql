CREATE SEQUENCE IF NOT EXISTS engine_predictions_public_id_seq;

ALTER TABLE engine_predictions
  ADD COLUMN IF NOT EXISTS public_id BIGINT;

UPDATE engine_predictions
SET public_id = nextval('engine_predictions_public_id_seq')
WHERE public_id IS NULL;

ALTER TABLE engine_predictions
  ALTER COLUMN public_id SET DEFAULT nextval('engine_predictions_public_id_seq'),
  ALTER COLUMN public_id SET NOT NULL;

ALTER SEQUENCE engine_predictions_public_id_seq
  OWNED BY engine_predictions.public_id;

CREATE UNIQUE INDEX IF NOT EXISTS idx_engine_predictions_public_id
  ON engine_predictions (public_id);
