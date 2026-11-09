-- Shadow predictions from challenger models (architecture v2 sections 18
-- and 21). Models B (Elo) and C (trained trees) run beside the published
-- champion at zero weight. Their forecasts are written here, never to
-- engine_predictions, so no public surface can read them.
--
-- Each row also carries the champion's full 1X2 for the same fixture at the
-- same as-of time. engine_predictions stores only the champion's most likely
-- selection and is pruned after a few days, so without this copy the
-- champion could not be scored by log loss against the same fixtures.
--
-- Settlement writes one shadow_settlements row per challenger row, keyed by
-- this table's id; that table is the immutable evidence. This one is working
-- state and follows its fixture if the fixture is removed.
CREATE TABLE challenger_predictions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  model_registry_id UUID NOT NULL REFERENCES model_registry(id) ON DELETE RESTRICT,
  fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE CASCADE,
  -- live: forecast before kick-off by the scheduled job. replay: a
  -- point-in-time backtest of a finished fixture. They are never pooled.
  origin TEXT NOT NULL,
  lifecycle_stage TEXT NOT NULL,
  market TEXT NOT NULL,
  home_probability NUMERIC(8,6) NOT NULL,
  draw_probability NUMERIC(8,6) NOT NULL,
  away_probability NUMERIC(8,6) NOT NULL,
  champion_home_probability NUMERIC(8,6),
  champion_draw_probability NUMERIC(8,6),
  champion_away_probability NUMERIC(8,6),
  explanation_factors JSONB NOT NULL DEFAULT '{}',
  fallback_flags TEXT[] NOT NULL DEFAULT '{}',
  as_of_timestamp TIMESTAMPTZ NOT NULL,
  data_cutoff_at TIMESTAMPTZ,
  kickoff_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (model_registry_id, fixture_id, origin, lifecycle_stage, market),
  CHECK (origin IN ('live', 'replay')),
  CHECK (lifecycle_stage IN ('EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED')),
  CHECK (btrim(market) <> ''),
  CHECK (as_of_timestamp < kickoff_at),
  CHECK (data_cutoff_at IS NULL OR data_cutoff_at <= as_of_timestamp),
  CHECK (home_probability BETWEEN 0 AND 1 AND draw_probability BETWEEN 0 AND 1 AND away_probability BETWEEN 0 AND 1),
  CHECK (abs(home_probability + draw_probability + away_probability - 1) < 0.0001),
  CHECK (
    (champion_home_probability IS NULL AND champion_draw_probability IS NULL AND champion_away_probability IS NULL)
    OR (champion_home_probability IS NOT NULL AND champion_draw_probability IS NOT NULL AND champion_away_probability IS NOT NULL)
  )
);

CREATE INDEX idx_challenger_predictions_fixture
  ON challenger_predictions (fixture_id);

CREATE INDEX idx_challenger_predictions_origin_created
  ON challenger_predictions (origin, created_at DESC);

COMMENT ON TABLE challenger_predictions IS
  'Zero-weight challenger forecasts (v2 Models B and C). Never published; settled into shadow_settlements.';
