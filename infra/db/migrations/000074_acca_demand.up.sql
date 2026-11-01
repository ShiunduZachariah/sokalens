-- What readers asked the ACCA generator for and did not get.
--
-- The precomputed inventory cannot cover the whole filter grid: 12 market tabs
-- by 19 leg counts by the reachable odds windows is more cells than the 3,000
-- active slips the readiness gate allows. Composing at request time is not the
-- answer either -- it is capped at two concurrent by design, so on a busy
-- weekend almost every miss would be handed an empty result regardless of how
-- fast the query got.
--
-- So the misses are recorded and the background composer stocks what people
-- actually ask for. A corner asked for once is composed before it is asked for
-- again, and the second reader is served from inventory in 154ms rather than
-- waiting 2s for a compose that usually failed.
--
-- The alternative -- guessing which corners matter -- is what produced an
-- inventory clustered at the standards floor, covering 201 of 1,596 cells.
CREATE TABLE IF NOT EXISTS acca_demand (
    -- The filter corner, normalised by the API so that two readers asking the
    -- same question land on one row.
    market_tab     TEXT        NOT NULL,
    leg_count      INTEGER     NOT NULL CHECK (leg_count BETWEEN 2 AND 20),
    min_fair_odds  NUMERIC(12,4) NOT NULL,
    max_fair_odds  NUMERIC(12,4) NOT NULL,
    period         TEXT        NOT NULL,

    -- How often it has been asked for, and when it was last asked. Both feed
    -- the composer's ordering: something asked for fifty times today outranks
    -- something asked for once last week.
    request_count  BIGINT      NOT NULL DEFAULT 1,
    first_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Set when the composer has stocked this corner, so it stops being asked
    -- for repeatedly. Cleared by a later miss, because inventory expires.
    satisfied_at   TIMESTAMPTZ,

    PRIMARY KEY (market_tab, leg_count, min_fair_odds, max_fair_odds, period)
);

-- The composer's read: unsatisfied corners, most wanted first.
--
-- Partial on satisfied_at IS NULL because that is the only rowset it ever
-- wants, and a satisfied row stays only as a record of what was asked.
CREATE INDEX IF NOT EXISTS idx_acca_demand_outstanding
    ON acca_demand (request_count DESC, last_seen_at DESC)
    WHERE satisfied_at IS NULL;
