ALTER TABLE recommendation_candidates
  ADD COLUMN IF NOT EXISTS title TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS subtitle TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS href TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_recommendation_candidates_generation_cleanup
  ON recommendation_candidates (expires_at, generation_key);
