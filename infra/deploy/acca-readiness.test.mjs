import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

function readiness(row, curlStatus = 0, body = '{"items":[{"id":"x","legs":[{"fair_odds":1.25},{"fair_odds":1.25}],"combined_fair_odds":1.5625}]}') {
  return spawnSync('bash', ['-c', `set -euo pipefail
source infra/deploy/acca-readiness.sh
docker() { if [[ "$*" == *"node -e"* ]]; then node -e "\${@: -1}"; else printf '%s\\n' '${row}'; fi; }
curl() { echo "curl:$*" >&2; printf '%s\\n' '${body}'; return ${curlStatus}; }
sleep() { :; }
ACCA_READINESS_ATTEMPTS=3
ACCA_READINESS_INTERVAL=0
acca_readiness
`], { encoding: 'utf8' });
}

test('ready bounded inventory warms list and featured API', () => {
  const result = readiness('150|0|0|8');
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stderr, /v1\/accas\?limit=10/);
  assert.match(result.stderr, /v1\/accas\/featured/);
});
for (const [name, row] of [
  ['empty inventory', '0|0|0|0'],
  ['inventory over the ceiling', '3001|0|0|8'],
  ['duplicate hashes', '150|1|0|8'],
  ['invalid or started slips', '150|0|1|8'],
  ['unexpanded markets', '150|0|0|4'],
  ['malformed database response', 'error'],
]) {
  test(`readiness rejects ${name}`, () => {
    const result = readiness(row);
    assert.notEqual(result.status, 0);
    assert.doesNotMatch(result.stderr, /v1\/accas/);
  });
}
test('readiness fails when API warmup fails', () => {
  assert.notEqual(readiness('150|0|0|8', 22).status, 0);
});
for (const body of ['{"items":[]}', '{"wrong":[]}', 'not-json']) {
  test(`readiness rejects HTTP 200 with unusable body ${body}`, () => {
    assert.notEqual(readiness('150|0|0|8', 0, body).status, 0);
  });
}

test('readiness SQL applies the generated ACCA odds standard', () => {
  const script = readFileSync('infra/deploy/acca-readiness.sh', 'utf8');
  assert.match(script, /power\(1\.5, s\.leg_count::double precision \/ 2\)/);
  assert.match(script, /l\.fair_odds_at_generation < 1\.10/);
});

for (const body of [
  '{"items":[{"id":"weak-combined","legs":[{"fair_odds":1.25},{"fair_odds":1.25},{"fair_odds":1.25},{"fair_odds":1.25}],"combined_fair_odds":1.04}]}',
  '{"items":[{"id":"weak-leg","legs":[{"fair_odds":1.01},{"fair_odds":1.60}],"combined_fair_odds":1.60}]}',
]) {
  test('readiness rejects system ACCAs below the odds standard', () => {
    assert.notEqual(readiness('150|0|0|8', 0, body).status, 0);
  });
}
