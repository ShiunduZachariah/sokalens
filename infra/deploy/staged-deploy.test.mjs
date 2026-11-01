import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { test } from 'node:test';

const source = readFileSync(new URL('./ci-deploy.sh', import.meta.url), 'utf8').replace(/\r\n/g, '\n');
const definitions = source.split('[ -f "$TARBALL" ]')[0];
function stage(gate = 0, services = 'api worker web admin', backend = 0, frontend = 0) {
  return spawnSync('bash', ['-c', `${definitions}
trap - EXIT
SERVICES='${services}'
ACTIVATED_SERVICES=''
compose() { echo "compose:$*"; if [[ "$*" == *"up"*"api"* ]]; then return ${backend}; fi; if [[ "$*" == *"up"*"web"* ]]; then return ${frontend}; fi; }
wait_for_release_health() { echo health; }
wait_for_container() { echo "healthy:$1"; }
run_acca_readiness() { echo inventory-gate; return ${gate}; }
trap 'echo "activated:$ACTIVATED_SERVICES"; echo "backend-ready:\${BACKEND_READY:-0}"' EXIT
stage_release
`], { encoding: 'utf8' });
}

test('backend activation and inventory readiness precede frontend activation', () => {
  const result = stage();
  assert.equal(result.status, 0, result.stderr);
  const output = result.stdout;
  assert.match(output, /compose:build api worker web admin/);
  assert.match(output, /compose:up -d --no-build --no-deps --force-recreate api worker/);
  assert.ok(output.indexOf('inventory-gate') < output.indexOf('--force-recreate web admin'));
  assert.match(output, /activated: api worker web admin/);
});

test('failed inventory gate leaves frontend untouched and rollback scope backend-only', () => {
  const result = stage(1);
  assert.notEqual(result.status, 0);
  assert.doesNotMatch(result.stdout, /--force-recreate web/);
  assert.match(result.stdout, /activated: api worker\n/);
});

test('partially failed backend activation is recorded before compose runs', () => {
  const result = stage(0, 'api worker web', 1);
  assert.notEqual(result.status, 0);
  assert.match(result.stdout, /activated: api worker\n/);
  assert.doesNotMatch(result.stdout, /inventory-gate|--force-recreate web/);
});

test('frontend-only release still requires ready inventory', () => {
  const result = stage(1, 'web');
  assert.notEqual(result.status, 0);
  assert.doesNotMatch(result.stdout, /--force-recreate web/);
  assert.match(result.stdout, /activated:\n/);
});

test('rollback recreates only activated services and never dependencies', () => {
  assert.match(source, /compose up -d --no-build --no-deps --force-recreate \$ACTIVATED_SERVICES/);
});

