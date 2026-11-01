-- Quota planner cost accounting + throttle persistence.
--
-- provider_request_logs gains reason + priority so every provider request is
-- auditable by why it was made (live / pre-match / prediction / reference /
-- backfill / retry). provider_rate_limits gains throttle_until so the worker's
-- minute throttle state survives restarts and is visible to operators.

ALTER TABLE provider_request_logs
  ADD COLUMN IF NOT EXISTS reason TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS priority INTEGER NOT NULL DEFAULT 0;

ALTER TABLE provider_rate_limits
  ADD COLUMN IF NOT EXISTS throttle_until TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_provider_request_logs_quota_accounting
  ON provider_request_logs (requested_at DESC, reason)
  WHERE reason <> '';
