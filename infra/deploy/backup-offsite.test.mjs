import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';

/**
 * Exercises the off-site half of infra/deploy/backup.sh.
 *
 * The dump/verify half needs a live PostgreSQL container, so it is not
 * reachable here. The upload half is where user PII leaves the host, and it is
 * reachable: the block is sourced with `aws` and `gpg` stubbed, so the branch
 * that decides *whether* to upload -- and whether the bytes are encrypted first
 * -- is tested without a bucket or a database.
 */

const script = readFileSync('infra/deploy/backup.sh', 'utf8');

/** The off-site block, lifted out of the script so it can run on its own. */
function offsiteBlock() {
  const start = script.indexOf('# --- Off-site copy');
  assert.notEqual(start, -1, 'off-site block marker missing from backup.sh');
  // Anchored on the retention block's first statement rather than its comment.
  // The comment was rewritten upstream when local retention changed from
  // fourteen days to one dump, which silently broke every test in this file --
  // a prose marker is not a contract.
  const end = script.indexOf('\nKEEP_LOCAL_DUMPS=', start);
  assert.notEqual(end, -1, 'local retention marker missing from backup.sh');
  return script.slice(start, end);
}

/**
 * The real cleanup trap, not a copy of it.
 *
 * The ciphertext is removed from an EXIT trap rather than after a successful
 * upload, because `set -e` means a failed upload never reaches a cleanup line.
 * Re-declaring an equivalent trap here would test the harness instead of the
 * script, and would keep passing if the real trap were ever deleted.
 */
function cleanupBlock() {
  const start = script.indexOf('ENCRYPTED_FILE=""');
  assert.notEqual(start, -1, 'ciphertext cleanup missing from backup.sh');
  const marker = 'trap cleanup_run EXIT';
  const end = script.indexOf(marker, start);
  assert.notEqual(end, -1, 'cleanup trap missing from backup.sh');
  return `RESTORE_CREATED=0
${script.slice(start, end)}${marker}
`;
}

function runOffsite(env = {}, { realGpg = false } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'sokalens-backup-'));
  const dumpFile = join(dir, 'sokalens-20260908T000000Z.dump');
  writeFileSync(dumpFile, 'PGDMP fake dump with user@example.com and $2a$hash\n');

  const gpgStub = realGpg ? '' : `
gpg() {
  local out=""
  local prev=""
  for arg in "$@"; do
    if [ "$prev" = "--output" ]; then out="$arg"; fi
    prev="$arg"
  done
  cat >/dev/null
  if [ -n "$out" ] && [ "$out" != "/dev/null" ]; then printf 'ENCRYPTED' > "$out"; fi
  return \${GPG_EXIT:-0}
}
export -f gpg 2>/dev/null || true
`;

  const result = spawnSync('bash', ['-c', `set -euo pipefail
TIMESTAMP=20260908T000000Z
DUMP_FILE=${JSON.stringify(dumpFile)}
BUCKET=test-bucket
aws() { echo "aws:$*" >&2; }
command() {
  if [ "\${2:-}" = "aws" ]; then return 0; fi
  builtin command "$@"
}
${gpgStub}
${cleanupBlock()}
${offsiteBlock()}
`], { encoding: 'utf8', env: { ...process.env, ...env } });

  return { ...result, dir, dumpFile, files: readdirSync(dir) };
}

test('an unencrypted dump is never uploaded by default', () => {
  // The dump carries account emails and password hashes. Silence here would
  // mean a misconfigured bucket exposes every user.
  const result = runOffsite({
    S3_ENDPOINT: 'https://s3.example.com',
    S3_ACCESS_KEY_ID: 'key',
    S3_SECRET_ACCESS_KEY: 'secret',
  });
  assert.notEqual(result.status, 0, result.stderr);
  assert.match(result.stderr, /refusing to upload an unencrypted dump/);
  assert.doesNotMatch(result.stderr, /aws:/, 'aws was called despite the refusal');
});

test('a configured passphrase uploads ciphertext, not the dump', () => {
  const result = runOffsite({
    S3_ENDPOINT: 'https://s3.example.com',
    S3_ACCESS_KEY_ID: 'key',
    S3_SECRET_ACCESS_KEY: 'secret',
    BACKUP_ENCRYPTION_PASSPHRASE: 'correct horse battery staple',
  });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stderr, /aws:.*\.dump\.gpg/, 'uploaded object is not the encrypted one');
  assert.doesNotMatch(result.stderr, /aws:.*\.dump /, 'the plaintext dump was uploaded');
});

test('the plaintext escape hatch is explicit and loud', () => {
  const result = runOffsite({
    S3_ENDPOINT: 'https://s3.example.com',
    S3_ACCESS_KEY_ID: 'key',
    S3_SECRET_ACCESS_KEY: 'secret',
    BACKUP_ALLOW_PLAINTEXT_UPLOAD: 'true',
  });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stderr, /WARNING: uploading an UNENCRYPTED dump/);
  assert.match(result.stderr, /aws:/);
});

test('nothing is uploaded when no destination is configured', () => {
  const result = runOffsite({});
  assert.equal(result.status, 0, result.stderr);
  assert.doesNotMatch(result.stderr, /aws:/);
});

