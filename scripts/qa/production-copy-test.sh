#!/usr/bin/env bash
# Test the code on qa against production's football data, without touching it.
#
#   gh workflow run qa-deploy.yml --ref qa -f production_copy_test=true
#
# WHY THIS EXISTS. QA's provider is a static mock, so QA can show a path runs but
# not how a model behaves on production's cups, statistics and odds. Replaying on
# production itself is no answer: production lacks the new code until it is
# promoted, and shadow settlements are immutable, so a test could not be cleared.
#
# So this streams named tables into a scratch database inside QA's PostgreSQL,
# runs QA's worker against it, prints the results and drops it. It is one database
# for one run, not an environment (docs/operations/environments.md).
#
# - Production is read-only: every session sets default_transaction_read_only.
# - Only the tables listed below are copied. They hold fixtures, statistics, odds
#   and public predictions. Nothing about users is on the list.
# - The scratch database is dropped on every exit, and at the start of the next run
#   if a killed job left one behind.
#
# Runs on the host as the deploy user, which reaches production's Docker. QA's
# rootless Docker is reached by re-running this script as the QA user (`qa ...`).
set -euo pipefail

SCRATCH_DB="sports_prediction_prodcopy"
PROD_CONTAINER="${PROD_POSTGRES_CONTAINER:-sports-prediction-platform-postgres-1}"
PROD_DB="${PROD_POSTGRES_DB:-sports_prediction}"
PROD_USER="${PROD_POSTGRES_USER:-sports}"
QA_DEPLOY_DIR="${QA_DEPLOY_DIR:-/home/dev/sokalens}"
QA_DB="sports_prediction_qa"
QA_USER="sports"
QA_SCRIPT="$QA_DEPLOY_DIR/scripts/qa/production-copy-test.sh"
PROJECT="${COMPOSE_PROJECT_NAME:-sokalens-dev}"
REPLAY_BATCHES="${REPLAY_BATCHES:-4}"
REPLAY_LIMIT="${REPLAY_LIMIT:-500}"
UPCOMING_LIMIT="${UPCOMING_LIMIT:-300}"
MAX_COPY_GB="${MAX_COPY_GB:-12}"

# Table|filter. Order does not matter: constraints are off while loading.
TABLES=(
  "sports|"
  "leagues|"
  "league_coverage|"
  "teams|"
  "fixtures|"
  "fixture_statistics|"
  "accuracy_snapshots|"
  # The champion reads 1X2 prices only, and replay targets are recent fixtures.
  "odds|fetched_at > now() - interval '120 days' AND lower(market) IN ('1x2', 'match winner', 'match_winner')"
  "engine_predictions|superseded_at IS NULL AND fixture_id IN (SELECT id FROM fixtures WHERE kickoff_at > now())"
)

log() { printf '\n[production-copy] %s\n' "$*"; }

# --- the QA side, running as the QA user --------------------------------------

qa_compose() {
  COMPOSE_PROJECT_NAME="$PROJECT" docker compose \
    -f "$QA_DEPLOY_DIR/infra/docker/docker-compose.yml" \
    -f "$QA_DEPLOY_DIR/infra/deploy/docker-compose.qa.yml" \
    --env-file "$QA_DEPLOY_DIR/.env.qa" "$@"
}

qa_psql() {
  local database="$1"
  shift
  qa_compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U "$QA_USER" -d "$database" "$@"
}

# The QA worker with its database URL pointed at the scratch copy. The rewrite
# happens inside the container so the password is never on a host command line.
engine() {
  qa_compose exec -T -e "SCRATCH_DB=$SCRATCH_DB" -e "QA_DB=$QA_DB" worker sh -c '
    DATABASE_URL=$(printf "%s" "$DATABASE_URL" | sed "s#/$QA_DB#/$SCRATCH_DB#")
    case "$DATABASE_URL" in *"/$SCRATCH_DB"*) ;; *) echo "scratch URL was not built" >&2; exit 1 ;; esac
    export DATABASE_URL
    exec python -m prediction_engine.cli "$@"
  ' sh "$@"
}

qa_drop() {
  qa_psql postgres -q -c "DROP DATABASE IF EXISTS $SCRATCH_DB WITH (FORCE)"
}

qa_create() {
  qa_drop
  qa_psql postgres -q -c "CREATE DATABASE $SCRATCH_DB"
  # QA is already on the code under test, so its schema is the one to test. The
  # registry and market catalogue come from QA too: they name the models this code
  # expects, and production has not been promoted to them yet.
  qa_compose exec -T postgres pg_dump -U "$QA_USER" -d "$QA_DB" --schema-only --no-owner --no-privileges \
    | qa_psql "$SCRATCH_DB" -q >/dev/null
  qa_compose exec -T postgres pg_dump -U "$QA_USER" -d "$QA_DB" --data-only --no-owner --no-privileges \
    --disable-triggers -t model_registry -t market_metadata \
    | qa_psql "$SCRATCH_DB" -q >/dev/null
}

