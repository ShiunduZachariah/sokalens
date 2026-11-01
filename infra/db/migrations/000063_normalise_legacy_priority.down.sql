-- Reverse of 000063_normalise_legacy_priority: restore the legacy 90 this
-- league carried before the feed began reading the column.
--
-- Guarded on the value 000063 set, so re-running the down file after the score
-- has been deliberately changed to something else leaves that choice alone.

UPDATE leagues
SET priority_score = 90
WHERE name = 'Kenyan Premier League'
  AND country_code = 'KE'
  AND priority_score = 60;
