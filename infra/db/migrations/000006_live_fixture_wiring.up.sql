ALTER TABLE matches
  ADD COLUMN IF NOT EXISTS provider_status_raw TEXT,
  ADD COLUMN IF NOT EXISTS provider_status_label TEXT,
  ADD COLUMN IF NOT EXISTS status_elapsed INTEGER,
  ADD COLUMN IF NOT EXISTS venue_name TEXT,
  ADD COLUMN IF NOT EXISTS venue_city TEXT;

CREATE TABLE IF NOT EXISTS prediction_generation_failures (
  id BIGSERIAL PRIMARY KEY,
  match_id BIGINT NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
  market_id BIGINT NOT NULL REFERENCES odds_markets(id) ON DELETE CASCADE,
  model_id BIGINT NOT NULL REFERENCES prediction_models(id) ON DELETE CASCADE,
  reason_code TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (match_id, market_id, model_id)
);

CREATE INDEX IF NOT EXISTS idx_prediction_generation_failures_match_market
  ON prediction_generation_failures (match_id, market_id, updated_at DESC);
