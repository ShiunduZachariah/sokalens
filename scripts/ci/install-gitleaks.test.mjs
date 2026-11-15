import assert from "node:assert/strict";
import test from "node:test";
import { createHash } from "node:crypto";
import { execFileSync, spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const script = fileURLToPath(new URL("./install-gitleaks.sh", import.meta.url));
const VERSION = "8.30.1";
const ARCHIVE_NAME = `gitleaks_${VERSION}_linux_x64.tar.gz`;

/**
 * A stand-in release: a tarball holding a `gitleaks` that prints a marker.
 * Served over file:// so the tests never touch the network, and so "the CDN is
 * down" is simply a URL that does not exist.
 */
function fixture() {
  const dir = mkdtempSync(join(tmpdir(), "gitleaks-install-"));
  const src = join(dir, "src");
  mkdirSync(src);
  writeFileSync(join(src, "gitleaks"), "#!/usr/bin/env bash\necho stub-gitleaks\n");
  chmodSync(join(src, "gitleaks"), 0o755);
  const archive = join(dir, ARCHIVE_NAME);
  execFileSync("tar", ["-czf", archive, "-C", src, "gitleaks"]);
  const sha256 = createHash("sha256").update(readFileSync(archive)).digest("hex");
  return {
    dir,
    archive,
    sha256,
    url: pathToFileURL(archive).href,
    missingUrl: pathToFileURL(join(dir, "nope.tar.gz")).href,
    cacheRoot: join(dir, "toolcache"),
    cached: join(dir, "toolcache", "gitleaks", VERSION, ARCHIVE_NAME),
    bin: join(dir, "bin"),
  };
}

function run(f, { url = f.url, sha256 = f.sha256, attempts = "3" } = {}) {
  // spawnSync rather than execFileSync: warnings go to stderr even when the
  // install succeeds, and the tests assert on them.
  const result = spawnSync("bash", [script, f.bin], {
    encoding: "utf8",
    env: {
      ...process.env,
      GITLEAKS_VERSION: VERSION,
      GITLEAKS_SHA256: sha256,
      GITLEAKS_URL: url,
      GITLEAKS_CACHE_DIR: f.cacheRoot,
      GITLEAKS_DOWNLOAD_ATTEMPTS: attempts,
      GITLEAKS_DOWNLOAD_BASE_DELAY: "0",
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  const status = result.status ?? 1;
  const output = `${result.stdout}${result.stderr}`;
  const failedAttempts = (output.match(/\(attempt \d+\/\d+\)/g) ?? []).length;
  return { status, output, failedAttempts, installed: existsSync(join(f.bin, "gitleaks")) };
}

test("a verified download is installed and cached for the next run", () => {
  const f = fixture();
  const { status, output, installed } = run(f);
  assert.equal(status, 0, output);
  assert.ok(installed);
  assert.match(output, /stub-gitleaks/, "the installed binary must be the one that runs");
  assert.ok(existsSync(f.cached), "the verified archive should be kept in the tool cache");
});

test("a cached archive is used without touching the network", () => {
  // The whole point: a CDN outage after the first download is irrelevant.
  const f = fixture();
  assert.equal(run(f).status, 0);
  const { status, output, installed } = run(f, { url: f.missingUrl, attempts: "1" });
  assert.equal(status, 0, output);
  assert.ok(installed);
  assert.match(output, /tool cache/);
  assert.doesNotMatch(output, /attempt/);
});

test("a cached archive that fails its checksum is discarded and fetched again", () => {
  const f = fixture();
  mkdirSync(join(f.cacheRoot, "gitleaks", VERSION), { recursive: true });
  writeFileSync(f.cached, "half a file");
  const { status, output, installed } = run(f);
  assert.equal(status, 0, output);
  assert.ok(installed);
  assert.match(output, /failed its checksum and was discarded/);
  assert.equal(
    createHash("sha256").update(readFileSync(f.cached)).digest("hex"),
    f.sha256,
    "the bad cache entry should be replaced by the verified archive",
  );
});

test("an archive that does not match the pin is never installed or cached", () => {
  const f = fixture();
  const { status, output, installed, failedAttempts } = run(f, { sha256: "0".repeat(64) });
  assert.equal(status, 1);
  assert.equal(installed, false, "an unverified binary must never land on PATH");
  assert.equal(existsSync(f.cached), false, "an unverified archive must never be cached");
  assert.equal(failedAttempts, 3);
  assert.match(output, /did not match the pinned SHA-256/);
  assert.match(output, /did NOT run, so this is not a pass/);
});

test("an unreachable release fails closed after the bounded retries", () => {
  const f = fixture();
  const { status, output, installed, failedAttempts } = run(f, { url: f.missingUrl });
  assert.equal(status, 1);
  assert.equal(installed, false);
  assert.equal(failedAttempts, 3, "every attempt should be made before giving up");
  assert.match(output, /did NOT run, so this is not a pass/);
});

test("the archive must be pinned", () => {
  const f = fixture();
  const { status, output } = run(f, { sha256: "" });
  assert.equal(status, 1);
  assert.match(output, /GITLEAKS_SHA256 is required/);
});
