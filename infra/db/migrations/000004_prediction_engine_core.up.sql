CREATE TABLE IF NOT EXISTS prediction_market_support (
  market_key TEXT PRIMARY KEY REFERENCES odds_markets(market_key),
  support_status TEXT NOT NULL,
  tier TEXT NOT NULL,
  settlement_source TEXT NOT NULL,
  minimum_data_notes TEXT NOT NULL DEFAULT '',
  enabled BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (support_status IN ('enabled', 'planned', 'disabled')),
  CHECK (tier IN ('core', 'advanced', 'planned')),
  CHECK (settlement_source IN ('final_score', 'fixture_events', 'statistics', 'manual_review', 'not_supported'))
);

ALTER TABLE prediction_models
  DROP CONSTRAINT IF EXISTS prediction_models_model_key_key;

CREATE UNIQUE INDEX IF NOT EXISTS idx_prediction_models_model_key_version
  ON prediction_models (model_key, version);

CREATE TABLE IF NOT EXISTS prediction_candidates (
  id BIGSERIAL PRIMARY KEY,
  match_id BIGINT NOT NULL REFERENCES matches(id),
  market_id BIGINT NOT NULL REFERENCES odds_markets(id),
  model_id BIGINT NOT NULL REFERENCES prediction_models(id),
  status TEXT NOT NULL DEFAULT 'generated',
  selection TEXT NOT NULL,
  confidence NUMERIC(5,2) NOT NULL,
  probabilities JSONB NOT NULL DEFAULT '{}',
  input_snapshot_hash TEXT NOT NULL,
  input_snapshot JSONB NOT NULL DEFAULT '{}',
  generated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (status IN ('generated', 'reviewed', 'published', 'rejected', 'expired')),
  CHECK (confidence >= 0 AND confidence <= 100)
);

CREATE INDEX IF NOT EXISTS idx_prediction_candidates_match_market
  ON prediction_candidates (match_id, market_id, generated_at DESC);

CREATE INDEX IF NOT EXISTS idx_prediction_candidates_status
  ON prediction_candidates (status, generated_at DESC);

CREATE TABLE IF NOT EXISTS prediction_versions (
  id BIGSERIAL PRIMARY KEY,
  prediction_id BIGINT NOT NULL REFERENCES predictions(id) ON DELETE CASCADE,
  version INTEGER NOT NULL,
  model_id BIGINT NOT NULL REFERENCES prediction_models(id),
  selection TEXT NOT NULL,
  confidence NUMERIC(5,2) NOT NULL,
  probabilities JSONB NOT NULL DEFAULT '{}',
  input_snapshot_hash TEXT NOT NULL,
  input_snapshot JSONB NOT NULL DEFAULT '{}',
  publication_status TEXT NOT NULL DEFAULT 'published',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  published_at TIMESTAMPTZ,
  UNIQUE (prediction_id, version),
  CHECK (version > 0),
  CHECK (confidence >= 0 AND confidence <= 100),
  CHECK (publication_status IN ('draft', 'published', 'retracted', 'superseded'))
);

CREATE INDEX IF NOT EXISTS idx_prediction_versions_prediction
  ON prediction_versions (prediction_id, version DESC);

CREATE TABLE IF NOT EXISTS prediction_outcomes (
  id BIGSERIAL PRIMARY KEY,
  prediction_version_id BIGINT NOT NULL UNIQUE REFERENCES prediction_versions(id) ON DELETE CASCADE,
  result TEXT NOT NULL DEFAULT 'pending',
  settled_at TIMESTAMPTZ,
  evidence JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (result IN ('pending', 'won', 'lost', 'void'))
);

CREATE INDEX IF NOT EXISTS idx_prediction_outcomes_result
  ON prediction_outcomes (result, settled_at DESC);