qa_load() {
  local table="$1" columns="$2"
  qa_compose exec -T -e "PGOPTIONS=-c session_replication_role=replica" postgres \
    psql -X -v ON_ERROR_STOP=1 -U "$QA_USER" -d "$SCRATCH_DB" -q -c "COPY $table ($columns) FROM STDIN"
}

qa_size_gb() {
  qa_psql "$SCRATCH_DB" -At -c "SELECT round(pg_database_size(current_database()) / 1073741824.0, 2)"
}

qa_test() {
  # COPY keeps production's ids without moving the sequences, so the engine's first
  # insert would collide with row 1.
  qa_psql "$SCRATCH_DB" -q <<'SQL'
DO $$
DECLARE
  owned record;
  highest bigint;
BEGIN
  FOR owned IN
    SELECT sequence.oid AS sequence_oid, owner.oid AS table_oid, attribute.attname AS column_name
    FROM pg_class AS sequence
    JOIN pg_depend AS dependency
      ON dependency.objid = sequence.oid AND dependency.deptype IN ('a', 'i')
    JOIN pg_class AS owner ON owner.oid = dependency.refobjid
    JOIN pg_attribute AS attribute
      ON attribute.attrelid = owner.oid AND attribute.attnum = dependency.refobjsubid
    WHERE sequence.relkind = 'S'
  LOOP
    EXECUTE format('SELECT max(%I) FROM %s', owned.column_name, owned.table_oid::regclass) INTO highest;
    IF highest IS NOT NULL THEN
      PERFORM setval(owned.sequence_oid::regclass, highest);
    END IF;
  END LOOP;
END
$$;
SQL

  log "what was copied"
  qa_psql "$SCRATCH_DB" -At <<'SQL'
SELECT 'fixtures ' || count(*) || ', finished ' || count(*) FILTER (WHERE home_goals_ft IS NOT NULL)
  || ', upcoming ' || count(*) FILTER (WHERE kickoff_at > now()) FROM fixtures;
SELECT 'fixtures with statistics ' || count(DISTINCT fixture_id) FROM fixture_statistics;
SELECT 'odds rows ' || count(*) FROM odds;
SELECT 'live predictions ' || count(*) || ' across ' || count(DISTINCT fixture_id) || ' fixtures' FROM engine_predictions;
SQL

  log "v3 on production's upcoming fixtures with no stored forecasts, limit $UPCOMING_LIMIT"
  local started=$SECONDS
  engine run-scope --scope upcoming --limit "$UPCOMING_LIMIT" | python3 -c '
import json, sys
for line in sys.stdin:
    line = line.strip()
    if line.startswith("{"):
        row = json.loads(line)
        print(json.dumps({key: value for key, value in row.items() if key not in {"fixture_ids", "outcomes"}}, sort_keys=True)[:1500])
'
  log "that took $((SECONDS - started)) seconds"

  log "Models B and C forecast those fixtures"
  started=$SECONDS
  engine run-challengers --origin live --limit "$UPCOMING_LIMIT" --min-interval-minutes 0 | tail -1 | cut -c1-600
  log "that took $((SECONDS - started)) seconds"

  log "v3 publishes again, now reading those forecasts"
  started=$SECONDS
  engine run-scope --scope upcoming --limit "$UPCOMING_LIMIT" | tail -1 | cut -c1-300
  log "that took $((SECONDS - started)) seconds"

  log "v2 as live on production against v3 on the same fixtures"
  # One line per market, so the log can be read and diffed without a parser.
  qa_psql "$SCRATCH_DB" -At -F ' | ' <<'SQL'
SELECT 'market | source | rows | mean v2 | mean v3 | same pick share | v2 picks at 80%+ | v3 picks at 80%+';
SELECT new.market, new.features_used ->> 'probability_source', count(*), round(avg(old.probability), 3), round(avg(new.probability), 3),
       round(avg(CASE WHEN old.prediction = new.prediction THEN 1.0 ELSE 0.0 END), 3),
       count(*) FILTER (WHERE old.probability >= 0.8), count(*) FILTER (WHERE new.probability >= 0.8)
FROM engine_predictions AS new
JOIN engine_predictions AS old
  ON old.fixture_id = new.fixture_id AND old.market = new.market AND old.model_version = 'python-engine-v2'
WHERE new.model_version = 'python-engine-v3' AND new.superseded_at IS NULL
GROUP BY new.market, new.features_used ->> 'probability_source'
ORDER BY new.market, 2;
SELECT 'v3 rows without a registry link: ' || count(*)
FROM engine_predictions WHERE model_version = 'python-engine-v3' AND model_registry_id IS NULL;
SELECT 'v3 fixtures published ' || count(DISTINCT fixture_id) || ', reading a stored forecast '
  || count(DISTINCT fixture_id) FILTER (WHERE features_used ->> 'probability_source' <> 'champion')
FROM engine_predictions WHERE model_version = 'python-engine-v3' AND superseded_at IS NULL;
SQL

  # A release in which no row reads a forecast publishes the champion under a new
  # name. Nothing else notices, because every row is still valid.
  local reading
  reading="$(qa_psql "$SCRATCH_DB" -At -c "SELECT count(*) FROM engine_predictions WHERE model_version = 'python-engine-v3' AND superseded_at IS NULL AND features_used ->> 'probability_source' <> 'champion'")"
  if [ "$reading" -eq 0 ]; then
    log "FAILED: no v3 row read a Model B or C forecast"
    return 1
  fi

  local batch
  for batch in $(seq 1 "$REPLAY_BATCHES"); do
    log "replay batch $batch of $REPLAY_BATCHES, limit $REPLAY_LIMIT"
    started=$SECONDS
    engine run-challengers --origin replay --limit "$REPLAY_LIMIT" | python3 -c '
import json, sys
for line in sys.stdin:
    line = line.strip()
    if line.startswith("{"):
        row = json.loads(line)
        print({key: row.get(key) for key in ("targets", "champion_paired", "challenger_forecasts", "markets")})
'
    log "that took $((SECONDS - started)) seconds"
  done

  log "settlement"
  engine settle-challengers --limit 100000

  log "report"
  # One line per market, so the log can be read and diffed without a parser.
  engine challenger-report --origin replay --no-write | python3 -c '
import json, sys
for line in sys.stdin:
    line = line.strip()
    if not line.startswith("{"):
        continue
    row = json.loads(line)
    for entry in row.get("evaluations", []):
        print("1x2-shadow", entry["model_key"], entry["lifecycle_stage"], entry["paired_with_champion"], entry["log_loss"], entry["champion_log_loss"], entry.get("z"))
    markets = row.get("markets", {})
    if "error_type" in markets:
        print("MARKET STEP FAILED", markets)
        raise SystemExit(1)
    print("market_settled_rows", markets.get("settled_rows"))
    for entry in sorted(markets.get("market_evaluations", []), key=lambda item: (item["market"], item["model_key"])):
        print("market", entry["market"], entry["model_key"], entry["paired_with_champion"], entry["log_loss"], entry["champion_log_loss"], entry.get("z"))
'
}

