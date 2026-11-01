import assert from "node:assert/strict";
import test from "node:test";
import { adsterraScriptUrl, parseAdPlacementDecision, validateAdSenseConfig, validateAdsterraConfig } from "./adDelivery.ts";

test("placement response preserves server order and clamps attempt timeouts", () => {
  const decision = parseAdPlacementDecision({
    allowed: true,
    reason: "",
    providers: [
      { code: "monetag", adapter_key: "monetag", timeout_ms: 50, public_config: {}, attempt_token: "one.token" },
      { code: "adsense", adapter_key: "adsense", timeout_ms: 50_000, public_config: {}, attempt_token: "two_token" },
    ],
  });
  assert.deepEqual(decision?.providers.map((provider) => provider.code), ["monetag", "adsense"]);
  assert.deepEqual(decision?.providers.map((provider) => provider.timeoutMs), [500, 5_000]);
});

test("placement response fails closed on malformed executable-looking adapter data", () => {
  assert.equal(parseAdPlacementDecision({
    allowed: true,
    providers: [{
      code: "adsense", adapter_key: "javascript:alert(1)", timeout_ms: 1_000,
      public_config: {}, attempt_token: "safe-token",
    }],
  }), null);
});

test("AdSense accepts only publisher and numeric slot identifiers", () => {
  assert.deepEqual(validateAdSenseConfig({ client_id: "ca-pub-5331089211665627", slot_id: "1234567890" }), {
    clientId: "ca-pub-5331089211665627", slotId: "1234567890",
  });
  assert.equal(validateAdSenseConfig({ client_id: "https://evil.example/x.js", slot_id: "1234567890" }), null);
  assert.equal(validateAdSenseConfig({ client_id: "ca-pub-5331089211665627", slot_id: "<script>" }), null);
});

/*
 * Adsterra. The keys below are the real zones for this account; they are
 * public identifiers that ship in the page source, which is why asserting on
 * them is safe and useful -- a zone silently dropped from the migration is a
 * slot that earns nothing and fails no test otherwise.
 */
const leaderboard = "bc5283f86ffa8d1825f26b6ed0890189";
const rectangle = "d1f0d90bc1562d7b39503f9a38c8a85e";
const native = "63f75d6bc7a2d8e0cdd87f0217d2a8be";

const adsterraConfig = {
  script_origin: "https://bauval.org",
  zones: {
    top: { kind: "banner", zone_id: leaderboard, width: 728, height: 90 },
    "in-feed": { kind: "banner", zone_id: rectangle, width: 300, height: 250 },
    inline: { kind: "native", zone_id: native },
  },
};

test("Adsterra resolves the zone for the format being rendered", () => {
  assert.deepEqual(validateAdsterraConfig(adsterraConfig, "in-feed"), {
    scriptOrigin: "https://bauval.org",
    zone: { height: 250, key: rectangle, kind: "banner", width: 300 },
  });
  // A format with no zone is the normal case for a partially configured
  // account, and must fall through the provider chain rather than render.
  assert.equal(validateAdsterraConfig(adsterraConfig, "rail"), null);
});

test("Adsterra pins the script origin and rejects anything else", () => {
  assert.equal(validateAdsterraConfig({ ...adsterraConfig, script_origin: "https://evil.example" }, "top"), null);
  assert.equal(validateAdsterraConfig({ ...adsterraConfig, script_origin: "javascript:alert(1)" }, "top"), null);
  // Not a prefix match: a lookalike host must not pass.
  assert.equal(validateAdsterraConfig({ ...adsterraConfig, script_origin: "https://bauval.org.evil.example" }, "top"), null);
});

test("Adsterra rejects a zone id that is not 32 hex characters", () => {
  const malformed = (zoneId) => validateAdsterraConfig({
    script_origin: "https://bauval.org",
    zones: { top: { kind: "banner", zone_id: zoneId, width: 728, height: 90 } },
  }, "top");
  assert.equal(malformed("../../evil"), null);
  assert.equal(malformed("'};alert(1);//"), null);
  assert.equal(malformed(leaderboard.toUpperCase()), null);
  assert.equal(malformed(""), null);
});

test("Adsterra requires a bounded box for a banner and none for a native unit", () => {
  const banner = (box) => validateAdsterraConfig({
    script_origin: "https://bauval.org",
    zones: { top: { kind: "banner", zone_id: leaderboard, ...box } },
  }, "top");
  assert.equal(banner({ width: 728 }), null, "a banner with no height must not resolve");
  assert.equal(banner({ width: 0, height: 90 }), null);
  assert.equal(banner({ width: 728, height: 9_999 }), null);
  assert.equal(banner({ width: 728.5, height: 90 }), null);
  assert.deepEqual(validateAdsterraConfig(adsterraConfig, "inline")?.zone, {
    height: 0, key: native, kind: "native", width: 0,
  });
});

test("the Adsterra script URL is built from the pinned origin and the unit kind", () => {
  const banner = validateAdsterraConfig(adsterraConfig, "top");
  const nativeUnit = validateAdsterraConfig(adsterraConfig, "inline");
  assert.equal(adsterraScriptUrl(banner), `https://bauval.org/22/${leaderboard}`);
  assert.equal(adsterraScriptUrl(nativeUnit), `https://bauval.org/21/${native}`);
});

test("an unknown unit kind does not resolve", () => {
  assert.equal(validateAdsterraConfig({
    script_origin: "https://bauval.org",
    zones: { top: { kind: "popunder", zone_id: leaderboard, width: 1, height: 1 } },
  }, "top"), null);
});
