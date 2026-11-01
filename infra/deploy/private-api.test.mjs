import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync, mkdirSync, existsSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const root = fileURLToPath(new URL('../../', import.meta.url));

test('Caddy bounds browser API bodies before forwarding to web and admin', () => {
  const config = JSON.parse(execFileSync('docker', ['run', '--rm',
    '-v', `${join(root, 'infra/deploy/Caddyfile')}:/etc/caddy/Caddyfile:ro`,
    'caddy:2', 'caddy', 'adapt', '--config', '/etc/caddy/Caddyfile'], { encoding: 'utf8' }));
  const server = Object.values(config.apps.http.servers).find(server => server.listen.includes(':443'));
  assert.equal(server.read_timeout, 10_000_000_000);
  assert.equal(server.read_header_timeout, 5_000_000_000);
  for (const host of ['sokalens.com', 'hq.sokalens.com']) {
    const site = server.routes.find(route => route.match?.some(match => match.host?.includes(host)));
    const routes = site.handle[0].routes;
    const capIndex = routes.findIndex(route => route.handle?.some(handler => handler.handler === 'request_body'));
    const proxyIndex = routes.findIndex(route => route.handle?.some(handler => handler.handler === 'reverse_proxy'));
    assert.ok(capIndex >= 0 && proxyIndex > capIndex, `${host}: cap must precede proxy`);
    assert.deepEqual(routes[capIndex].match, [{ path: ['/api/*'] }]);
    assert.equal(routes[capIndex].handle[0].max_size, 65_536);
  }
});

test('Caddy log preparation repairs a root-owned log without erasing it', () => {
  const deploy = readFileSync(join(root, 'infra/deploy/ci-deploy.sh'), 'utf8').replace(/\r\n/g, '\n');
  const definitions = deploy.split('[ -f "$TARBALL" ]')[0];
  execFileSync('docker', ['run', '--rm', 'node:24-bookworm', 'bash', '-c', `${definitions}
trap - EXIT
sudo() { "$@"; }
useradd --system caddy
mkdir -p /var/log/caddy
printf 'existing log entry\\n' > /var/log/caddy/sokalens-access.log
chmod 0600 /var/log/caddy/sokalens-access.log
if runuser -u caddy -- test -w /var/log/caddy/sokalens-access.log; then exit 1; fi
prepare_caddy_logging
runuser -u caddy -- test -w /var/log/caddy/sokalens-access.log
test "$(cat /var/log/caddy/sokalens-access.log)" = 'existing log entry'
test "$(stat -c '%U:%G %a' /var/log/caddy/sokalens-access.log)" = 'caddy:caddy 640'
prepare_caddy_logging
runuser -u caddy -- test -w /var/log/caddy/sokalens-access.log
`], { stdio: 'pipe' });
});

// Exercise Compose merge semantics, not the text of either input file. A
// normal list addition would leave the inherited public binding in place.
for (const bind of ['0.0.0.0', '::', '127.0.0.1']) {
  test(`production replaces persisted API bind ${bind} with loopback only`, () => {
    const config = JSON.parse(execFileSync('docker', [
      'compose', '-f', 'infra/docker/docker-compose.yml',
      '-f', 'infra/deploy/docker-compose.prod.yml', '--profile', 'all',
      'config', '--format', 'json',
    ], {
      cwd: root,
      env: { ...process.env, API_BIND_ADDRESS: bind, API_PORT: '8080' },
      encoding: 'utf8',
      maxBuffer: 8 * 1024 * 1024,
    }));
    const { api, web, admin } = config.services;
    assert.equal(api.ports.length, 1);
    assert.equal(api.ports[0].host_ip, '127.0.0.1');
    assert.equal(api.ports[0].published, '8080');
    assert.equal(api.ports[0].target, 8080);
    assert.equal(web.environment.PREDICTION_API_BASE_URL, 'http://api:8080');
    assert.equal(admin.environment.ADMIN_API_BASE_URL, 'http://api:8080');
  });
}

