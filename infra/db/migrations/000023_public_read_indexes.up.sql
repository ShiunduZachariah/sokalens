CREATE INDEX IF NOT EXISTS idx_engine_predictions_fixture_market_version
  ON engine_predictions (fixture_id, market, generated_at, public_id);

CREATE INDEX IF NOT EXISTS idx_fixtures_kickoff_id
  ON fixtures (kickoff_at, id);

CREATE INDEX IF NOT EXISTS idx_matches_finished_home_history
  ON matches (league_id, home_team_id, kickoff_at)
  WHERE lower(status) IN ('ft', 'aet', 'pen', 'finished')
    AND home_score IS NOT NULL
    AND away_score IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_matches_finished_away_history
  ON matches (league_id, away_team_id, kickoff_at)
  WHERE lower(status) IN ('ft', 'aet', 'pen', 'finished')
    AND home_score IS NOT NULL
    AND away_score IS NOT NULL;
