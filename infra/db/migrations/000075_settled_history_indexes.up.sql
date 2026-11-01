-- Settled match history per team, for the SEO fixture history gate.
--
-- The two history subqueries in services/api/internal/matches/source.go and
-- services/worker/internal/seourl/postgres.go count a team's concluded matches
-- before a given kickoff, across every competition. Without a league_id
-- predicate to narrow them there is nothing for the planner to seek on:
-- idx_matches_unresolved_home_kickoff and its away twin are partial indexes
-- over UNRESOLVED matches, which is the exact complement of what these count.
--
-- That matters because the worker's sweep runs both subqueries for every
-- fixture in its window, every fifteen minutes.
--
-- Partial on the settled statuses and on both scores being present, mirroring
-- the query predicates and the shape of migration 000055, so the index stays
-- small and covers only rows that can ever be counted.
CREATE INDEX IF NOT EXISTS idx_matches_settled_home_kickoff
  ON matches (home_team_id, kickoff_at)
  WHERE lower(status) IN ('ft', 'aet', 'pen', 'finished')
    AND home_score IS NOT NULL
    AND away_score IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_matches_settled_away_kickoff
  ON matches (away_team_id, kickoff_at)
  WHERE lower(status) IN ('ft', 'aet', 'pen', 'finished')
    AND home_score IS NOT NULL
    AND away_score IS NOT NULL;
