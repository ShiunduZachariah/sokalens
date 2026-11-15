#!/usr/bin/env bash
# Run within CI after migrations and activation; failure belongs to rollback.
set -euo pipefail
project="${1:?compose project required}"
api_origin="${2:?API origin required}"
expected_buffers="${3:?expected shared_buffers in MB required}"
[[ "$project" == sports-prediction-platform || "$project" == sokalens-dev ]] || exit 2
[[ "$expected_buffers" =~ ^[0-9]+$ ]] || exit 2
container="${project}-postgres-1"
curl --fail --silent --show-error --max-time 10 "$api_origin/readyz" >/dev/null
docker exec "$project-api-1" wget -q -O - http://127.0.0.1:9092/metrics | grep '^sokalens_db_open_connections ' >/dev/null
sql() {
 docker exec -i "$container" sh -ceu 'PGOPTIONS="-c default_transaction_read_only=on -c statement_timeout=10000 -c lock_timeout=1000" exec psql -X -qAt -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
}
actual_buffers="$(sql <<< "SELECT pg_size_bytes(current_setting('shared_buffers'))/1024/1024")"
[[ "$actual_buffers" == "$expected_buffers" ]] || { echo "shared_buffers mismatch: $actual_buffers MB" >&2; exit 1; }
[[ "$(sql <<< "SELECT count(*) FROM pg_extension WHERE extname='pg_stat_statements'")" == 1 ]]
# This view fails if the extension exists but was never preloaded.
sql <<< "SELECT count(*) FROM pg_stat_statements" >/dev/null
[[ "$(sql <<< "SELECT count(*) FROM pg_index WHERE indexrelid IN ('idx_jobs_queued_depth'::regclass,'idx_fixtures_unresolved_retention'::regclass) AND indisvalid")" == 2 ]]
# Tiny QA tables may legitimately prefer sequential scans. A production-size
# job history must use the partial queue index rather than scan completed jobs.
job_rows="$(sql <<< "SELECT reltuples::bigint FROM pg_class WHERE oid='jobs'::regclass")"
if (( job_rows > 10000 )); then
 sql <<< "EXPLAIN (FORMAT JSON) SELECT queue,count(*) FROM jobs WHERE status='queued' GROUP BY queue" | python3 -c '
import json,sys
p=json.load(sys.stdin)
def visit(x):
 if isinstance(x,dict):
  if x.get("Relation Name")=="jobs" and "Seq Scan" in x.get("Node Type",""): raise SystemExit("queue metrics still scan job history")
  for v in x.values(): visit(v)
 elif isinstance(x,list):
  for v in x: visit(v)
visit(p)'
fi
printf 'backend checks passed: project=%s shared_buffers=%sMB queue indexes valid\n' "$project" "$actual_buffers"
