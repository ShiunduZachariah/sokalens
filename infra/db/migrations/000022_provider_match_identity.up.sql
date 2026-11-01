CREATE UNIQUE INDEX IF NOT EXISTS idx_matches_canonical_identity
  ON matches (league_id, home_team_id, away_team_id, kickoff_at);
