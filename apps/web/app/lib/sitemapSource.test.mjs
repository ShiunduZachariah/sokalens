import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const SOURCES = readFileSync(path.resolve(HERE, "sitemapSources.ts"), "utf8");
const FEED = readFileSync(path.resolve(HERE, "seoUrlFeed.ts"), "utf8");

/**
 * The sitemaps must declare what `seo_urls` says is indexable, and nothing
 * else must decide it here.
 *
 * These tests replace an earlier set that guarded a lookback window and a page
 * cap in this file. Both are gone, and so are the defects they were written
 * for -- but the defects are worth recording, because the new arrangement
 * exists to make them unrepresentable rather than merely fixed:
 *
 * The window counted whole days while the page archived on an hour-granular
 * age, so a fixture 14.16 days old was declared and noindexed at the same
 * time. The deploy's own sitemap gate caught it.
 *
 * The loaders then borrowed reader-facing endpoints, whose pagination limits
 * exist for readers and mean nothing to a crawler. 4,462 URLs were declared of
 * 21,377 that qualified: 100 leagues asked for where 845 qualify, teams fanned
 * out from those same 100, fixtures filtered to the 1x2 market alone.
 *
 * Both were silent. A sitemap that is too small looks exactly like a sitemap
 * that is correct, and the feedback arrives weeks later as traffic that never
 * came.
 */

test("every set is loaded from seo_urls, not rebuilt here", () => {
  for (const entity of ["fixture", "league", "team"]) {
    assert.match(
      SOURCES,
      new RegExp(`loadSeoUrls\\("${entity}"\\)`),
      `the ${entity} sitemap no longer reads seo_urls; something else is deciding what is indexable`,
    );
  }
});

test("the capped reader-facing endpoints are gone", () => {
  // Each of these was a ceiling on the sitemap that had nothing to do with
  // whether a page should rank.
  for (const [symbol, why] of [
    ["fetchLeagues", "asked for 100 leagues where 845 qualify"],
    ["fetchLeague(", "fanned out per league, inheriting that ceiling"],
    ["fetchAllTodayPredictionsWithProvider", "filtered fixtures to a single market"],
  ]) {
    assert.ok(
      !SOURCES.includes(symbol),
      `${symbol} is back in the sitemap loaders; it ${why}`,
    );
  }
});

test("no lookback window is computed here any more", () => {
  // Age is decided by the worker's sweep, against the same threshold the page
  // uses. A second opinion in this file is what produced the 14.16-day URL.
  for (const symbol of ["FIXTURE_DAYS_BACK", "sitemapWindow", "SETTLED_INDEXABLE_DAYS"]) {
    assert.ok(
      !SOURCES.includes(symbol),
      `${symbol} is back in sitemapSources.ts; indexability must be decided in one place`,
    );
  }
});

// HTTP failure, pagination and ceiling behavior are exercised against the
// actual loader in seoUrlFeed.test.mjs instead of prescribing source text.

test("lastmod is only sent when the API sends one", () => {
  // A lastmod that appears and disappears between crawls teaches Google to
  // stop trusting the field across the whole property.
  assert.match(
    FEED,
    /typeof item\.last_modified === "string"/,
    "the feed no longer guards lastmod; it may emit an invented or empty value",
  );
});
