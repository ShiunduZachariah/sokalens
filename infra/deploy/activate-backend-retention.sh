#!/usr/bin/env bash
# Invoked by backend-maintenance.yml after both release deployments passed.
set -euo pipefail
umask 077
target="${1:?target required}"
expected_sha="${2:?expected SHA required}"
[[ "$expected_sha" =~ ^[0-9a-f]{40}$ ]] || exit 2
case "$target" in
 qa)
  if [[ "$(id -un)" != dev ]]; then exec sudo -u dev bash "$0" "$@"; fi
  deploy_dir="$HOME/sokalens"; env_file=.env.qa; project=sokalens-dev
  api=http://127.0.0.1:18080; web=http://127.0.0.1:13000; buffers=512
  ;;
 production)
  deploy_dir="$HOME/sokalens"; env_file=.env.prod; project=sports-prediction-platform
  api=http://127.0.0.1:8080; web=http://127.0.0.1:3000; buffers=1536
  ;;
 *) exit 2 ;;
esac
mkdir -p "$HOME/.cache"
exec 9>"${BACKEND_RELEASE_LOCK:-$HOME/.cache/sokalens-$target-release.lock}"
flock -w 600 9
deploy_dir="${BACKEND_DEPLOY_DIR:-$deploy_dir}"
cd "$deploy_dir"
if [[ "$target" == qa ]]; then actual_sha="$(git rev-parse HEAD)";
else actual_sha="$(cat .release-backend-sha)"; fi
[[ "$actual_sha" == "$expected_sha" ]] || { echo "deployed SHA changed; refusing maintenance" >&2; exit 1; }
bash infra/deploy/backend-check.sh "$project" "$api" "$buffers"
QA_WEB_ORIGIN="$web" QA_API_ORIGIN="$api" bash scripts/qa/smoke.sh
# A fresh, restore-proven recovery point precedes every new expiry activation.
# Backup configuration remains in the selected environment and never crosses
# into QA from production. The dump contains no shell-expanded credentials.
(
 set -a
 . "$env_file"
 set +a
 export POSTGRES_CONTAINER="$project-postgres-1"
 export POSTGRES_DB="$(docker exec "$POSTGRES_CONTAINER" sh -ceu 'printf %s "$POSTGRES_DB"')"
 export POSTGRES_USER="$(docker exec "$POSTGRES_CONTAINER" sh -ceu 'printf %s "$POSTGRES_USER"')"
 if [[ "$target" == production ]]; then export BACKUP_DIR="$HOME/backups/sokalens";
 else export BACKUP_DIR="$HOME/backups/sokalens-qa"; fi
 export BACKUP_DIR="${BACKEND_BACKUP_DIR:-$BACKUP_DIR}"
 mkdir -p "$BACKUP_DIR"
 chmod 700 "$BACKUP_DIR"
 proof="$(find "$BACKUP_DIR" -maxdepth 1 -name 'sokalens-*.dump.verified' -mmin -120 -printf '%T@ %p\n' | sort -rn | head -1 | cut -d' ' -f2-)"
 if [[ -n "$proof" && ! -f "$BACKUP_DIR/restore-check-pending" ]] && sha256sum --check "$proof"; then
  echo "reusing a fresh restore-verified recovery point"
 else
  bash infra/deploy/backup.sh
 fi
 test -z "$(find "$BACKUP_DIR" -maxdepth 1 -name restore-check-pending -print)"
 test -n "$(find "$BACKUP_DIR" -maxdepth 1 -name 'sokalens-*.dump.verified' -mmin -360 -print)"
)
bash infra/deploy/backend-maintenance.sh "$project"
# Preserve the prior runtime configuration until post-activation checks pass.
previous_env="$(mktemp)"
cp "$env_file" "$previous_env"
succeeded=0
activated=0
compose() {
 docker compose -p "$project" -f infra/docker/docker-compose.yml -f "infra/deploy/docker-compose.$([[ "$target" == qa ]] && echo qa || echo prod).yml" \
  --env-file "$env_file" --profile core --profile frontend --profile search "$@"
}
finish() {
 status=$?
 trap - EXIT
 if (( status != 0 && succeeded == 0 && activated == 1 )); then
  compose stop worker
  bash infra/deploy/restore-expired-predictions.sh "$project" "$expected_sha"
  cp "$previous_env" "$env_file"
  compose up -d --no-build --no-deps --force-recreate worker
  echo "retention activation failed; prior worker configuration restored; verified recovery point retained" >&2
 fi
 rm -f "$previous_env"
 exit "$status"
}
trap finish EXIT
python3 - "$env_file" "$expected_sha" <<'CONFIG'
import pathlib,re,sys
p=pathlib.Path(sys.argv[1]); s=p.read_text()
for k,v in {'EXPIRE_UNRESOLVED_PREDICTIONS':'true','ENABLE_RAW_PREDICTION_DRAIN':'true','RETENTION_DRY_RUN':'false','PREDICTION_RESULT_RAW_RETENTION_DAYS':'1','RETENTION_RELEASE_SHA':sys.argv[2],'RAW_PREDICTION_DRAIN_BATCH_SIZE':'1000','RAW_PREDICTION_DRAIN_MAX_BATCHES':'100'}.items():
 s=re.sub(r'^'+k+r'=.*$',k+'='+v,s,flags=re.M) if re.search(r'^'+k+r'=',s,re.M) else s+'\n'+k+'='+v+'\n'
p.write_text(s);p.chmod(0o600)
CONFIG
activated=1
compose up -d --no-build --no-deps --force-recreate worker
for _ in $(seq 1 60); do
 state="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$project-worker-1")"
 [[ "$state" != healthy ]] || break
 sleep 2
done
[[ "$state" == healthy ]]
bash infra/deploy/backend-check.sh "$project" "$api" "$buffers"
QA_WEB_ORIGIN="$web" QA_API_ORIGIN="$api" bash scripts/qa/smoke.sh
bash infra/deploy/check-retention-canary.sh "$project" "$expected_sha"
bash infra/deploy/backend-check.sh "$project" "$api" "$buffers"
QA_WEB_ORIGIN="$web" QA_API_ORIGIN="$api" bash scripts/qa/smoke.sh
succeeded=1
printf 'bounded expiry enabled: target=%s sha=%s; hourly worker drain owns deletion\n' "$target" "$expected_sha"
