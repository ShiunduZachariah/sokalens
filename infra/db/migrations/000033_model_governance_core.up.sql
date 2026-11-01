-- Prediction-engine v2 governance is additive. Legacy rows remain valid and
-- intentionally keep unknown v2 metadata NULL.
CREATE TABLE model_registry (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  model_key TEXT NOT NULL,
  model_version TEXT NOT NULL,
  market_family TEXT NOT NULL,
  runtime TEXT NOT NULL,
  artifact_uri TEXT,
  artifact_hash TEXT,
  configuration_hash TEXT,
  feature_version TEXT NOT NULL,
  training_start TIMESTAMPTZ,
  training_end TIMESTAMPTZ,
  training_cutoff_at TIMESTAMPTZ,
  validation_window JSONB NOT NULL DEFAULT '{}',
  hyperparameters JSONB NOT NULL DEFAULT '{}',
  training_dataset_hash TEXT,
  code_commit TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'candidate',
  historical_metadata_approximate BOOLEAN NOT NULL DEFAULT false,
  audit_metadata JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  promoted_at TIMESTAMPTZ,
  retired_at TIMESTAMPTZ,
  UNIQUE (model_key, model_version, market_family),
  CHECK (btrim(model_key) <> ''),
  CHECK (btrim(model_version) <> ''),
  CHECK (btrim(market_family) <> ''),
  CHECK (btrim(runtime) <> ''),
  CHECK (btrim(feature_version) <> ''),
  CHECK (btrim(code_commit) <> ''),
  CHECK (status IN ('candidate', 'shadow', 'production', 'retired')),
  CHECK (training_end IS NULL OR training_start IS NULL OR training_end >= training_start),
  CHECK (training_cutoff_at IS NULL OR training_end IS NULL OR training_cutoff_at >= training_end),
  CHECK (promoted_at IS NULL OR status IN ('production', 'retired')),
  CHECK (retired_at IS NULL OR status = 'retired')
);

CREATE UNIQUE INDEX idx_model_registry_one_production_per_market
  ON model_registry (market_family)
  WHERE status = 'production';

CREATE INDEX idx_model_registry_market_status_created
  ON model_registry (market_family, status, created_at DESC);

CREATE OR REPLACE FUNCTION guard_model_registry_identity()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $function$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'model registry rows cannot be deleted; retire the model instead';
  END IF;
  IF NEW.model_key IS DISTINCT FROM OLD.model_key
     OR NEW.model_version IS DISTINCT FROM OLD.model_version
     OR NEW.market_family IS DISTINCT FROM OLD.market_family
     OR NEW.runtime IS DISTINCT FROM OLD.runtime
     OR NEW.artifact_uri IS DISTINCT FROM OLD.artifact_uri
     OR NEW.artifact_hash IS DISTINCT FROM OLD.artifact_hash
     OR NEW.configuration_hash IS DISTINCT FROM OLD.configuration_hash
     OR NEW.feature_version IS DISTINCT FROM OLD.feature_version
     OR NEW.training_start IS DISTINCT FROM OLD.training_start
     OR NEW.training_end IS DISTINCT FROM OLD.training_end
     OR NEW.training_cutoff_at IS DISTINCT FROM OLD.training_cutoff_at
     OR NEW.validation_window IS DISTINCT FROM OLD.validation_window
     OR NEW.hyperparameters IS DISTINCT FROM OLD.hyperparameters
     OR NEW.training_dataset_hash IS DISTINCT FROM OLD.training_dataset_hash
     OR NEW.code_commit IS DISTINCT FROM OLD.code_commit
     OR NEW.historical_metadata_approximate IS DISTINCT FROM OLD.historical_metadata_approximate
     OR NEW.audit_metadata IS DISTINCT FROM OLD.audit_metadata
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'model identity and training metadata are immutable';
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER model_registry_identity_immutable
BEFORE UPDATE OR DELETE ON model_registry
FOR EACH ROW EXECUTE FUNCTION guard_model_registry_identity();

CREATE TABLE market_metadata (
  market_family TEXT PRIMARY KEY,
  display_name TEXT NOT NULL,
  supported_lifecycle_stages TEXT[] NOT NULL,
  settlement_version TEXT NOT NULL,
  calibration_required BOOLEAN NOT NULL DEFAULT true,
  active BOOLEAN NOT NULL DEFAULT true,
  metadata JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (btrim(market_family) <> ''),
  CHECK (btrim(display_name) <> ''),
  CHECK (cardinality(supported_lifecycle_stages) > 0),
  CHECK (supported_lifecycle_stages <@ ARRAY['EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED']::TEXT[]),
  CHECK (btrim(settlement_version) <> '')
);