if [ "${1:-}" = "qa" ]; then
  shift
  cd "$QA_DEPLOY_DIR"
  case "$1" in
    create) qa_create ;;
    drop) qa_drop ;;
    load) qa_load "$2" "$3" ;;
    size) qa_size_gb ;;
    test) qa_test ;;
    *) echo "unknown QA step: $1" >&2; exit 2 ;;
  esac
  exit 0
fi

# --- the production side, running as the deploy user ---------------------------

qa() {
  sudo -u dev env HOME=/home/dev \
    REPLAY_BATCHES="$REPLAY_BATCHES" REPLAY_LIMIT="$REPLAY_LIMIT" UPCOMING_LIMIT="$UPCOMING_LIMIT" \
    bash "$QA_SCRIPT" qa "$@"
}

prod_psql() {
  docker exec -i -e "PGOPTIONS=-c default_transaction_read_only=on" "$PROD_CONTAINER" \
    psql -X -v ON_ERROR_STOP=1 -U "$PROD_USER" -d "$PROD_DB" "$@"
}

clean_up() {
  trap - EXIT HUP INT TERM
  log "dropping $SCRATCH_DB"
  qa drop || echo "WARNING: $SCRATCH_DB was not dropped; the next run removes it" >&2
}
trap clean_up EXIT HUP INT TERM

log "disk before copying: $(df -h --output=avail,pcent / | tail -1 | xargs)"

log "creating $SCRATCH_DB from QA's schema"
qa create

for entry in "${TABLES[@]}"; do
  table="${entry%%|*}"
  filter="${entry#*|}"
  columns=$(prod_psql -At -c "SELECT string_agg(quote_ident(column_name), ',' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = '$table' AND is_generated = 'NEVER'")
  if [ -z "$columns" ]; then
    echo "ERROR: production has no table $table" >&2
    exit 1
  fi
  started=$SECONDS
  prod_psql -c "COPY (SELECT $columns FROM $table${filter:+ WHERE $filter}) TO STDOUT" | qa load "$table" "$columns"
  # A filtered table's size on production says little about what its filter keeps,
  # so the limit is checked against what actually arrived.
  size_gb=$(qa size)
  log "copied $table in $((SECONDS - started)) seconds; scratch database now ${size_gb} GB"
  if awk -v size="$size_gb" -v limit="$MAX_COPY_GB" 'BEGIN { exit !(size > limit) }'; then
    echo "ERROR: ${size_gb} GB is over the ${MAX_COPY_GB} GB this test will copy" >&2
    exit 1
  fi
done

qa test
