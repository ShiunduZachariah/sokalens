-- Live event timeline storage (see docs/ai/plans/2026-09-15-live-event-timeline.md).
--
-- The events are already arriving. `fixtures?live=all` embeds the full event
-- list on every poll we already make -- 60 fixtures and 410 events in one
-- measured request -- and we currently discard them. This table is where they
-- land. It costs zero additional provider requests, which is why §4.1 of the
-- architecture document forbids calling `fixtures/events` per fixture.
--
-- The column choices below are driven by the six traps documented in §4 of the
-- plan. Each one is a way a naive schema silently loses meaning:
--
--   * event_type AND event_detail are both stored, because `detail` carries the
--     meaning and `type` does not. An own goal and a penalty are both
--     type='Goal'; only detail distinguishes them. Storing type alone would
--     credit an own goal to the scoring team.
--   * elapsed_minute and elapsed_extra are separate, because a 90+3 event
--     arrives as {"elapsed": 90, "extra": 3}. One column collapses all added
--     time onto 90'.
--   * is_retracted exists because `Var` events withdraw earlier ones
--     ("Goal cancelled"). The timeline must be able to un-say something, and an
--     append-only table cannot do that by deleting.
--   * assist_id/assist_name are nullable because the provider sends the assist
--     object present with null-valued fields rather than omitting it.
--   * identity_key exists because the provider sends NO event id. Identity is
--     derived from fixture, minute, extra, type, detail, team and player, which
--     is also what makes re-ingesting the same event on the next poll a no-op.
--
-- WHY IDENTITY IS NOT A UNIQUE CONSTRAINT. PostgreSQL requires the partition
-- key in every unique constraint on a partitioned table, and the partition key
-- here is observed_at -- which differs between polls of the same event. A
-- unique index on (identity_key, observed_at) would therefore permit exactly
-- the duplicate it is meant to prevent. Idempotency is enforced by the writer
-- looking up identity_key first, the same way fixture_live_snapshots dedupes on
-- response_hash. idx_fixture_events_identity exists to make that lookup cheap.

CREATE TABLE IF NOT EXISTS fixture_events (
    id              BIGSERIAL,
    fixture_id      BIGINT      NOT NULL,
    provider        TEXT        NOT NULL,
    observed_at     TIMESTAMPTZ NOT NULL,

    -- Match clock, not wall clock. extra is NULL outside added time rather
    -- than 0, so "90'" and "90+0'" stay distinguishable.
    elapsed_minute  SMALLINT    NOT NULL,
    elapsed_extra   SMALLINT,

    -- Stored as the provider sends them, including inconsistent casing:
    -- 'Goal', 'Card' and 'Var' are capitalised while 'subst' is not. Callers
    -- match case-insensitively; normalising on write would lose the raw value
    -- and make a provider change invisible.
    event_type      TEXT        NOT NULL,
    event_detail    TEXT        NOT NULL,

    team_id         BIGINT,
    player_id       BIGINT,
    player_name     TEXT,
    assist_id       BIGINT,
    assist_name     TEXT,
    comments        TEXT,

    -- Derived; see the note above.
    identity_key    TEXT        NOT NULL,

    -- Set when a later Var event withdraws this one. The row stays: a timeline
    -- that deletes a cancelled goal cannot explain why it vanished, and the
    -- retraction is itself information the reader wants.
    is_retracted    BOOLEAN     NOT NULL DEFAULT false,
    retracted_at    TIMESTAMPTZ,

    -- observed_at is in the key because Postgres requires the partition column
    -- in every unique constraint on a partitioned table.
    PRIMARY KEY (id, observed_at)
) PARTITION BY RANGE (observed_at);

COMMENT ON TABLE fixture_events IS
  'Append-only live match events parsed from the embedded event list on fixtures?live=all. Never deleted row-by-row: retention is by dropping a whole monthly partition. Retraction is a flag, not a delete, because a withdrawn goal is information.';

-- Timeline reads: every event for one fixture, in match order.
CREATE INDEX IF NOT EXISTS idx_fixture_events_fixture_clock
  ON fixture_events (fixture_id, elapsed_minute, elapsed_extra);

-- Idempotency lookup on re-poll. Not unique -- see the note above.
CREATE INDEX IF NOT EXISTS idx_fixture_events_identity
  ON fixture_events (fixture_id, identity_key);

-- Three months ahead, matching fixture_live_snapshots. The partition
-- maintenance job (snapshots.PartitionJob) keeps the window rolling; a
-- partition that does not exist is an insert failure the moment a match kicks
-- off, which is the worst possible time to discover it.
DO $$
DECLARE
    month_start DATE := date_trunc('month', now())::date;
    offset_months INTEGER;
    partition_start DATE;
    partition_end DATE;
BEGIN
    FOR offset_months IN 0..2 LOOP
        partition_start := month_start + (offset_months || ' months')::interval;
        partition_end := partition_start + interval '1 month';
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS %I PARTITION OF fixture_events FOR VALUES FROM (%L) TO (%L)',
            'fixture_events_' || to_char(partition_start, 'YYYY_MM'),
            partition_start,
            partition_end
        );
    END LOOP;
END
$$;
