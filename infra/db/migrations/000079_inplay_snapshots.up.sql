-- The in-play model time series: what the model said, and when (§7.6, and the
-- client data contract at docs/integrations/in-play-data-contract.md).
--
-- WHY THIS IS NOT engine_predictions. Those two records answer different
-- questions and must not be merged:
--
--   * `engine_predictions` is the auditable, settleable published prediction.
--     One row per market per publication, superseded and settled individually,
--     because each market needs its own terminal state and audit envelope.
--   * `inplay_snapshots` is the series a client draws. One row per fixture per
--     tick, append-only, never settled.
--
-- The 60-second timer the clients need, multiplied by the dynamic market ladder,
-- is roughly 75 market rows per fixture per tick -- about 1.8M rows on a busy
-- Saturday at that grain. As a snapshot header carrying its markets as one JSONB
-- payload it is ~24,000 rows instead, and a graph reads one row per point.
-- Merging the two records would either make the graph unaffordable or make
-- settlement un-auditable.
--
-- Nothing here is settled, superseded or gated on its own: entitlement is
-- enforced where these are served, behind the /v1/inplay/ prefix.

-- One shared, global stream cursor.
--
-- The contract asks for a sequence that only goes up per fixture and resumes a
-- dropped stream. A single global sequence satisfies that -- a global monotonic
-- counter is also monotonic within any one fixture -- and it is strictly better
-- for the transport, because one cursor then covers a stream spanning many
-- fixtures instead of needing a cursor per fixture.
--
-- Gaps are expected and the contract says so: a client must not infer a dropped
-- message from a gap. Sequences do not roll back on a failed transaction, and
-- events and snapshots draw from the same counter.
CREATE SEQUENCE IF NOT EXISTS inplay_stream_sequence AS BIGINT START 1;

COMMENT ON SEQUENCE inplay_stream_sequence IS
  'Shared monotonic cursor for the in-play stream, drawn by both inplay_snapshots '
  'and fixture_events. Strictly increasing, not gapless: clients resume from the '
  'last sequence they saw and must not treat a gap as a lost message.';

CREATE TABLE IF NOT EXISTS inplay_snapshots (
    id                  BIGSERIAL,
    fixture_id          BIGINT      NOT NULL,

    -- The stream cursor. Defaulted rather than supplied so no writer can forget
    -- it, and so two concurrent writers cannot collide on a value.
    sequence            BIGINT      NOT NULL DEFAULT nextval('inplay_stream_sequence'),

    -- When the snapshot was written, and the cutoff of the data behind it. Both,
    -- because §14 requires a published figure to be auditable against what was
    -- known at the time, and the two differ whenever a poll is late.
    captured_at         TIMESTAMPTZ NOT NULL,
    data_cutoff_at      TIMESTAMPTZ,

    -- Why this snapshot exists. The pre_/post_ pairs are what make an exact
    -- "before -> after" figure possible around a goal, card or VAR decision.
    trigger             TEXT        NOT NULL CHECK (trigger IN (
                            'timer',
                            'pre_goal', 'post_goal',
                            'pre_red_card', 'post_red_card',
                            'pre_var', 'post_var',
                            'period_change',
                            'final'
                        )),

    -- The event this snapshot brackets, as fixture_events.identity_key.
    --
    -- Keys, not surrogate ids, for the reasons 000077 records: the provider
    -- sends no event id, identity_key is derived and survives re-observation,
    -- and fixture_events is partitioned so a foreign key would have to carry the
    -- partition column. The before/after pair for one event is the two rows
    -- sharing this key with pre_* and post_* triggers.
    triggered_by_event_key TEXT,

    -- Match state as the model saw it.
    status              TEXT        NOT NULL,
    period              TEXT,
    elapsed_minute      SMALLINT,
    elapsed_extra       SMALLINT,
    home_goals          SMALLINT,
    away_goals          SMALLINT,
    home_red_cards      SMALLINT,
    away_red_cards      SMALLINT,

    -- What a client may do with the numbers.
    --
    -- 'stale' means "this is what we last knew" and must not be shown as live:
    -- §14 suppresses on stale data rather than extrapolating. 'suspended' means
    -- show no probabilities at all. Derived from the model's SuppressionReason
    -- rather than judged separately, so there is one opinion about freshness.
    model_health        TEXT        NOT NULL CHECK (model_health IN ('ok', 'stale', 'suspended')),
    suppression_reason  TEXT,

    -- The markets, as basis points out of 10,000 per selection.
    --
    -- JSONB rather than child rows: see the header. A snapshot is read whole, by
    -- one client, to draw one point on a line -- there is no query that wants a
    -- single market across snapshots badly enough to pay 75x the row count.
    markets             JSONB       NOT NULL DEFAULT '{}'::JSONB,

    -- Provenance. Versioned separately because in-play versions features and the
    -- model independently, and a snapshot must be attributable to both.
    model_version       TEXT        NOT NULL,
    feature_version     TEXT,
    fixture_live_snapshot_id BIGINT,

    -- captured_at is in the key because Postgres requires the partition column
    -- in every unique constraint on a partitioned table.
    PRIMARY KEY (id, captured_at)
) PARTITION BY RANGE (captured_at);

