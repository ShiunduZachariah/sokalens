-- Collapse duplicate team and league rows onto one canonical row each.
--
-- Cause: entity identity was keyed on a slug built from the *competition's*
-- country. API-Football reports a continental competition's country as "World",
-- so every Champions League fixture minted a second, country-less row for a club
-- that already existed under its domestic country: `team-world-bayern-munich`
-- beside `team-germany-bayern-munich`.
--
-- The damage was not cosmetic. Crests were written to the row carrying a country
-- while fixtures pointed at the stub, so pages rendered initials; and each club's
-- match history was split across two or three ids, leaving the prediction engine
-- reading a fraction of a team's record -- few enough matches, on the fixture
-- that surfaced this, for the quality gate to skip it entirely.
--
-- The ingestion fix that stops new duplicates ships separately. This migration
-- repairs the rows already stored.
--
-- Safety rules, in order of importance:
--   1. Only a row with NO crest and NO country is ever merged away. A row
--      carrying a country is real data and always survives.
--   2. A group merges only when exactly ONE row in it has a crest, so the
--      surviving row is never ambiguous. Everton of England and Everton of Chile
--      both carry crests, so that group is skipped, not collapsed.
--   3. Anything skipped is recorded in entity_merge_conflicts rather than
--      guessed at or silently dropped.

-- OPERATIONAL NOTE: run this in a maintenance window, not against live
-- traffic. It rewrites tens of thousands of rows across matches, fixtures and
-- every table keyed on a team, inside one transaction. A rehearsal against
-- production held a lock on matches long enough to stall the ingestion
-- worker's writes; the ANALYZE calls below address the plan that caused it,
-- but the transaction is still long enough to matter.

BEGIN;

-- Accent-folded, alphanumeric-only comparison key. Duplicates are sometimes
-- spelled differently across providers ("Bodo/Glimt" vs "Bodø/Glimt").
-- IMMUTABLE so it can back an index.
CREATE OR REPLACE FUNCTION canonical_entity_key(value TEXT)
RETURNS TEXT
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
  SELECT regexp_replace(
           lower(translate(value,
             'ÀÁÂÃÄÅàáâãäåÈÉÊËèéêëÌÍÎÏìíîïÒÓÔÕÖØòóôõöøÙÚÛÜùúûüÑñÇçÝýŠšŽžÐðÞþ',
             'AAAAAAaaaaaaEEEEeeeeIIIIiiiiOOOOOOooooooUUUUuuuuNnCcYySsZzDdTt')),
           '[^a-z0-9]', '', 'g')
$$;

