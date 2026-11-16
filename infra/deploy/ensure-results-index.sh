#!/usr/bin/env bash
# Build the declared additive index online before the transactional migrator.
# A valid index is a no-op; only our matching, interrupted build is removed.
set -euo pipefail
project="${1:?compose project required}"
[[ "$project" == sports-prediction-platform || "$project" == sokalens-dev ]] || exit 2
root="$(cd "$(dirname "$0")/../.." && pwd)"
index=idx_engine_predictions_results_btts_cover
sql() {
 docker exec -i "$project-postgres-1" sh -ceu 'PGOPTIONS="-c statement_timeout=600000 -c lock_timeout=60000" exec psql -X -qAt -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
}
state() {
 sql <<'SQL'
-- RESULTS_COVER_INDEX_STATE
SELECT CASE
 WHEN i.indexrelid IS NULL THEN 'missing'
 WHEN i.indrelid <> 'engine_predictions'::regclass
   OR i.indnkeyatts <> 1 OR i.indnatts <> 6
   OR ARRAY(SELECT a.attname FROM unnest(i.indkey) WITH ORDINALITY k(attnum,n)
      JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=k.attnum ORDER BY k.n)
      <> ARRAY['id','fixture_id','market','generated_at','prediction','confidence']::name[]
   OR pg_get_expr(i.indpred,i.indrelid) IS DISTINCT FROM '(market = ''btts_total_goals''::text)'
   OR (SELECT amname FROM pg_class c JOIN pg_am am ON am.oid=c.relam WHERE c.oid=i.indexrelid) <> 'btree'
 THEN 'conflict'
 WHEN i.indisvalid AND i.indisready THEN 'valid'
 ELSE 'invalid'
 END
FROM (SELECT to_regclass('idx_engine_predictions_results_btts_cover') AS id) target
LEFT JOIN pg_index i ON i.indexrelid=target.id;
SQL
}
current="$(state)"
case "$current" in
 valid) echo 'results covering index already valid'; exit 0 ;;
 invalid) sql <<< "DROP INDEX CONCURRENTLY $index" ;;
 missing) ;;
 *) echo 'results index definition conflicts with the declared migration; refusing replacement' >&2; exit 1 ;;
esac
ddl="$(sed '/^[[:space:]]*--/d' "$root/infra/db/migrations/000096_results_btts_cover.up.sql")"
[[ "$ddl" == *"CREATE INDEX IF NOT EXISTS $index"* ]]
# Execute one statement outside a transaction; writers remain available.
sql <<< "${ddl/CREATE INDEX IF NOT EXISTS/CREATE INDEX CONCURRENTLY IF NOT EXISTS}"
[[ "$(state)" == valid ]] || { echo 'online results index build did not become valid' >&2; exit 1; }
sql <<< 'ANALYZE engine_predictions'
echo 'results covering index built concurrently and verified'
