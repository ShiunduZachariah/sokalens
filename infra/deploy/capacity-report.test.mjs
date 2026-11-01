import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

/**
 * The capacity report's whole design promise is that every section degrades to
 * a named "unavailable" line rather than aborting. You run it because something
 * is already wrong, so it has to survive the tools it queries being broken.
 */

const script = readFileSync('infra/deploy/capacity-report.sh', 'utf8');

function runWith(stubs) {
  return spawnSync('bash', ['-c', `${stubs}\n${script.replace(/^#!.*\n/, '')}`], { encoding: 'utf8' });
}

test('a dead Docker daemon does not end the report early', () => {
  // Under `set -euo pipefail` a command substitution takes its pipeline's exit
  // status, so an unguarded `x="$(docker ps | head -1)"` aborts the script --
  // silently, before disk usage and the reading guide.
  const result = runWith('docker() { return 1; }');
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /how to read this/, 'the report stopped before its last section');
  assert.match(result.stdout, /unavailable/, 'nothing was reported as unavailable');
});

test('every section still prints when no tool is available at all', () => {
  const stubs = [
    'docker() { return 1; }',
    'free() { return 127; }',
    'vmstat() { return 127; }',
    'df() { return 127; }',
    'du() { return 127; }',
  ].join('\n');
  const result = runWith(stubs);
  assert.equal(result.status, 0, result.stderr);
  for (const section of ['memory', 'memory pressure', 'swap activity', 'container memory', 'disk', 'database size', 'how to read this']) {
    assert.ok(result.stdout.includes(`== ${section} ==`), `missing section: ${section}`);
  }
});

test('the report is read-only', () => {
  // It is meant to be safe to run on production during a fixture window, which
  // is also the most informative time to run it.
  for (const forbidden of [/docker\s+(rm|stop|kill|restart|prune)/, /\bDROP\b/, /\bDELETE\b/, /\bTRUNCATE\b/, /\bUPDATE\b/]) {
    assert.doesNotMatch(script, forbidden, `capacity-report.sh is not read-only: ${forbidden}`);
  }
});
