-- What is left of the duplicate-team problem, and why it is still there.
--
-- READ ONLY. This reports; it changes nothing.
--
-- BACKGROUND. Migration 000061 already collapsed duplicate teams and leagues,
-- and it already ran. It merged only the unambiguous cases, under three rules:
--
--   1. a row is merged away only if it has NO crest AND NO country
--   2. a group merges only when exactly ONE row in it carries a crest
--   3. anything skipped is recorded in entity_merge_conflicts
--
-- The ~706 duplicate name groups still in the catalogue are not rows that
-- migration missed. They are the rows it refused, because merging them would
-- be a guess -- Everton of England and Everton of Chile share a name and both
-- carry a crest, and collapsing them would destroy a real club's history to
-- tidy a list.
--
-- So there is no bulk fix left to write. A merge keyed on name equality would
-- merge exactly the groups that were deliberately spared. What is left is a
-- review problem: read the conflicts, decide the genuine duplicates one at a
-- time, and let the ingestion-side canonical-entity mapping
-- (LookupCanonicalEntityID / FindSuspectedDuplicate, already on main) stop new
-- ones being minted.
--
-- This report is the input to that review.
--
--   psql -f report-duplicate-teams.sql

\echo '== 1. Scale: duplicate name groups still present =='
SELECT count(*) AS groups, sum(members) - count(*) AS surplus_rows
FROM (SELECT name, count(*) AS members FROM teams GROUP BY name HAVING count(*) > 1) g;

\echo ''
\echo '== 2. Why 000061 skipped them, by recorded reason =='
SELECT reason, count(*) AS groups
FROM entity_merge_conflicts
GROUP BY reason
ORDER BY groups DESC;

\echo ''
\echo '== 3. The safe tail: groups where exactly one row carries a crest =='
\echo '   (these match 000061 rule 2 and may have appeared since it ran)'
SELECT t.name,
       count(*) AS members,
       count(*) FILTER (WHERE COALESCE(t.logo_url,'') <> '') AS with_crest,
       count(*) FILTER (WHERE t.country_code IS NOT NULL)    AS with_country
FROM teams t
GROUP BY t.name
HAVING count(*) > 1
   AND count(*) FILTER (WHERE COALESCE(t.logo_url,'') <> '') = 1
   AND count(*) FILTER (WHERE t.country_code IS NOT NULL) < count(*)
ORDER BY members DESC, t.name
LIMIT 50;

\echo ''
\echo '== 4. Genuinely distinct clubs: same name, different country =='
\echo '   (these must NEVER be merged)'
SELECT name, count(DISTINCT country_code) AS countries,
       string_agg(DISTINCT country_code, ', ' ORDER BY country_code) AS which
FROM teams
WHERE country_code IS NOT NULL
GROUP BY name
HAVING count(DISTINCT country_code) > 1
ORDER BY countries DESC, name
LIMIT 30;

\echo ''
\echo '== 5. Does any duplicate actually reach a reader? =='
\echo '   A duplicate only matters if both ids carry upcoming fixtures.'
SELECT t.name, count(DISTINCT t.id) AS ids_with_upcoming
FROM teams t
WHERE EXISTS (SELECT 1 FROM matches m
              WHERE (m.home_team_id = t.id OR m.away_team_id = t.id)
                AND m.kickoff_at > now())
GROUP BY t.name
HAVING count(DISTINCT t.id) > 1
ORDER BY ids_with_upcoming DESC, t.name;
