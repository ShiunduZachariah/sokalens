-- Allow 'insufficient_history' as an excluded_reason.
--
-- 000073 pinned the reason vocabulary to a CHECK constraint, which is the right
-- shape -- excluded_reason is read by operators asking "why is this page not in
-- Google?", and a free-text column becomes decoration the first time a typo
-- lands. But a closed vocabulary has to be opened when a new reason is added,
-- and 0ecb101 added one in Go without touching the database.
--
-- It was invisible while the gate shipped disabled. Report-only mode rewrites a
-- gated decision back to indexable with no reason, so the new value never
-- reached a write. The first sweep with SEO_FIXTURE_HISTORY_GATE=true fails on
-- the very first batch that contains one thin fixture:
--
--   upsert seo urls: ERROR: new row for relation "seo_urls"
--   violates check constraint "seo_urls_excluded_reason_check"
--
-- and because Upsert writes a batch at a time, the whole sweep fails rather
-- than the single row. Nothing else re-derives seo_urls, so the crawlable set
-- would have frozen at whatever it last was while the job retried and gave up
-- -- the failure mode seourl/job.go calls out as the worst one, because frozen
-- looks exactly like working until pages age out of an index that still
-- advertises them.
--
-- Caught on QA 2026-09-28 by enabling the flag there first.
ALTER TABLE seo_urls DROP CONSTRAINT IF EXISTS seo_urls_excluded_reason_check;

ALTER TABLE seo_urls ADD CONSTRAINT seo_urls_excluded_reason_check
  CHECK (excluded_reason IS NULL OR excluded_reason IN (
    'no_prediction', 'archived', 'not_played', 'thin', 'duplicate', 'retired',
    'insufficient_history'
  ));
