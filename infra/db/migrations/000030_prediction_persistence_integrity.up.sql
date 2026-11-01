CREATE OR REPLACE FUNCTION engine_prediction_content_hash(
  p_fixture_id BIGINT,
  p_market TEXT,
  p_prediction TEXT,
  p_probability NUMERIC,
  p_confidence NUMERIC,
  p_confidence_tier TEXT,
  p_is_best_bet BOOLEAN,
  p_lifecycle_stage TEXT,
  p_data_completeness NUMERIC,
  p_features_used JSONB,
  p_explanation TEXT,
  p_gate_tier TEXT,
  p_horizon_eligible BOOLEAN,
  p_embargo_active BOOLEAN,
  p_model_version TEXT
) RETURNS TEXT
LANGUAGE SQL
IMMUTABLE
PARALLEL SAFE
STRICT
AS $function$
  SELECT encode(
    digest(
      jsonb_build_array(
        p_fixture_id,
        p_market,
        p_prediction,
        p_probability,
        p_confidence,
        p_confidence_tier,
        p_is_best_bet,
        p_lifecycle_stage,
        p_data_completeness,
        p_features_used,
        p_explanation,
        p_gate_tier,
        p_horizon_eligible,
        p_embargo_active,
        p_model_version
      )::TEXT,
      'sha256'
    ),
    'hex'
  )
$function$;

ALTER TABLE engine_predictions
  ADD COLUMN IF NOT EXISTS content_hash TEXT,
  ADD COLUMN IF NOT EXISTS last_evaluated_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ;

-- Prevent workers from creating or settling versions while keeper mappings are
-- calculated. Migration runners execute this file in one transaction, so the
-- locks are held through constraint creation and released at commit.
LOCK TABLE engine_predictions IN ACCESS EXCLUSIVE MODE;
LOCK TABLE prediction_results IN ACCESS EXCLUSIVE MODE;
LOCK TABLE saved_predictions IN ACCESS EXCLUSIVE MODE;

UPDATE engine_predictions
SET
  content_hash = engine_prediction_content_hash(
    fixture_id,
    market,
    prediction,
    probability,
    confidence,
    confidence_tier,
    is_best_bet,
    lifecycle_stage,
    data_completeness,
    features_used,
    explanation,
    gate_tier,
    horizon_eligible,
    embargo_active,
    model_version
  ),
  last_evaluated_at = COALESCE(last_evaluated_at, generated_at),
  updated_at = COALESCE(updated_at, created_at, generated_at)
WHERE content_hash IS NULL
   OR last_evaluated_at IS NULL
   OR updated_at IS NULL;

