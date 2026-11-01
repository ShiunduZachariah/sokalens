-- Reverses 000079. Dropping inplay_final_snapshots destroys the permanent
-- full-time record, which no other table holds and no provider will serve back;
-- copy it out first if the data matters.
DROP INDEX IF EXISTS idx_fixture_events_sequence;

ALTER TABLE fixture_events
  ALTER COLUMN sequence DROP DEFAULT;

ALTER TABLE fixture_events
  DROP COLUMN IF EXISTS sequence;

DROP TABLE IF EXISTS inplay_final_snapshots;

-- Dropping the parent takes its partitions with it.
DROP TABLE IF EXISTS inplay_snapshots;

DROP SEQUENCE IF EXISTS inplay_stream_sequence;
