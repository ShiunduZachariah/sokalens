CREATE INDEX IF NOT EXISTS idx_matches_public_status_kickoff
  ON matches (lower(COALESCE(status, 'upcoming')), kickoff_at, id);

CREATE INDEX IF NOT EXISTS idx_matches_unresolved_home_kickoff
  ON matches (home_team_id, kickoff_at)
  WHERE lower(COALESCE(status, 'scheduled')) NOT IN
    ('ft', 'aet', 'pen', 'finished', 'cancelled', 'canceled', 'postponed', 'abandoned');

CREATE INDEX IF NOT EXISTS idx_matches_unresolved_away_kickoff
  ON matches (away_team_id, kickoff_at)
  WHERE lower(COALESCE(status, 'scheduled')) NOT IN
    ('ft', 'aet', 'pen', 'finished', 'cancelled', 'canceled', 'postponed', 'abandoned');
