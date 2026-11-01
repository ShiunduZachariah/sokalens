DELETE FROM team_rivalries
WHERE source = 'prediction_engine_seed';

WITH rivalry_seed(league_slug, team_a, team_b, rivalry_name) AS (
  VALUES
    ('football-data-england-e0-2026', 'Arsenal', 'Tottenham', 'North London derby'),
    ('football-data-england-e0-2026', 'Liverpool', 'Everton', 'Merseyside derby'),
    ('football-data-england-e0-2026', 'Man United', 'Man City', 'Manchester derby'),
    ('football-data-england-e1-2026', 'Sheffield Weds', 'Sheffield United', 'Steel City derby')
),
teams_in_league AS (
  SELECT DISTINCT l.id AS league_id, t.id AS team_id, t.name
  FROM leagues AS l
  JOIN fixtures AS f ON f.league_id = l.id
  JOIN teams AS t ON t.id = f.home_team_id
  UNION
  SELECT DISTINCT l.id AS league_id, t.id AS team_id, t.name
  FROM leagues AS l
  JOIN fixtures AS f ON f.league_id = l.id
  JOIN teams AS t ON t.id = f.away_team_id
),
resolved AS (
  SELECT
    l.id AS league_id,
    LEAST(team_a.team_id, team_b.team_id) AS team_id_a,
    GREATEST(team_a.team_id, team_b.team_id) AS team_id_b,
    rs.rivalry_name
  FROM rivalry_seed AS rs
  JOIN leagues AS l ON l.slug = rs.league_slug
  JOIN teams_in_league AS team_a ON team_a.league_id = l.id AND team_a.name = rs.team_a
  JOIN teams_in_league AS team_b ON team_b.league_id = l.id AND team_b.name = rs.team_b
)
INSERT INTO team_rivalries (league_id, team_id_a, team_id_b, rivalry_name)
SELECT league_id, team_id_a, team_id_b, rivalry_name
FROM resolved
ON CONFLICT (league_id, team_id_a, team_id_b) DO UPDATE SET
  rivalry_name = EXCLUDED.rivalry_name;

CREATE UNIQUE INDEX IF NOT EXISTS idx_team_rivalries_seed_unique_name
  ON team_rivalries (league_id, rivalry_name)
  WHERE source = 'prediction_engine_seed';
