import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

import { decodeSlipToken, encodeSlipToken } from "./slipToken.ts";

/**
 * The token travels in a share URL, so it round-trips what a card draws and
 * refuses everything else. It is rendered into an image from a URL a stranger
 * can edit, which is why the validation is not decorative.
 */
test("a slip round-trips through the URL", () => {
  const token = encodeSlipToken({
    label: "9-leg ACCA",
    odds: "7.51",
    legs: [
      { market: "Match Result", selection: "Home" },
      { market: "Both Teams to Score", selection: "Yes" },
    ],
  });

  const decoded = decodeSlipToken(token);
  assert.equal(decoded?.label, "9-leg ACCA");
  assert.equal(decoded?.odds, "7.51");
  assert.deepEqual(decoded?.legs, [
    { market: "Match Result", selection: "Home" },
    { market: "Both Teams to Score", selection: "Yes" },
  ]);
});

test("the token is URL-safe", () => {
  const token = encodeSlipToken({ legs: [{ market: "Total Goals", selection: "Over 2.5" }] });
  assert.match(token, /^[A-Za-z0-9_-]+$/, "the token would need escaping in a URL");
  assert.equal(encodeURIComponent(token), token);
});

test("a hostile or broken token is refused rather than rendered", () => {
  for (const bad of [
    "",
    "not base64!",
    "a".repeat(5000),
    Buffer.from("null", "utf8").toString("base64url"),
    Buffer.from('["only one"]', "utf8").toString("base64url"),
    Buffer.from('["label","1.0",[]]', "utf8").toString("base64url"),
    Buffer.from(JSON.stringify(["l", "1", [["m"]]]), "utf8").toString("base64url"),
    Buffer.from(JSON.stringify(["l", "1", [["m", 7]]]), "utf8").toString("base64url"),
  ]) {
    assert.equal(decodeSlipToken(bad), null, `accepted a token it should not: ${bad.slice(0, 24)}`);
  }
});

test("an oversized slip cannot grow the URL without bound", () => {
  const legs = Array.from({ length: 50 }, (_, index) => ({ market: `m${index}`, selection: `s${index}` }));
  const decoded = decodeSlipToken(encodeSlipToken({ legs }));
  assert.ok(decoded);
  assert.equal(decoded.legs.length, 20, "the leg cap is not applied on encode");
});

/**
 * This module is imported by AccaExplorer, a client component, so it runs in
 * the browser. It previously used Buffer's "base64url", a Node-only encoding
 * that Next's browser Buffer polyfill does not implement -- the call threw
 * `TypeError: Unknown encoding: base64url` and took the whole
 * /acca-predictions page down.
 *
 * The suite could not catch that, because it runs under Node where Buffer
 * supports base64url perfectly well. So assert against the environment the bug
 * actually lived in: the source must not reach for Node built-ins at all.
 */
test("the token encoder uses only primitives a browser has", async () => {
  const source = await readFile(new URL("./slipToken.ts", import.meta.url), "utf8");
  const code = source.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
  assert.doesNotMatch(code, /\bBuffer\b/, "Buffer is not available in the browser");
  assert.doesNotMatch(code, /base64url/, "base64url is a Node-only Buffer encoding");
});

/**
 * btoa is Latin-1 only. Encoding without converting to UTF-8 bytes first
 * corrupts non-ASCII silently rather than throwing, which is harder to notice
 * than a crash -- team and competition names are full of accents.
 */
test("a slip with non-ASCII names round-trips intact", () => {
  const legs = [
    { market: "Résultat du Match", selection: "Bayern München" },
    { market: "Gols Mais/Menos", selection: "Atlético Mineiro" },
  ];
  const decoded = decodeSlipToken(encodeSlipToken({ label: "Köln × Málaga", odds: "3.40", legs }));
  assert.equal(decoded?.label, "Köln × Málaga");
  assert.deepEqual(decoded?.legs, legs);
});
