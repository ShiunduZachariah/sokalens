CREATE INDEX IF NOT EXISTS idx_jobs_idempotency_all
  ON jobs (idempotency_key, id DESC)
  WHERE idempotency_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_jobs_running_lease
  ON jobs (locked_at)
  WHERE status = 'running';
