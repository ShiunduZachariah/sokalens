-- seo_urls is derived state: every row can be rebuilt from matches,
-- engine_predictions, teams and leagues by running the sweep once. Dropping it
-- therefore loses no fact, only the cached answer.
--
-- What it does lose, until the sweep runs again, is the single definition of
-- indexability -- which returns the page and the sitemap to computing it
-- independently, the arrangement that produced three silent defects in three
-- weeks. A rollback here is safe for the data and costly for the property, so
-- it should be followed by a sweep rather than left.
DROP INDEX IF EXISTS idx_seo_urls_entity;
DROP INDEX IF EXISTS idx_seo_urls_sweep;
DROP INDEX IF EXISTS idx_seo_urls_crawlable;
DROP TABLE IF EXISTS seo_urls;
