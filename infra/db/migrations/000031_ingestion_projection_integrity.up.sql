-- Repair the read-optimized fixture projection from canonical matches and
-- collapse only byte-for-byte equivalent odds observations. This migration is
-- intentionally conservative: different capture times and different payloads
-- remain separate historical observations.

LOCK TABLE matches IN SHARE MODE;
LOCK TABLE fixtures IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE odds_snapshots IN ACCESS EXCLUSIVE MODE;
LOCK TABLE odds IN ACCESS EXCLUSIVE MODE;

CREATE TABLE IF NOT EXISTS data_maintenance_audit (
  id BIGSERIAL PRIMARY KEY,
  operation TEXT NOT NULL,
  criteria TEXT NOT NULL,
  rows_examined BIGINT NOT NULL DEFAULT 0,
  rows_changed BIGINT NOT NULL DEFAULT 0,
  details JSONB NOT NULL DEFAULT '{}'::jsonb,
  performed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

WITH divergent AS MATERIALIZED (
  SELECT
    m.id,
    m.league_id,
    EXTRACT(YEAR FROM m.kickoff_at AT TIME ZONE 'UTC')::integer AS season_id,
    m.home_team_id,
    m.away_team_id,
    m.kickoff_at,
    m.status,
    NULLIF(trim(m.venue_name), '') AS venue_name,
    m.home_score,
    m.away_score,
    m.half_time_home_score,
    m.half_time_away_score,
    f.id IS NULL AS was_missing
  FROM matches AS m
  LEFT JOIN fixtures AS f ON f.id = m.id
  WHERE f.id IS NULL
     OR f.league_id IS DISTINCT FROM m.league_id
     OR f.home_team_id IS DISTINCT FROM m.home_team_id
     OR f.away_team_id IS DISTINCT FROM m.away_team_id
     OR f.kickoff_at IS DISTINCT FROM m.kickoff_at
     OR f.status IS DISTINCT FROM m.status
     OR f.venue_name IS DISTINCT FROM NULLIF(trim(m.venue_name), '')
     OR f.home_goals_ft IS DISTINCT FROM m.home_score
     OR f.away_goals_ft IS DISTINCT FROM m.away_score
     OR f.home_goals_ht IS DISTINCT FROM m.half_time_home_score
     OR f.away_goals_ht IS DISTINCT FROM m.half_time_away_score
     OR f.source_match_id IS DISTINCT FROM m.id
), reconciled AS (
  INSERT INTO fixtures (
    id,
    league_id,
    season_id,
    home_team_id,
    away_team_id,
    kickoff_at,
    status,
    venue_name,
    home_goals_ft,
    away_goals_ft,
    home_goals_ht,
    away_goals_ht,
    source_match_id,
    source,
    updated_at
  )
  SELECT
    id,
    league_id,
    season_id,
    home_team_id,
    away_team_id,
    kickoff_at,
    status,
    venue_name,
    home_score,
    away_score,
    half_time_home_score,
    half_time_away_score,
    id,
    'matches_reconciliation',
    now()
  FROM divergent
  ON CONFLICT (id) DO UPDATE SET
    league_id = EXCLUDED.league_id,
    home_team_id = EXCLUDED.home_team_id,
    away_team_id = EXCLUDED.away_team_id,
    kickoff_at = EXCLUDED.kickoff_at,
    status = EXCLUDED.status,
    venue_name = EXCLUDED.venue_name,
    home_goals_ft = EXCLUDED.home_goals_ft,
    away_goals_ft = EXCLUDED.away_goals_ft,
    home_goals_ht = EXCLUDED.home_goals_ht,
    away_goals_ht = EXCLUDED.away_goals_ht,
    source_match_id = EXCLUDED.source_match_id,
    updated_at = now()
  RETURNING id
)
INSERT INTO data_maintenance_audit (
  operation,
  criteria,
  rows_examined,
  rows_changed,
  details
)
SELECT
  'reconcile_fixture_projection',
  'matches is canonical; preserve existing fixture season_id and source provenance',
  count(*),
  (SELECT count(*) FROM reconciled),
  jsonb_build_object(
    'missing_inserted', count(*) FILTER (WHERE was_missing),
    'divergent_updated', count(*) FILTER (WHERE NOT was_missing)
  )
FROM divergent;

CREATE TEMP TABLE odds_snapshot_dedupe ON COMMIT DROP AS
WITH normalized AS (
  SELECT source_snapshot_id, count(*) AS normalized_count
  FROM odds
  WHERE source_snapshot_id IS NOT NULL
  GROUP BY source_snapshot_id
), ranked AS (
  SELECT
    snapshots.id AS snapshot_id,
    first_value(snapshots.id) OVER identity AS keeper_id,
    row_number() OVER identity AS duplicate_rank,
    count(*) OVER content AS duplicate_count
  FROM odds_snapshots AS snapshots
  LEFT JOIN normalized
    ON normalized.source_snapshot_id = snapshots.id
  WINDOW
    content AS (
      PARTITION BY
        snapshots.match_id,
        snapshots.market_id,
        snapshots.provider,
        snapshots.captured_at,
        snapshots.payload
    ),
    identity AS (
      PARTITION BY
        snapshots.match_id,
        snapshots.market_id,
        snapshots.provider,
        snapshots.captured_at,
        snapshots.payload
      ORDER BY COALESCE(normalized.normalized_count, 0) DESC, snapshots.id
    )
)
SELECT snapshot_id, keeper_id, duplicate_rank
FROM ranked
WHERE duplicate_count > 1;

DO $block$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM odds_snapshot_dedupe AS duplicates
    JOIN odds AS prices
      ON prices.source_snapshot_id = duplicates.snapshot_id
    GROUP BY duplicates.keeper_id, prices.outcome
    HAVING count(DISTINCT ROW(
      prices.fixture_id,
      prices.bookmaker,
      prices.market,
      prices.value,
      prices.implied_prob,
      prices.fetched_at
    )) > 1
  ) THEN
    RAISE EXCEPTION 'odds deduplication stopped: conflicting normalized prices require manual review';
  END IF;
