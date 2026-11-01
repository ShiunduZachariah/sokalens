-- A whole in-play publication may contain zero current markets. Retain its
-- generation time so a delayed older job cannot resurrect old lines, and the
-- exact visible key set so absent active rows can still settle without being
-- displayed. Prediction versions remain in engine_predictions for audit.
CREATE TABLE IF NOT EXISTS inplay_publication_state (
  fixture_id BIGINT PRIMARY KEY REFERENCES fixtures(id) ON DELETE CASCADE,
  generated_at TIMESTAMPTZ NOT NULL,
  current_markets TEXT[] NOT NULL DEFAULT '{}'
);

COMMENT ON TABLE inplay_publication_state IS
  'Newest complete in-play engine publication and visible market keys per fixture, including valid empty sets.';
