-- Shadow forecasts across every market, beside the 1X2-only challenger_predictions.
-- One row per model, fixture and lifecycle stage, holding a document of market ->
-- selection -> probability. The published champion gets a row too, under its own
-- registry id, so each challenger is compared with what it would have replaced.
-- Nothing public reads these tables.
CREATE TABLE challenger_market_predictions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  model_registry_id UUID NOT NULL REFERENCES model_registry(id) ON DELETE RESTRICT,
  fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE CASCADE,
  origin TEXT NOT NULL,
  lifecycle_stage TEXT NOT NULL,
  markets JSONB NOT NULL,
  fallback_flags TEXT[] NOT NULL DEFAULT '{}',
  as_of_timestamp TIMESTAMPTZ NOT NULL,
  data_cutoff_at TIMESTAMPTZ,
  kickoff_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (model_registry_id, fixture_id, origin, lifecycle_stage),
  CHECK (origin IN ('live', 'replay')),
  CHECK (lifecycle_stage IN ('EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED')),
  CHECK (jsonb_typeof(markets) = 'object'),
  CHECK (as_of_timestamp < kickoff_at),
  CHECK (data_cutoff_at IS NULL OR data_cutoff_at <= as_of_timestamp)
);

CREATE INDEX idx_challenger_market_predictions_fixture
  ON challenger_market_predictions (fixture_id);

CREATE INDEX idx_challenger_market_predictions_origin_as_of
  ON challenger_market_predictions (origin, as_of_timestamp DESC);

-- Immutable evidence: for each market, what happened and the probability the model
-- gave it. fixture_id has no foreign key so this row can never block a fixture merge
-- or removal; the evidence stands on its own.
CREATE TABLE challenger_market_settlements (
  prediction_id UUID NOT NULL,
  model_registry_id UUID NOT NULL REFERENCES model_registry(id) ON DELETE RESTRICT,
  fixture_id BIGINT NOT NULL,
  origin TEXT NOT NULL,
  lifecycle_stage TEXT NOT NULL,
  scored JSONB NOT NULL,
  evaluation_eligible BOOLEAN NOT NULL,
  exclusion_reason TEXT,
  settlement_contract_version TEXT NOT NULL,
  predicted_at TIMESTAMPTZ NOT NULL,
  settled_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (prediction_id, settlement_contract_version),
  CHECK (origin IN ('live', 'replay')),
  CHECK (lifecycle_stage IN ('EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED')),
  CHECK (jsonb_typeof(scored) = 'object'),
  CHECK ((evaluation_eligible AND exclusion_reason IS NULL) OR
         (NOT evaluation_eligible AND exclusion_reason IS NOT NULL AND btrim(exclusion_reason) <> '')),
  CHECK (btrim(settlement_contract_version) <> ''),
  CHECK (settled_at >= predicted_at)
);

CREATE INDEX idx_challenger_market_settlements_report
  ON challenger_market_settlements (origin, settlement_contract_version, fixture_id);

CREATE TRIGGER challenger_market_settlements_immutable
BEFORE UPDATE OR DELETE ON challenger_market_settlements
FOR EACH ROW EXECUTE FUNCTION reject_model_evidence_mutation();

COMMENT ON TABLE challenger_market_predictions IS
  'Zero-weight forecasts across all markets, with the champion alongside. Never published.';