COMMENT ON TABLE inplay_snapshots IS
  'Append-only in-play model time series: one row per fixture per tick, markets '
  'as JSONB. The series a client graphs, NOT the settleable prediction record -- '
  'that is engine_predictions. Never updated; retention is by dropping a whole '
  'monthly partition. The frozen full-time snapshot is copied to '
  'inplay_final_snapshots, which is never dropped.';

-- The three read patterns, and nothing else:
--   the series for one fixture, oldest first (the graph);
--   everything after a cursor (the stream and its polling fallback);
--   the pair bracketing one event (the before/after figure).
CREATE INDEX IF NOT EXISTS idx_inplay_snapshots_fixture_captured
  ON inplay_snapshots (fixture_id, captured_at);

CREATE INDEX IF NOT EXISTS idx_inplay_snapshots_sequence
  ON inplay_snapshots (sequence);

CREATE INDEX IF NOT EXISTS idx_inplay_snapshots_event_key
  ON inplay_snapshots (fixture_id, triggered_by_event_key)
  WHERE triggered_by_event_key IS NOT NULL;

-- The frozen full-time snapshot, kept for ever.
--
-- A SEPARATE TABLE, not a flag on the partitioned one. Retention there is a
-- partition drop, which is indiscriminate by design -- it cannot spare rows, and
-- a flag would be silently discarded the first time a month aged out. This is
-- the permanent record of what the model said at the whistle, and it is what a
-- "how did we do" view reads long after the partitions are gone.
--
-- One row per fixture: the final whistle happens once.
CREATE TABLE IF NOT EXISTS inplay_final_snapshots (
    fixture_id          BIGINT      PRIMARY KEY REFERENCES fixtures(id) ON DELETE CASCADE,
    sequence            BIGINT      NOT NULL,
    captured_at         TIMESTAMPTZ NOT NULL,
    data_cutoff_at      TIMESTAMPTZ,
    status              TEXT        NOT NULL,
    period              TEXT,
    elapsed_minute      SMALLINT,
    elapsed_extra       SMALLINT,
    home_goals          SMALLINT,
    away_goals          SMALLINT,
    home_red_cards      SMALLINT,
    away_red_cards      SMALLINT,
    model_health        TEXT        NOT NULL CHECK (model_health IN ('ok', 'stale', 'suspended')),
    suppression_reason  TEXT,
    markets             JSONB       NOT NULL DEFAULT '{}'::JSONB,
    model_version       TEXT        NOT NULL,
    feature_version     TEXT,
    fixture_live_snapshot_id BIGINT,
    recorded_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE inplay_final_snapshots IS
  'The frozen full-time in-play snapshot, one row per fixture, retained '
  'permanently. Separate from inplay_snapshots because retention there drops '
  'whole partitions and cannot spare individual rows.';

-- Initial partitions, generated relative to now() for the reason 000066 records:
-- a migration with literal months is correct on the day it is written and wrong
-- on every disposable database created in a later month. A partitioned table
-- with no matching child rejects every insert.
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
            'CREATE TABLE IF NOT EXISTS %I PARTITION OF inplay_snapshots FOR VALUES FROM (%L) TO (%L)',
            'inplay_snapshots_' || to_char(partition_start, 'YYYY_MM'),
            partition_start,
            partition_end
        );
    END LOOP;
END
$$;

-- Give fixture_events a cursor from the same sequence, so one stream can carry
-- events and model snapshots in a single total order. Nullable: rows written
-- before this migration have no place in the stream and must not pretend to.
ALTER TABLE fixture_events
  ADD COLUMN IF NOT EXISTS sequence BIGINT;

ALTER TABLE fixture_events
  ALTER COLUMN sequence SET DEFAULT nextval('inplay_stream_sequence');

COMMENT ON COLUMN fixture_events.sequence IS
  'Position in the shared in-play stream, from inplay_stream_sequence. NULL for '
  'rows written before the column existed.';

CREATE INDEX IF NOT EXISTS idx_fixture_events_sequence
  ON fixture_events (sequence)
  WHERE sequence IS NOT NULL;
