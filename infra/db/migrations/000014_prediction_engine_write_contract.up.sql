CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS engine_predictions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE CASCADE,
  market TEXT NOT NULL,
  prediction TEXT NOT NULL,
  probability NUMERIC(8,6) NOT NULL CHECK (probability >= 0.01 AND probability <= 0.99),
  confidence NUMERIC(8,6) NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
  confidence_tier TEXT NOT NULL CHECK (confidence_tier IN ('strong', 'medium', 'low', 'none')),
  is_best_bet BOOLEAN NOT NULL DEFAULT false,
  lifecycle_stage TEXT NOT NULL CHECK (lifecycle_stage IN ('EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED')),
  data_completeness NUMERIC(8,6) NOT NULL CHECK (data_completeness >= 0 AND data_completeness <= 1),
  features_used JSONB NOT NULL DEFAULT '{}',
  explanation TEXT NOT NULL DEFAULT '',
  generated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  superseded_at TIMESTAMPTZ,
  gate_tier TEXT NOT NULL CHECK (gate_tier IN ('SKIP', 'BASIC', 'FULL')),
  horizon_eligible BOOLEAN NOT NULL DEFAULT true,
  embargo_active BOOLEAN NOT NULL DEFAULT false,
  model_version TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (superseded_at IS NULL OR superseded_at >= generated_at)
);

CREATE INDEX IF NOT EXISTS idx_engine_predictions_fixture_current
  ON engine_predictions (fixture_id, market, generated_at DESC)
  WHERE superseded_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_engine_predictions_best_bets
  ON engine_predictions (is_best_bet, confidence_tier, generated_at DESC)
  WHERE superseded_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_engine_predictions_market_current
  ON engine_predictions (market, confidence_tier, generated_at DESC)
  WHERE superseded_at IS NULL;

CREATE TABLE IF NOT EXISTS prediction_results (
  prediction_id UUID PRIMARY KEY REFERENCES engine_predictions(id) ON DELETE CASCADE,
  fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE CASCADE,
  market TEXT NOT NULL,
  predicted TEXT NOT NULL,
  actual TEXT NOT NULL,
  is_correct BOOLEAN NOT NULL,
  probability NUMERIC(8,6) NOT NULL CHECK (probability >= 0.01 AND probability <= 0.99),
  confidence NUMERIC(8,6) NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
  confidence_tier TEXT NOT NULL CHECK (confidence_tier IN ('strong', 'medium', 'low', 'none')),
  lifecycle_stage_at_settlement TEXT NOT NULL CHECK (lifecycle_stage_at_settlement IN ('EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED')),
  league_id BIGINT NOT NULL REFERENCES leagues(id),
  settled_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_prediction_results_market_settled
  ON prediction_results (market, settled_at DESC);

CREATE INDEX IF NOT EXISTS idx_prediction_results_league_settled
  ON prediction_results (league_id, settled_at DESC);

CREATE TABLE IF NOT EXISTS accuracy_snapshots (
  id BIGSERIAL PRIMARY KEY,
  snapshot_date DATE NOT NULL,
  scope TEXT NOT NULL,
  scope_id TEXT,
  total_predictions INTEGER NOT NULL CHECK (total_predictions >= 0),
  correct INTEGER NOT NULL CHECK (correct >= 0),
  accuracy NUMERIC(8,6) NOT NULL CHECK (accuracy >= 0 AND accuracy <= 1),
  roi_simulated NUMERIC(12,6),
  computed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (snapshot_date, scope, scope_id)
);

CREATE INDEX IF NOT EXISTS idx_accuracy_snapshots_scope_date
  ON accuracy_snapshots (scope, scope_id, snapshot_date DESC);

CREATE TABLE IF NOT EXISTS acca_selections (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  requested_at TIMESTAMPTZ NOT NULL,
  legs_requested INTEGER NOT NULL CHECK (legs_requested > 0),
  target_odds NUMERIC(12,4) CHECK (target_odds IS NULL OR target_odds > 0),
  markets_filter JSONB NOT NULL DEFAULT '[]',
  start_datetime TIMESTAMPTZ NOT NULL,
  end_datetime TIMESTAMPTZ NOT NULL,
  legs JSONB NOT NULL DEFAULT '[]',
  combined_fair_odds NUMERIC(12,4) NOT NULL CHECK (combined_fair_odds > 0),
  combined_fair_probability NUMERIC(8,6) NOT NULL CHECK (combined_fair_probability > 0 AND combined_fair_probability <= 1),
  leg_count INTEGER NOT NULL CHECK (leg_count >= 0),
  any_leg_embargoed BOOLEAN NOT NULL DEFAULT false,
  generated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (end_datetime >= start_datetime)
);

CREATE INDEX IF NOT EXISTS idx_acca_selections_generated
  ON acca_selections (generated_at DESC);

CREATE TABLE IF NOT EXISTS fixture_prediction_horizon (
  id BIGSERIAL PRIMARY KEY,
  fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE CASCADE,
  checked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  kickoff_at TIMESTAMPTZ NOT NULL,
  days_to_kickoff NUMERIC(8,3) NOT NULL,
  horizon_eligible BOOLEAN NOT NULL,
  embargo_active BOOLEAN NOT NULL DEFAULT false,
  embargo_reason TEXT,
  embargo_blocking_fixture_id BIGINT REFERENCES fixtures(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_fixture_prediction_horizon_fixture_checked
  ON fixture_prediction_horizon (fixture_id, checked_at DESC);

CREATE INDEX IF NOT EXISTS idx_fixture_prediction_horizon_eligible
  ON fixture_prediction_horizon (horizon_eligible, embargo_active, kickoff_at);
