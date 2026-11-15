import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { parseCampaignToken, signCampaignPayload } from "./campaignToken.ts";

const APP = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

// Shared with services/worker/internal/marketing/card_test.go. If the Go
// signer and this verifier ever disagree, every automated post fails, so both
// sides pin the same token.
const SECRET = "test-secret-that-is-at-least-32-characters";
const VECTOR =
  "eyJ2IjoxLCJrIjoic3RhdCIsImYiOiJzcXVhcmUiLCJlIjoiT3VyIHJlY29yZCIsImgiOiI2OCUiLCJiIjoib2Ygb3VyIHRvcC1sZWFndWUgbWF0Y2ggcmVzdWx0IHBpY2tzIHdlcmUgcmlnaHQiLCJuIjoiMTM2IG9mIDIwMCBncmFkZWQgcGlja3MgwrcgbGFzdCA5MCBkYXlzIn0.EmYlgKih11hM1HI59tnklOvfmUJJhV4z8YmSP2mYptE";

const encode = (value) => Buffer.from(JSON.stringify(value)).toString("base64url");

test("the worker's signed card verifies", () => {
  const card = parseCampaignToken(VECTOR, SECRET);
  assert.ok(card, "the shared vector did not verify");
  assert.equal(card.kind, "stat");
  assert.equal(card.format, "square");
  assert.equal(card.headline, "68%");
  assert.equal(card.note, "136 of 200 graded picks · last 90 days");
});

test("an edited or unsigned card is refused", () => {
  const [payload, signature] = VECTOR.split(".");
  const forged = encode({ v: 1, k: "stat", h: "100%", b: "forged words on our template" });
  assert.equal(parseCampaignToken(`${forged}.${signature}`, SECRET), null, "a payload swap verified");
  assert.equal(parseCampaignToken(payload, SECRET), null, "a token with no signature verified");
  assert.equal(parseCampaignToken(VECTOR, "another-secret-that-is-also-32-characters"), null, "a different secret verified");
  assert.equal(parseCampaignToken(VECTOR, undefined), null, "no secret configured must refuse everything");
  assert.equal(parseCampaignToken(VECTOR, "short"), null, "a weak secret must refuse everything");
  assert.equal(parseCampaignToken(`${payload}.${signature}.x`, SECRET), null);
});

test("a signed card is still bounded", () => {
  const sign = (value) => signCampaignPayload(encode(value), SECRET);
  assert.equal(parseCampaignToken(sign({ v: 2, k: "stat" }), SECRET), null, "an unknown version rendered");
  assert.equal(parseCampaignToken(sign({ v: 1, k: "anything" }), SECRET), null, "an unknown kind rendered");

  const items = Array.from({ length: 9 }, (_, index) => ({ h: `Home ${index}`, a: "Away", p: "Draw", c: 250, r: "maybe" }));
  const card = parseCampaignToken(sign({ v: 1, k: "picks", f: "story", i: items, p: { h: -5, d: 20000, a: 1 } }), SECRET);
  assert.equal(card.items.length, 5, "a card took more rows than fit");
  assert.equal(card.items[0].confidence, 100);
  assert.equal(card.items[0].result, "");
  assert.equal(card.format, "story");
  assert.deepEqual(card.probs, { home: 0, draw: 10000, away: 1 });
  assert.equal(parseCampaignToken("x".repeat(7000), SECRET), null, "an oversize token was parsed");
});

test("the card route refuses to draw anything it cannot verify", () => {
  const route = path.join(APP, "og/campaign/route.tsx");
  assert.ok(existsSync(route), "the campaign card route is missing");
  const source = readFileSync(route, "utf8");
  assert.match(source, /parseCampaignToken\(/);
  assert.match(source, /MARKETING_CARD_SECRET/);
  assert.match(source, /brandShareCard/, "an unverified token must fall back to the plain brand card");
  // The worker posts only cards this header vouches for.
  assert.match(source, /"X-Campaign-Card": "verified"/);
  assert.match(source, /noindex/);
});
