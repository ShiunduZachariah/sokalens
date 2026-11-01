#!/usr/bin/env bash
# Read-only production promotion gate. No inventory is fabricated by deployment.
set -euo pipefail

acca_readiness() {
  local attempt row active duplicates invalid markets consecutive=0 previous=""
  # The scheduler enqueues prediction generation on startup. A successful
  # generation enqueues acca.generate-inventory as an idempotent follow-up,
  # so the budget covers both jobs and a deployment landing in an already-spent
  # prediction cadence bucket (15m by default). The gate still requires stable
  # valid inventory; it never fabricates rows or weakens validity checks.
  # 240 x 10s = 40m covers that wait; 120 x 10s = 20m left no margin.
  local attempts="${ACCA_READINESS_ATTEMPTS:-240}"
  local interval="${ACCA_READINESS_INTERVAL:-10}"
  local schema="${ACCA_DB_SCHEMA:-public}"
  local postgres="${ACCA_POSTGRES_CONTAINER:-sports-prediction-platform-postgres-1}"
  local web="${ACCA_WEB_CONTAINER:-sports-prediction-platform-web-1}"
  local api="${ACCA_READINESS_API_URL:-http://127.0.0.1:8080}"
  [[ "$schema" =~ ^[a-z_][a-z0-9_]*$ ]] || return 1
  local sql="
WITH active AS (SELECT * FROM $schema.acca_slips WHERE status='active'),
invalid AS (
 SELECT s.id FROM active s
 WHERE s.earliest_kickoff <= now() OR s.leg_count NOT BETWEEN 2 AND 20
 OR s.combined_fair_odds < round(power(1.5, s.leg_count::double precision / 2)::numeric, 4)
 OR s.combined_fair_odds > 1000
 OR EXISTS (SELECT 1 FROM $schema.acca_slip_legs l
   WHERE l.slip_id=s.id AND l.fair_odds_at_generation < 1.10)
 OR s.leg_count <> (SELECT count(*) FROM $schema.acca_slip_legs l WHERE l.slip_id=s.id)
 OR s.leg_count <> (SELECT count(DISTINCT l.match_id) FROM $schema.acca_slip_legs l WHERE l.slip_id=s.id)
 OR EXISTS (SELECT 1 FROM $schema.acca_slip_legs l
   JOIN public.matches m ON m.id=l.match_id
   LEFT JOIN public.engine_predictions ep ON ep.id=CASE
     WHEN l.prediction_id ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
     THEN l.prediction_id::uuid END AND l.prediction_source='engine'
   WHERE l.slip_id=s.id AND (m.kickoff_at <= now() OR ep.id IS NULL
     OR ep.superseded_at IS NOT NULL OR l.market_key='correct_score'))
)
SELECT (SELECT count(*) FROM active),
 (SELECT count(*)-count(DISTINCT composition_hash) FROM $schema.acca_slips),
 (SELECT count(*) FROM invalid),
 (SELECT count(DISTINCT market_tab) FROM active);"
  # 15s was the original budget and it stopped being achievable once the slip
  # pool grew: the statement now measures ~43s on production, so every deploy
  # timed out here, ON_ERROR_STOP aborted, and the release rolled back. The
  # retry loop below never helped, because `|| return 1` treats a timeout as a
  # hard failure rather than an attempt worth repeating.
  #
  # Measured breakdown of the 43s, none of it pathological:
  #   ~13s  count(DISTINCT composition_hash) over all 658k slips
  #   ~10s  the per-slip invariant checks (3k active slips)
  #   ~20s  the legs -> matches -> engine_predictions existence check
  #
  # So this is a budget that no longer matches the data, not a query to index
  # around -- the per-slip probes already use acca_slip_legs_pkey. Two things
  # would genuinely shrink it, and both are out of scope here: the dedup scan
  # reports 125,982 heap fetches against 658,335 rows, so a VACUUM would restore
  # the visibility map and most of that 13s; and the scan covers every slip ever
  # generated when only ~3,000 are active, so a retention policy on expired
  # slips is what stops this growing back into the ceiling.
  #
  # Kept as an explicit ceiling rather than removed: the gate exists to catch a
  # database that is genuinely wedged, and an unbounded wait would hang the
  # deploy instead of failing it. The statement runs at least three times (the
  # consecutive-reads requirement below), so this bounds a deploy at ~6 minutes
  # of gating in the worst case.
  local stmt_timeout="${ACCA_READINESS_STATEMENT_TIMEOUT_MS:-120000}"
  [[ "$stmt_timeout" =~ ^[1-9][0-9]*$ ]] || return 1
  [[ "$attempts" =~ ^[1-9][0-9]*$ && "$interval" =~ ^[0-9]+$ ]] || return 1
  for ((attempt=1; attempt<=attempts; attempt++)); do
    row="$(docker exec "$postgres" sh -c \
      'export PGOPTIONS="-c statement_timeout=$4"; exec psql -X -v ON_ERROR_STOP=1 -U "${3:-$POSTGRES_USER}" -d "${2:-$POSTGRES_DB}" -At -c "$1"' sh "$sql" "${ACCA_POSTGRES_DB:-}" "${ACCA_POSTGRES_USER:-}" "$stmt_timeout")" || return 1
    if [[ "$row" =~ ^[0-9]+\|[0-9]+\|[0-9]+\|[0-9]+$ ]]; then
      IFS='|' read -r active duplicates invalid markets <<< "$row"
      if (( active > 0 && active <= 3000 && duplicates == 0 && invalid == 0 && markets > 4 )); then
        if [ "$row" = "$previous" ]; then consecutive=$((consecutive+1)); else consecutive=1; fi
        if (( consecutive >= 3 )); then
          curl --fail --silent --show-error --max-time 20 "$api/readyz" >/dev/null || return 1
          # Read twice to exercise the cache path without flushing Redis.
          local pass path
          for pass in 1 2; do
            for path in '/v1/accas?limit=10' '/v1/accas/featured'; do
              curl --fail --silent --show-error --max-time 20 --max-filesize 1048576 "$api$path" |
                docker exec -i "$web" node -e '
                  let body = "";
                  process.stdin.setEncoding("utf8");
                  process.stdin.on("data", chunk => {
                    body += chunk;
                    if (body.length > 1048576) process.exit(1);
                  });
                  process.stdin.on("end", () => {
                    try {
                      const { items } = JSON.parse(body);
                      if (!Array.isArray(items) || !items.length || items.length > 50 ||
                          items.some(s => !s || typeof s.id !== "string" || !s.id ||
                            !Array.isArray(s.legs) || s.legs.length < 2 || s.legs.length > 20 ||
                            s.legs.some(l => !l || !Number.isFinite(l.fair_odds) || l.fair_odds < 1.10) ||
                            !Number.isFinite(s.combined_fair_odds) ||
                            s.combined_fair_odds < Math.pow(1.5, s.legs.length / 2) ||
                            s.combined_fair_odds > 1000)) process.exit(1);
                    } catch { process.exit(1); }
                  });
                ' || return 1
            done
          done
          echo "ACCA readiness passed: $active active slips, $markets market tabs; three stable samples."
          return 0
        fi
      else
        consecutive=0
      fi
    else
      echo 'ERROR: malformed ACCA database readiness result' >&2
      return 1
    fi
    previous="$row"
    echo "ACCA readiness attempt $attempt/$attempts: $row (active|duplicates|invalid|markets)"
    if (( attempt < attempts )); then sleep "$interval"; fi
  done
  echo 'ERROR: ACCA inventory is not ready; frontend promotion refused.' >&2
  return 1
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then acca_readiness; fi
