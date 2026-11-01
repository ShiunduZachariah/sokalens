ALTER TABLE provider_request_logs
  DROP COLUMN IF EXISTS reason,
  DROP COLUMN IF EXISTS priority;

ALTER TABLE provider_rate_limits
  DROP COLUMN IF EXISTS throttle_until;

DROP INDEX IF EXISTS idx_provider_request_logs_quota_accounting;
