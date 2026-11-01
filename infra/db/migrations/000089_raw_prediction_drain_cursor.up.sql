CREATE TABLE IF NOT EXISTS raw_prediction_drain_cursor (
  name TEXT PRIMARY KEY,
  finished_at TIMESTAMPTZ,
  fixture_id BIGINT,
  generated_at TIMESTAMPTZ,
  prediction_id UUID,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((finished_at IS NULL) = (fixture_id IS NULL)),
  CHECK ((generated_at IS NULL) = (prediction_id IS NULL))
);

INSERT INTO raw_prediction_drain_cursor (name)
VALUES ('engine_predictions')
ON CONFLICT (name) DO NOTHING;
