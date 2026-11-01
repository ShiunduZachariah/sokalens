import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("root layout never loads an ad provider before eligibility resolves", async () => {
  const source = await readFile(new URL("../layout.tsx", import.meta.url), "utf8");
  assert.doesNotMatch(source, /pagead2\.googlesyndication\.com/);
  assert.doesNotMatch(source, /adsbygoogle\.js/);
});

test("third-party adapters are selected only through the reviewed registry", async () => {
  const source = await readFile(new URL("./ads/providerRegistry.tsx", import.meta.url), "utf8");
  assert.doesNotMatch(source, /dangerouslySetInnerHTML|eval\s*\(|new Function/);
  // Every adapter is a module imported at build time. A provider whose
  // adapter were resolved from configuration -- a URL, a component name, a
  // dynamic import of a stored string -- is the stored-XSS shape this whole
  // registry exists to prevent.
  for (const [, key, adapter] of source.matchAll(/^\s*([a-z0-9_-]+): ([A-Za-z]+),$/gm)) {
    assert.match(adapter, /^(UnsupportedProvider|[A-Z][A-Za-z]*Unit)$/,
      `provider "${key}" must map to an imported adapter component`);
    if (adapter !== "UnsupportedProvider") {
      assert.match(source, new RegExp(`import \{ ${adapter} \} from "\.`),
        `${adapter} must be imported from this repository, not resolved at runtime`);
    }
  }
  // Monetag holds an ownership meta tag and no zone. Until a zone exists it
  // must fail closed: a provider in the chain that cannot fill spends the
  // slot's whole timeout budget rendering nothing.
  assert.match(source, /monetag:\s*UnsupportedProvider/);
});

/*
 * Adsterra is now wired, which moves the burden from "it is off" to "what it
 * loads is pinned". Three things carry that, and each has already been the
 * defect in somebody's ad integration: a script origin taken from the
 * database, a zone key interpolated into markup, and a config read that
 * bypasses validation.
 */
test("the Adsterra adapter builds its script URL from pinned data only", async () => {
  const adapter = await readFile(new URL("./AdsterraUnit.tsx", import.meta.url), "utf8");
  const delivery = await readFile(new URL("../lib/adDelivery.ts", import.meta.url), "utf8");

  assert.doesNotMatch(adapter, /dangerouslySetInnerHTML|eval\s*\(|new Function/);
  // The only read of provider configuration is through the validator.
  const configReads = [...adapter.matchAll(/attempt\.publicConfig/g)];
  assert.equal(configReads.length, 1, "publicConfig must be read once, through the validator");
  assert.match(adapter, /const zoneFormat = format === "top" && compactTop \? "adhesion" : format;/);
  assert.match(adapter, /validateAdsterraConfig\(attempt\.publicConfig, zoneFormat\)/);
  // The URL is assembled in one reviewed place, from the origin allowlist and
  // the kind-to-path table -- never from a stored path or URL.
  assert.match(adapter, /adsterraScriptUrl\(config\)/);
  assert.doesNotMatch(adapter, /script_origin|zones\[/);
  assert.match(delivery, /ADSTERRA_SCRIPT_ORIGINS = new Set\(\[/);
  assert.match(delivery, /ADSTERRA_SCRIPT_ORIGINS\.has\(value\.script_origin\)/);
  assert.match(delivery, /\/\^\[0-9a-f\]\{32\}\$\//, "zone keys must be pinned to 32 hex characters");
});

test("the Adsterra adapter refuses a second mount of a keyed unit", async () => {
  const adapter = await readFile(new URL("./AdsterraUnit.tsx", import.meta.url), "utf8");
  // Config is meant to keep keyed units on single-render formats; this is the
  // belt. Without it a duplicate renders nothing and only shows up as a slot
  // that quietly timed out.
  assert.match(adapter, /claimedSingletons/);
  assert.match(adapter, /if \(config\.zone\.kind !== "banner"\)/);
  assert.match(adapter, /claimedSingletons\.has\(config\.zone\.key\)/);
  assert.match(adapter, /failureReason: "INVALID_ZONE"/);
  // And it must be released, or a client-side navigation back to the page
  // would find the zone permanently claimed.
  assert.match(adapter, /claimedSingletons\.delete\(config\.zone\.key\)/);
});

test("Adsterra starts a slot's timer only when its queued script can run", async () => {
  const slot = await readFile(new URL("./AdSlot.tsx", import.meta.url), "utf8");
  const adapter = await readFile(new URL("./AdsterraUnit.tsx", import.meta.url), "utf8");
  assert.match(slot, /attempt\.adapterKey !== "adsterra"\) startAttempt/);
  assert.match(slot, /onStart=\{\(\) => startAttempt\(attempt\.attemptToken\)\}/);
  const queueTurn = adapter.indexOf("const run = async () => {");
  const start = adapter.indexOf("onStart();", queueTurn);
  const append = adapter.indexOf("host.append(script);", queueTurn);
  assert.ok(queueTurn >= 0 && start > queueTurn && append > start,
    "the banner must arm its timer after leaving the queue and before starting its script");
  assert.match(adapter, /bannerQueue = bannerQueue\.then\(run, run\)/);
});
