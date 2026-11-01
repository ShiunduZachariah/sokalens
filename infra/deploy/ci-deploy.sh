#!/bin/bash
# CI-driven production deploy with restore-verified backup and health-gated LKG rollback.
set -euo pipefail
umask 022

SERVICE_LIST="${1:-all}"
TARBALL="${2:-/tmp/sokalens-src.tar.gz}"
ACCOUNT_LAUNCH_ENV_FILE="${3:-/tmp/sokalens-account-launch.env}"
RELEASE_SHA="${4:-unknown-release}"
export RELEASE_SHA
PRODUCTION_WEB_URL="https://sokalens.com"
DEPLOY_DIR="/home/deploy/sokalens"
OLD_DIR="/home/deploy/sokalens-old"
NEW_DIR="/tmp/sokalens-new"
LKG_ROOT="/home/deploy/sokalens-lkg"
LKG_TAG=""
LKG_MANIFEST=""
ENV_UPDATE_TEMPORARY=""
CADDY_SOURCE=""
CADDY_LIVE_CONFIG="/etc/caddy/Caddyfile"
CADDY_BACKUP="/tmp/sokalens-caddy-lkg.$$"
CADDY_CHANGED=0
SWAPPED=0
DEPLOY_SUCCEEDED=0
ROLLBACK_ACTIVE=0
ACTIVATED_SERVICES=""
BACKEND_SERVICES=""
FRONTEND_SERVICES=""
BACKEND_READY=0
LKG_BACKEND_SHA="unknown-release"
LKG_FRONTEND_SHA="unknown-release"

if [ "$SERVICE_LIST" = "proxy-only" ]; then
  SERVICES=""
elif [ "$SERVICE_LIST" = "all" ]; then
  SERVICES="api worker web admin ops-controller"
else
  SERVICES="$SERVICE_LIST"
fi

compose() {
  docker compose \
    -f infra/docker/docker-compose.yml \
    -f infra/deploy/docker-compose.prod.yml \
    --env-file .env.prod \
    --profile core --profile frontend --profile search "$@"
}

cleanup_secret_files() {
  rm -f "$ACCOUNT_LAUNCH_ENV_FILE"
  rm -f "$CADDY_BACKUP"
  if [ -n "$ENV_UPDATE_TEMPORARY" ]; then
    rm -f "$ENV_UPDATE_TEMPORARY"
  fi
}

restore_caddy_config() {
  if [ "$CADDY_CHANGED" -eq 1 ] && [ -f "$CADDY_BACKUP" ]; then
    echo "restoring last-known-good Caddy configuration" >&2
    sudo install -m 0644 "$CADDY_BACKUP" "$CADDY_LIVE_CONFIG"
    sudo -u caddy caddy validate --config "$CADDY_LIVE_CONFIG"
    sudo systemctl reload caddy
    CADDY_CHANGED=0
  fi
}

prepare_caddy_logging() {
  # Validation opens file writers too. Pre-create the managed log so a root
  # validation cannot leave a root-owned file which the service cannot open.
  sudo install -d -o caddy -g caddy -m 0750 /var/log/caddy
  sudo touch /var/log/caddy/sokalens-access.log
  sudo chown caddy:caddy /var/log/caddy/sokalens-access.log
  sudo chmod 0640 /var/log/caddy/sokalens-access.log
}

sync_caddy_config() {
  CADDY_SOURCE="$DEPLOY_DIR/infra/deploy/Caddyfile"
  [ -r "$CADDY_SOURCE" ] || { echo "ERROR: Caddy configuration is missing" >&2; return 1; }
  prepare_caddy_logging
  sudo caddy validate --config "$CADDY_SOURCE"
  sudo cp "$CADDY_LIVE_CONFIG" "$CADDY_BACKUP"
  sudo chown "$(id -u):$(id -g)" "$CADDY_BACKUP"
  chmod 600 "$CADDY_BACKUP"
  CADDY_CHANGED=1
  sudo install -m 0644 "$CADDY_SOURCE" "$CADDY_LIVE_CONFIG"
  sudo -u caddy caddy validate --config "$CADDY_LIVE_CONFIG"
  sudo systemctl reload caddy
}

