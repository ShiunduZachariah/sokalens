#!/usr/bin/env bash
set -euo pipefail
project="${1:?compose project required}"
release_sha="${2:?release SHA required}"
[[ "$project" == sports-prediction-platform || "$project" == sokalens-dev ]] || exit 2
[[ "$release_sha" =~ ^[0-9a-f]{40}$ ]] || exit 2
source_dir="$(cd "$(dirname "$0")" && pwd)"
for _ in $(seq 1 1000); do
 count="$(
  { printf '%s\n' 'BEGIN;' "SET LOCAL statement_timeout='20s';" "SET LOCAL lock_timeout='2s';" 'SET LOCAL search_path=public,pg_catalog;'
    cat "$source_dir/restore-expired-predictions.sql"
    printf '%s\n' 'COMMIT;'
  } | docker exec -i "$project-postgres-1" sh -ceu 'exec psql -X -qAt -v ON_ERROR_STOP=1 -v "release_sha=$1" -U "$POSTGRES_USER" -d "$POSTGRES_DB"' sh "$release_sha"
 )"
 [[ "$count" =~ ^[0-9]+$ ]] || { echo 'unexpected restoration result' >&2; exit 1; }
 printf 'restored raw prediction recovery records=%s\n' "$count"
 (( count != 0 )) || exit 0
done
echo 'recovery batch limit reached; keep the worker stopped and continue recovery' >&2
exit 1