END
$block$;

WITH ranked_prices AS (
  SELECT
    duplicates.keeper_id,
    prices.fixture_id,
    prices.bookmaker,
    prices.market,
    prices.outcome,
    prices.value,
    prices.implied_prob,
    prices.fetched_at,
    row_number() OVER (
      PARTITION BY duplicates.keeper_id, prices.outcome
      ORDER BY
        CASE WHEN duplicates.snapshot_id = duplicates.keeper_id THEN 0 ELSE 1 END,
        prices.id
    ) AS choice_rank
  FROM odds_snapshot_dedupe AS duplicates
  JOIN odds AS prices
    ON prices.source_snapshot_id = duplicates.snapshot_id
)
INSERT INTO odds (
  fixture_id,
  bookmaker,
  market,
  outcome,
  value,
  implied_prob,
  fetched_at,
  source_snapshot_id
)
SELECT
  fixture_id,
  bookmaker,
  market,
  outcome,
  value,
  implied_prob,
  fetched_at,
  keeper_id
FROM ranked_prices
WHERE choice_rank = 1
ON CONFLICT (source_snapshot_id, outcome) DO UPDATE SET
  fixture_id = EXCLUDED.fixture_id,
  bookmaker = EXCLUDED.bookmaker,
  market = EXCLUDED.market,
  value = EXCLUDED.value,
  implied_prob = EXCLUDED.implied_prob,
  fetched_at = EXCLUDED.fetched_at;

WITH deleted_prices AS (
  DELETE FROM odds AS prices
  USING odds_snapshot_dedupe AS duplicates
  WHERE duplicates.duplicate_rank > 1
    AND prices.source_snapshot_id = duplicates.snapshot_id
  RETURNING prices.id
), deleted_snapshots AS (
  DELETE FROM odds_snapshots AS snapshots
  USING odds_snapshot_dedupe AS duplicates
  WHERE duplicates.duplicate_rank > 1
    AND snapshots.id = duplicates.snapshot_id
  RETURNING snapshots.id
)
INSERT INTO data_maintenance_audit (
  operation,
  criteria,
  rows_examined,
  rows_changed,
  details
)
SELECT
  'deduplicate_odds_snapshots',
  'same match_id, market_id, provider, captured_at, and canonical JSON payload',
  (SELECT count(*) FROM odds_snapshot_dedupe),
  (SELECT count(*) FROM deleted_snapshots),
  jsonb_build_object(
    'normalized_prices_deleted', (SELECT count(*) FROM deleted_prices),
    'duplicate_groups', (SELECT count(DISTINCT keeper_id) FROM odds_snapshot_dedupe)
  );

CREATE UNIQUE INDEX IF NOT EXISTS uq_odds_snapshots_exact_observation
  ON odds_snapshots (
    match_id,
    market_id,
    provider,
    captured_at,
    (digest(payload::text, 'sha256'))
  );

CREATE INDEX IF NOT EXISTS idx_fixtures_finished_home_history
  ON fixtures (league_id, season_id, home_team_id, kickoff_at DESC)
  WHERE lower(status) IN ('finished', 'ft', 'aet', 'pen');

CREATE INDEX IF NOT EXISTS idx_fixtures_finished_away_history
  ON fixtures (league_id, season_id, away_team_id, kickoff_at DESC)
  WHERE lower(status) IN ('finished', 'ft', 'aet', 'pen');