wait_for_container() {
  local service="$1"
  local container="sports-prediction-platform-${service}-1"
  local deadline=$((SECONDS + 240))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if docker inspect "$container" >/dev/null 2>&1; then
      local state health
      state="$(docker inspect --format '{{.State.Status}}' "$container" 2>/dev/null || true)"
      health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container" 2>/dev/null || true)"
      if [ "$state" = "running" ] && { [ "$health" = "healthy" ] || [ "$health" = "none" ]; }; then
        return 0
      fi
    fi
    sleep 5
  done
  echo "ERROR: container health timeout for $container" >&2
  docker logs --tail 100 "$container" >&2 || true
  return 1
}

wait_for_release_health() {
  local service
  for service in postgres redis api worker web admin ops-controller; do
    wait_for_container "$service"
  done
  curl --fail --silent --show-error --retry 12 --retry-delay 5 --retry-all-errors \
    http://127.0.0.1:8080/readyz >/dev/null
  curl --fail --silent --show-error --retry 12 --retry-delay 5 --retry-all-errors \
    http://127.0.0.1:3000/ >/dev/null
  curl --fail --silent --show-error --retry 12 --retry-delay 5 --retry-all-errors \
    http://127.0.0.1:3001/ >/dev/null
}

capture_lkg() {
  local current_sha service container image_id image_name lkg_ref
  current_sha="$(cat "$DEPLOY_DIR/.release-sha" 2>/dev/null || printf 'pre-marker-%s' "$(date -u +%Y%m%dT%H%M%SZ)")"
  LKG_BACKEND_SHA="$(cat "$DEPLOY_DIR/.release-backend-sha" 2>/dev/null || printf '%s' "$current_sha")"
  LKG_FRONTEND_SHA="$(cat "$DEPLOY_DIR/.release-frontend-sha" 2>/dev/null || printf '%s' "$current_sha")"
  LKG_TAG="$(printf '%s' "$current_sha" | tr -cs 'A-Za-z0-9_.-' '-' | cut -c1-40)"
  mkdir -p "$LKG_ROOT/$LKG_TAG"
  LKG_MANIFEST="$LKG_ROOT/$LKG_TAG/manifest"
  : > "$LKG_MANIFEST"
  chmod 600 "$LKG_MANIFEST"
  printf 'release_sha|%s\n' "$current_sha" >> "$LKG_MANIFEST"
  printf 'captured_at|%s\n' "$(date -u +%FT%TZ)" >> "$LKG_MANIFEST"
  for service in api worker web admin ops-controller; do
    container="sports-prediction-platform-${service}-1"
    if ! docker inspect "$container" >/dev/null 2>&1; then
      continue
    fi
    image_id="$(docker inspect --format '{{.Image}}' "$container")"
    image_name="$(docker inspect --format '{{.Config.Image}}' "$container")"
    lkg_ref="sokalens-lkg-${service}:${LKG_TAG}"
    docker tag "$image_id" "$lkg_ref"
    printf 'image|%s|%s|%s|%s\n' "$service" "$image_name" "$lkg_ref" "$image_id" >> "$LKG_MANIFEST"
  done
  (cd "$DEPLOY_DIR" && sha256sum infra/docker/docker-compose.yml infra/deploy/docker-compose.prod.yml) \
    > "$LKG_ROOT/$LKG_TAG/config.sha256"
}

record_release_state() {
  local backend_sha="$1" frontend_sha="$2" global_sha
  if [ "$backend_sha" = "$frontend_sha" ]; then
    global_sha="$backend_sha"
  else
    global_sha="split-backend-${backend_sha}-frontend-${frontend_sha}"
  fi
  printf '%s\n' "$backend_sha" > "$DEPLOY_DIR/.release-backend-sha" || return 1
  printf '%s\n' "$frontend_sha" > "$DEPLOY_DIR/.release-frontend-sha" || return 1
  printf '%s\n' "$RELEASE_SHA" > "$DEPLOY_DIR/.release-source-sha" || return 1
  printf '%s\n' "$global_sha" > "$DEPLOY_DIR/.release-sha" || return 1
  chmod 644 "$DEPLOY_DIR/.release-backend-sha" "$DEPLOY_DIR/.release-frontend-sha" \
    "$DEPLOY_DIR/.release-source-sha" "$DEPLOY_DIR/.release-sha"
}

