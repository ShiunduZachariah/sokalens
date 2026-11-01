CREATE TABLE model_evaluation_facts (
  prediction_id UUID PRIMARY KEY REFERENCES engine_predictions(id) ON DELETE RESTRICT,
  model_registry_id UUID NOT NULL REFERENCES model_registry(id) ON DELETE RESTRICT,
  fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE RESTRICT,
  market TEXT NOT NULL,
  selection TEXT NOT NULL,
  lifecycle_stage TEXT NOT NULL,
  prediction_timestamp TIMESTAMPTZ NOT NULL,
  data_cutoff_at TIMESTAMPTZ,
  feature_version TEXT,
  raw_probability NUMERIC(8,6) NOT NULL,
  calibrated_probability NUMERIC(8,6),
  confidence NUMERIC(8,6) NOT NULL,
  final_outcome TEXT NOT NULL,
  settlement_classification TEXT NOT NULL,
  settled_at TIMESTAMPTZ NOT NULL,
  odds_snapshot_id BIGINT REFERENCES odds_snapshots(id) ON DELETE RESTRICT,
  evaluation_eligible BOOLEAN NOT NULL,
  exclusion_reason TEXT,
  evaluation_contract_version TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (btrim(market) <> ''),
  CHECK (btrim(selection) <> ''),
  CHECK (lifecycle_stage IN ('EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED')),
  CHECK (data_cutoff_at IS NULL OR data_cutoff_at <= prediction_timestamp),
  CHECK (feature_version IS NULL OR btrim(feature_version) <> ''),
  CHECK (raw_probability >= 0 AND raw_probability <= 1),
  CHECK (calibrated_probability IS NULL OR (calibrated_probability >= 0 AND calibrated_probability <= 1)),
  CHECK (confidence >= 0 AND confidence <= 1),
  CHECK (btrim(final_outcome) <> ''),
  CHECK (settlement_classification IN ('correct', 'incorrect', 'void', 'push', 'excluded')),
  CHECK (settled_at >= prediction_timestamp),
  CHECK ((evaluation_eligible AND exclusion_reason IS NULL) OR
         (NOT evaluation_eligible AND exclusion_reason IS NOT NULL AND btrim(exclusion_reason) <> '')),
  CHECK (btrim(evaluation_contract_version) <> '')
);

CREATE INDEX idx_model_evaluation_facts_model_settled
  ON model_evaluation_facts (model_registry_id, market, settled_at DESC);

CREATE INDEX idx_model_evaluation_facts_eligible_settled
  ON model_evaluation_facts (market, lifecycle_stage, settled_at DESC)
  WHERE evaluation_eligible = true;

COMMENT ON TABLE model_evaluation_facts IS
  'Compact immutable model evidence. It has no user/provider payload and is never cascaded by public-retention cleanup.';
COMMENT ON COLUMN model_evaluation_facts.model_registry_id IS
  'Required governance identity. Settlement dual-write must remain disabled until ENG-003 bootstraps and links the v1 champion.';

CREATE OR REPLACE FUNCTION reject_model_evidence_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $function$
BEGIN
  RAISE EXCEPTION '% rows are immutable; use the audited governance retention process', TG_TABLE_NAME;
END
$function$;

CREATE TRIGGER model_evaluation_facts_immutable
BEFORE UPDATE OR DELETE ON model_evaluation_facts
FOR EACH ROW EXECUTE FUNCTION reject_model_evidence_mutation();

CREATE TABLE shadow_settlements (
  prediction_id UUID NOT NULL,
  model_registry_id UUID NOT NULL REFERENCES model_registry(id) ON DELETE RESTRICT,
  fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE RESTRICT,
  market TEXT NOT NULL,
  selection TEXT NOT NULL,
  lifecycle_stage TEXT NOT NULL,
  raw_probability NUMERIC(8,6) NOT NULL,
  calibrated_probability NUMERIC(8,6),
  final_outcome TEXT NOT NULL,
  settlement_classification TEXT NOT NULL,
  evaluation_eligible BOOLEAN NOT NULL,
  exclusion_reason TEXT,
  settlement_contract_version TEXT NOT NULL,
  predicted_at TIMESTAMPTZ NOT NULL,
  settled_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (prediction_id, model_registry_id, settlement_contract_version),
  CHECK (btrim(market) <> ''),
  CHECK (btrim(selection) <> ''),
  CHECK (lifecycle_stage IN ('EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED')),
  CHECK (raw_probability >= 0 AND raw_probability <= 1),
  CHECK (calibrated_probability IS NULL OR (calibrated_probability >= 0 AND calibrated_probability <= 1)),
  CHECK (btrim(final_outcome) <> ''),
  CHECK (settlement_classification IN ('correct', 'incorrect', 'void', 'push', 'excluded')),
  CHECK ((evaluation_eligible AND exclusion_reason IS NULL) OR
         (NOT evaluation_eligible AND exclusion_reason IS NOT NULL AND btrim(exclusion_reason) <> '')),
  CHECK (btrim(settlement_contract_version) <> ''),
  CHECK (settled_at >= predicted_at)
);

