#!/usr/bin/env bash
#
# Capacity diagnostics (in-play architecture §18.1).
#
# Answers one question: does this host need more RAM before in-play snapshot
# ingestion widens, or does it have headroom? §18.3 recommends 12 GB before
# widening to Tier B, but the document is explicit that the decision comes from
# these measurements and not from the recommendation -- "measure before buying".
#
# WHY THE TIMING MATTERS MORE THAN THE NUMBER. An OOM kill during a Saturday
# fixture window loses snapshots permanently. Every other failure in the
# ingestion path is recoverable by re-running a job; that one is not, because
# the minute-by-minute state of a match that has finished cannot be re-fetched
# from any provider at any price. So this is a Phase 0 measurement, taken while
# there is still time to act on it, not a Phase 4 one.
#
# Read-only. It starts nothing, stops nothing, and writes nothing outside its
# own output. Safe to run on production during a fixture window -- which is
# also the most informative time to run it.
#
# Usage:  bash infra/deploy/capacity-report.sh
#         bash infra/deploy/capacity-report.sh > capacity-$(date -u +%FT%H%MZ).txt

set -euo pipefail

section() { printf '\n== %s ==\n' "$1"; }
missing() { printf '(unavailable: %s)\n' "$1"; }

printf 'capacity_report_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf 'host=%s\n' "$(hostname 2>/dev/null || echo unknown)"

section "memory"
free -h 2>/dev/null || missing "free"

section "memory pressure"
# PSI is the signal that distinguishes "the page cache is doing its job" from
# "processes are stalling on memory". §18.1: "some avg60" above 5 is real
# pressure. A high `free -h` used figure with near-zero PSI is healthy caching,
# which is the reading most often mistaken for a problem.
if [ -r /proc/pressure/memory ]; then
  cat /proc/pressure/memory
else
  missing "/proc/pressure/memory (kernel without PSI, or not Linux)"
fi

section "swap activity"
# si/so are the columns that matter. Any sustained swapping is a red flag on a
# host running Postgres.
vmstat 1 5 2>/dev/null || missing "vmstat (install procps)"

section "container memory"
if command -v docker >/dev/null 2>&1; then
  docker stats --no-stream --format "table {{.Name}}\t{{.MemUsage}}\t{{.MemPerc}}\t{{.CPUPerc}}" 2>/dev/null || missing "docker stats"
else
  missing "docker"
fi

section "container restarts and OOM kills"
# A container that has been OOM-killed once will be again. This is the row that
# turns "it feels slow" into a decision.
if command -v docker >/dev/null 2>&1; then
  for container in $(docker ps --all --format '{{.Names}}' 2>/dev/null); do
    docker inspect --format \
      'name={{.Name}} status={{.State.Status}} oom_killed={{.State.OOMKilled}} restarts={{.RestartCount}} exit_code={{.State.ExitCode}}' \
      "$container" 2>/dev/null || true
  done
else
  missing "docker"
fi

section "postgres cache hit ratio"
# The upgrade signal. Below ~98% means Postgres is reading from disk for data
# that should be resident, and more RAM would directly buy back those reads.
# Above it, the working set fits and a bigger box mostly buys headroom.
#
# One caveat the number does not carry: this is cumulative since the last stats
# reset, so a long-lived server averages away a bad afternoon. Re-run it during
# a fixture window and compare.
postgres_container=""
if command -v docker >/dev/null 2>&1; then
  # The `|| true` is load-bearing under `set -euo pipefail`. A command
  # substitution whose pipeline fails takes its exit status, and the assignment
  # then aborts the script -- so a Docker daemon that is down would end the
  # report here, silently, before disk usage and the reading guide.
  #
  # That is the exact case this exists for: you run a capacity report because
  # something is already wrong, and "the daemon is down" is a very plausible
  # something. Every section is meant to degrade to a named line, not to be the
  # last one printed.
  postgres_container="$(docker ps --filter label=com.docker.compose.service=postgres --format '{{.ID}}' 2>/dev/null | head -n 1)" || true
fi
# The credentials are read from inside the container rather than guessed or
# taken from the caller's shell. The container is the only thing that reliably
# knows them, and a section that prints "unavailable" because it tried the
# wrong username is worse than no section: it looks like a finding.
pg() {
  docker exec "$postgres_container" sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc "'"$1"'"' 2>/dev/null
}

if [ -n "$postgres_container" ]; then
  pg "SELECT datname || ' cache_hit_pct=' || coalesce(round(100.0*blks_hit/nullif(blks_hit+blks_read,0),2)::text, 'no-reads-yet') || ' blocks_read=' || blks_read || ' stats_since=' || coalesce(stats_reset::text, 'never') FROM pg_stat_database WHERE datname NOT LIKE 'template%' AND datname IS NOT NULL AND blks_hit + blks_read > 0" \
    || missing "psql inside the postgres container"
else
  missing "postgres container"
fi

section "disk"
df -h / 2>/dev/null || missing "df"
if command -v docker >/dev/null 2>&1; then
  echo "-- docker reclaimable --"
  docker system df 2>/dev/null || missing "docker system df"
fi

section "largest disk consumers"
# §18.2 predicts the answer here is Loki retention or accumulated Docker
# layers, not the database -- snapshots reach only ~1.9 GB at steady state. If
# that holds, `docker builder prune -a -f` plus a Loki retention policy reclaims
# more than an upgrade would add, and the upgrade is about RAM, not disk.
du -sh /var/lib/docker 2>/dev/null || missing "du /var/lib/docker (needs root)"

section "database size"
if [ -n "$postgres_container" ]; then
  pg "SELECT current_database() || ' size=' || pg_size_pretty(pg_database_size(current_database()))" \
    || missing "psql inside the postgres container"
  # Snapshot ingestion reaches only ~1.9 GB at steady state (10.1), so a
  # database already far larger than that is growing somewhere else, and the
  # in-play plan is not what needs revisiting.
  pg "SELECT 'largest_tables: ' || string_agg(name || '=' || size, ', ') FROM (SELECT relname AS name, pg_size_pretty(pg_total_relation_size(c.oid)) AS size FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public' AND c.relkind = 'r' ORDER BY pg_total_relation_size(c.oid) DESC LIMIT 5) t" \
    || true
else
  missing "postgres container"
fi

section "how to read this"
cat <<'GUIDE'
Upgrade RAM when any of these hold:
  * memory PSI "some avg60" is sustained above 5
  * vmstat si/so show sustained swapping
  * postgres cache_hit_pct is below ~98%
  * any container reports oom_killed=true

All clean means headroom. §18.3 recommends 12 GB before widening in-play
ingestion to Tier B; take 16 GB only if the price gap is small.

Before spending anything, settle the open question in §18.4: Postgres reading
about 1 GiB on dashboards is most likely shared buffers plus page cache being
counted, not a leak. If it is genuinely a leak, more RAM only delays it.
GUIDE
