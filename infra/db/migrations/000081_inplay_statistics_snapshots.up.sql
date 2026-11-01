-- The in-play statistics time series: what the provider's match statistics said,
-- and at which minute of play (plan §7.7, contract §7 ingestion cadence).
--
-- WHY THIS IS NOT fixture_statistics. That table is one row per (fixture, team),
-- upserted, last write wins -- it answers "what are this match's statistics",
-- and for a finished match that is the right and only question. In-play asks a
-- different one: "how many corners had been taken when we published over 8.5
-- corners at minute 30". A last-write-wins row cannot answer it, because by
-- full time it has been overwritten with the final count. Settling and grading
-- a ladder published mid-match needs the series, so the series is stored.
--
-- WHY THE SAME COLUMNS. Deliberately identical to fixture_statistics, so one
-- StatRecord maps to both and one normaliser fills them. card_points in
-- particular must stay in the same unit as that table: it was once yellow*10 +
-- red*25 here and yellow + red there, and the engine settled total_cards lines
-- against the column, so a single yellow graded "over 5.5" as won. Two tables
-- with the same column names and different units would reintroduce that class
-- of bug at the worst possible moment -- mid-match, against live money markets.
--
-- Volume: 200 concurrent fixtures x 2 teams x a 60-second cadence over ~100
-- minutes is ~40,000 rows on a busy Saturday. Partitioned monthly like the
-- other live tables so retention is a partition drop rather than a delete.

CREATE TABLE IF NOT EXISTS fixture_statistics_snapshots (
    fixture_id        BIGINT      NOT NULL,
    team_id           BIGINT      NOT NULL,

    -- When we wrote it, and the minute of play it describes.
    --
    -- Both, because they are not interchangeable. captured_at orders the series
    -- and drives retention; match_minute is what a published market was
    -- conditioned on, and the two diverge across half time, a VAR check or a
    -- late poll. Nullable minute: a statistics payload that arrives without a
    -- clock is still worth keeping as a series point, and a guessed minute
    -- would be worse than none.
    captured_at       TIMESTAMPTZ NOT NULL,
    match_minute      SMALLINT,

    -- Exactly the fixture_statistics column set. See the header.
    shots_on_target   INTEGER,
    shots_total       INTEGER,
    shots_blocked     INTEGER,
    shots_inside_box  INTEGER,
    shots_outside_box INTEGER,
    possession_pct    NUMERIC(5,2),
    corners           INTEGER,
    fouls             INTEGER,
    offsides          INTEGER,
    yellow_cards      INTEGER,
    red_cards         INTEGER,
    goalkeeper_saves  INTEGER,
    passes_total      INTEGER,
    passes_accurate   INTEGER,
    pass_accuracy     NUMERIC(5,2),
    xg                NUMERIC(6,3),
    card_points       INTEGER,

    -- Which pass observed it. 'api-football-live' is an in-play observation from
    -- the batched fixtures?ids= poll; a backfill must never be mistaken for one,
    -- because a backfilled row carries the FINAL counts with an earlier
    -- captured_at and would corrupt the series it was inserted into.
    source            TEXT        NOT NULL DEFAULT 'api-football-live',

    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One row per team per capture. A poll that re-observes the same cutoff
    -- writes nothing (ON CONFLICT DO NOTHING at the writer) rather than
    -- duplicating a point, which would double-count nothing but would make the
    -- series lie about its own cadence.
    --
    -- captured_at is in the key because Postgres requires the partition column
    -- in every unique constraint on a partitioned table.
    PRIMARY KEY (fixture_id, team_id, captured_at),

    CONSTRAINT fixture_statistics_snapshots_card_points_check
      CHECK (card_points IS NULL OR card_points >= 0)
) PARTITION BY RANGE (captured_at);

COMMENT ON TABLE fixture_statistics_snapshots IS
  'Append-only per-team match statistics time series for in-play. One row per '
  'fixture per team per capture, same columns and units as fixture_statistics. '
  'Read to condition the in-play model and to grade a ladder published earlier '
  'in the match; retention is by dropping a whole monthly partition.';

-- The two read patterns, and nothing else:
--   the latest capture for a fixture (the in-play pass conditioning the model);
--   the series for one fixture and team (grading, and the graph).
CREATE INDEX IF NOT EXISTS idx_fixture_statistics_snapshots_fixture_captured
  ON fixture_statistics_snapshots (fixture_id, captured_at DESC);

CREATE INDEX IF NOT EXISTS idx_fixture_statistics_snapshots_team_series
  ON fixture_statistics_snapshots (fixture_id, team_id, captured_at);

-- Initial partitions relative to now(), for the reason 000066 and 000079 record:
-- literal months are correct on the day the migration is written and wrong on
-- every disposable database created later, and a partitioned table with no
-- matching child rejects every insert.
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
            'CREATE TABLE IF NOT EXISTS %I PARTITION OF fixture_statistics_snapshots FOR VALUES FROM (%L) TO (%L)',
            'fixture_statistics_snapshots_' || to_char(partition_start, 'YYYY_MM'),
            partition_start,
            partition_end
        );
    END LOOP;
END
$$;
