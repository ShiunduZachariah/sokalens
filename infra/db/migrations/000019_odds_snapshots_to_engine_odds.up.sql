WITH extracted AS (
  SELECT
    os.id AS snapshot_id,
    os.match_id AS fixture_id,
    os.captured_at AS fetched_at,
    os.payload->>'bookmaker' AS bookmaker,
    price->>'label' AS outcome,
    (price->>'value')::numeric(12,4) AS value
  FROM odds_snapshots AS os
  JOIN fixtures AS f ON f.id = os.match_id
  CROSS JOIN LATERAL jsonb_array_elements(os.payload->'prices') AS price
  WHERE os.payload ? 'prices'
    AND NULLIF(os.payload->>'bookmaker', '') IS NOT NULL
    AND price->>'label' IN ('Home', 'Draw', 'Away')
    AND NULLIF(price->>'value', '') IS NOT NULL
),
with_total AS (
  SELECT
    *,
    sum(1 / value) OVER (PARTITION BY snapshot_id) AS implied_total,
    count(*) OVER (PARTITION BY snapshot_id) AS outcome_count
  FROM extracted
  WHERE value > 0
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
  '1x2',
  outcome,
  value,
  (1 / value) / implied_total,
  fetched_at,
  snapshot_id
FROM with_total
WHERE outcome_count = 3
  AND implied_total > 0
ON CONFLICT (source_snapshot_id, outcome) DO UPDATE SET
  value = EXCLUDED.value,
  implied_prob = EXCLUDED.implied_prob,
  fetched_at = EXCLUDED.fetched_at;
