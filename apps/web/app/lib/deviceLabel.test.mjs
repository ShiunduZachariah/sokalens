import assert from "node:assert/strict";
import test from "node:test";

import { deviceLabel } from "./deviceLabel.ts";

test("a client-supplied name wins over anything inferred", () => {
  assert.equal(deviceLabel("mobile web browser", "Mozilla/5.0 (Linux; Android 10; K)"), "mobile web browser");
});

test("a user agent becomes something a reader recognises", () => {
  // The session list exists so someone can spot a sign-in that is not theirs.
  // Nobody scans a raw agent string and recognises themselves.
  assert.equal(deviceLabel("", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:156.0) Gecko/20100101 Firefox/156.0"), "Firefox on Windows");
  assert.equal(deviceLabel(null, "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1"), "Safari on iPhone");
  assert.equal(deviceLabel(null, "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 Chrome/140.0 Mobile Safari/537.36"), "Chrome on Android");
});

test("browsers that impersonate others are named correctly", () => {
  // Edge and Opera both carry "Chrome", and Chrome carries "Safari". Matching
  // in the wrong order labels every Edge session as Chrome.
  assert.equal(deviceLabel(null, "Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 Chrome/140.0 Safari/537.36 Edg/140.0"), "Edge on Windows");
  assert.equal(deviceLabel(null, "Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 Chrome/140.0 Safari/537.36 OPR/120.0"), "Opera on Windows");
  assert.equal(deviceLabel(null, "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/140.0 Safari/537.36"), "Chrome on Mac");
});

test("a session with nothing to go on says so rather than guessing", () => {
  assert.equal(deviceLabel("", ""), "Unknown device");
  assert.equal(deviceLabel(null, null), "Unknown device");
  assert.equal(deviceLabel(undefined, undefined), "Unknown device");
});

test("an unrecognised agent stays distinguishable from the others", () => {
  // Collapsing every unknown client into one label would put several rows
  // under the same name, which defeats the point of the list.
  const label = deviceLabel(null, "SomeScriptedClient/2.1 (+https://example.test/bot)");
  assert.notEqual(label, "Unknown device");
  assert.ok(label.length <= 41, `label ${label} is too long for a row`);
});
