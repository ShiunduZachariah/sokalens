#!/usr/bin/env bash
set -euo pipefail

postgres_container="$(docker ps --filter label=com.docker.compose.service=postgres --format '{{.ID}}' | head -n 1)"
if [[ -z "${postgres_container}" ]]; then
  echo "PostgreSQL container is not running" >&2
  exit 1
fi

echo "report_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "host_memory"
free -h
echo "container_memory"
docker stats --no-stream --format 'name={{.Name}} memory={{.MemUsage}} percent={{.MemPerc}} cpu={{.CPUPerc}} pids={{.PIDs}}' "${postgres_container}"
echo "postgres_processes_kib"
# sed consumes the complete stream, avoiding SIGPIPE under `set -o pipefail`
# when PostgreSQL has more than 24 child processes.
docker top "${postgres_container}" -eo pid,ppid,rss,vsz,comm | sed -n '1,25p'

echo "account_service_health"
for service in api web; do
  container="$(docker ps --filter "label=com.docker.compose.service=${service}" --format '{{.ID}}' | head -n 1)"
  if [[ -z "${container}" ]]; then
    echo "service=${service} running=false"
    continue
  fi
  docker inspect --format 'service='"${service}"' running={{.State.Running}} status={{.State.Status}} exit_code={{.State.ExitCode}} oom_killed={{.State.OOMKilled}} restarts={{.RestartCount}} health={{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}} error={{json .State.Error}}' "${container}"
done
curl --silent --show-error --output /dev/null --write-out 'host_api_ready status=%{http_code} time=%{time_total}\n' --max-time 10 http://127.0.0.1:8080/readyz || true
curl --silent --show-error --output /dev/null --write-out 'host_account_access status=%{http_code} time=%{time_total}\n' --max-time 10 http://127.0.0.1:8080/v1/access || true
web_container="$(docker ps --filter label=com.docker.compose.service=web --format '{{.ID}}' | head -n 1)"
if [[ -n "${web_container}" ]]; then
  docker exec "${web_container}" node -e "fetch('http://api:8080/v1/access').then(async response => console.log('web_to_account status=' + response.status)).catch(error => { console.log('web_to_account error=' + error.name); process.exitCode = 1 })" || true
fi
api_container="$(docker ps --all --filter label=com.docker.compose.service=api --format '{{.ID}}' | head -n 1)"
if [[ -n "${api_container}" ]]; then
  echo "api_recent_startup_logs"
  docker logs --tail 80 "${api_container}" 2>&1 |
    sed -E 's/([A-Za-z0-9_]*(SECRET|TOKEN|PASSWORD|KEY)[A-Za-z0-9_]*[=: ]+)[^ ,"]+/\1[redacted]/Ig'
fi

docker exec -i "${postgres_container}" sh -ceu 'exec psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"' <<'SQL'
\pset pager off
\echo postgres_settings
SELECT name, setting, unit
FROM pg_settings
WHERE name IN (
  'shared_buffers', 'effective_cache_size', 'work_mem',
  'maintenance_work_mem', 'max_connections', 'huge_pages',
  'autovacuum_max_workers', 'max_parallel_workers',
  'max_parallel_workers_per_gather'
)
ORDER BY name;
\echo connection_counts
SELECT backend_type, COALESCE(state, 'n/a') AS state, count(*)
FROM pg_stat_activity
GROUP BY backend_type, state
ORDER BY count(*) DESC, backend_type, state;
\echo database_sizes
SELECT datname, pg_size_pretty(pg_database_size(datname)) AS size
FROM pg_database
WHERE datallowconn
ORDER BY pg_database_size(datname) DESC;
\echo cache_and_io_counters
SELECT datname, blks_read, blks_hit,
       round(100.0 * blks_hit / NULLIF(blks_hit + blks_read, 0), 2) AS cache_hit_percent,
       temp_files, pg_size_pretty(temp_bytes) AS temp_bytes
FROM pg_stat_database
WHERE datname = current_database();
\echo largest_relations
SELECT n.nspname AS schema_name, c.relname,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total_size
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r','m') AND n.nspname NOT IN ('pg_catalog','information_schema')
ORDER BY pg_total_relation_size(c.oid) DESC
LIMIT 15;
SQL