CREATE INDEX idx_market_metadata_active
  ON market_metadata (market_family)
  WHERE active = true;

CREATE TABLE calibration_models (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  model_registry_id UUID NOT NULL REFERENCES model_registry(id) ON DELETE RESTRICT,
  market_family TEXT NOT NULL,
  lifecycle_stage TEXT NOT NULL,
  calibration_version TEXT NOT NULL,
  method TEXT NOT NULL,
  fitted_parameters JSONB NOT NULL,
  fitting_window_start TIMESTAMPTZ NOT NULL,
  fitting_window_end TIMESTAMPTZ NOT NULL,
  sample_count BIGINT NOT NULL,
  metrics JSONB NOT NULL DEFAULT '{}',
  artifact_hash TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (model_registry_id, market_family, lifecycle_stage, calibration_version),
  CHECK (btrim(market_family) <> ''),
  CHECK (lifecycle_stage IN ('EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED')),
  CHECK (btrim(calibration_version) <> ''),
  CHECK (btrim(method) <> ''),
  CHECK (fitting_window_end >= fitting_window_start),
  CHECK (sample_count > 0)
);

CREATE INDEX idx_calibration_models_lookup
  ON calibration_models (market_family, lifecycle_stage, created_at DESC);

ALTER TABLE engine_predictions
  ADD COLUMN model_registry_id UUID,
  ADD COLUMN as_of_timestamp TIMESTAMPTZ,
  ADD COLUMN data_cutoff_at TIMESTAMPTZ,
  ADD COLUMN feature_version TEXT,
  ADD COLUMN raw_probability NUMERIC(8,6),
  ADD COLUMN calibrated_probability NUMERIC(8,6),
  ADD COLUMN model_agreement_score NUMERIC(8,6),
  ADD COLUMN ood_score NUMERIC(8,6),
  ADD COLUMN stability_score NUMERIC(8,6),
  ADD COLUMN recommendation_score NUMERIC(10,6),
  ADD COLUMN recommendation_reason TEXT,
  ADD COLUMN odds_snapshot_id BIGINT,
  ADD COLUMN value_edge NUMERIC(12,6),
  ADD COLUMN explanation_factors JSONB;

ALTER TABLE engine_predictions
  ADD CONSTRAINT engine_predictions_model_registry_fk
    FOREIGN KEY (model_registry_id) REFERENCES model_registry(id) ON DELETE RESTRICT NOT VALID,
  ADD CONSTRAINT engine_predictions_odds_snapshot_fk
    FOREIGN KEY (odds_snapshot_id) REFERENCES odds_snapshots(id) ON DELETE SET NULL NOT VALID,
  ADD CONSTRAINT engine_predictions_as_of_cutoff_order
    CHECK (data_cutoff_at IS NULL OR as_of_timestamp IS NULL OR data_cutoff_at <= as_of_timestamp),
  ADD CONSTRAINT engine_predictions_raw_probability_range
    CHECK (raw_probability IS NULL OR (raw_probability >= 0 AND raw_probability <= 1)),
  ADD CONSTRAINT engine_predictions_calibrated_probability_range
    CHECK (calibrated_probability IS NULL OR (calibrated_probability >= 0 AND calibrated_probability <= 1)),
  ADD CONSTRAINT engine_predictions_model_agreement_range
    CHECK (model_agreement_score IS NULL OR (model_agreement_score >= 0 AND model_agreement_score <= 1)),
  ADD CONSTRAINT engine_predictions_ood_score_range
    CHECK (ood_score IS NULL OR (ood_score >= 0 AND ood_score <= 1)),
  ADD CONSTRAINT engine_predictions_stability_score_range
    CHECK (stability_score IS NULL OR (stability_score >= 0 AND stability_score <= 1)),
  ADD CONSTRAINT engine_predictions_feature_version_nonempty
    CHECK (feature_version IS NULL OR btrim(feature_version) <> ''),
  ADD CONSTRAINT engine_predictions_recommendation_reason_nonempty
    CHECK (recommendation_reason IS NULL OR btrim(recommendation_reason) <> '');

COMMENT ON COLUMN engine_predictions.as_of_timestamp IS
  'Point in time for which the prediction was valid; never inferred for legacy rows.';
COMMENT ON COLUMN engine_predictions.data_cutoff_at IS
  'Latest source-data timestamp admitted to feature generation; never inferred from generated_at.';
COMMENT ON COLUMN engine_predictions.raw_probability IS
  'Uncalibrated model probability. Legacy backfill may copy canonical probability only when equivalence is known.';
