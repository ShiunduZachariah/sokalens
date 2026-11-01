DROP VIEW IF EXISTS retention_candidates;
DROP VIEW IF EXISTS current_engine_predictions;

DROP INDEX IF EXISTS idx_prediction_accuracy_daily_retention;
DROP INDEX IF EXISTS idx_model_evaluation_facts_retention;
DROP INDEX IF EXISTS idx_engine_predictions_fixture_generated;
DROP INDEX IF EXISTS idx_fixtures_finished_at;

ALTER TABLE prediction_result_rollup_ledger
  DROP CONSTRAINT IF EXISTS prediction_result_rollup_ledger_prediction_id_fkey;
ALTER TABLE prediction_result_rollup_ledger
  ADD CONSTRAINT prediction_result_rollup_ledger_prediction_id_fkey
  FOREIGN KEY (prediction_id) REFERENCES engine_predictions(id) ON DELETE RESTRICT;

DROP TRIGGER IF EXISTS model_evaluation_facts_immutable ON model_evaluation_facts;
DROP FUNCTION IF EXISTS reject_model_evidence_update();
CREATE TRIGGER model_evaluation_facts_immutable
BEFORE UPDATE OR DELETE ON model_evaluation_facts
FOR EACH ROW EXECUTE FUNCTION reject_model_evidence_mutation();

ALTER TABLE model_evaluation_facts
  ADD CONSTRAINT model_evaluation_facts_prediction_id_fkey
  FOREIGN KEY (prediction_id) REFERENCES engine_predictions(id) ON DELETE RESTRICT;

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

ALTER TABLE fixtures
  DROP COLUMN IF EXISTS finished_at;
