-- Keep queue observability proportional to queued work, including locked rows.
CREATE INDEX IF NOT EXISTS idx_jobs_queued_depth ON jobs (queue) WHERE status='queued';
-- Separate cursor stream for raw detail expiry without fabricating match outcomes.
CREATE INDEX IF NOT EXISTS idx_fixtures_unresolved_retention ON fixtures (kickoff_at,id)
 WHERE finished_at IS NULL AND status IN ('unknown','cancelled','postponed');
INSERT INTO raw_prediction_drain_cursor (name) VALUES ('expired_fixture_predictions') ON CONFLICT (name) DO NOTHING;
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;

-- A short recovery window makes new unresolved expiry reversible without
-- replacing accounts or newer live writes from a whole-database dump.
CREATE TABLE IF NOT EXISTS raw_prediction_retention_recovery (
 prediction_id UUID PRIMARY KEY,
 release_sha TEXT NOT NULL,
 deleted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 prediction_data JSONB NOT NULL,
 result_data JSONB,
 tracker_data JSONB
);
CREATE INDEX IF NOT EXISTS idx_raw_prediction_retention_recovery_age
 ON raw_prediction_retention_recovery (deleted_at,prediction_id);
CREATE INDEX IF NOT EXISTS idx_raw_prediction_retention_recovery_release
 ON raw_prediction_retention_recovery (release_sha,prediction_id);
