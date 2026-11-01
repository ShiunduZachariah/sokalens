-- Drops the recent-form indexes. The feed still returns correct results
-- without them; it returns them too slowly for the request budget, so a
-- rollback restores the 503 rather than any loss of data.

DROP INDEX IF EXISTS idx_matches_form_away;
DROP INDEX IF EXISTS idx_matches_form_home;
