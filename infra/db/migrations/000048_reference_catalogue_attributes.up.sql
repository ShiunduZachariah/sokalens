-- Reference catalogue display attributes (Task 3: football entity catalogue).
-- API-Football reference entities are low-churn; these columns are populated by
-- the worker's reference-sync ingestion (countries flag, league/team logos).
ALTER TABLE countries
  ADD COLUMN IF NOT EXISTS flag_url TEXT;

ALTER TABLE teams
  ADD COLUMN IF NOT EXISTS logo_url TEXT;

ALTER TABLE leagues
  ADD COLUMN IF NOT EXISTS logo_url TEXT;
