-- Restores the 000073 vocabulary.
--
-- Rows already carrying 'insufficient_history' would fail the narrowed check,
-- so they are returned to the indexable state the report-only sweep would have
-- given them. The next sweep re-derives the correct answer either way; this
-- only has to leave the table satisfying its own constraint.
UPDATE seo_urls SET indexable = true, excluded_reason = NULL
WHERE excluded_reason = 'insufficient_history';

ALTER TABLE seo_urls DROP CONSTRAINT IF EXISTS seo_urls_excluded_reason_check;

ALTER TABLE seo_urls ADD CONSTRAINT seo_urls_excluded_reason_check
  CHECK (excluded_reason IS NULL OR excluded_reason IN (
    'no_prediction', 'archived', 'not_played', 'thin', 'duplicate', 'retired'
  ));