-- ---------------------------------------------------------------------------
-- Aliases: keep every observed spelling pointing at the surviving row, so a
-- later ingest reporting an old variant resolves to the same entity instead of
-- minting another duplicate.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS team_aliases (
  id BIGSERIAL PRIMARY KEY,
  team_id BIGINT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  alias TEXT NOT NULL,
  normalized_alias TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT 'dedup-migration',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_team_aliases_unique
  ON team_aliases (normalized_alias, team_id);
CREATE INDEX IF NOT EXISTS idx_team_aliases_team ON team_aliases (team_id);

CREATE TABLE IF NOT EXISTS league_aliases (
  id BIGSERIAL PRIMARY KEY,
  league_id BIGINT NOT NULL REFERENCES leagues(id) ON DELETE CASCADE,
  alias TEXT NOT NULL,
  normalized_alias TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT 'dedup-migration',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_league_aliases_unique
  ON league_aliases (normalized_alias, league_id);
CREATE INDEX IF NOT EXISTS idx_league_aliases_league ON league_aliases (league_id);

CREATE TABLE IF NOT EXISTS entity_merge_conflicts (
  id BIGSERIAL PRIMARY KEY,
  entity_type TEXT NOT NULL,
  entity_key TEXT NOT NULL,
  reason TEXT NOT NULL,
  candidate_ids BIGINT[] NOT NULL DEFAULT '{}',
  detected_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Index the foreign keys that point at the rows this migration rewrites.
--
-- PostgreSQL does not index a referencing column automatically, so every delete
-- of a parent row had to sequentially scan each child table to enforce the
-- constraint. A rehearsal spent 45 seconds inside the cascade to
-- acca_slip_legs alone for 500 deleted matches. These indexes are why the
-- merge below completes in seconds instead of minutes, and they make ordinary
-- parent deletes cheaper long after this migration has run.
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_acca_slip_legs_match ON acca_slip_legs (match_id);
CREATE INDEX IF NOT EXISTS idx_head_to_head_away_team ON head_to_head (away_team_id);
CREATE INDEX IF NOT EXISTS idx_player_availability_team ON player_availability (team_id);
CREATE INDEX IF NOT EXISTS idx_data_backfill_requests_team ON data_backfill_requests (team_id);
CREATE INDEX IF NOT EXISTS idx_data_backfill_requests_competition ON data_backfill_requests (competition_id);

-- ---------------------------------------------------------------------------
-- Merge plans
-- ---------------------------------------------------------------------------
CREATE TEMP TABLE team_merge ON COMMIT DROP AS
WITH groups AS (
  SELECT canonical_entity_key(name) AS key,
         count(*) FILTER (WHERE COALESCE(logo_url, '') <> '') AS with_crest
  FROM teams
  GROUP BY 1
),
survivors AS (
  SELECT canonical_entity_key(t.name) AS key, t.id AS canonical_id
  FROM teams t
  JOIN groups g ON g.key = canonical_entity_key(t.name)
  WHERE g.with_crest = 1
    AND COALESCE(t.logo_url, '') <> ''
)
SELECT t.id AS dup_id, s.canonical_id, t.name AS dup_name
FROM teams t
JOIN survivors s ON s.key = canonical_entity_key(t.name)
WHERE COALESCE(t.logo_url, '') = ''
  AND t.country_code IS NULL
  AND t.id <> s.canonical_id;

CREATE UNIQUE INDEX ON team_merge (dup_id);
-- Temp tables carry no statistics until analysed, and the planner's default
-- guess drove a sequential-scan plan that held locks on matches for minutes
-- during a rehearsal against production. Analyse before anything joins these.
ANALYZE team_merge;

CREATE TEMP TABLE league_merge ON COMMIT DROP AS
WITH groups AS (
  SELECT canonical_entity_key(name) AS key,
         count(*) FILTER (WHERE COALESCE(logo_url, '') <> '') AS with_crest
  FROM leagues
  GROUP BY 1
),
survivors AS (
  SELECT canonical_entity_key(l.name) AS key, l.id AS canonical_id
  FROM leagues l
  JOIN groups g ON g.key = canonical_entity_key(l.name)
  WHERE g.with_crest = 1
    AND COALESCE(l.logo_url, '') <> ''
)
SELECT l.id AS dup_id, s.canonical_id, l.name AS dup_name
FROM leagues l
JOIN survivors s ON s.key = canonical_entity_key(l.name)
WHERE COALESCE(l.logo_url, '') = ''
  AND l.country_code IS NULL
  AND l.id <> s.canonical_id;

CREATE UNIQUE INDEX ON league_merge (dup_id);
ANALYZE league_merge;

-- Record what we refused to merge, so a skipped group is visible rather than
-- silently left behind.
INSERT INTO entity_merge_conflicts (entity_type, entity_key, reason, candidate_ids)
SELECT 'team', canonical_entity_key(name),
       CASE WHEN count(*) FILTER (WHERE COALESCE(logo_url, '') <> '') > 1
            THEN 'multiple crested rows: distinct clubs sharing a name'
            ELSE 'no crested row to merge into' END,
       array_agg(id ORDER BY id)
FROM teams
GROUP BY canonical_entity_key(name), name
HAVING count(*) > 1
   AND count(*) FILTER (WHERE COALESCE(logo_url, '') <> '') <> 1;

-- ---------------------------------------------------------------------------
-- Matches: remapping team and league ids collides with the canonical-identity
-- unique index wherever the same fixture was stored under both id sets. Drop
-- the redundant copy first, keeping whichever row actually carries a scoreline.
-- ---------------------------------------------------------------------------
CREATE TEMP TABLE match_losers ON COMMIT DROP AS
WITH remapped AS (
  SELECT m.id,
         COALESCE(lm.canonical_id, m.league_id) AS league_id,
         COALESCE(hm.canonical_id, m.home_team_id) AS home_team_id,
         COALESCE(am.canonical_id, m.away_team_id) AS away_team_id,
         m.kickoff_at,
         (m.home_score IS NOT NULL)::int AS has_score
  FROM matches m
  LEFT JOIN league_merge lm ON lm.dup_id = m.league_id
  LEFT JOIN team_merge hm ON hm.dup_id = m.home_team_id
  LEFT JOIN team_merge am ON am.dup_id = m.away_team_id
)
SELECT id FROM (
  SELECT id,
         row_number() OVER (
           PARTITION BY league_id, home_team_id, away_team_id, kickoff_at
           ORDER BY has_score DESC, id ASC
         ) AS rank
  FROM remapped
) ranked
WHERE rank > 1;

CREATE UNIQUE INDEX ON match_losers (id);
ANALYZE match_losers;

-- Refuse to delete a match anything downstream still points at. These children
-- are NO ACTION, so a surprise reference must abort the migration rather than
-- be quietly discarded.
DO $$
DECLARE referenced BIGINT;
BEGIN
  SELECT count(*) INTO referenced FROM (
    SELECT match_id FROM predictions WHERE match_id IN (SELECT id FROM match_losers)
    UNION ALL SELECT match_id FROM odds_snapshots WHERE match_id IN (SELECT id FROM match_losers)
    UNION ALL SELECT match_id FROM prediction_candidates WHERE match_id IN (SELECT id FROM match_losers)
    UNION ALL SELECT match_id FROM prediction_feature_snapshots WHERE match_id IN (SELECT id FROM match_losers)
    UNION ALL SELECT match_id FROM provider_prediction_benchmarks WHERE match_id IN (SELECT id FROM match_losers)
    UNION ALL SELECT match_id FROM acca_slip_legs WHERE match_id IN (SELECT id FROM match_losers)
  ) refs;
  IF referenced > 0 THEN
    RAISE EXCEPTION 'refusing to drop % duplicate match rows that still have dependent records', referenced;
  END IF;
END $$;

DELETE FROM matches m USING match_losers l WHERE m.id = l.id;

-- ---------------------------------------------------------------------------
-- Remap team references. For each table carrying a uniqueness constraint that
-- the remap could violate, drop the row that would collide before updating.
-- ---------------------------------------------------------------------------
DELETE FROM fixture_statistics s USING team_merge m
WHERE s.team_id = m.dup_id
  AND EXISTS (SELECT 1 FROM fixture_statistics k
              WHERE k.fixture_id = s.fixture_id AND k.team_id = m.canonical_id);
UPDATE fixture_statistics s SET team_id = m.canonical_id FROM team_merge m WHERE s.team_id = m.dup_id;

DELETE FROM team_season_stats s USING team_merge m
WHERE s.team_id = m.dup_id
  AND EXISTS (SELECT 1 FROM team_season_stats k
              WHERE k.team_id = m.canonical_id AND k.league_id = s.league_id AND k.season_id = s.season_id);
UPDATE team_season_stats s SET team_id = m.canonical_id FROM team_merge m WHERE s.team_id = m.dup_id;

DELETE FROM standings s USING team_merge m
WHERE s.team_id = m.dup_id
  AND EXISTS (SELECT 1 FROM standings k
              WHERE k.team_id = m.canonical_id AND k.league_id = s.league_id AND k.season_id = s.season_id);
UPDATE standings s SET team_id = m.canonical_id FROM team_merge m WHERE s.team_id = m.dup_id;

DELETE FROM player_availability p USING team_merge m
WHERE p.team_id = m.dup_id
  AND EXISTS (SELECT 1 FROM player_availability k
              WHERE k.fixture_id = p.fixture_id AND k.team_id = m.canonical_id
                AND k.player_name = p.player_name AND k.reason = p.reason);
UPDATE player_availability p SET team_id = m.canonical_id FROM team_merge m WHERE p.team_id = m.dup_id;

DELETE FROM data_backfill_requests d USING team_merge m
WHERE d.team_id = m.dup_id
  AND EXISTS (SELECT 1 FROM data_backfill_requests k
              WHERE k.target_fixture_id IS NOT DISTINCT FROM d.target_fixture_id
                AND k.team_id = m.canonical_id
                AND k.competition_id IS NOT DISTINCT FROM d.competition_id
                AND k.feature_group = d.feature_group);
UPDATE data_backfill_requests d SET team_id = m.canonical_id FROM team_merge m WHERE d.team_id = m.dup_id;

-- Rivalries are an unordered pair; a remap can both collide and self-pair.
DELETE FROM team_rivalries r USING team_merge m
WHERE (r.team_id_a = m.dup_id AND r.team_id_b = m.canonical_id)
   OR (r.team_id_b = m.dup_id AND r.team_id_a = m.canonical_id);
UPDATE team_rivalries r SET team_id_a = m.canonical_id FROM team_merge m WHERE r.team_id_a = m.dup_id;
UPDATE team_rivalries r SET team_id_b = m.canonical_id FROM team_merge m WHERE r.team_id_b = m.dup_id;
DELETE FROM team_rivalries a USING team_rivalries b
WHERE a.id > b.id AND a.league_id = b.league_id
  AND a.team_id_a = b.team_id_a AND a.team_id_b = b.team_id_b;

UPDATE players p SET team_id = m.canonical_id FROM team_merge m WHERE p.team_id = m.dup_id;
UPDATE head_to_head h SET home_team_id = m.canonical_id FROM team_merge m WHERE h.home_team_id = m.dup_id;
UPDATE head_to_head h SET away_team_id = m.canonical_id FROM team_merge m WHERE h.away_team_id = m.dup_id;
UPDATE fixtures f SET home_team_id = m.canonical_id FROM team_merge m WHERE f.home_team_id = m.dup_id;
UPDATE fixtures f SET away_team_id = m.canonical_id FROM team_merge m WHERE f.away_team_id = m.dup_id;
UPDATE matches x SET home_team_id = m.canonical_id FROM team_merge m WHERE x.home_team_id = m.dup_id;
UPDATE matches x SET away_team_id = m.canonical_id FROM team_merge m WHERE x.away_team_id = m.dup_id;

-- ---------------------------------------------------------------------------
-- Remap league references.
-- ---------------------------------------------------------------------------
DELETE FROM standings s USING league_merge m
WHERE s.league_id = m.dup_id
  AND EXISTS (SELECT 1 FROM standings k
              WHERE k.league_id = m.canonical_id AND k.season_id = s.season_id AND k.team_id = s.team_id);
UPDATE standings s SET league_id = m.canonical_id FROM league_merge m WHERE s.league_id = m.dup_id;

DELETE FROM team_season_stats s USING league_merge m
WHERE s.league_id = m.dup_id
  AND EXISTS (SELECT 1 FROM team_season_stats k
              WHERE k.league_id = m.canonical_id AND k.season_id = s.season_id AND k.team_id = s.team_id);
UPDATE team_season_stats s SET league_id = m.canonical_id FROM league_merge m WHERE s.league_id = m.dup_id;

UPDATE league_coverage c SET league_id = m.canonical_id FROM league_merge m WHERE c.league_id = m.dup_id;
UPDATE prediction_results r SET league_id = m.canonical_id FROM league_merge m WHERE r.league_id = m.dup_id;
UPDATE fixtures f SET league_id = m.canonical_id FROM league_merge m WHERE f.league_id = m.dup_id;
UPDATE matches x SET league_id = m.canonical_id FROM league_merge m WHERE x.league_id = m.dup_id;

-- ---------------------------------------------------------------------------
-- Repoint provider mappings, preserve the old spellings, drop the dead rows.
-- ---------------------------------------------------------------------------
UPDATE provider_entity_maps p SET canonical_entity_id = m.canonical_id::text
FROM team_merge m
WHERE p.canonical_entity_type = 'team' AND p.canonical_entity_id = m.dup_id::text;

UPDATE provider_entity_maps p SET canonical_entity_id = m.canonical_id::text
FROM league_merge m
WHERE p.canonical_entity_type = 'league' AND p.canonical_entity_id = m.dup_id::text;

INSERT INTO team_aliases (team_id, alias, normalized_alias, source)
SELECT m.canonical_id, m.dup_name, canonical_entity_key(m.dup_name), 'dedup-migration'
FROM team_merge m
ON CONFLICT DO NOTHING;

INSERT INTO league_aliases (league_id, alias, normalized_alias, source)
SELECT m.canonical_id, m.dup_name, canonical_entity_key(m.dup_name), 'dedup-migration'
FROM league_merge m
ON CONFLICT DO NOTHING;

DELETE FROM teams t USING team_merge m WHERE t.id = m.dup_id;
DELETE FROM leagues l USING league_merge m WHERE l.id = m.dup_id;

-- Nothing may still reference a row we just deleted.
DO $$
DECLARE stranded BIGINT;
BEGIN
  SELECT count(*) INTO stranded
  FROM matches m
  WHERE NOT EXISTS (SELECT 1 FROM teams t WHERE t.id = m.home_team_id)
     OR NOT EXISTS (SELECT 1 FROM teams t WHERE t.id = m.away_team_id)
     OR NOT EXISTS (SELECT 1 FROM leagues l WHERE l.id = m.league_id);
  IF stranded > 0 THEN
    RAISE EXCEPTION 'dedup left % matches pointing at removed entities', stranded;
  END IF;
END $$;

COMMIT;
