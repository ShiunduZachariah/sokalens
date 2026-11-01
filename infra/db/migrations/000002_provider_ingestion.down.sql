DROP TABLE IF EXISTS provider_rate_limits;

DROP INDEX IF EXISTS idx_jobs_active_idempotency;
DROP INDEX IF EXISTS idx_jobs_claimable;

ALTER TABLE jobs
  DROP COLUMN IF EXISTS idempotency_key,
  DROP COLUMN IF EXISTS last_error,
  DROP COLUMN IF EXISTS completed_at,
  DROP COLUMN IF EXISTS locked_at,
  DROP COLUMN IF EXISTS locked_by,
  DROP COLUMN IF EXISTS max_attempts,
  DROP COLUMN IF EXISTS attempts,
  DROP COLUMN IF EXISTS run_after,
  DROP COLUMN IF EXISTS priority,
  DROP COLUMN IF EXISTS queue;

DROP INDEX IF EXISTS idx_provider_request_logs_endpoint_params;
DROP INDEX IF EXISTS idx_provider_request_logs_recent;

ALTER TABLE provider_request_logs
  DROP COLUMN IF EXISTS provider_request_id,
  DROP COLUMN IF EXISTS error_category,
  DROP COLUMN IF EXISTS rate_limit_headers,
  DROP COLUMN IF EXISTS minute_remaining,
  DROP COLUMN IF EXISTS minute_limit,
  DROP COLUMN IF EXISTS daily_remaining,
  DROP COLUMN IF EXISTS daily_limit,
  DROP COLUMN IF EXISTS response_bytes,
  DROP COLUMN IF EXISTS latency_ms,
  DROP COLUMN IF EXISTS params_hash,
  DROP COLUMN IF EXISTS method,
  DROP COLUMN IF EXISTS job_id;
