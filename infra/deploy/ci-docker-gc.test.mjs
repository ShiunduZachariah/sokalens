import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync, chmodSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const script = fileURLToPath(new URL('./ci-docker-gc.sh', import.meta.url));

function runCase({ root, running = false }) {
  const directory = mkdtempSync(join(tmpdir(), 'ci-docker-gc-'));
  try {
    const log = join(directory, 'docker.log');
    const docker = join(directory, 'docker');
    writeFileSync(docker, `#!/usr/bin/env bash
set -euo pipefail
printf '%s\\n' "$*" >> "$DOCKER_TEST_LOG"
case "$1 $2" in
  'info --format') printf '%s\\n' "$DOCKER_TEST_ROOT" ;;
  'ps --quiet') if [[ "$DOCKER_TEST_RUNNING" == true ]]; then echo abc123; fi ;;
  'volume ls') echo volume1 ;;
  'volume prune') echo 'Total reclaimed space: 1GB' ;;
  *) exit 2 ;;
esac
`);
    chmodSync(docker, 0o755);
    const result = spawnSync('bash', [script], {
      encoding: 'utf8',
      env: { ...process.env, PATH: `${directory}:${process.env.PATH}`, DOCKER_TEST_LOG: log, DOCKER_TEST_ROOT: root, DOCKER_TEST_RUNNING: String(running) },
    });
    return { ...result, calls: readFileSync(log, 'utf8') };
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}

test('CI cleanup refuses a different Docker daemon', { skip: process.platform === 'win32' }, () => {
  const result = runCase({ root: '/var/lib/docker' });
  assert.equal(result.status, 1);
  assert.doesNotMatch(result.calls, /volume prune/);
});

test('CI cleanup leaves volumes while another job has running containers', { skip: process.platform === 'win32' }, () => {
  const result = runCase({ root: '/home/ci/.local/share/docker', running: true });
  assert.equal(result.status, 0);
  assert.doesNotMatch(result.calls, /volume prune/);
});

test('CI cleanup prunes unused volumes on the idle CI daemon', { skip: process.platform === 'win32' }, () => {
  const result = runCase({ root: '/home/ci/.local/share/docker' });
  assert.equal(result.status, 0);
  assert.match(result.calls, /volume prune --force/);
});
