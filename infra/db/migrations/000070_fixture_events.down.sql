-- Drops the event timeline and every monthly partition with it. The events are
-- re-derivable: they arrive embedded in fixtures?live=all on every poll, so a
-- rollback loses history but costs no provider quota to begin refilling.

DROP INDEX IF EXISTS idx_fixture_events_identity;
DROP INDEX IF EXISTS idx_fixture_events_fixture_clock;
DROP TABLE IF EXISTS fixture_events;
