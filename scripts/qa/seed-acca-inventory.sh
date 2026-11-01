#!/usr/bin/env bash
# Give QA enough upcoming fixtures for the ACCA engine to compose from.
#
# WHY THIS EXISTS. ACCA_INVENTORY_V2_ENABLED was false in QA while production
# ran it true, so every ACCA change reached production untested -- which is how
# two ACCA faults shipped on 2026-09-22: an endpoint that answered 503 half the
# time, and a generator that timed out on the narrow filter corners.
#
# Turning the flag on is not enough on its own. Composition draws legs from
# fixtures that have not kicked off, and QA had exactly one of those against
# 82,466 predictions, because its provider is a static mock and nothing advances
# the calendar. The inventory job would run and compose nothing, which looks the
# same as a broken engine.
#
# WHAT IT DOES. Moves a bounded sample of already-played fixtures into the near
# future. Their predictions are already there and already eligible, so the pool
# becomes composable without inventing predictions that no model produced --
# which matters, because a seeded prediction would test the composer against
# data the engine would never generate.
#
# The kickoffs are spread across the next week so period filters
# (seven_days, weekend) have something to select, and leagues are varied so the
# country and league facets are not single-valued.
#
# THIS IS DESTRUCTIVE TO QA HISTORY. The sampled fixtures stop being historical
# results. QA is a test environment and its history is not evidence of anything,
# but the original kickoffs are recorded in qa_acca_seed_backup so a run can be
# undone.
set -euo pipefail

DEPLOY_DIR="${QA_DEPLOY_DIR:-/home/dev/sokalens}"
PROJECT="${COMPOSE_PROJECT_NAME:-sokalens-dev}"
SAMPLE="${QA_ACCA_SEED_SAMPLE:-400}"

# Refuse to run anywhere that looks like production. This moves fixtures into
# the future; doing that to production would publish matches that have already
# been played as though they were still to come.
case "$DEPLOY_DIR" in
  /home/deploy/sokalens|/home/deploy/sokalens/*)
    echo "ERROR: $DEPLOY_DIR is the production checkout. This seeds QA only." >&2
    exit 1
    ;;
esac
if [ ! -f "$DEPLOY_DIR/.env.qa" ]; then
  echo "ERROR: $DEPLOY_DIR/.env.qa is missing; this does not look like QA." >&2
  exit 1
fi

psql() {
  docker exec -i "${PROJECT}-postgres-1" sh -c \
    "psql -U \$POSTGRES_USER -d \$POSTGRES_DB -v ON_ERROR_STOP=1 $*"
}

if [ "${1:-}" = "--undo" ]; then
  echo "==> Restoring the fixtures this script moved"
  psql <<'SQL'
UPDATE matches m
SET kickoff_at = b.kickoff_at, status = b.status
FROM qa_acca_seed_backup b
WHERE m.id = b.match_id;

UPDATE fixtures f
SET kickoff_at = b.kickoff_at
FROM qa_acca_seed_backup b
WHERE f.id = b.match_id;

DELETE FROM qa_acca_seed_backup;
SELECT count(*) AS upcoming_now FROM matches WHERE kickoff_at > now();
SQL
  echo "==> Restored."
  exit 0
fi

echo "==> Seeding up to $SAMPLE upcoming fixtures for ACCA composition"

psql <<SQL
-- Records what was changed so a run can be undone.
CREATE TABLE IF NOT EXISTS qa_acca_seed_backup (
  match_id     BIGINT PRIMARY KEY,
  kickoff_at   TIMESTAMPTZ NOT NULL,
  status       TEXT        NOT NULL,
  seeded_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

WITH candidates AS (
  -- Played fixtures that already carry current predictions, which is what
  -- makes them composable once they are in the future.
  SELECT m.id
  FROM matches m
  WHERE m.kickoff_at < now()
    AND NOT EXISTS (SELECT 1 FROM qa_acca_seed_backup b WHERE b.match_id = m.id)
    AND EXISTS (
      SELECT 1 FROM engine_predictions p
      WHERE p.fixture_id = m.id AND p.superseded_at IS NULL
    )
  ORDER BY m.kickoff_at DESC
  LIMIT ${SAMPLE}
)
INSERT INTO qa_acca_seed_backup (match_id, kickoff_at, status)
SELECT m.id, m.kickoff_at, m.status FROM matches m JOIN candidates c ON c.id = m.id;

-- Spread across the next seven days so period filters have range, and off the
-- hour so several fixtures do not collapse onto one kickoff.
--
-- Both tables. `matches` and `fixtures` are separate rows sharing an id, each
-- with its own kickoff_at, and eligible_acca_predictions_v joins `fixtures`.
-- Moving only `matches` is what the first version of this did: the board
-- showed 401 upcoming fixtures and the ACCA engine still saw one, so it
-- composed nothing and looked broken for a reason that had nothing to do with
-- the engine.
UPDATE matches m
SET kickoff_at = now()
      + ((b.match_id % 7) * interval '1 day')
      + ((b.match_id % 11) * interval '1 hour')
      + interval '2 hours',
    status = 'upcoming'
FROM qa_acca_seed_backup b
WHERE m.id = b.match_id;

UPDATE fixtures f
SET kickoff_at = now()
      + ((b.match_id % 7) * interval '1 day')
      + ((b.match_id % 11) * interval '1 hour')
      + interval '2 hours'
FROM qa_acca_seed_backup b
WHERE f.id = b.match_id;

SELECT count(*) AS upcoming_matches FROM matches WHERE kickoff_at > now();
SELECT count(*) AS upcoming_fixtures FROM fixtures WHERE kickoff_at > now();
-- The number that decides whether the ACCA engine has anything to work with.
SELECT count(*) AS acca_eligible_legs FROM eligible_acca_predictions_v;
SQL

echo "==> Done. Run the ACCA inventory job, then check /v1/accas."
echo "    Undo with: scripts/qa/seed-acca-inventory.sh --undo"
