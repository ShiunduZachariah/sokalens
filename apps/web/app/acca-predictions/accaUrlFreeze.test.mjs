import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import path from "node:path";

/**
 * The ACCA URL set is frozen, and this is the net under changes to how the
 * landing page groups its slips.
 *
 * These six URLs are indexed and carry the property's market-family search
 * intent. Regrouping the landing page is a display change and must not move
 * any of them -- but the URL set is declared in two places that cannot see each
 * other, and the failure mode is silent: drop a slug from `accaMarkets.ts` and
 * the route stops being generated while the sitemap keeps advertising it, which
 * is a 404 served to Google with no build error anywhere.
 *
 * If a URL genuinely should change, this test is the place that records the
 * decision -- update the snapshot deliberately, with a redirect in hand.
 */
const webRoot = path.resolve(import.meta.dirname, "../..");

/** The indexed set, as of the ACCA regroup. Verified live on sokalens.com. */
const FROZEN_ACCA_PATHS = [
  "/acca-predictions",
  "/acca-predictions/1x2-accas",
  "/acca-predictions/over-2-5-accas",
  "/acca-predictions/btts-accas",
  "/acca-predictions/corners-accas",
  "/acca-predictions/cards-accas",
];

const markets = await readFile(
  path.join(webRoot, "app/acca-predictions/accaMarkets.ts"),
  "utf8",
);
const sitemap = await readFile(path.join(webRoot, "app/lib/sitemapSources.ts"), "utf8");

const definitionSlugs = [...markets.matchAll(/\{\s*slug:\s*"([^"]+)"/g)].map((m) => m[1]);
const sitemapPaths = [...sitemap.matchAll(/path:\s*"(\/acca-predictions[^"]*)"/g)].map((m) => m[1]);

test("the market-family routes are exactly the frozen set", () => {
  // These drive generateStaticParams, so this is the set of pages that exist.
  assert.deepEqual(
    definitionSlugs.map((slug) => `/acca-predictions/${slug}`),
    FROZEN_ACCA_PATHS.filter((p) => p !== "/acca-predictions"),
  );
});

test("the sitemap advertises exactly the routes that exist", () => {
  // A sitemap entry with no route is a 404 handed to a crawler; a route with no
  // sitemap entry is a page that will not be found. Both are silent.
  assert.deepEqual(sitemapPaths, FROZEN_ACCA_PATHS);
});

test("every market definition still carries what a hub page needs", () => {
  // A slug that survives with an empty title or no market keys renders an
  // indexable page about nothing, which is worse than the page not existing.
  const entries = [...markets.matchAll(/\{\s*slug:\s*"([^"]+)"[\s\S]*?marketKeys:\s*\[([^\]]*)\]/g)];
  assert.equal(entries.length, definitionSlugs.length, "a definition lost its marketKeys");
  for (const [, slug, keys] of entries) {
    assert.ok(keys.trim().length > 0, `${slug} has no market keys`);
  }
  for (const field of ["title", "heading", "description"]) {
    const found = [...markets.matchAll(new RegExp(`${field}:\\s*"([^"]*)"`, "g"))];
    assert.ok(
      found.length >= definitionSlugs.length,
      `${field} is missing from at least one definition`,
    );
    assert.ok(found.every(([, value]) => value.trim().length > 0), `${field} is empty somewhere`);
  }
});
