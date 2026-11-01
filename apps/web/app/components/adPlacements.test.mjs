import assert from "node:assert/strict";
import test from "node:test";
import { readdir, readFile } from "node:fs/promises";
import path from "node:path";

const appDir = path.resolve(import.meta.dirname, "..");
const opsDoc = path.resolve(appDir, "../../../docs/operations/ad-provider-routing.md");

/*
 * Every placement the app asks for must be one someone has configured.
 *
 * `AdSlot` resolves `/api/ads/placements/<id>` at runtime, and the routing layer
 * fails closed: an id with no AdSense slot reports INVALID_ZONE and the slot
 * renders nothing. That is the right behaviour -- it is what keeps an unfilled
 * slot from leaving a bordered box behind -- and it is also why this gap is
 * invisible. A page with four slots and no routing rows looks exactly like a
 * page whose ads simply did not fill, on every device, forever.
 *
 * The 2026-10-01 refresh took the app from 19 placements to 40 while the ops doc
 * still configured three. Nothing failed; the pages just earned nothing. So the
 * doc and the code are pinned to each other here: add a slot and this test fails
 * until the id is written down for whoever creates the AdSense unit.
 */

async function sourceFiles(dir) {
  const entries = await readdir(dir, { recursive: true });
  return entries
    .map((entry) => entry.replaceAll("\\", "/"))
    .filter((entry) => entry.endsWith(".tsx") && !entry.includes(".test."))
    .map((entry) => path.join(dir, entry));
}

/**
 * The placement ids the app can request.
 *
 * There are six, and that is the point. The 2026-10-01 refresh produced 39 by
 * templating an id per page (`leagues-top`, `team-rail`, ...), which meant 39
 * AdSense units to create by hand before any of those pages earned anything.
 * They collapsed onto ids named for the POSITION rather than the page, because
 * AdSense allows one ad unit to be rendered many times on a page -- so the
 * dynamic feed needs one unit, not one per position.
 *
 * The trade is real and deliberate: AdSense now reports these as six units, so
 * revenue cannot be split between the Builder's feed and Today's at the network
 * level. Page attribution comes from the site's own analytics instead.
 */
async function usedPlacements() {
  const literals = new Set();
  for (const file of await sourceFiles(appDir)) {
    const source = await readFile(file, "utf8");
    for (const match of source.matchAll(/placement="([a-z0-9-]+)"/g)) literals.add(match[1]);
  }
  assert.ok(literals.size > 0, "no placements found at all; the attribute spelling changed");

  // A templated placement is how the 39 happened. If one reappears, the set above
  // is no longer the whole truth and this test would silently cover a fraction of
  // what the app requests.
  for (const file of await sourceFiles(appDir)) {
    const source = await readFile(file, "utf8");
    assert.ok(
      !/placement=\{`/.test(source),
      `${file} builds a placement id from a template; ids must be literal so they can be enumerated and configured`,
    );
  }
  return literals;
}

/** The ids listed in the ops doc's inventory table. */
async function documentedPlacements() {
  const doc = await readFile(opsDoc, "utf8");
  const section = doc.slice(doc.indexOf("## Placement inventory"));
  assert.ok(section.length > 0, "the ops doc has no '## Placement inventory' section");
  return new Set([...section.matchAll(/^\| `([a-z0-9-]+)` \|/gm)].map((match) => match[1]));
}

test("every ad placement the app requests is documented for configuration", async () => {
  const used = await usedPlacements();
  const documented = await documentedPlacements();

  const undocumented = [...used].filter((id) => !documented.has(id)).sort();
  assert.deepEqual(
    undocumented,
    [],
    `these placements are requested by the app but not listed in docs/operations/ad-provider-routing.md, so nobody will create an AdSense unit for them and they will serve nothing:\n  ${undocumented.join("\n  ")}`,
  );
});

test("the documented inventory has no placements the app never requests", async () => {
  const used = await usedPlacements();
  const documented = await documentedPlacements();

  // A stale entry is a slot id someone may create and pay attention to for a
  // placement no page asks for.
  const unused = [...documented].filter((id) => !used.has(id)).sort();
  assert.deepEqual(
    unused,
    [],
    `these placements are documented but no longer requested by any page:\n  ${unused.join("\n  ")}`,
  );
});
