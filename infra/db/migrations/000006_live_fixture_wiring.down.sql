DROP INDEX IF EXISTS idx_prediction_generation_failures_match_market;
DROP TABLE IF EXISTS prediction_generation_failures;

ALTER TABLE matches
  DROP COLUMN IF EXISTS venue_city,
  DROP COLUMN IF EXISTS venue_name,
  DROP COLUMN IF EXISTS status_elapsed,
  DROP COLUMN IF EXISTS provider_status_label,
  DROP COLUMN IF EXISTS provider_status_raw;
