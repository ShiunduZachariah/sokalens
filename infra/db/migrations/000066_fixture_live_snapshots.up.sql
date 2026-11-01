-- Append-only minute-by-minute live match history (in-play architecture §5).
--
-- WHY THIS IS THE URGENT HALF OF THE PLAN. Every other dataset in this system
-- can be re-fetched: results, fixtures, odds histories, team statistics are all
-- still sitting on the provider tomorrow. The minute-by-minute state of a match
-- that has finished is not. No provider sells it back at any price, so every
-- fixture that runs before this table exists is a permanent hole in the record
-- the in-play model will eventually be trained on.
--
-- ONE TABLE, NOT THREE. §5.1 specifies fixture_live_snapshots,
-- fixture_statistics_snapshots and fixture_events together. This migration
-- creates only the first, because it is the only one with a writer: the bulk
-- `fixtures?live=all` response carries score, status and elapsed minute, and
-- nothing else in this repository consumes embedded statistics or events yet.
-- The partition maintenance job is table-agnostic, so adding the other two is
-- a migration plus a config line rather than a rewrite.
--
-- IDENTITY. fixture_id references fixtures(id), which is matches(id) -- the
-- engine fixture shares the match's primary key (see upsertEngineFixture). So
-- this keys on the same identity as both the live write path and the
-- prediction contract, with no mapping layer to disagree with.

CREATE TABLE IF NOT EXISTS fixture_live_snapshots (
    id                  BIGSERIAL,
    fixture_id          BIGINT      NOT NULL,
    provider            TEXT        NOT NULL,
    captured_at         TIMESTAMPTZ NOT NULL,
    provider_timestamp  TIMESTAMPTZ,

    status              TEXT        NOT NULL,
    period              TEXT,
    elapsed_minute      SMALLINT,
    elapsed_extra       SMALLINT,

    home_goals          SMALLINT,
    away_goals          SMALLINT,

    -- Nullable, and deliberately not NOT NULL DEFAULT 0.
    --
    -- The bulk live endpoint does not carry cards. Defaulting to zero would
    -- record "no red cards" for every snapshot, which is an assertion we cannot
    -- make -- and the in-play model's red-card adjustment (§7.2) would then
    -- train on a column that is confidently wrong rather than honestly empty.
    -- NULL means "not observed"; a writer that learns cards can fill it.
    home_red_cards      SMALLINT,
    away_red_cards      SMALLINT,

    -- Hash of the provider fields this row is derived from. §4.3 skips the
    -- insert when it matches the previous snapshot for the fixture, which cuts
    -- write volume substantially on quiet matches and keeps the partition
    -- indexes smaller. It is a write-side decision only: the fetch still
    -- happens, because a poll that returns unchanged data is how we learn the
    -- match is still being watched.
    response_hash       TEXT        NOT NULL,

    -- captured_at is in the key because Postgres requires the partition column
    -- in every unique constraint on a partitioned table.
    PRIMARY KEY (id, captured_at)
) PARTITION BY RANGE (captured_at);

COMMENT ON TABLE fixture_live_snapshots IS
  'Append-only live match state. Never updated, never deleted row-by-row: retention is by dropping a whole monthly partition, which is instant and reclaims space immediately, where a bulk DELETE would bloat the table and trigger heavy autovacuum on a VPS already tight on memory.';

-- The read pattern is "the timeline for this fixture, newest first" and, for
-- the reaper and freshness work, "the most recent snapshot for this fixture".
-- Both are the same index.
CREATE INDEX IF NOT EXISTS idx_fixture_live_snapshots_fixture_captured
  ON fixture_live_snapshots (fixture_id, captured_at DESC);

-- Initial partitions.
--
-- A partitioned table with no matching child rejects every insert, so the
-- current month and the two after it are created here rather than left to the
-- maintenance job. Deploying the schema and then discovering that writes fail
-- until a nightly job runs is the kind of gap that only shows up during a
-- fixture window.
--
-- Generated relative to now() rather than written as literal months. A
-- migration with hard-coded 2026-09 and 2026-10 partitions is correct on the
-- day it is written and useless the first time it is applied to a fresh
-- database in a later month -- including every disposable database the
-- migration verifier and the local stack create.
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
            'CREATE TABLE IF NOT EXISTS %I PARTITION OF fixture_live_snapshots FOR VALUES FROM (%L) TO (%L)',
            'fixture_live_snapshots_' || to_char(partition_start, 'YYYY_MM'),
            partition_start,
            partition_end
        );
    END LOOP;
END
$$;
