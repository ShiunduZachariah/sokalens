package jobs

const (
	lockIdempotencySQL = `SELECT pg_advisory_xact_lock(hashtextextended('ops-job:' || $1, 0))`
	findByKeySQL       = `SELECT id::text, COALESCE(incident_id::text, ''), queue_name, job_type, idempotency_key,
 priority, payload, status, available_at, attempts, max_attempts,
 COALESCE(lease_owner, ''), COALESCE(lease_token::text, ''), lease_expires_at,
 COALESCE(last_error_class, ''), COALESCE(last_error_code, ''), COALESCE(last_error_message, ''),
 created_at, started_at, completed_at, dead_lettered_at, updated_at
FROM ops_jobs WHERE idempotency_key = $1`
	insertJobSQL = `INSERT INTO ops_jobs (
 incident_id, queue_name, job_type, idempotency_key, priority, payload,
 status, available_at, max_attempts, created_at, updated_at
) VALUES (NULLIF($1, '')::uuid, $2, $3, $4, $5, $6, 'pending', $7, $8, $9, $9)
RETURNING id::text, COALESCE(incident_id::text, ''), queue_name, job_type, idempotency_key,
 priority, payload, status, available_at, attempts, max_attempts,
 COALESCE(lease_owner, ''), COALESCE(lease_token::text, ''), lease_expires_at,
 COALESCE(last_error_class, ''), COALESCE(last_error_code, ''), COALESCE(last_error_message, ''),
 created_at, started_at, completed_at, dead_lettered_at, updated_at`
	claimJobSQL = `WITH candidate AS (
 SELECT id, status AS prior_status
 FROM ops_jobs
 WHERE queue_name = $1
   AND attempts < max_attempts
   AND ((status IN ('pending', 'retry') AND available_at <= $2)
        OR (status = 'leased' AND lease_expires_at <= $2))
 ORDER BY priority DESC, available_at ASC, created_at ASC, id ASC
 FOR UPDATE SKIP LOCKED
 LIMIT 1
)
UPDATE ops_jobs j
SET status = 'leased', attempts = j.attempts + 1,
    lease_owner = $3, lease_token = $4::uuid, lease_expires_at = $5,
    started_at = COALESCE(j.started_at, $2),
    last_error_class = CASE WHEN candidate.prior_status = 'leased' THEN 'lease_expired' ELSE j.last_error_class END,
    last_error_code = CASE WHEN candidate.prior_status = 'leased' THEN 'lease_expired' ELSE j.last_error_code END,
    last_error_message = CASE WHEN candidate.prior_status = 'leased' THEN 'previous worker lease expired' ELSE j.last_error_message END,
    updated_at = $2
FROM candidate WHERE j.id = candidate.id
RETURNING j.id::text, COALESCE(j.incident_id::text, ''), j.queue_name, j.job_type, j.idempotency_key,
 j.priority, j.payload, j.status, j.available_at, j.attempts, j.max_attempts,
 COALESCE(j.lease_owner, ''), COALESCE(j.lease_token::text, ''), j.lease_expires_at,
 COALESCE(j.last_error_class, ''), COALESCE(j.last_error_code, ''), COALESCE(j.last_error_message, ''),
 j.created_at, j.started_at, j.completed_at, j.dead_lettered_at, j.updated_at`
	renewJobSQL = `UPDATE ops_jobs
SET lease_expires_at = $5, updated_at = $4
WHERE id = $1::uuid AND status = 'leased' AND lease_owner = $2
  AND lease_token = $3::uuid AND lease_expires_at > $4
RETURNING id::text, COALESCE(incident_id::text, ''), queue_name, job_type, idempotency_key,
 priority, payload, status, available_at, attempts, max_attempts,
 COALESCE(lease_owner, ''), COALESCE(lease_token::text, ''), lease_expires_at,
 COALESCE(last_error_class, ''), COALESCE(last_error_code, ''), COALESCE(last_error_message, ''),
 created_at, started_at, completed_at, dead_lettered_at, updated_at`
	completeJobSQL = `UPDATE ops_jobs
SET status = 'succeeded', lease_owner = NULL, lease_token = NULL, lease_expires_at = NULL,
    completed_at = $4, dead_lettered_at = NULL, updated_at = $4
WHERE id = $1::uuid AND status = 'leased' AND lease_owner = $2
  AND lease_token = $3::uuid AND lease_expires_at > $4
RETURNING id::text, COALESCE(incident_id::text, ''), queue_name, job_type, idempotency_key,
 priority, payload, status, available_at, attempts, max_attempts,
 COALESCE(lease_owner, ''), COALESCE(lease_token::text, ''), lease_expires_at,
 COALESCE(last_error_class, ''), COALESCE(last_error_code, ''), COALESCE(last_error_message, ''),
 created_at, started_at, completed_at, dead_lettered_at, updated_at`
	failJobSQL = `UPDATE ops_jobs
SET status = CASE WHEN $5 = 'permanent' OR attempts >= max_attempts THEN 'dead_letter' ELSE 'retry' END,
    available_at = CASE WHEN $5 = 'retryable' AND attempts < max_attempts THEN
      $4 + (LEAST($9::double precision, $8::double precision * power(2, GREATEST(attempts - 1, 0))) * interval '1 second')
      ELSE available_at END,
    lease_owner = NULL, lease_token = NULL, lease_expires_at = NULL,
    last_error_class = $5, last_error_code = $6, last_error_message = $7,
    completed_at = CASE WHEN $5 = 'permanent' OR attempts >= max_attempts THEN $4 ELSE NULL END,
    dead_lettered_at = CASE WHEN $5 = 'permanent' OR attempts >= max_attempts THEN $4 ELSE NULL END,
    updated_at = $4
WHERE id = $1::uuid AND status = 'leased' AND lease_owner = $2
  AND lease_token = $3::uuid AND lease_expires_at > $4
RETURNING id::text, COALESCE(incident_id::text, ''), queue_name, job_type, idempotency_key,
 priority, payload, status, available_at, attempts, max_attempts,
 COALESCE(lease_owner, ''), COALESCE(lease_token::text, ''), lease_expires_at,
 COALESCE(last_error_class, ''), COALESCE(last_error_code, ''), COALESCE(last_error_message, ''),
 created_at, started_at, completed_at, dead_lettered_at, updated_at`
	reclaimExpiredSQL = `WITH expired AS (
 SELECT id FROM ops_jobs
 WHERE status = 'leased' AND lease_expires_at <= $1
 ORDER BY lease_expires_at ASC, id ASC
 FOR UPDATE SKIP LOCKED LIMIT $2
)
UPDATE ops_jobs j
SET status = CASE WHEN j.attempts >= j.max_attempts THEN 'dead_letter' ELSE 'retry' END,
    available_at = CASE WHEN j.attempts < j.max_attempts THEN $1 ELSE j.available_at END,
    lease_owner = NULL, lease_token = NULL, lease_expires_at = NULL,
    last_error_class = 'lease_expired', last_error_code = 'lease_expired',
    last_error_message = 'worker lease expired before completion',
    completed_at = CASE WHEN j.attempts >= j.max_attempts THEN $1 ELSE NULL END,
    dead_lettered_at = CASE WHEN j.attempts >= j.max_attempts THEN $1 ELSE NULL END,
    updated_at = $1
FROM expired WHERE j.id = expired.id`
)