test('the legacy R2 variables still work', () => {
  // An existing deployment must keep running after this change.
  const result = runOffsite({
    R2_ENDPOINT: 'https://r2.example.com',
    R2_ACCESS_KEY_ID: 'key',
    R2_SECRET_ACCESS_KEY: 'secret',
    BACKUP_ENCRYPTION_PASSPHRASE: 'passphrase',
  });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stderr, /--endpoint-url https:\/\/r2\.example\.com/);
});

test('a backup that will not decrypt is never uploaded', () => {
  // An unopenable off-site backup is indistinguishable from no off-site
  // backup, and the difference is only discovered on the worst day. The stub
  // encrypts to garbage and then refuses to decrypt it.
  const dir = mkdtempSync(join(tmpdir(), 'sokalens-backup-'));
  const dumpFile = join(dir, 'sokalens-20260908T000000Z.dump');
  writeFileSync(dumpFile, 'PGDMP fake dump\n');

  const result = spawnSync('bash', ['-c', `set -euo pipefail
TIMESTAMP=20260908T000000Z
DUMP_FILE=${JSON.stringify(dumpFile)}
BUCKET=test-bucket
aws() { echo "aws:$*" >&2; }
command() { if [ "\${2:-}" = "aws" ]; then return 0; fi; builtin command "$@"; }
gpg() {
  # Keyed on the argument rather than a call counter: gpg runs inside a
  # \`printf | gpg\` pipeline, so a counter would increment in a subshell and
  # never be seen by the next invocation -- the stub would silently succeed and
  # the test would pass while asserting nothing.
  cat >/dev/null
  local out="" prev="" decrypting=0
  for arg in "$@"; do
    if [ "$prev" = "--output" ]; then out="$arg"; fi
    if [ "$arg" = "--decrypt" ]; then decrypting=1; fi
    prev="$arg"
  done
  if [ "$decrypting" -eq 1 ]; then return 2; fi
  printf 'CORRUPT' > "$out"
  return 0
}
${cleanupBlock()}
${offsiteBlock()}
`], {
    encoding: 'utf8',
    env: {
      ...process.env,
      S3_ENDPOINT: 'https://s3.example.com',
      S3_ACCESS_KEY_ID: 'key',
      S3_SECRET_ACCESS_KEY: 'secret',
      BACKUP_ENCRYPTION_PASSPHRASE: 'passphrase',
    },
  });

  assert.notEqual(result.status, 0, result.stderr);
  assert.match(result.stderr, /failed to decrypt; refusing to upload/);
  assert.doesNotMatch(result.stderr, /aws:/, 'an unverifiable backup was uploaded');
  assert.ok(!readdirSync(dir).some((name) => name.endsWith('.gpg')), 'the bad ciphertext was left behind');
});

test('the script never deletes a remote object', () => {
  // §17.2 wants Object Lock precisely so a compromised host cannot erase its
  // own backups. Off-site retention belongs to a bucket lifecycle rule this
  // host has no credentials to change.
  assert.doesNotMatch(script, /aws\s+s3\s+rm/);
  assert.doesNotMatch(script, /aws\s+s3api\s+delete/);
});

test('round-trips through real gpg when it is installed', (t) => {
  const probe = spawnSync('gpg', ['--version'], { encoding: 'utf8' });
  if (probe.status !== 0) {
    t.skip('gpg not installed');
    return;
  }
  const result = runOffsite({
    S3_ENDPOINT: 'https://s3.example.com',
    S3_ACCESS_KEY_ID: 'key',
    S3_SECRET_ACCESS_KEY: 'secret',
    BACKUP_ENCRYPTION_PASSPHRASE: 'correct horse battery staple',
  }, { realGpg: true });

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stderr, /aws:.*\.dump\.gpg/);
});

test('a successful upload leaves no ciphertext on the host', () => {
  // The encrypted dump carries the same account emails and password hashes as
  // the plaintext one. It is a transient artefact of the upload, not something
  // the host should accumulate.
  const result = runOffsite({
    S3_ENDPOINT: 'https://s3.example.com',
    S3_ACCESS_KEY_ID: 'key',
    S3_SECRET_ACCESS_KEY: 'secret',
    BACKUP_ENCRYPTION_PASSPHRASE: 'passphrase',
  });
  assert.equal(result.status, 0, result.stderr);
  assert.ok(
    !readdirSync(result.dir).some((name) => name.endsWith('.gpg')),
    `ciphertext left behind: ${readdirSync(result.dir).join(', ')}`,
  );
});

test('local retention also sweeps stray ciphertext', () => {
  // Backstop for a run killed hard enough to skip its own EXIT trap. Without
  // it, an encrypted dump orphaned by a SIGKILL would sit on the host forever.
  //
  // Retention upstream moved from 'delete anything older than fourteen days'
  // to 'keep the newest verified dump and nothing else', so the sweep is now a
  // loop over superseded dumps rather than a find with -mtime. The ciphertext
  // has to be removed alongside each one it belonged to.
  const sweep = script.split(/\r?\n/).find((line) => line.includes('rm -f --') && line.includes('$obsolete'));
  assert.ok(sweep, 'local retention sweep missing from backup.sh');
  assert.match(sweep, /\$obsolete\.gpg/);
});

