-- Reverse of 000062_fixture_live_snapshots.
--
-- DROP TABLE on the parent takes every child partition with it, including any
-- the maintenance job created after this migration ran. That is the intended
-- behaviour and it is also irreversible in the way that matters: the snapshots
-- are minute-by-minute state no provider will sell back. Re-running the up
-- migration restores the schema, never the data.
--
-- The index is named explicitly even though DROP TABLE would take it, so this
-- file stays an accurate inventory of what 000062 owns.

DROP INDEX IF EXISTS idx_fixture_live_snapshots_fixture_captured;

DROP TABLE IF EXISTS fixture_live_snapshots;
