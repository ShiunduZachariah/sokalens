DROP INDEX IF EXISTS idx_fixtures_finished_away_history;
DROP INDEX IF EXISTS idx_fixtures_finished_home_history;
DROP INDEX IF EXISTS uq_odds_snapshots_exact_observation;

-- The exact duplicate cleanup and fixture reconciliation in the up migration
-- cannot be reconstructed from database state alone. Restore the documented
-- pre-migration pg_dump if those data changes must be reversed.
DROP TABLE IF EXISTS data_maintenance_audit;
