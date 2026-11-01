#!/usr/bin/env bash
# Deploy the qa branch to the QA environment.
#
# Run on the QA host, as the user that owns the rootless Docker daemon:
#
#   sudo -u dev env HOME=/home/dev bash /home/dev/sokalens/scripts/qa/deploy.sh
#
# WHY THIS EXISTS. `docker compose up` does not run migrations. Production is
# covered because ci-deploy.sh runs dbtool as a separate step; QA had no
# equivalent, so on 2026-09-16 the QA stack came up on code that expected
# migration 000065 against a database that had never had it applied. Every
# request to /v1/matches/today answered 503 with
# `column m.live_state_at does not exist`, and /in-play -- the page that feed
# powers -- returned 500. Nothing in the container status said anything was
# wrong: all nine containers were healthy.
#
# The migration step below is the reason this script exists. Everything else
# is convenience.
set -euo pipefail

DEPLOY_DIR="${QA_DEPLOY_DIR:-/home/dev/sokalens}"
# `.env.qa`, matching docs/operations/environments.md and .env.qa.example. The
# host file was `.env.dev` until 2026-09-18 -- a leftover from the pre-rename
# name, and the reason three files disagreed about what QA's env file is called.
ENV_FILE="${QA_ENV_FILE:-$DEPLOY_DIR/.env.qa}"
# The live project name. Renaming it recreates every container against a fresh
# volume, which discards the QA database -- so it is deliberately not changed
# here. The public hostnames were renamed to qa.sokalens.com separately; see
# docs/operations/environments.md.
PROJECT="${COMPOSE_PROJECT_NAME:-sokalens-dev}"
# QA Postgres publishes here. dbtool runs on the host, where the compose
# service name `postgres` does not resolve, so the URL is rewritten the same
# way ci-deploy.sh rewrites production's.
QA_PG_HOST_PORT="${QA_PG_HOST_PORT:-15433}"
GO_BIN="${GO_BIN:-/usr/local/go/bin/go}"

COMPOSE_FILES=(
  -f "$DEPLOY_DIR/infra/docker/docker-compose.yml"
  -f "$DEPLOY_DIR/infra/deploy/docker-compose.qa.yml"
)
PROFILES=(--profile core --profile frontend --profile search)

log() { printf '\n[qa-deploy] %s\n' "$*"; }

if [ ! -r "$ENV_FILE" ]; then
  echo "ERROR: $ENV_FILE is not readable" >&2
  [ -r "$DEPLOY_DIR/.env.dev" ] && echo "       (found .env.dev -- it was renamed to .env.qa on 2026-09-18)" >&2
  exit 1
fi

# Run from the deploy directory. Compose resolves build contexts through the
# calling process's working directory, so invoking this from a path the QA user
# cannot stat -- an operator's own checkout under another home -- fails the
# build with `permission denied` on a directory the stack never needed.
cd "$DEPLOY_DIR"

log "syncing $DEPLOY_DIR to origin/qa"
git -C "$DEPLOY_DIR" fetch --quiet origin
git -C "$DEPLOY_DIR" reset --quiet --hard origin/qa
git -C "$DEPLOY_DIR" log --oneline -1

log "building and starting containers"
COMPOSE_PROJECT_NAME="$PROJECT" docker compose "${COMPOSE_FILES[@]}" \
  --env-file "$ENV_FILE" "${PROFILES[@]}" up -d --build

# Postgres must be accepting connections before dbtool runs, and `up -d`
# returns as soon as the container is started, not when it is ready.
log "waiting for postgres"
for _ in $(seq 1 60); do
  if COMPOSE_PROJECT_NAME="$PROJECT" docker compose "${COMPOSE_FILES[@]}" \
      --env-file "$ENV_FILE" exec -T postgres pg_isready -q 2>/dev/null; then
    break
  fi
  sleep 2
done

log "applying migrations"
(
  set -a
  # shellcheck disable=SC1090
  . "$ENV_FILE"
  set +a
  export DATABASE_URL="${DATABASE_URL/@postgres:5432/@127.0.0.1:$QA_PG_HOST_PORT}"
  cd "$DEPLOY_DIR"
  "$GO_BIN" run ./services/api/cmd/dbtool -env-file "" -mode up \
    -migrations infra/db/migrations -timeout 300s
)

log "restarting application containers onto the migrated schema"
# The API caches nothing across a schema change, but it does hold a connection
# pool whose prepared statements were planned against the old schema.
COMPOSE_PROJECT_NAME="$PROJECT" docker compose "${COMPOSE_FILES[@]}" \
  --env-file "$ENV_FILE" "${PROFILES[@]}" restart api worker web admin

# `restart` returns when the container is started, not when Next.js is
# listening, and the web service has no healthcheck to wait on. Smoking
# immediately reports four page failures on a deploy that is fine -- a false
# alarm that is indistinguishable from the real 2026-09-16 outage.
log "waiting for web"
WEB_ORIGIN="${QA_WEB_ORIGIN:-http://127.0.0.1:13000}"
for _ in $(seq 1 60); do
  if [ "$(curl -so /dev/null -w '%{http_code}' --max-time 5 "$WEB_ORIGIN/")" = "200" ]; then
    break
  fi
  sleep 2
done

log "smoke test"
bash "$DEPLOY_DIR/scripts/qa/smoke.sh"