rollback_release() {
  [ "$ROLLBACK_ACTIVE" -eq 0 ] || return 1
  ROLLBACK_ACTIVE=1
  local frontend_activated="" restored_frontend="" service backend_sha
  for service in $ACTIVATED_SERVICES; do
    case "$service" in web|admin) frontend_activated="$frontend_activated $service" ;; esac
  done
  if [ "$BACKEND_READY" -eq 1 ] && [ -n "$frontend_activated" ]; then
    echo "ERROR: frontend release failed; preserving the validated backend and restoring frontend only" >&2
    [ -n "$LKG_MANIFEST" ] && [ -f "$LKG_MANIFEST" ] || return 1
    while IFS='|' read -r kind service image_name lkg_ref _image_id; do
      [ "$kind" = "image" ] || continue
      case " $frontend_activated " in
        *" $service "*)
          docker tag "$lkg_ref" "$image_name" || return 1
          restored_frontend="$restored_frontend $service"
          ;;
      esac
    done < "$LKG_MANIFEST"
    for service in $frontend_activated; do
      case " $restored_frontend " in *" $service "*) ;; *) return 1 ;; esac
    done
    restore_caddy_config || return 1
    cd "$DEPLOY_DIR" || return 1
    # Retain new source/env/additive data for the backend. Only the frontend
    # images revert; do not move OLD_DIR over this validated backend release.
    # shellcheck disable=SC2086
    compose up -d --no-build --no-deps --force-recreate $frontend_activated || return 1
    backend_sha="$LKG_BACKEND_SHA"
    [ -z "$BACKEND_SERVICES" ] || backend_sha="$RELEASE_SHA"
    record_release_state "$backend_sha" "$LKG_FRONTEND_SHA" || return 1
    wait_for_release_health || return 1
    echo "last-known-good frontend restored; backend retained (see per-component release markers)" >&2
    return 0
  fi
  echo "ERROR: release health failed; restoring last-known-good release $LKG_TAG" >&2
  if [ -n "$LKG_MANIFEST" ] && [ -f "$LKG_MANIFEST" ]; then
    while IFS='|' read -r kind _service image_name lkg_ref _image_id; do
      [ "$kind" = "image" ] || continue
      docker tag "$lkg_ref" "$image_name"
    done < "$LKG_MANIFEST"
  fi
  if [ -d "$OLD_DIR" ]; then
    failed_dir="/tmp/sokalens-failed-$(printf '%s' "$RELEASE_SHA" | tr -cs 'A-Za-z0-9_.-' '-' | cut -c1-40)"
    rm -rf "$failed_dir"
    [ -d "$DEPLOY_DIR" ] && mv "$DEPLOY_DIR" "$failed_dir"
    mv "$OLD_DIR" "$DEPLOY_DIR"
  fi
  restore_caddy_config
  cd "$DEPLOY_DIR"
  # Restoring old source must not restore the public API binding that this
  # release closed. The previous base Compose also honors this variable.
  upsert_env_value API_BIND_ADDRESS 127.0.0.1 "$DEPLOY_DIR/.env.prod"
  # SERVICE_LIST is an intentional whitespace-separated Compose service list.
  # shellcheck disable=SC2086
  if [ -n "$ACTIVATED_SERVICES" ]; then
    compose up -d --no-build --no-deps --force-recreate $ACTIVATED_SERVICES
  fi
  wait_for_release_health
  echo "last-known-good application restored" >&2
}

on_exit() {
  local status=$?
  trap - EXIT
  if [ "$status" -ne 0 ] && [ "$SWAPPED" -eq 1 ] && [ "$DEPLOY_SUCCEEDED" -eq 0 ]; then
    rollback_release || echo "ERROR: automatic application rollback also failed" >&2
  fi
  cleanup_secret_files
  exit "$status"
}
trap on_exit EXIT

