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
  source
)
SELECT
  m.id,
  m.league_id,
  EXTRACT(YEAR FROM m.kickoff_at)::integer,
  m.home_team_id,
  m.away_team_id,
  m.kickoff_at,
  m.status,
  m.venue_name,
  m.home_score,
  m.away_score,
  m.half_time_home_score,
  m.half_time_away_score,
  m.id,
  'provider_ingestion_backfill'
FROM matches AS m
LEFT JOIN fixtures AS f ON f.id = m.id
WHERE f.id IS NULL;
