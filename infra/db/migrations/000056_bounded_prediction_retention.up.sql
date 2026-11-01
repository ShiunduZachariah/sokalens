ALTER TABLE fixtures
  ADD COLUMN IF NOT EXISTS finished_at TIMESTAMPTZ;

-- Only terminal provider states are safe to backfill. Recent or ambiguous
-- fixtures deliberately remain NULL until ingestion observes completion.
UPDATE fixtures
SET finished_at = GREATEST(updated_at, kickoff_at + INTERVAL '2 hours')
WHERE finished_at IS NULL
  AND lower(status) IN (
    'finished', 'ft', 'aet', 'pen', 'awarded', 'walkover',
    'canceled', 'cancelled', 'abandoned'
  );

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
        p_features_used - 'as_of_timestamp',
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

DROP VIEW IF EXISTS current_engine_predictions;
CREATE VIEW current_engine_predictions AS
SELECT *
FROM engine_predictions
WHERE superseded_at IS NULL;

COMMENT ON VIEW current_engine_predictions IS
  'Index-backed current prediction rows. Public reads should use the equivalent superseded_at IS NULL predicate.';

-- Compact model evidence keeps its raw prediction UUID as provenance but is
-- independently retained after the bulky parent row is removed.
ALTER TABLE model_evaluation_facts
  DROP CONSTRAINT IF EXISTS model_evaluation_facts_prediction_id_fkey;

DROP TRIGGER IF EXISTS model_evaluation_facts_immutable ON model_evaluation_facts;
CREATE OR REPLACE FUNCTION reject_model_evidence_update()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $function$
BEGIN
  RAISE EXCEPTION '% rows cannot be updated; governed age deletion is permitted', TG_TABLE_NAME;
END
$function$;

CREATE TRIGGER model_evaluation_facts_immutable
BEFORE UPDATE ON model_evaluation_facts
FOR EACH ROW EXECUTE FUNCTION reject_model_evidence_update();

ALTER TABLE prediction_result_rollup_ledger
  DROP CONSTRAINT IF EXISTS prediction_result_rollup_ledger_prediction_id_fkey;
ALTER TABLE prediction_result_rollup_ledger
  ADD CONSTRAINT prediction_result_rollup_ledger_prediction_id_fkey
  FOREIGN KEY (prediction_id) REFERENCES engine_predictions(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_fixtures_finished_at
  ON fixtures (finished_at, id)
  WHERE finished_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_engine_predictions_fixture_generated
  ON engine_predictions (fixture_id, generated_at, id);

CREATE INDEX IF NOT EXISTS idx_model_evaluation_facts_retention
  ON model_evaluation_facts (settled_at, prediction_id);

CREATE INDEX IF NOT EXISTS idx_prediction_accuracy_daily_retention
  ON prediction_accuracy_daily (aggregate_date);

DROP VIEW IF EXISTS retention_candidates;
CREATE VIEW retention_candidates AS
SELECT
  'raw_engine_predictions'::TEXT AS candidate_type,
  count(*)::BIGINT AS eligible_count,
  min(f.finished_at) AS oldest_at
FROM engine_predictions AS ep
JOIN fixtures AS f ON f.id = ep.fixture_id
WHERE f.finished_at < now() - INTERVAL '24 hours'
UNION ALL
SELECT
  'model_evaluation_facts'::TEXT,
  count(*)::BIGINT,
  min(facts.settled_at)
FROM model_evaluation_facts AS facts
WHERE facts.settled_at < now() - INTERVAL '365 days'
UNION ALL
SELECT
  'prediction_accuracy_daily'::TEXT,
  count(*)::BIGINT,
  min(daily.aggregate_date)::TIMESTAMPTZ
FROM prediction_accuracy_daily AS daily
WHERE daily.aggregate_date < CURRENT_DATE - 365;

COMMENT ON VIEW retention_candidates IS
  'Read-only operational counts. Cleanup is performed only by bounded audited maintenance jobs.';
