-- Harden the PostgreSQL-authoritative operations queue. Redis may emit wake-up
-- hints, but it never owns a lease or changes job state.
ALTER TABLE ops_jobs
  ADD COLUMN last_error_class TEXT,
  ADD COLUMN dead_lettered_at TIMESTAMPTZ;

ALTER TABLE ops_jobs
  ADD CONSTRAINT ops_jobs_error_class_check
    CHECK (last_error_class IS NULL OR last_error_class IN ('retryable', 'permanent', 'lease_expired')) NOT VALID,
  ADD CONSTRAINT ops_jobs_lease_shape_check
    CHECK (
      (status = 'leased' AND lease_owner IS NOT NULL AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL) OR
      (status <> 'leased' AND lease_owner IS NULL AND lease_token IS NULL AND lease_expires_at IS NULL)
    ) NOT VALID,
  ADD CONSTRAINT ops_jobs_terminal_time_check
    CHECK (
      (status IN ('succeeded', 'failed', 'dead_letter', 'cancelled') AND completed_at IS NOT NULL) OR
      (status NOT IN ('succeeded', 'failed', 'dead_letter', 'cancelled') AND completed_at IS NULL)
    ) NOT VALID,
  ADD CONSTRAINT ops_jobs_dead_letter_time_check
    CHECK ((status = 'dead_letter' AND dead_lettered_at IS NOT NULL) OR
           (status <> 'dead_letter' AND dead_lettered_at IS NULL)) NOT VALID;

DROP INDEX IF EXISTS idx_ops_jobs_claim;
CREATE INDEX idx_ops_jobs_claim
  ON ops_jobs (queue_name, priority DESC, available_at, created_at, id)
  WHERE status IN ('pending', 'retry');

CREATE INDEX idx_ops_jobs_idempotency_lookup
  ON ops_jobs (idempotency_key, created_at, id);

COMMENT ON COLUMN ops_jobs.last_error_class IS
  'Typed worker failure classification: retryable, permanent, or lease_expired.';
COMMENT ON COLUMN ops_jobs.dead_lettered_at IS
  'Authoritative UTC timestamp when retry exhaustion or a permanent failure dead-lettered the job.';
COMMENT ON COLUMN ops_jobs.priority IS
  'Raw durable priority; larger numeric values are claimed first. Semantic P0/P1/P2 classes require an explicit adapter mapping.';
