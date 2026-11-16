import assert from "node:assert/strict";
import test from "node:test";
import { execFileSync, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, unlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const script = fileURLToPath(new URL("./verify-scan-checkout.sh", import.meta.url));
function fixture(t, shallow = false) {
  const dir = mkdtempSync(join(tmpdir(), "scan-checkout-test-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const source = join(dir, "source");
  const checkout = join(dir, "checkout");
  mkdirSync(source);
  const git = (...args) => execFileSync("git", args, { cwd: source, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();
  git("init", "--initial-branch=main");
  git("config", "user.name", "Checkout fixture");
  git("config", "user.email", "fixture@example.test");
  let first;
  for (let i = 0; i < 3; i++) {
    writeFileSync(join(source, "sample"), String(i));
    git("add", "sample"); git("commit", "-m", "fixture " + i);
    if (i === 0) { first = git("rev-parse", "HEAD"); git("tag", "before-update"); }
  }
  git("clone", ...(shallow ? ["--depth=1", pathToFileURL(source).href] : [source]), checkout);
  return { checkout, first, expected: git("rev-parse", "HEAD") };
}
function verify(f, expected = f.expected) {
  const r = spawnSync("bash", [script, expected], { cwd: f.checkout, encoding: "utf8" });
  return { status: r.status, output: r.stdout + r.stderr };
}
test("complete checkout of the expected commit is accepted", t => {
  const f = fixture(t); const r = verify(f);
  assert.equal(r.status, 0, r.output);
  assert.match(r.output, /exact commit and complete history/);
});
test("a shallow checkout cannot produce a green secret scan", t => {
  const r = verify(fixture(t, true));
  assert.notEqual(r.status, 0); assert.match(r.output, /shallow checkout refused/);
});
test("a checkout of a different commit is refused", t => {
  const f = fixture(t); const r = verify(f, f.first);
  assert.notEqual(r.status, 0); assert.match(r.output, /identity differs/);
});
test("missing historical objects fail connectivity validation", t => {
  const f = fixture(t);
  const object = join(f.checkout, ".git", "objects", f.first.slice(0, 2), f.first.slice(2));
  assert.ok(existsSync(object)); unlinkSync(object);
  assert.notEqual(verify(f).status, 0);
});
test("persisted credentials are rejected without printing their value", t => {
  const f = fixture(t); const secret = "test-header-never-log";
  execFileSync("git", ["config", "http.https://github.com/.extraheader", secret], { cwd: f.checkout });
  const r = verify(f); assert.notEqual(r.status, 0);
  assert.match(r.output, /retained an HTTP authorization header/);
  assert.ok(!r.output.includes(secret));
});
test("workflow retries transport failures but keeps a fatal full-history scan gate", () => {
  const ci = readFileSync(new URL("../../.github/workflows/ci.yml", import.meta.url), "utf8");
  const job = ci.match(/\n  secret-scan:\n([\s\S]*?)(?=\n  [a-z][a-z-]*:\n)/)[1];
  const steps = job.split(/^      - /m).slice(1);
  const checkouts = steps.filter(s => s.includes("uses: actions/checkout@v4"));
  assert.equal(checkouts.length, 3);
  for (const checkout of checkouts) {
    assert.match(checkout, /fetch-depth: 0/); assert.match(checkout, /persist-credentials: false/);
    assert.ok(checkout.includes("ref: ${{ github.sha }}"));
  }
  assert.match(checkouts[0], /continue-on-error: true/);
  assert.match(checkouts[1], /continue-on-error: true/);
  assert.match(checkouts[1], /if: steps.scan_checkout_1.outcome == 'failure'/);
  assert.match(checkouts[2], /if: steps.scan_checkout_1.outcome == 'failure' && steps.scan_checkout_2.outcome == 'failure'/);
  assert.doesNotMatch(checkouts[2], /continue-on-error/);
  const verification = steps.findIndex(s => s.includes("verify-scan-checkout.sh"));
  const scan = steps.findIndex(s => s.includes("gitleaks detect"));
  assert.ok(verification >= 0 && verification < scan);
  assert.doesNotMatch(steps[verification], /continue-on-error/);
  assert.doesNotMatch(steps[scan], /continue-on-error/);
  assert.match(steps[scan], /--redact/);
});
