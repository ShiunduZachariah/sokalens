DROP INDEX IF EXISTS idx_fixtures_unresolved_retention;
DROP INDEX IF EXISTS idx_jobs_queued_depth;
-- Keep the harmless cursor and query statistics extension across rollbacks.
-- Recovery rows deliberately survive an application/schema rollback.
DROP INDEX IF EXISTS idx_raw_prediction_retention_recovery_age;
DROP INDEX IF EXISTS idx_raw_prediction_retention_recovery_release;
