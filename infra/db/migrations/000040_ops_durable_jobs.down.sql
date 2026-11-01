DROP INDEX IF EXISTS idx_ops_jobs_idempotency_lookup;

DROP INDEX IF EXISTS idx_ops_jobs_claim;
CREATE INDEX idx_ops_jobs_claim
  ON ops_jobs (queue_name, priority DESC, available_at, created_at)
  WHERE status IN ('pending', 'retry');

ALTER TABLE ops_jobs
  DROP CONSTRAINT IF EXISTS ops_jobs_dead_letter_time_check,
  DROP CONSTRAINT IF EXISTS ops_jobs_terminal_time_check,
  DROP CONSTRAINT IF EXISTS ops_jobs_lease_shape_check,
  DROP CONSTRAINT IF EXISTS ops_jobs_error_class_check,
  DROP COLUMN IF EXISTS dead_lettered_at,
  DROP COLUMN IF EXISTS last_error_class;