CREATE TABLE IF NOT EXISTS prediction_maintenance_audit (
  id BIGSERIAL PRIMARY KEY,
  operation TEXT NOT NULL,
  criteria TEXT NOT NULL,
  rows_before BIGINT NOT NULL,
  duplicate_groups BIGINT NOT NULL DEFAULT 0,
  rows_removed BIGINT NOT NULL DEFAULT 0,
  rows_after BIGINT NOT NULL,
  performed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS prediction_accuracy_daily (
  aggregate_date DATE NOT NULL,
  market TEXT NOT NULL,
  league_id BIGINT NOT NULL,
  confidence_tier TEXT NOT NULL,
  model_version TEXT NOT NULL,
  total_predictions INTEGER NOT NULL,
  correct INTEGER NOT NULL,
  incorrect INTEGER NOT NULL,
  voided INTEGER NOT NULL,
  pending INTEGER NOT NULL,
  first_settled_at TIMESTAMPTZ NOT NULL,
  last_settled_at TIMESTAMPTZ NOT NULL,
  aggregated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (aggregate_date, market, league_id, confidence_tier, model_version),
  CHECK (total_predictions = correct + incorrect + voided + pending),
  CHECK (correct >= 0 AND incorrect >= 0 AND voided >= 0 AND pending >= 0)
);

CREATE TEMP TABLE engine_prediction_duplicate_map ON COMMIT DROP AS
WITH ranked AS (
  SELECT
    id,
    first_value(id) OVER (
      PARTITION BY fixture_id, market, content_hash
      ORDER BY (superseded_at IS NULL) DESC, generated_at DESC, created_at DESC, id DESC
    ) AS keeper_id,
    row_number() OVER (
      PARTITION BY fixture_id, market, content_hash
      ORDER BY (superseded_at IS NULL) DESC, generated_at DESC, created_at DESC, id DESC
    ) AS duplicate_rank
  FROM engine_predictions
)
SELECT id AS duplicate_id, keeper_id
FROM ranked
WHERE duplicate_rank > 1;

DO $block$
BEGIN
  IF EXISTS (
    WITH mapped_results AS (
      SELECT
        COALESCE(duplicates.keeper_id, results.prediction_id) AS target_prediction_id,
        results.*
      FROM prediction_results AS results
      LEFT JOIN engine_prediction_duplicate_map AS duplicates
        ON duplicates.duplicate_id = results.prediction_id
    )
    SELECT 1
    FROM mapped_results AS left_result
    JOIN mapped_results AS right_result
      ON right_result.target_prediction_id = left_result.target_prediction_id
     AND right_result.prediction_id > left_result.prediction_id
    WHERE left_result.fixture_id IS DISTINCT FROM right_result.fixture_id
       OR left_result.market IS DISTINCT FROM right_result.market
       OR left_result.predicted IS DISTINCT FROM right_result.predicted
       OR left_result.actual IS DISTINCT FROM right_result.actual
       OR left_result.is_correct IS DISTINCT FROM right_result.is_correct
       OR left_result.settlement_status IS DISTINCT FROM right_result.settlement_status
       OR left_result.probability IS DISTINCT FROM right_result.probability
       OR left_result.confidence IS DISTINCT FROM right_result.confidence
       OR left_result.confidence_tier IS DISTINCT FROM right_result.confidence_tier
       OR left_result.lifecycle_stage_at_settlement IS DISTINCT FROM right_result.lifecycle_stage_at_settlement
       OR left_result.league_id IS DISTINCT FROM right_result.league_id
  ) THEN
    RAISE EXCEPTION 'prediction deduplication stopped: conflicting settlement rows require manual review';
  END IF;

  IF EXISTS (
    WITH mapped_saved AS (
      SELECT
        saved.id,
        saved.user_id,
        saved.notes,
        COALESCE(duplicates.keeper_id, saved.prediction_id) AS target_prediction_id
      FROM saved_predictions AS saved
      LEFT JOIN engine_prediction_duplicate_map AS duplicates
        ON duplicates.duplicate_id = saved.prediction_id
    )
    SELECT 1
    FROM mapped_saved AS left_saved
    JOIN mapped_saved AS right_saved
      ON right_saved.user_id = left_saved.user_id
     AND right_saved.target_prediction_id = left_saved.target_prediction_id
     AND right_saved.id > left_saved.id
    WHERE left_saved.notes IS DISTINCT FROM right_saved.notes
  ) THEN
    RAISE EXCEPTION 'prediction deduplication stopped: conflicting saved-prediction notes require manual review';
  END IF;
END
$block$;

CREATE TEMP TABLE prediction_result_repoint ON COMMIT DROP AS
SELECT
  prediction_id AS original_prediction_id,
  target_prediction_id,
  row_number() OVER (
    PARTITION BY target_prediction_id
    ORDER BY (prediction_id = target_prediction_id) DESC, settled_at DESC, prediction_id
  ) AS result_rank
FROM (
  SELECT
    results.prediction_id,
    results.settled_at,
    COALESCE(duplicates.keeper_id, results.prediction_id) AS target_prediction_id
  FROM prediction_results AS results
  LEFT JOIN engine_prediction_duplicate_map AS duplicates
    ON duplicates.duplicate_id = results.prediction_id
) AS mapped;

DELETE FROM prediction_results AS results
USING prediction_result_repoint AS repoint
WHERE results.prediction_id = repoint.original_prediction_id
  AND repoint.result_rank > 1;

UPDATE prediction_results AS results
SET prediction_id = repoint.target_prediction_id
FROM prediction_result_repoint AS repoint
WHERE results.prediction_id = repoint.original_prediction_id
  AND repoint.result_rank = 1
  AND repoint.original_prediction_id <> repoint.target_prediction_id;

CREATE TEMP TABLE saved_prediction_repoint ON COMMIT DROP AS
SELECT
  id AS saved_prediction_id,
  target_prediction_id,
  row_number() OVER (
    PARTITION BY user_id, target_prediction_id
    ORDER BY (prediction_id = target_prediction_id) DESC, created_at, id
  ) AS saved_rank
FROM (
  SELECT
    saved.id,
    saved.user_id,
    saved.prediction_id,
    saved.created_at,
    COALESCE(duplicates.keeper_id, saved.prediction_id) AS target_prediction_id
  FROM saved_predictions AS saved
  LEFT JOIN engine_prediction_duplicate_map AS duplicates
    ON duplicates.duplicate_id = saved.prediction_id
) AS mapped;

DELETE FROM saved_predictions AS saved
USING saved_prediction_repoint AS repoint
WHERE saved.id = repoint.saved_prediction_id
  AND repoint.saved_rank > 1;

UPDATE saved_predictions AS saved
SET prediction_id = repoint.target_prediction_id
FROM saved_prediction_repoint AS repoint
WHERE saved.id = repoint.saved_prediction_id
  AND repoint.saved_rank = 1
  AND saved.prediction_id <> repoint.target_prediction_id;

WITH cleanup_counts AS (
  SELECT
    (SELECT count(*) FROM engine_predictions) AS rows_before,
    (SELECT count(DISTINCT keeper_id) FROM engine_prediction_duplicate_map) AS duplicate_groups,
    (SELECT count(*) FROM engine_prediction_duplicate_map) AS rows_removed
), deleted AS (
  DELETE FROM engine_predictions AS predictions
  USING engine_prediction_duplicate_map AS duplicates
  WHERE predictions.id = duplicates.duplicate_id
  RETURNING predictions.id
)
INSERT INTO prediction_maintenance_audit (
  operation,
  criteria,
  rows_before,
  duplicate_groups,
  rows_removed,
  rows_after
)
SELECT
  'deduplicate_engine_predictions',
  'same fixture_id, market, and SHA-256 of prediction content; newest current row retained',
  cleanup_counts.rows_before,
  cleanup_counts.duplicate_groups,
  (SELECT count(*) FROM deleted),
  cleanup_counts.rows_before - (SELECT count(*) FROM deleted)
FROM cleanup_counts;

ALTER TABLE engine_predictions
  ALTER COLUMN content_hash SET NOT NULL,
  ALTER COLUMN last_evaluated_at SET NOT NULL,
  ALTER COLUMN last_evaluated_at SET DEFAULT now(),
  ALTER COLUMN updated_at SET NOT NULL,
  ALTER COLUMN updated_at SET DEFAULT now();

ALTER TABLE engine_predictions
  ADD CONSTRAINT engine_predictions_content_hash_length
  CHECK (length(content_hash) = 64);

CREATE OR REPLACE FUNCTION guard_engine_prediction_insert()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $function$
DECLARE
  latest engine_predictions%ROWTYPE;
BEGIN
  PERFORM pg_advisory_xact_lock(hashtextextended(NEW.fixture_id::TEXT || ':' || NEW.market, 0));
  NEW.content_hash := engine_prediction_content_hash(
    NEW.fixture_id,
    NEW.market,
    NEW.prediction,
    NEW.probability,
    NEW.confidence,
    NEW.confidence_tier,
    NEW.is_best_bet,
    NEW.lifecycle_stage,
    NEW.data_completeness,
    NEW.features_used,
    NEW.explanation,
    NEW.gate_tier,
    NEW.horizon_eligible,
    NEW.embargo_active,
    NEW.model_version
  );
  NEW.last_evaluated_at := COALESCE(NEW.last_evaluated_at, NEW.generated_at);
  NEW.updated_at := COALESCE(NEW.updated_at, NEW.generated_at);

  SELECT predictions.*
  INTO latest
  FROM engine_predictions AS predictions
  WHERE predictions.fixture_id = NEW.fixture_id
    AND predictions.market = NEW.market
  ORDER BY predictions.generated_at DESC, predictions.created_at DESC, predictions.id DESC
  LIMIT 1
  FOR UPDATE;

  IF FOUND
     AND latest.content_hash = NEW.content_hash
     AND (latest.superseded_at IS NULL OR latest.superseded_at = NEW.generated_at) THEN
    UPDATE engine_predictions
    SET
      superseded_at = NULL,
      last_evaluated_at = GREATEST(last_evaluated_at, NEW.last_evaluated_at)
    WHERE id = latest.id;
    RETURN NULL;
  END IF;
  RETURN NEW;
END
$function$;

DROP TRIGGER IF EXISTS engine_predictions_idempotent_insert ON engine_predictions;
CREATE TRIGGER engine_predictions_idempotent_insert
BEFORE INSERT ON engine_predictions
FOR EACH ROW
EXECUTE FUNCTION guard_engine_prediction_insert();

CREATE UNIQUE INDEX IF NOT EXISTS idx_engine_predictions_one_current_market
  ON engine_predictions (fixture_id, market)
  WHERE superseded_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_engine_predictions_content_history
  ON engine_predictions (fixture_id, market, content_hash, generated_at DESC);

CREATE INDEX IF NOT EXISTS idx_engine_predictions_current_best_bet_rank
  ON engine_predictions (confidence DESC, generated_at DESC, fixture_id)
  WHERE superseded_at IS NULL AND is_best_bet = true;

CREATE INDEX IF NOT EXISTS idx_prediction_results_recent_settled
  ON prediction_results (settled_at DESC, market, confidence_tier)
  WHERE settlement_status = 'settled';

CREATE INDEX IF NOT EXISTS idx_prediction_accuracy_daily_period
  ON prediction_accuracy_daily (aggregate_date DESC, market, league_id, confidence_tier, model_version);
