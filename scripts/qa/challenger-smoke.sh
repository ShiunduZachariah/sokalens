#!/usr/bin/env bash
# Exercise the shadow challengers (v2 Models B and C) on QA's own data.
#
# WHY THIS EXISTS. QA's provider is a static mock, so no QA fixture ever
# kicks off and finishes: the live shadow path would forecast upcoming
# fixtures and then wait forever for a result to settle against. A green QA
# deploy would prove only that the commands start.
#
# So this replays QA's recently finished fixtures as they would have looked
# an hour before kick-off (`--origin replay`), settles them and prints the
# comparison. That runs every statement the live path runs -- registration,
# the league-wide history read, both models, the champion read, settlement and
# the report -- against QA's real schema and data. Replay rows are stored
# under their own origin and excluded from evaluation, so they can never be
# mistaken for shadow evidence. A live pass runs too, to show which upcoming
# fixtures the worker will forecast.
#
# Every command must exit 0 and print its JSON summary; anything else fails
# the deploy, which is the point of running it in QA.
set -euo pipefail

DEPLOY_DIR="${QA_DEPLOY_DIR:-/home/dev/sokalens}"
ENV_FILE="${QA_ENV_FILE:-$DEPLOY_DIR/.env.qa}"
PROJECT="${COMPOSE_PROJECT_NAME:-sokalens-dev}"
REPLAY_LIMIT="${QA_CHALLENGER_REPLAY_LIMIT:-300}"

case "$DEPLOY_DIR" in
  /home/deploy/sokalens|/home/deploy/sokalens/*)
    echo "ERROR: $DEPLOY_DIR is the production checkout. This runs on QA only." >&2
    exit 1
    ;;
esac

engine() {
  COMPOSE_PROJECT_NAME="$PROJECT" docker compose \
    -f "$DEPLOY_DIR/infra/docker/docker-compose.yml" \
    -f "$DEPLOY_DIR/infra/deploy/docker-compose.qa.yml" \
    --env-file "$ENV_FILE" exec -T worker \
    python -m prediction_engine.cli "$@"
}

echo "[challengers] replay $REPLAY_LIMIT finished fixtures"
engine run-challengers --origin replay --limit "$REPLAY_LIMIT"
echo "[challengers] settle"
engine settle-challengers
echo "[challengers] replay comparison (not recorded as evidence)"
engine challenger-report --origin replay --no-write
echo "[challengers] live pass"
engine run-challengers --origin live --min-interval-minutes 0
