-- Request expiry is distinct from provider-confirmed unavailability. A request
-- that outlives its target fixture cannot be claimed again, so close it with an
-- honest terminal state while retaining the full audit row.
ALTER TABLE data_backfill_requests
  DROP CONSTRAINT data_backfill_requests_status_check,
  DROP CONSTRAINT data_backfill_requests_check1;

ALTER TABLE data_backfill_requests
  ADD CONSTRAINT data_backfill_requests_status_check
    CHECK (status IN ('pending', 'in_progress', 'completed',
                     'confirmed_unavailable', 'expired')),
  ADD CONSTRAINT data_backfill_requests_check1
    CHECK (
      (status IN ('completed', 'confirmed_unavailable', 'expired')
       AND completed_at IS NOT NULL)
      OR (status IN ('pending', 'in_progress') AND completed_at IS NULL)
    );

UPDATE data_backfill_requests AS request
SET status = 'expired', completed_at = now(), updated_at = now(),
    last_error = 'REQUEST_EXPIRED'
FROM fixtures AS target
WHERE request.target_fixture_id = target.id
  AND request.status IN ('pending', 'in_progress')
  AND request.expires_at <= now()
  AND target.kickoff_at <= now();