upsert_env_value() {
  local name="$1"
  local value="$2"
  local target="$3"
  ENV_UPDATE_TEMPORARY="$(mktemp /tmp/sokalens-env.XXXXXX)"
  awk -v key="$name" -v replacement="$value" '
    BEGIN { found = 0 }
    index($0, key "=") == 1 {
      if (!found) print key "=" replacement
      found = 1
      next
    }
    { print }
    END { if (!found) print key "=" replacement }
  ' "$target" > "$ENV_UPDATE_TEMPORARY"
  chmod 600 "$ENV_UPDATE_TEMPORARY"
  mv "$ENV_UPDATE_TEMPORARY" "$target"
  ENV_UPDATE_TEMPORARY=""
}

ensure_env_value() {
  local name="$1"
  local default_value="$2"
  local target="$3"
  grep -q "^${name}=" "$target" || upsert_env_value "$name" "$default_value" "$target"
}

run_acca_readiness() {
  bash infra/deploy/acca-readiness.sh
}

stage_release() {
  local service backend="" frontend=""
  [ -n "$SERVICES" ] || return 0
  for service in $SERVICES; do
    case "$service" in
      web|admin) frontend="$frontend $service" ;;
      *) backend="$backend $service" ;;
    esac
  done
  BACKEND_SERVICES="$backend"
  FRONTEND_SERVICES="$frontend"
  # Complete builds before touching running services. --no-deps keeps unrelated
  # applications unchanged, including when a selected-service rollback runs.
  # shellcheck disable=SC2086
  compose build $SERVICES
  if [ -n "$backend" ]; then
    ACTIVATED_SERVICES="$ACTIVATED_SERVICES$backend"
    # Record first: Compose can partially activate services before failing.
    # shellcheck disable=SC2086
    compose up -d --no-build --no-deps --force-recreate $backend
    wait_for_release_health
  fi
  run_acca_readiness
  BACKEND_READY=1
  if [ -n "$frontend" ]; then
    ACTIVATED_SERVICES="$ACTIVATED_SERVICES$frontend"
    # shellcheck disable=SC2086
    compose up -d --no-build --no-deps --force-recreate $frontend
    # Nothing waited here, so the deploy continued the moment Compose had
    # *created* the containers -- which is well before Next can answer a
    # request. A visitor arriving in that window reached a web container that
    # was up but not yet able to reach the API, and pages that swallow an
    # upstream failure rendered their empty state: not an outage, just a
    # product message that was not true.
    #
    # The backend half already gated on this; the frontend half simply never
    # did. Waiting costs a few seconds of deploy time and closes the window in
    # which the site is serving but not working.
    wait_for_release_health
  fi
}

[ -f "$TARBALL" ] || { echo "ERROR: source tarball $TARBALL not found" >&2; exit 1; }
[ -f "$DEPLOY_DIR/.env.prod" ] || { echo "ERROR: production env is missing" >&2; exit 1; }
echo "== CI deploy $RELEASE_SHA started $(date -u) =="

rm -rf "$NEW_DIR"
mkdir -p "$NEW_DIR"
tar -xzf "$TARBALL" -C "$NEW_DIR"
cp "$DEPLOY_DIR/.env.prod" "$NEW_DIR/.env.prod"
chmod 600 "$NEW_DIR/.env.prod"

if [ -f "$ACCOUNT_LAUNCH_ENV_FILE" ]; then
  chmod 600 "$ACCOUNT_LAUNCH_ENV_FILE"
  for required_name in TURNSTILE_SECRET_KEY NEXT_PUBLIC_TURNSTILE_SITE_KEY; do
    required_value="$(grep "^${required_name}=" "$ACCOUNT_LAUNCH_ENV_FILE" | head -1 | cut -d= -f2-)"
    [[ "$required_value" =~ ^[A-Za-z0-9_-]{10,256}$ ]] || { echo "ERROR: invalid ${required_name}" >&2; exit 1; }
    upsert_env_value "$required_name" "$required_value" "$NEW_DIR/.env.prod"
  done
