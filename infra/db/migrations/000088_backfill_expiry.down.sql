-- Restore the prior schema. Expired audit rows become their prior unclaimable
-- in_progress state; provider-confirmed-unavailable remains distinct.
UPDATE data_backfill_requests
SET status = 'in_progress', completed_at = NULL, updated_at = now()
WHERE status = 'expired';

ALTER TABLE data_backfill_requests
  DROP CONSTRAINT data_backfill_requests_status_check,
  DROP CONSTRAINT data_backfill_requests_check1;

ALTER TABLE data_backfill_requests
  ADD CONSTRAINT data_backfill_requests_status_check
    CHECK (status IN ('pending', 'in_progress', 'completed',
                     'confirmed_unavailable')),
  ADD CONSTRAINT data_backfill_requests_check1
    CHECK (
      (status IN ('completed', 'confirmed_unavailable')
       AND completed_at IS NOT NULL)
      OR (status IN ('pending', 'in_progress') AND completed_at IS NULL)
    );
