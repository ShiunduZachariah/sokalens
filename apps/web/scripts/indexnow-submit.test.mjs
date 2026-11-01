import assert from "node:assert/strict";
import test from "node:test";

import {
  batchUrls, buildPayload, discoverUrls, extractEntries, extractLocations,
  INDEXNOW_KEY, MAX_BATCH_URLS, selectRecentUrls,
} from "./indexnow-submit.mjs";

test("extractLocations reads sitemap index and URL-set locations", () => {
  assert.deepEqual(extractLocations("<urlset><url><loc>https://sokalens.com/a&amp;b</loc></url></urlset>"), ["https://sokalens.com/a&b"]);
});

test("discoverUrls follows owned child sitemaps and deduplicates URLs", async () => {
  const responses = new Map([
    ["https://sokalens.com/sitemap.xml", "<sitemapindex><sitemap><loc>https://sokalens.com/sitemaps/a.xml</loc></sitemap></sitemapindex>"],
    ["https://sokalens.com/sitemaps/a.xml", "<urlset><url><loc>https://sokalens.com/predictions/today</loc></url><url><loc>https://sokalens.com/predictions/today</loc></url></urlset>"],
  ]);
  const fetchImpl = async (url) => ({ ok: responses.has(url), status: responses.has(url) ? 200 : 404, text: async () => responses.get(url) ?? "" });
  assert.deepEqual(await discoverUrls({ fetchImpl }), ["https://sokalens.com/", "https://sokalens.com/predictions/today"]);
});

test("buildPayload binds the public key to the SokaLens host", () => {
  const payload = buildPayload(["https://sokalens.com/predictions/today"]);
  assert.equal(payload.host, "sokalens.com");
  assert.equal(payload.key, INDEXNOW_KEY);
  assert.equal(payload.keyLocation, `https://sokalens.com/${INDEXNOW_KEY}.txt`);
});

test("buildPayload rejects off-domain and insecure URLs", () => {
  assert.throws(() => buildPayload(["https://example.com/page"]), /outside sokalens.com/);
  assert.throws(() => buildPayload(["http://sokalens.com/page"]), /outside sokalens.com/);
});

test("a scheduled run announces only what changed inside the window", () => {
  // A prediction published between deploys used to wait for the next release
  // before Bing heard about it. A fixture's search demand is a ~72-hour window
  // that closes at kickoff, so that gap was the whole opportunity.
  const now = Date.parse("2026-09-07T12:00:00Z");
  const entries = [
    { loc: "https://sokalens.com/predictions/arsenal-vs-chelsea-1", lastmod: "2026-09-07T11:40:00Z" },
    { loc: "https://sokalens.com/predictions/old-vs-stale-2", lastmod: "2026-09-01T09:00:00Z" },
    { loc: "https://sokalens.com/terms", lastmod: null },
  ];

  assert.deepEqual(selectRecentUrls(entries, { now, sinceHours: 48 }), [
    "https://sokalens.com/predictions/arsenal-vs-chelsea-1",
  ]);
  // A deploy can change any page's markup, so it sweeps everything -- including
  // the URLs that carry no lastmod at all.
  assert.deepEqual(selectRecentUrls(entries, { now, sinceHours: 0 }), entries.map((entry) => entry.loc));
});

test("a full sweep is batched rather than rejected at the protocol cap", () => {
  // The sitemaps now declare the whole property instead of ~6% of it. The old
  // code threw above 10,000 URLs, which would have turned a coverage win into
  // a red deploy step.
  const urls = Array.from({ length: 23_000 }, (_, index) => `https://sokalens.com/p/${index}`);
  const batches = batchUrls(urls);

  assert.equal(batches.length, 3);
  assert.equal(batches[0].length, MAX_BATCH_URLS);
  assert.equal(batches.at(-1).length, 3_000);
  assert.deepEqual(batches.flat(), urls);
});

test("lastmod is read back alongside each location", () => {
  const entries = extractEntries(
    "<urlset><url><loc>https://sokalens.com/a</loc><lastmod>2026-09-07T10:00:00.000Z</lastmod></url>" +
    "<url><loc>https://sokalens.com/b</loc></url></urlset>",
  );

  assert.deepEqual(entries, [
    { loc: "https://sokalens.com/a", lastmod: "2026-09-07T10:00:00.000Z" },
    { loc: "https://sokalens.com/b", lastmod: null },
  ]);
});