fi
upsert_env_value TURNSTILE_REQUIRED true "$NEW_DIR/.env.prod"
upsert_env_value PUBLIC_WEB_URL "$PRODUCTION_WEB_URL" "$NEW_DIR/.env.prod"
upsert_env_value API_BIND_ADDRESS 127.0.0.1 "$NEW_DIR/.env.prod"
upsert_env_value PREDICTION_SCHEDULE_INTERVAL 15m "$NEW_DIR/.env.prod"
# API-Football Pro subscription: 7,500 requests per provider day, 300 a minute.
# Normalize preserved Ultra settings before the new worker starts -- its budget
# reset otherwise inherits the old ceiling until a response header arrives.
#
# These lines are load-bearing in both directions, and the daily one used to be
# pointed the wrong way: because it upserts rather than defaults, it overwrote
# whatever the host held on every deploy, so a correction made on the box was
# reverted by the next release and the worker went back to pacing against
# 75,000 requests it does not have. Changing the package means changing these
# numbers here, in the repository, not on the host.
#
# infra/deploy/staged-deploy.test.mjs asserts the daily limit and reserve, so a
# value edited here without the test fails CI rather than production.
upsert_env_value PROVIDER_DAILY_LIMIT 7500 "$NEW_DIR/.env.prod"
upsert_env_value PROVIDER_DAILY_RESERVE 900 "$NEW_DIR/.env.prod"
upsert_env_value PROVIDER_MINUTE_LIMIT 300 "$NEW_DIR/.env.prod"
upsert_env_value QUOTA_BACKFILL_MAX_REQUESTS_PER_DAY 1200 "$NEW_DIR/.env.prod"
upsert_env_value QUOTA_FIXED_DAILY_OVERHEAD 900 "$NEW_DIR/.env.prod"
upsert_env_value QUOTA_LIVE_POLL_SECONDS 60 "$NEW_DIR/.env.prod"
ensure_env_value ENABLE_DESTRUCTIVE_RETENTION false "$NEW_DIR/.env.prod"
ensure_env_value RETENTION_DRY_RUN true "$NEW_DIR/.env.prod"
# This release promotes the already-QA'd inventory into the production schema.
# The old frontend keeps its captured environment until the readiness gate.
upsert_env_value ACCA_INVENTORY_V2_ENABLED true "$NEW_DIR/.env.prod"
upsert_env_value ACCA_DB_SCHEMA public "$NEW_DIR/.env.prod"
upsert_env_value ACCA_REDIS_NAMESPACE acca "$NEW_DIR/.env.prod"
upsert_env_value ACCA_INVENTORY_READ_SOURCE v2 "$NEW_DIR/.env.prod"

for required_name in TURNSTILE_SECRET_KEY NEXT_PUBLIC_TURNSTILE_SITE_KEY; do
  required_value="$(grep "^${required_name}=" "$NEW_DIR/.env.prod" | head -1 | cut -d= -f2-)"
  [ -n "$required_value" ] && [[ "$required_value" != REPLACE_WITH_* ]] || { echo "ERROR: ${required_name} is required" >&2; exit 1; }
done

test -r "$NEW_DIR/services/prediction-engine/prediction_engine/cli.py"
test -r "$NEW_DIR/infra/observability/grafana/provisioning/dashboards/dashboards.yml"
find "$NEW_DIR/services/prediction-engine" -type d -exec chmod a+rx {} +
find "$NEW_DIR/services/prediction-engine" -type f -exec chmod a+r {} +
find "$NEW_DIR/infra/observability" -type d -exec chmod a+rx {} +
find "$NEW_DIR/infra/observability" -type f -exec chmod a+r {} +

