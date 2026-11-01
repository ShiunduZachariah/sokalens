import assert from "node:assert/strict";
import test from "node:test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync, chmodSync, readFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const script = fileURLToPath(new URL("./npm-audit-with-retry.sh", import.meta.url));

/**
 * Run the script with a stub `npm` earlier on PATH. The stub records every
 * invocation, so we can assert exactly how many attempts were made -- the
 * whole point of this script is that transport failures retry and real
 * findings do not. `$CALLS` inside stubBody is the attempt count so far,
 * counting the current one.
 */
function run(stubBody, { attempts = "3", baseDelay = "0" } = {}) {
  const dir = mkdtempSync(join(tmpdir(), "npm-audit-retry-"));
  const counter = join(dir, "calls");
  const stub = join(dir, "npm");
  writeFileSync(
    stub,
    `#!/usr/bin/env bash\necho x >> "${counter}"\nCALLS=$(wc -l < "${counter}")\n${stubBody}\n`,
  );
  chmodSync(stub, 0o755);

  let status = 0;
  let output = "";
  try {
    output = execFileSync("bash", [script, "apps/web", "high"], {
      encoding: "utf8",
      env: {
        ...process.env,
        PATH: `${dir}:${process.env.PATH}`,
        NPM_AUDIT_ATTEMPTS: attempts,
        NPM_AUDIT_BASE_DELAY: baseDelay,
      },
      stdio: ["ignore", "pipe", "pipe"],
    });
  } catch (error) {
    status = error.status ?? 1;
    output = `${error.stdout ?? ""}${error.stderr ?? ""}`;
  }
  const calls = existsSync(counter)
    ? readFileSync(counter, "utf8").trim().split("\n").filter(Boolean).length
    : 0;
  return { calls, output, status };
}

const UNAVAILABLE = `echo "npm warn audit 503 Service Unavailable - POST https://registry.npmjs.org/-/npm/v1/security/advisories/bulk" >&2
   echo "npm error audit endpoint returned an error" >&2
   exit 1`;

test("a clean audit passes on the first attempt", () => {
  const { calls, status } = run(`echo "found 0 vulnerabilities"; exit 0`);
  assert.equal(status, 0);
  assert.equal(calls, 1);
});

test("a real finding fails immediately and is never retried", () => {
  // This is the property that stops the retry becoming "run until green".
  const { calls, status, output } = run(
    `echo "1 high severity vulnerability"; echo "found 1 vulnerability"; exit 1`,
  );
  assert.equal(status, 1);
  assert.equal(calls, 1, "a finding must not be retried");
  assert.match(output, /reported findings/);
});

test("a 503 from the advisory service is retried, then fails closed", () => {
  const { calls, status, output } = run(UNAVAILABLE);
  assert.equal(calls, 3, "exhausts the bounded attempts");
  assert.equal(status, 1, "an unavailable audit is NOT a pass");
  assert.match(output, /could not complete after 3 attempts/);
  assert.match(output, /NOT a pass/);
});

test("a network timeout is retried, then fails closed", () => {
  const { calls, status } = run(
    `echo "npm warn audit network timeout at: https://registry.npmjs.org/-/npm/v1/security/advisories/bulk" >&2; exit 1`,
  );
  assert.equal(calls, 3);
  assert.equal(status, 1);
});

test("an outage that clears mid-run recovers and passes", () => {
  const { calls, status } = run(
    `if [ "$CALLS" -lt 3 ]; then
       echo "npm error audit endpoint returned an error" >&2; exit 1
     fi
     echo "found 0 vulnerabilities"; exit 0`,
  );
  assert.equal(status, 0, "recovers once the service answers");
  assert.equal(calls, 3, "took three attempts to get an answer");
});

test("a finding discovered after a transient outage still fails", () => {
  // The service coming back must not launder a real vulnerability into a pass.
  const { calls, status, output } = run(
    `if [ "$CALLS" -lt 2 ]; then
       echo "npm error audit endpoint returned an error" >&2; exit 1
     fi
     echo "found 3 vulnerabilities (2 high, 1 critical)"; exit 1`,
  );
  assert.equal(status, 1);
  assert.equal(calls, 2, "stops as soon as a real answer arrives");
  assert.match(output, /reported findings/);
});
