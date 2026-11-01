DROP INDEX IF EXISTS idx_recommendation_candidates_generation_cleanup;
ALTER TABLE recommendation_candidates
  DROP COLUMN IF EXISTS href,
  DROP COLUMN IF EXISTS subtitle,
  DROP COLUMN IF EXISTS title;