# Reclaim only what is genuinely dead before the recovery point: dangling
# images and stopped containers. The BUILD cache is deliberately left alone
# here -- pruning it before a build guarantees the build that follows starts
# cold, which is what made a routine deploy take ten minutes of pure
# recompilation. Storage is reclaimed after a successful deploy instead, where
# it costs nothing.
docker image prune -f >/dev/null 2>&1 || true
docker container prune -f >/dev/null 2>&1 || true
# A recovery point before a schema change, and only then.
#
# This used to take a dump roughly once a day regardless of what was deploying.
# That is the wrong trigger for this application: fixtures, odds and predictions
# are re-fetched from the provider, so a day-old copy of that data is not worth
# restoring -- it describes a world that has moved on, and the pipeline would
# rebuild it anyway. Paying a 1GB restore-verify cycle to protect data that
# regenerates itself buys very little.
#
# What genuinely cannot be re-fetched is the state on the other side of a
# migration. A migration that drops or rewrites a column is the one deploy step
# with no natural undo, so that -- not the calendar -- is when a snapshot earns
# its cost. Runs with no pending migration skip it and say so.
#
# Pending is computed by diffing the migration files in the incoming release
# against schema_migrations, because that is exactly what dbtool is about to
# apply a few lines below. If the database cannot be reached the check fails
# open and takes the snapshot: a needless dump is recoverable, a missing one is
# not.
BACKUP_DIR=/home/deploy/backups/sokalens
BACKUP_MAX_AGE_DAYS="${BACKUP_MAX_AGE_DAYS:-7}"
BACKUP_POSTGRES_CONTAINER="${POSTGRES_CONTAINER:-sports-prediction-platform-postgres-1}"
BACKUP_DB_USER="${POSTGRES_USER:-sports}"
BACKUP_DB_NAME="${POSTGRES_DB:-sports_prediction}"
pending_migration_count() {
  local applied name count=0
  applied="$(docker exec "$BACKUP_POSTGRES_CONTAINER" psql -X -U "$BACKUP_DB_USER" -d "$BACKUP_DB_NAME" \
    -At -c 'SELECT version FROM schema_migrations' 2>/dev/null)" || { echo 1; return; }
  for migration in "$NEW_DIR"/infra/db/migrations/*.up.sql; do
    [ -e "$migration" ] || continue
    name="$(basename "$migration" .up.sql)"
    printf '%s\n' "$applied" | grep -qxF "$name" || count=$((count + 1))
  done
  echo "$count"
}
BACKUP_PENDING_MIGRATIONS="$(pending_migration_count)"
if [ "$BACKUP_PENDING_MIGRATIONS" -gt 0 ]; then
  echo "backup: $BACKUP_PENDING_MIGRATIONS pending migration(s), taking a pre-migration recovery point"
  BACKUP_DIR="$BACKUP_DIR" bash "$NEW_DIR/infra/deploy/backup.sh"
else
  echo "backup: skipped, no pending migrations in this release"
fi
# Expire the snapshot on age as well as on replacement.
#
# backup.sh already keeps only the newest dump, so the next migration retires
# this one. That alone is not enough: if no migration ships for a month, a
# month-old snapshot sits on disk looking like a recovery point while
# describing a schema and a dataset that no longer exist. Restoring it would be
# worse than having nothing, because nothing does not invite you to try.
#
# Runs on every deploy, including ones that took no snapshot, so the expiry does
# not depend on migration cadence. Matches only this filename pattern, so it
# cannot reach PostgreSQL volumes or unrelated backups.
find "$BACKUP_DIR" -maxdepth 1 -type f \( -name 'sokalens-*.dump' -o -name 'sokalens-*.dump.sha256' \) \
  -mtime "+$BACKUP_MAX_AGE_DAYS" -print -delete 2>/dev/null || true
capture_lkg

rm -rf "$OLD_DIR"
mv "$DEPLOY_DIR" "$OLD_DIR"
mv "$NEW_DIR" "$DEPLOY_DIR"
SWAPPED=1
chmod 600 "$DEPLOY_DIR/.env.prod"

cd "$DEPLOY_DIR"
export DATABASE_URL
DATABASE_URL="$(grep '^DATABASE_URL=' .env.prod | head -1 | cut -d= -f2- | sed 's|@postgres:5432|@127.0.0.1:5433|')"
/home/deploy/dbtool-linux -mode up -migrations infra/db/migrations -timeout 120s

if [ -n "$SERVICES" ]; then
  echo "== rebuilding: $SERVICES =="
  # SERVICE_LIST is an intentional whitespace-separated Compose service list.
  # shellcheck disable=SC2086
  stage_release
else
  echo "== proxy-only release: application containers remain running =="
fi
wait_for_release_health
sync_caddy_config

backend_sha="$LKG_BACKEND_SHA"
frontend_sha="$LKG_FRONTEND_SHA"
[ -z "$BACKEND_SERVICES" ] || backend_sha="$RELEASE_SHA"
[ -z "$FRONTEND_SERVICES" ] || frontend_sha="$RELEASE_SHA"
record_release_state "$backend_sha" "$frontend_sha"
DEPLOY_SUCCEEDED=1

# Post-success GC never touches images in use, named volumes, or the verified
# dump. It runs only here, after a green deploy, so reclaiming space can never
# cost the next build its cache when it matters.
#
# --keep-storage retains the most recently used layers up to a ceiling instead
# of deleting everything past an age. That is the difference between a warm
# incremental build and a cold one, and the age filter was deleting cache that
# was hours from being reused.
#
# Two things were wrong with the previous settings, and only one of them was
# the number. The ceiling was 15GB, which the cache had not yet reached, and a
# seven-day age filter made only week-old layers eligible at all. Between them
# nothing was ever reclaimed: measured on the box, the cache stood at 13.2GB on
# a 99GB disk that was 64% full, growing with every deploy.
#
# The age filter is gone rather than shortened. Deleting cache by age is what
# made deploys start cold in the first place, and a layer's age says nothing
# about whether the next build wants it. The ceiling alone is the right control:
# it keeps the most recently used layers and discards the rest, so every deploy
# trims back to a bounded size while the layers a rebuild actually reuses -- the
# base images and dependency layers -- survive.
#
# 3GB holds those comfortably. Raise BUILD_CACHE_KEEP if builds start going cold.
docker builder prune -f --keep-storage "${BUILD_CACHE_KEEP:-3GB}"

# Last-known-good images accumulate one tag per release with no ceiling, and
# they are the other half of the disk problem. Keep the newest few per
# repository -- enough to roll back through a bad run -- and drop the rest.
# Never touches an image an existing container references.
prune_old_lkg_images() {
  local keep repo stale_tags
  keep="${LKG_IMAGES_KEEP:-3}"
  # No LKG repositories is a valid first-deploy state. Keep grep's no-match
  # status from tripping `set -euo pipefail` before the loop can be skipped.
  for repo in $(docker image ls --format '{{.Repository}}' | grep '^sokalens-lkg-' | sort -u || true); do
    # --filter reference= does the matching, so no awk quoting is involved.
    # Newest first, then remove tags past the keep count. Use repository:tag,
    # not image IDs: an LKG image can share an ID with another tag, and Docker
    # correctly refuses to remove that ID while multiple references exist.
    stale_tags="$(docker image ls --filter "reference=${repo}:*" --format '{{.Repository}}:{{.Tag}}' | tail -n "+$((keep + 1))")"
    [ -n "$stale_tags" ] && echo "$stale_tags" | xargs -r docker image rm >/dev/null 2>&1
  done
  return 0
}
prune_old_lkg_images

# Rotated access logs. Caddy already caps these three ways -- 50 MiB a file, 3
# rolls, 7 days -- so this is a backstop for the case that actually bites: a
# rotation setting edited wrongly, or logs left behind by a previous config.
# Never touches the live file, only rolled ones Caddy has finished with.
prune_rotated_logs() {
  local keep_days="${ACCESS_LOG_KEEP_DAYS:-7}"
  [ -d /var/log/caddy ] || return 0
  sudo find /var/log/caddy -type f -name '*.log.*' -mtime "+${keep_days}" -delete 2>/dev/null || true
  echo "access logs on disk: $(sudo du -sh /var/log/caddy 2>/dev/null | cut -f1)"
  return 0
}
prune_rotated_logs
echo "== CI deploy finished $(date -u) =="
docker ps --format '{{.Names}} {{.Status}}' | grep '^sports-prediction-platform-' || true
