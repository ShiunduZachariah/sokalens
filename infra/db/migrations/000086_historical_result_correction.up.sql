-- Preserve the exact accuracy bucket and settlement history after raw prediction
-- retention. These UUIDs are provenance, not live parent references.
ALTER TABLE prediction_result_rollup_ledger
  DROP CONSTRAINT IF EXISTS prediction_result_rollup_ledger_prediction_id_fkey;
ALTER TABLE prediction_result_revisions
  DROP CONSTRAINT IF EXISTS prediction_result_revisions_prediction_id_fkey;

-- A reviewed operation may be retried, but must never append the same repair
-- twice. Ordinary provider revisions leave these nullable columns empty.
ALTER TABLE prediction_result_revisions
  ADD COLUMN correction_operation_id UUID,
  ADD COLUMN correction_context JSONB;
ALTER TABLE prediction_result_revisions
  ADD CONSTRAINT prediction_result_revisions_correction_context_check
  CHECK ((correction_operation_id IS NULL AND correction_context IS NULL)
      OR (correction_operation_id IS NOT NULL AND correction_context IS NOT NULL
          AND jsonb_typeof(correction_context) = 'object'));
CREATE UNIQUE INDEX idx_prediction_result_revisions_correction_operation
  ON prediction_result_revisions (prediction_id, correction_operation_id)
  WHERE correction_operation_id IS NOT NULL;

COMMENT ON COLUMN prediction_result_revisions.correction_context IS
  'Audited historical correction provenance: algorithm, official score, prior result and reviewed manifest hash. Never use for provider retractions.';

-- Immutable initial evidence remains untouched. A later legitimate provider
-- revision naturally supersedes a manual correction in this projection.
CREATE VIEW effective_model_evaluation_facts AS
SELECT
  facts.prediction_id, facts.model_registry_id, facts.fixture_id,
  facts.market, facts.selection, facts.lifecycle_stage,
  facts.prediction_timestamp, facts.data_cutoff_at, facts.feature_version,
  facts.raw_probability, facts.calibrated_probability, facts.confidence,
  CASE WHEN newest.settlement_status = 'settled' AND newest.actual IS NOT NULL
    THEN newest.actual
    WHEN newest.settlement_status IN ('void', 'unsettled_data_unavailable', 'settlement_reversed')
    THEN newest.settlement_status
    ELSE facts.final_outcome END AS final_outcome,
  CASE WHEN newest.settlement_status = 'settled' AND newest.is_correct IS NOT NULL
    THEN CASE WHEN newest.is_correct THEN 'correct' ELSE 'incorrect' END
    WHEN newest.settlement_status = 'void' THEN 'void'
    WHEN newest.settlement_status IN ('unsettled_data_unavailable', 'settlement_reversed') THEN 'excluded'
    ELSE facts.settlement_classification END AS settlement_classification,
  facts.settled_at, facts.odds_snapshot_id,
  CASE WHEN newest.settlement_status IN ('void', 'unsettled_data_unavailable', 'settlement_reversed')
    THEN false ELSE facts.evaluation_eligible END AS evaluation_eligible,
  CASE WHEN newest.settlement_status IN ('void', 'unsettled_data_unavailable', 'settlement_reversed')
    THEN coalesce(newest.void_reason, newest.settlement_status)
    ELSE facts.exclusion_reason END AS exclusion_reason,
  facts.evaluation_contract_version, facts.created_at,
  newest.id AS effective_revision_id,
  newest.recorded_at AS effective_revision_at
FROM model_evaluation_facts AS facts
LEFT JOIN LATERAL (
  SELECT revisions.id, revisions.recorded_at, revisions.settlement_status,
         revisions.actual, revisions.is_correct, revisions.void_reason
  FROM prediction_result_revisions AS revisions
  WHERE revisions.prediction_id = facts.prediction_id
  ORDER BY revisions.recorded_at DESC, revisions.id DESC
  LIMIT 1
) AS newest ON TRUE;

COMMENT ON VIEW effective_model_evaluation_facts IS
  'Original immutable model evidence with the latest audited settlement revision projected over settlement-derived fields. Evaluation readers must use this view.';