CREATE INDEX idx_shadow_settlements_model_market_settled
  ON shadow_settlements (model_registry_id, market, settled_at DESC);

CREATE TRIGGER shadow_settlements_immutable
BEFORE UPDATE OR DELETE ON shadow_settlements
FOR EACH ROW EXECUTE FUNCTION reject_model_evidence_mutation();

CREATE TABLE champion_challenger_evaluations (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  market_family TEXT NOT NULL,
  lifecycle_stage TEXT NOT NULL,
  champion_model_id UUID NOT NULL REFERENCES model_registry(id) ON DELETE RESTRICT,
  challenger_model_id UUID NOT NULL REFERENCES model_registry(id) ON DELETE RESTRICT,
  window_start TIMESTAMPTZ NOT NULL,
  window_end TIMESTAMPTZ NOT NULL,
  evaluation_contract_version TEXT NOT NULL,
  sample_count BIGINT NOT NULL,
  champion_metrics JSONB NOT NULL,
  challenger_metrics JSONB NOT NULL,
  gate_results JSONB NOT NULL,
  decision TEXT NOT NULL,
  decision_reason TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (market_family, lifecycle_stage, champion_model_id, challenger_model_id,
          window_start, window_end, evaluation_contract_version),
  CHECK (btrim(market_family) <> ''),
  CHECK (lifecycle_stage IN ('EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED')),
  CHECK (champion_model_id <> challenger_model_id),
  CHECK (window_end > window_start),
  CHECK (btrim(evaluation_contract_version) <> ''),
  CHECK (sample_count >= 0),
  CHECK (decision IN ('pending', 'promote', 'reject', 'extend')),
  CHECK (btrim(decision_reason) <> '')
);

CREATE INDEX idx_champion_challenger_market_window
  ON champion_challenger_evaluations (market_family, window_end DESC);

CREATE TRIGGER champion_challenger_evaluations_immutable
BEFORE UPDATE OR DELETE ON champion_challenger_evaluations
FOR EACH ROW EXECUTE FUNCTION reject_model_evidence_mutation();

CREATE TABLE drift_alerts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  dedupe_key TEXT NOT NULL UNIQUE,
  model_registry_id UUID NOT NULL REFERENCES model_registry(id) ON DELETE RESTRICT,
  market_family TEXT NOT NULL,
  drift_type TEXT NOT NULL,
  severity TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'open',
  window_start TIMESTAMPTZ NOT NULL,
  window_end TIMESTAMPTZ NOT NULL,
  observed_value NUMERIC,
  threshold_value NUMERIC,
  evidence JSONB NOT NULL DEFAULT '{}',
  detected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  acknowledged_at TIMESTAMPTZ,
  resolved_at TIMESTAMPTZ,
  resolution_note TEXT,
  CHECK (btrim(dedupe_key) <> ''),
  CHECK (btrim(market_family) <> ''),
  CHECK (drift_type IN ('feature', 'prediction', 'calibration', 'performance', 'coverage')),
  CHECK (severity IN ('info', 'warning', 'critical')),
  CHECK (status IN ('open', 'acknowledged', 'resolved', 'suppressed')),
  CHECK (window_end > window_start),
  CHECK (acknowledged_at IS NULL OR acknowledged_at >= detected_at),
  CHECK (resolved_at IS NULL OR resolved_at >= detected_at),
  CHECK (resolved_at IS NULL OR (resolution_note IS NOT NULL AND btrim(resolution_note) <> ''))
);

CREATE INDEX idx_drift_alerts_unresolved
  ON drift_alerts (severity, detected_at DESC)
  WHERE status IN ('open', 'acknowledged');

CREATE INDEX idx_drift_alerts_model_window
  ON drift_alerts (model_registry_id, window_end DESC);
