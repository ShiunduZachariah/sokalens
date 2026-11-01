-- Recent-form lookups for the live/today match feed.
--
-- THE DEFECT. /v1/matches/today returned 503 in production, every time. The
-- handler's 2750ms budget (API_REQUEST_TIMEOUT) was exhausted before the query
-- returned, so the endpoint that powers the match feed -- and the in-play board
-- built on it -- was serving nothing at all.
--
-- The cost is in two correlated subqueries per result row, home_form and
-- away_form, each fetching a team's last five finished matches:
--
--   WHERE (home_team_id = $t OR away_team_id = $t)
--     AND kickoff_at < $k
--     AND lower(status) IN ('ft','aet','pen','finished')
--     AND home_score IS NOT NULL AND away_score IS NOT NULL
--   ORDER BY kickoff_at DESC LIMIT 5
--
-- idx_matches_finished_{home,away}_history already cover the same predicate,
-- but they lead with league_id, and these two subqueries do not filter on it --
-- the history COUNT(*) subqueries in the same statement do. Leading column
-- absent, so neither index applies: PostgreSQL fell back to a BitmapAnd across
-- the single-column team indexes, then sorted, at 8.3ms a call. Two calls per
-- row against a limit of 200 is roughly 3.3 seconds of the 2.75 available,
-- before any other work in the statement.
--
-- Measured on 44,580 rows: subquery 8.283ms -> 0.678ms, and the endpoint
-- 503 after 2.78s -> 200 in 0.24s.
--
-- The predicate matches the existing partial indexes exactly so the planner
-- treats them as interchangeable for the history subqueries; the difference is
-- only the leading column. Partial, so they index the finished-with-scores
-- rows the form query can actually return and nothing else.
--
-- ADDITIVE. Indexes only. No column, constraint or trigger is touched, and no
-- data is rewritten.

CREATE INDEX IF NOT EXISTS idx_matches_form_home
  ON public.matches USING btree (home_team_id, kickoff_at DESC)
  WHERE lower(status) = ANY (ARRAY['ft'::text, 'aet'::text, 'pen'::text, 'finished'::text])
    AND home_score IS NOT NULL
    AND away_score IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_matches_form_away
  ON public.matches USING btree (away_team_id, kickoff_at DESC)
  WHERE lower(status) = ANY (ARRAY['ft'::text, 'aet'::text, 'pen'::text, 'finished'::text])
    AND home_score IS NOT NULL
    AND away_score IS NOT NULL;
