ALTER TABLE provider_request_logs
  ADD COLUMN IF NOT EXISTS job_id BIGINT REFERENCES jobs(id),
  ADD COLUMN IF NOT EXISTS method TEXT NOT NULL DEFAULT 'GET',
  ADD COLUMN IF NOT EXISTS params_hash TEXT,
  ADD COLUMN IF NOT EXISTS latency_ms BIGINT,
  ADD COLUMN IF NOT EXISTS response_bytes INTEGER,
  ADD COLUMN IF NOT EXISTS daily_limit INTEGER,
  ADD COLUMN IF NOT EXISTS daily_remaining INTEGER,
  ADD COLUMN IF NOT EXISTS minute_limit INTEGER,
  ADD COLUMN IF NOT EXISTS minute_remaining INTEGER,
  ADD COLUMN IF NOT EXISTS rate_limit_headers JSONB NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS error_category TEXT,
  ADD COLUMN IF NOT EXISTS provider_request_id TEXT;

CREATE INDEX IF NOT EXISTS idx_provider_request_logs_recent
  ON provider_request_logs (provider, requested_at DESC);

CREATE INDEX IF NOT EXISTS idx_provider_request_logs_endpoint_params
  ON provider_request_logs (provider, endpoint, params_hash, requested_at DESC);

ALTER TABLE jobs
  ADD COLUMN IF NOT EXISTS queue TEXT NOT NULL DEFAULT 'default',
  ADD COLUMN IF NOT EXISTS priority INTEGER NOT NULL DEFAULT 100,
  ADD COLUMN IF NOT EXISTS run_after TIMESTAMPTZ NOT NULL DEFAULT now(),
  ADD COLUMN IF NOT EXISTS attempts INTEGER NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS max_attempts INTEGER NOT NULL DEFAULT 3,
  ADD COLUMN IF NOT EXISTS locked_by TEXT,
  ADD COLUMN IF NOT EXISTS locked_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS last_error TEXT,
  ADD COLUMN IF NOT EXISTS idempotency_key TEXT;

CREATE INDEX IF NOT EXISTS idx_jobs_claimable
  ON jobs (queue, priority, run_after, id)
  WHERE status = 'queued' AND locked_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_active_idempotency
  ON jobs (idempotency_key)
  WHERE idempotency_key IS NOT NULL AND status IN ('queued', 'running');

CREATE TABLE IF NOT EXISTS provider_rate_limits (
  provider TEXT PRIMARY KEY,
  daily_limit INTEGER NOT NULL,
  daily_remaining INTEGER NOT NULL,
  minute_limit INTEGER NOT NULL,
  minute_remaining INTEGER NOT NULL,
  daily_reserve INTEGER NOT NULL,
  minute_reserve INTEGER NOT NULL,
  throttle_until TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO provider_rate_limits (
  provider,
  daily_limit,
  daily_remaining,
  minute_limit,
  minute_remaining,
  daily_reserve,
  minute_reserve
) VALUES (
  'api-football',
  100,
  100,
  10,
  10,
  20,
  2
) ON CONFLICT (provider) DO NOTHING;
