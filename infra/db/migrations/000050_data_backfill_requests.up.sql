-- Durable, provider-agnostic requests emitted by the prediction engine and
-- fulfilled asynchronously by ingestion workers. Provider calls remain out of
-- the prediction and public request paths.
CREATE TABLE data_backfill_requests (
  id BIGSERIAL PRIMARY KEY,
  target_fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE CASCADE,
  team_id BIGINT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  competition_id BIGINT NOT NULL REFERENCES leagues(id) ON DELETE CASCADE,
  feature_group TEXT NOT NULL CHECK (feature_group IN ('results', 'rich_statistics')),
  matches_needed INTEGER NOT NULL CHECK (matches_needed > 0 AND matches_needed <= 100),
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'in_progress', 'completed', 'confirmed_unavailable')),
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0 AND attempts <= 12),
  provider_attempts JSONB NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(provider_attempts) = 'object'),
  requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '24 hours'),
  completed_at TIMESTAMPTZ,
  last_error TEXT,
  UNIQUE (target_fixture_id, team_id, competition_id, feature_group),
  CHECK (expires_at > requested_at),
  CHECK (
    (status IN ('completed', 'confirmed_unavailable') AND completed_at IS NOT NULL)
    OR (status IN ('pending', 'in_progress') AND completed_at IS NULL)
  )
);

CREATE INDEX idx_data_backfill_requests_claim
  ON data_backfill_requests (status, next_attempt_at, requested_at, id)
  WHERE status IN ('pending', 'in_progress');

CREATE INDEX idx_data_backfill_requests_fixture
  ON data_backfill_requests (target_fixture_id, feature_group, status);

COMMENT ON TABLE data_backfill_requests IS
  'PostgreSQL-authoritative prediction input requests; workers choose providers and record each attempt.';
