ALTER TABLE matches
  ADD COLUMN IF NOT EXISTS home_score INTEGER,
  ADD COLUMN IF NOT EXISTS away_score INTEGER,
  ADD COLUMN IF NOT EXISTS half_time_home_score INTEGER,
  ADD COLUMN IF NOT EXISTS half_time_away_score INTEGER,
  ADD COLUMN IF NOT EXISTS home_corners INTEGER,
  ADD COLUMN IF NOT EXISTS away_corners INTEGER,
  ADD COLUMN IF NOT EXISTS home_cards INTEGER,
  ADD COLUMN IF NOT EXISTS away_cards INTEGER,
  ADD COLUMN IF NOT EXISTS home_fouls INTEGER,
  ADD COLUMN IF NOT EXISTS away_fouls INTEGER,
  ADD COLUMN IF NOT EXISTS home_throw_ins INTEGER,
  ADD COLUMN IF NOT EXISTS away_throw_ins INTEGER,
  ADD COLUMN IF NOT EXISTS settled_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS prediction_feature_snapshots (
  id BIGSERIAL PRIMARY KEY,
  match_id BIGINT NOT NULL REFERENCES matches(id),
  market_id BIGINT NOT NULL REFERENCES odds_markets(id),
  model_id BIGINT NOT NULL REFERENCES prediction_models(id),
  feature_key TEXT NOT NULL,
  snapshot_hash TEXT NOT NULL,
  payload JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (match_id, market_id, model_id, snapshot_hash)
);

CREATE INDEX IF NOT EXISTS idx_prediction_feature_snapshots_lookup
  ON prediction_feature_snapshots (match_id, market_id, model_id, created_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_prediction_candidates_unique_snapshot
  ON prediction_candidates (match_id, market_id, model_id, input_snapshot_hash);

CREATE INDEX IF NOT EXISTS idx_prediction_versions_status
  ON prediction_versions (publication_status, published_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_predictions_current_model_market
  ON predictions (match_id, market_id, model_id);

CREATE TABLE IF NOT EXISTS provider_prediction_benchmarks (
  id BIGSERIAL PRIMARY KEY,
  provider TEXT NOT NULL,
  match_id BIGINT REFERENCES matches(id),
  provider_fixture_id TEXT NOT NULL,
  market_key TEXT NOT NULL,
  provider_selection TEXT NOT NULL,
  provider_confidence NUMERIC(5,2),
  payload_metadata JSONB NOT NULL DEFAULT '{}',
  captured_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (provider, provider_fixture_id, market_key, captured_at)
);

CREATE INDEX IF NOT EXISTS idx_provider_prediction_benchmarks_match
  ON provider_prediction_benchmarks (match_id, market_key, captured_at DESC);
