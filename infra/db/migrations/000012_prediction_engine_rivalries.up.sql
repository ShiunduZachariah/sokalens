CREATE TABLE IF NOT EXISTS team_rivalries (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  league_id BIGINT NOT NULL REFERENCES leagues(id) ON DELETE CASCADE,
  team_id_a BIGINT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  team_id_b BIGINT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  rivalry_name TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT 'prediction_engine_seed',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (team_id_a <> team_id_b),
  UNIQUE (league_id, team_id_a, team_id_b)
);

CREATE INDEX IF NOT EXISTS idx_team_rivalries_team_a
  ON team_rivalries (team_id_a, league_id);

CREATE INDEX IF NOT EXISTS idx_team_rivalries_team_b
  ON team_rivalries (team_id_b, league_id);

WITH rivalry_seed(league_slug, team_a, team_b, rivalry_name) AS (
  VALUES
    ('football-data-england-e0-2026', 'Arsenal', 'Tottenham', 'North London derby'),
    ('football-data-england-e0-2026', 'Liverpool', 'Everton', 'Merseyside derby'),
    ('football-data-england-e0-2026', 'Man United', 'Man City', 'Manchester derby'),
    ('football-data-england-e1-2026', 'Sheffield Weds', 'Sheffield United', 'Steel City derby')
),
resolved AS (
  SELECT
    l.id AS league_id,
    LEAST(team_a.id, team_b.id) AS team_id_a,
    GREATEST(team_a.id, team_b.id) AS team_id_b,
    rs.rivalry_name
  FROM rivalry_seed AS rs
  JOIN leagues AS l ON l.slug = rs.league_slug
  JOIN teams AS team_a ON team_a.name = rs.team_a
  JOIN teams AS team_b ON team_b.name = rs.team_b
)
INSERT INTO team_rivalries (league_id, team_id_a, team_id_b, rivalry_name)
SELECT league_id, team_id_a, team_id_b, rivalry_name
FROM resolved
ON CONFLICT (league_id, team_id_a, team_id_b) DO UPDATE SET
  rivalry_name = EXCLUDED.rivalry_name;
