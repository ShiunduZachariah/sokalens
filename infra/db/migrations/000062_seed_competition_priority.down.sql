-- Reverse of 000062_seed_competition_priority: return the seeded competitions
-- to the 0 they held before, which is the column's default and the value both
-- ingestion paths insert.
--
-- Scoped to the exact rows the up file touched. A blanket reset would also
-- clear any score set by hand or by a later migration, which this file has no
-- business discarding.

UPDATE leagues AS l
SET priority_score = 0
FROM (VALUES
  ('UEFA Champions League',         NULL),
  ('Premier League',                'GB'),
  ('La Liga',                       'ES'),
  ('Serie A',                       'IT'),
  ('Bundesliga',                    'DE'),
  ('Ligue 1',                       'FR'),
  ('UEFA Europa League',            NULL),
  ('UEFA Europa Conference League', NULL),
  ('Primeira Liga',                 'PT'),
  ('Eredivisie',                    'NL'),
  ('Major League Soccer',           'US'),
  ('Serie A',                       'BR'),
  ('Liga Profesional Argentina',    'AR')
) AS v(name, country_code)
WHERE l.name = v.name
  AND (
    (v.country_code IS NULL AND l.country_code IS NULL)
    OR l.country_code = v.country_code
  );
