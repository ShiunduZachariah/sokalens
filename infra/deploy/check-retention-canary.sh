#!/usr/bin/env bash
# Prove the deployed worker executes a tiny real cleanup before declaring the
# activation healthy. Job payloads cannot override its configured safety gate.
set -euo pipefail
project="${1:?compose project required}"
release_sha="${2:?release SHA required}"
[[ "$project" == sports-prediction-platform || "$project" == sokalens-dev ]] || exit 2
[[ "$release_sha" =~ ^[0-9a-f]{40}$ ]] || exit 2
canary_key="retention-canary:$project:$release_sha:$(date -u +%s%N)"
sql() {
 docker exec -i "$project-postgres-1" sh -ceu 'PGOPTIONS="-c statement_timeout=5000 -c lock_timeout=1000" exec psql -X -qAt -v ON_ERROR_STOP=1 -v "canary_key=$1" -v "canary_id=$2" -U "$POSTGRES_USER" -d "$POSTGRES_DB"' sh "$canary_key" "${canary_id:-0}"
}
canary_id="$(sql <<'SQL'
WITH existing AS (
 SELECT id FROM jobs WHERE idempotency_key=:'canary_key' ORDER BY id DESC LIMIT 1
), inserted AS (
 INSERT INTO jobs(job_key,status,queue,priority,payload,run_after,idempotency_key,max_attempts)
 SELECT 'maintenance.prune-raw-predictions','queued','maintenance',1,
  '{"batch_size":100,"max_batches":2}'::jsonb,now(),:'canary_key',1
 WHERE NOT EXISTS(SELECT 1 FROM existing)
 RETURNING id
)
SELECT id FROM inserted UNION ALL SELECT id FROM existing LIMIT 1;
SQL
)"
[[ "$canary_id" =~ ^[0-9]+$ ]] || { echo 'could not identify cleanup canary job' >&2; exit 1; }
for _ in $(seq 1 120); do
 status="$(sql <<< "SELECT status FROM jobs WHERE id=:'canary_id'::bigint")"
 case "$status" in
 completed) echo "bounded deployed retention canary passed: job=$canary_id"; exit 0 ;;
 failed) echo "bounded deployed retention canary failed: job=$canary_id" >&2; exit 1 ;;
 queued|running) sleep 5 ;;
 *) echo "cleanup canary has an unexpected status" >&2; exit 1 ;;
 esac
done
echo 'cleanup canary exceeded its observation deadline' >&2
exit 1