test('environment-file paths never reach the application handler', async () => {
  const source = readFileSync(join(root, 'infra/deploy/Caddyfile'), 'utf8');
  const snippet = source.match(/\(deny_environment_files\) \{[\s\S]*?\n\}/)?.[0];
  assert.ok(snippet);
  for (const host of ['sokalens.com', 'www.sokalens.com', 'hq.sokalens.com']) {
    assert.ok(source.includes(`${host} {\n\timport deny_environment_files`));
  }

  // The QA hosts reach the same protection one level down, through qa_gate,
  // so asserting on the literal import would miss it. Check the indirection
  // holds at both ends rather than trusting the snippet name alone.
  for (const host of ['qa.sokalens.com', 'qa-admin.sokalens.com']) {
    assert.ok(source.includes(`${host} {\n\timport qa_gate`), `${host} must import qa_gate`);
  }
  const qaGate = source.match(/\(qa_gate\) \{[\s\S]*?\n\}/)?.[0];
  assert.ok(qaGate, 'qa_gate snippet must exist');
  assert.match(qaGate, /\timport deny_environment_files/);
  // A QA host that loses its auth gate is worse than one that never had it:
  // the stack is seeded from production data.
  assert.match(qaGate, /\tbasic_auth \{/);
  assert.match(qaGate, /X-Robots-Tag "noindex/);

  // dev.* was this same stack's previous name, retired 2026-09-16 along with
  // its DNS records. Re-introducing it would resurrect a second name for one
  // environment, which is the confusion the rename removed.
  for (const host of ['dev.sokalens.com', 'dev-admin.sokalens.com']) {
    assert.ok(!source.includes(`\n${host} {`), `${host} must not be served`);
  }

  // The rename is the whole point of the change; a stray re-introduction of the
  // old hostname would quietly undo it.
  assert.ok(!source.includes('\nadmin.sokalens.com {'), 'admin.sokalens.com must not be served');
  const dir = mkdtempSync(join(tmpdir(), 'sokalens-caddy-test-'));
  const name = `sokalens-caddy-test-${process.pid}`;
  writeFileSync(join(dir, 'Caddyfile'), `${snippet}\n:80 {\n import deny_environment_files\n respond "APPLICATION_HANDLER" 200\n}\n`);
  try {
    execFileSync('docker', ['run', '-d', '--name', name, '-p', '127.0.0.1::80',
      '-v', `${dir}:/etc/caddy:ro`, 'caddy:2'], { encoding: 'utf8' });
    const port = execFileSync('docker', ['port', name, '80/tcp'], { encoding: 'utf8' }).trim().split(':').at(-1);
    const base = `http://127.0.0.1:${port}`;
    let ready = false;
    for (let i = 0; i < 30; i++) {
      try { if ((await fetch(base)).status === 200) { ready = true; break; } } catch {}
      await new Promise(resolve => setTimeout(resolve, 200));
    }
    assert.ok(ready, 'isolated Caddy test listener did not start');
    for (const path of ['/.env', '/.env.prod', '/.ENV.local', '/nested/.env.backup', '/%2eenv', '/nested%2f.env', '/.env/child']) {
      for (const method of ['GET', 'HEAD']) {
        const response = await fetch(base + path, { method });
        assert.equal(response.status, 404, `${method} ${path}`);
        assert.doesNotMatch(await response.text(), /APPLICATION_HANDLER/);
      }
    }
    for (const path of ['/', '/predictions/today', '/api/account/v1/access', '/.well-known/acme-challenge/test']) {
      const response = await fetch(base + path);
      assert.equal(response.status, 200, path);
      assert.equal(await response.text(), 'APPLICATION_HANDLER');
    }
  } finally {
    execFileSync('docker', ['rm', '-f', name], { stdio: 'ignore' });
    rmSync(dir, { recursive: true, force: true });
  }
});

test('Docker context excludes root and nested environment files', () => {
  const dir = mkdtempSync(join(tmpdir(), 'sokalens-context-test-'));
  const context = join(dir, 'context');
  const output = join(dir, 'output');
  mkdirSync(join(context, 'apps/web/public'), { recursive: true });
  writeFileSync(join(context, '.dockerignore'), readFileSync(join(root, '.dockerignore')));
  writeFileSync(join(context, 'Dockerfile'), 'FROM scratch\nCOPY . /\n');
  const denied = ['.env', '.env.prod', 'apps/web/.env.local', 'apps/web/public/.env', 'apps/web/public/.env.backup'];
  for (const path of denied) writeFileSync(join(context, path), 'TEST_ONLY_NOT_A_SECRET');
  writeFileSync(join(context, 'apps/web/public/robots.txt'), 'User-agent: *');
  try {
    execFileSync('docker', ['build', '--output', `type=local,dest=${output}`, context], { stdio: 'pipe' });
    for (const path of denied) assert.equal(existsSync(join(output, path)), false, path);
    assert.equal(existsSync(join(output, 'apps/web/public/robots.txt')), true);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
