#!/usr/bin/env bash
# Explicit maintenance after a verified recovery point and passing release.
# Rebuild only known oversized operational-log indexes; never application data.
set -euo pipefail
project="${1:?compose project required}"
[[ "$project" == sports-prediction-platform || "$project" == sokalens-dev ]] || exit 2
container="$project-postgres-1"
sql() {
 docker exec -i "$container" sh -ceu 'PGOPTIONS="-c statement_timeout=600000 -c lock_timeout=5000" exec psql -X -qAt -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
}
for index in provider_request_logs_pkey idx_provider_request_logs_recent idx_provider_request_logs_endpoint_params idx_provider_request_logs_quota_accounting; do
 bytes="$(sql <<< "SELECT pg_relation_size('public.$index'::regclass)")"
 if (( bytes < 67108864 )); then continue; fi
 # Concurrent rebuild keeps writes available and is deliberately outside any
 # transaction. A failed rebuild fails the maintenance job and is reported.
 sql <<< "REINDEX INDEX CONCURRENTLY public.$index;"
 after="$(sql <<< "SELECT pg_relation_size('public.$index'::regclass)")"
 printf 'index=%s before_bytes=%s after_bytes=%s\n' "$index" "$bytes" "$after"
done
sql <<< "ANALYZE public.jobs; ANALYZE public.provider_request_logs;"
