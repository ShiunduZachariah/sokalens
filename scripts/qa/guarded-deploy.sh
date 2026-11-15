#!/usr/bin/env bash
# CI entrypoint: capture the actual old runtime before deploy.sh resets source.
set -euo pipefail
umask 077
release_lock="${QA_RELEASE_LOCK:-$HOME/.cache/sokalens-qa-release.lock}"
mkdir -p "$(dirname "$release_lock")"
exec 9>"$release_lock"
flock -w 600 9
DEPLOY_DIR="${QA_DEPLOY_DIR:-$HOME/sokalens}"
cd "$DEPLOY_DIR"
git diff --quiet && git diff --cached --quiet || { echo "QA checkout has tracked changes; refusing to discard them" >&2; exit 1; }
previous_sha="$(git rev-parse HEAD)"
project=sokalens-dev
scratch="$(mktemp -d "${TMPDIR:-/tmp}/sokalens-qa-rollback.XXXXXX")"
manifest="$scratch/images"
: > "$manifest"
docker inspect --format '{"services":{"postgres":{"command":{{json .Config.Cmd}},"shm_size":{{.HostConfig.ShmSize}}}}}' "$project-postgres-1" > "$scratch/postgres-runtime.json"
cp .env.qa "$scratch/env.qa"
for service in postgres redis api worker web admin ops-controller typesense mock-api-football; do
 container="$project-$service-1"
 docker inspect --format "$service|{{.Config.Image}}|{{.Image}}" "$container" >> "$manifest"
done
# Keep old images reachable even if Docker's normal image tags move.
while IFS='|' read -r service _ image; do
 docker tag "$image" "sokalens-qa-lkg-$service:${previous_sha:0:12}"
done < "$manifest"
succeeded=0
activated=0
finish() {
 status=$?
 trap - EXIT
 if (( status != 0 && succeeded == 0 && activated == 1 )); then
  echo "QA deployment failed; restoring previous source, environment and images" >&2
  git reset --hard "$previous_sha"
  cp "$scratch/env.qa" .env.qa
  while IFS='|' read -r _ name image; do docker tag "$image" "$name"; done < "$manifest"
  docker compose -p "$project" -f infra/docker/docker-compose.yml -f infra/deploy/docker-compose.qa.yml \
    -f "$scratch/postgres-runtime.json" --env-file .env.qa --profile core --profile frontend --profile search \
    up -d --no-build --force-recreate || status=1
  # Allow the restored web container to finish starting before smoke checks.
  for _ in $(seq 1 60); do
   if curl --fail --silent --max-time 5 http://127.0.0.1:13000/ >/dev/null; then break; fi
   sleep 2
  done
  bash scripts/qa/smoke.sh || status=1
 fi
 if (( succeeded == 1 || activated == 0 )); then
  while IFS='|' read -r service _ _; do docker image rm "sokalens-qa-lkg-$service:${previous_sha:0:12}" >/dev/null; done < "$manifest"
  rm -rf "$scratch"
 else
  echo "QA rollback evidence retained at $scratch" >&2
 fi
 exit "$status"
}
trap finish EXIT
# A pre-existing outage must not be misattributed to this release.
bash scripts/qa/smoke.sh
activated=1
bash scripts/qa/deploy.sh
bash infra/deploy/backend-check.sh "$project" http://127.0.0.1:18080 512
succeeded=1