test('full rollback preserves private API binding in the restored environment', () => {
  const result = spawnSync('bash', ['-c', `${definitions}
trap - EXIT
scratch=$(mktemp -d)
DEPLOY_DIR="$scratch/current"
OLD_DIR="$scratch/old"
mkdir -p "$DEPLOY_DIR" "$OLD_DIR"
printf '%s\\n' 'API_BIND_ADDRESS=127.0.0.1' > "$DEPLOY_DIR/.env.prod"
printf '%s\\n' 'API_BIND_ADDRESS=0.0.0.0' 'OTHER_SETTING=preserved' > "$OLD_DIR/.env.prod"
LKG_MANIFEST=''
BACKEND_READY=0
ACTIVATED_SERVICES=' api'
RELEASE_SHA="private-api-test-$$"
trap 'rm -rf "$scratch" "/tmp/sokalens-failed-$RELEASE_SHA"; cleanup_secret_files' EXIT
restore_caddy_config() { :; }
wait_for_release_health() { :; }
compose() { echo "compose:$*"; grep '^API_BIND_ADDRESS=' .env.prod; grep '^OTHER_SETTING=' .env.prod; }
rollback_release
`], { encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /compose:up -d --no-build --no-deps --force-recreate api/);
  assert.match(result.stdout, /API_BIND_ADDRESS=127\.0\.0\.1/);
  assert.doesNotMatch(result.stdout, /API_BIND_ADDRESS=0\.0\.0\.0/);
  assert.match(result.stdout, /OTHER_SETTING=preserved/);
});

test('production environment contract replaces stale provider plan ceilings', () => {
  assert.match(source, /upsert_env_value PROVIDER_DAILY_LIMIT 7500 "\$NEW_DIR\/\.env\.prod"/);
  assert.match(source, /upsert_env_value PROVIDER_DAILY_RESERVE 900 "\$NEW_DIR\/\.env\.prod"/);
  assert.match(source, /upsert_env_value PROVIDER_MINUTE_LIMIT 300 "\$NEW_DIR\/\.env\.prod"/);

  const result = spawnSync('bash', ['-c', `${definitions}
trap - EXIT
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
env_file="$scratch/.env.prod"
printf '%s\\n' 'PROVIDER_DAILY_LIMIT=75000' 'PROVIDER_DAILY_RESERVE=1500' 'PROVIDER_MINUTE_LIMIT=450' 'OTHER_SETTING=preserved' > "$env_file"
upsert_env_value PROVIDER_DAILY_LIMIT 7500 "$env_file"
upsert_env_value PROVIDER_DAILY_RESERVE 900 "$env_file"
upsert_env_value PROVIDER_MINUTE_LIMIT 300 "$env_file"
cat "$env_file"
`], { encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /^PROVIDER_DAILY_LIMIT=7500$/m);
  assert.match(result.stdout, /^PROVIDER_DAILY_RESERVE=900$/m);
  assert.match(result.stdout, /^PROVIDER_MINUTE_LIMIT=300$/m);
  assert.match(result.stdout, /^OTHER_SETTING=preserved$/m);
  assert.doesNotMatch(result.stdout, /PROVIDER_DAILY_LIMIT=75000$/m);
});

test('backend checkpoint survives failed frontend activation', () => {
  const result = stage(0, 'api worker web admin', 0, 1);
  assert.notEqual(result.status, 0);
  assert.match(result.stdout, /backend-ready:1/);
});

test('frontend failure rolls back only frontend and records split release identities', () => {
  const result = spawnSync('bash', ['-c', `${definitions}
trap - EXIT
DEPLOY_DIR=$(mktemp -d)
OLD_DIR="$DEPLOY_DIR/not-present"
trap 'rm -rf "$DEPLOY_DIR"' EXIT
LKG_MANIFEST="$DEPLOY_DIR/manifest"
printf '%s\\n' 'image|api|api-current|api-lkg|api-id' 'image|worker|worker-current|worker-lkg|worker-id' 'image|web|web-current|web-lkg|web-id' 'image|admin|admin-current|admin-lkg|admin-id' > "$LKG_MANIFEST"
BACKEND_READY=1
BACKEND_SERVICES=' api worker'
FRONTEND_SERVICES=' web admin'
ACTIVATED_SERVICES=' api worker web admin'
RELEASE_SHA=new-release
LKG_BACKEND_SHA=old-backend
LKG_FRONTEND_SHA=old-frontend
docker() { echo "docker:$*"; }
compose() { echo "compose:$*"; }
restore_caddy_config() { :; }
wait_for_release_health() { :; }
rollback_release
printf 'backend:%s\\n' "$(cat "$DEPLOY_DIR/.release-backend-sha")"
printf 'frontend:%s\\n' "$(cat "$DEPLOY_DIR/.release-frontend-sha")"
printf 'global:%s\\n' "$(cat "$DEPLOY_DIR/.release-sha")"
`], { encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  assert.doesNotMatch(result.stdout, /docker:tag (api|worker)-lkg/);
  assert.match(result.stdout, /docker:tag web-lkg web-current/);
  assert.match(result.stdout, /compose:up -d --no-build --no-deps --force-recreate web admin/);
  assert.match(result.stdout, /backend:new-release/);
  assert.match(result.stdout, /frontend:old-frontend/);
  assert.match(result.stdout, /global:split-/);
});
