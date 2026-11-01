CREATE TABLE IF NOT EXISTS acca_slips (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  composition_hash TEXT NOT NULL UNIQUE,
  content_hash TEXT NOT NULL,
  market_tab TEXT NOT NULL DEFAULT 'mixed',
  period TEXT NOT NULL,
  leg_count INTEGER NOT NULL CHECK (leg_count BETWEEN 2 AND 20),
  combined_fair_odds NUMERIC(12,4) NOT NULL CHECK (combined_fair_odds > 0 AND combined_fair_odds <= 1000),
  ranking_score NUMERIC(12,6) NOT NULL DEFAULT 0,
  earliest_kickoff TIMESTAMPTZ NOT NULL,
  latest_kickoff TIMESTAMPTZ NOT NULL,
  odds_band TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','invalid','expired')),
  generation_intent TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS acca_slip_legs (
  slip_id UUID NOT NULL REFERENCES acca_slips(id) ON DELETE CASCADE,
  position INTEGER NOT NULL CHECK (position >= 0),
  prediction_source TEXT NOT NULL,
  prediction_id TEXT NOT NULL,
  prediction_version TEXT,
  match_id BIGINT NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
  market_key TEXT NOT NULL,
  selection TEXT NOT NULL,
  fair_odds_at_generation NUMERIC(12,6) NOT NULL CHECK (fair_odds_at_generation > 1),
  PRIMARY KEY (slip_id, position),
  UNIQUE (slip_id, prediction_source, prediction_id, match_id, market_key, selection)
);

CREATE INDEX IF NOT EXISTS idx_acca_slips_active_pool
  ON acca_slips (period, market_tab, leg_count, odds_band, ranking_score DESC)
  WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_acca_slips_active_until
  ON acca_slips (earliest_kickoff)
  WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_acca_slip_legs_prediction
  ON acca_slip_legs (prediction_source, prediction_id);

CREATE OR REPLACE VIEW eligible_acca_predictions_v AS
SELECT
  'engine'::TEXT AS prediction_source,
  ep.id::TEXT AS prediction_id,
  ep.model_version AS prediction_version,
  ep.fixture_id AS match_id,
  f.league_id,
  l.country_code,
  f.kickoff_at,
  ep.market AS market_key,
  ep.prediction AS selection,
  ep.probability,
  (1 / ep.probability)::NUMERIC(12,6) AS fair_odds,
  COALESCE(ep.recommendation_score, ep.confidence)::NUMERIC(12,6) AS ranking_score,
  ep.gate_tier,
  ep.horizon_eligible,
  ep.superseded_at
FROM engine_predictions ep
JOIN fixtures f ON f.id = ep.fixture_id
JOIN leagues l ON l.id = f.league_id
WHERE ep.superseded_at IS NULL
  AND ep.horizon_eligible = true
  AND ep.embargo_active = false
  AND ep.gate_tier = 'FULL'
  AND f.kickoff_at > now()
  AND ep.probability > 0
  AND ep.probability <= 1;
