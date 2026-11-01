-- Give the major competitions a priority score, so the prediction feed can
-- rank by importance instead of by clock alone.
--
-- WHY. On 2026-09-12 the site published 11,031 predictions for the day and
-- served the first 100 ordered by kickoff time. Those 100 were Western
-- Australia NPL, K League 1 and 2, K4 League, Liga MX U21 and South Australia
-- State League 1. The eight Premier League fixtures kicking off between 14:00
-- and 19:00 UTC appeared nowhere: a reader looking for Liverpool vs Fulham on
-- the day it is played could not find it on the predictions page.
--
-- Nothing was wrong with the data. All eight fixtures were present, 136 of
-- their predictions passed every gate, and only one was held back by the
-- unresolved-earlier-fixture policy. They were simply sorted behind several
-- thousand fixtures that happened to kick off earlier in the day.
--
-- `leagues.priority_score` already existed for exactly this, and is already
-- what the catalogue and search order by. It had never been populated: 1,239
-- of 1,240 rows were 0, because both ingestion paths insert a literal 0 and
-- their ON CONFLICT clauses do not touch the column. That last part is what
-- makes seeding here durable -- re-ingestion will not overwrite these.
--
-- Additive: one UPDATE of a non-null column that already has a default. No
-- schema change, no row created or removed.
--
-- Names are matched exactly, with the country, because league names are not
-- unique in this table. "Premier League" also exists for AM, BB, BD, BH, BM
-- and BT; "Bundesliga" for AT; "Ligue 1" for BF, CD, CG, CI, DZ, GN, SN and
-- TN. A substring match is how "Kenyan Premier League" once outranked
-- England's. For the same reason the continental competitions are matched on
-- their full name: "AFC Women's Champions League" and "Nasjonal U19 Champions
-- League" must not inherit the UEFA score.
--
-- GB Premier League currently has two rows (1406 and 2043) because league
-- deduplication is still outstanding. Both are scored, so the ranking holds
-- whichever row today's fixtures point at, and stays correct after a merge.

UPDATE leagues AS l
SET priority_score = v.score
FROM (VALUES
  ('UEFA Champions League',         NULL,  100),
  ('Premier League',                'GB',   95),
  ('La Liga',                       'ES',   90),
  ('Serie A',                       'IT',   88),
  ('Bundesliga',                    'DE',   86),
  ('Ligue 1',                       'FR',   84),
  ('UEFA Europa League',            NULL,   82),
  ('UEFA Europa Conference League', NULL,   78),
  ('Primeira Liga',                 'PT',   74),
  ('Eredivisie',                    'NL',   72),
  ('Major League Soccer',           'US',   68),
  ('Serie A',                       'BR',   66),
  ('Liga Profesional Argentina',    'AR',   64)
) AS v(name, country_code, score)
WHERE l.name = v.name
  AND (
    (v.country_code IS NULL AND l.country_code IS NULL)
    OR l.country_code = v.country_code
  )
  AND l.priority_score <> v.score;
