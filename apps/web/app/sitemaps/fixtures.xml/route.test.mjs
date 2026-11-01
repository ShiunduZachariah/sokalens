import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("fixture sitemap uses the lightweight canonical prediction feed", async () => {
  const [route, sources] = await Promise.all([
    readFile(new URL("./route.ts", import.meta.url), "utf8"),
    readFile(new URL("../../lib/sitemapSources.ts", import.meta.url), "utf8"),
  ]);

  // The route is now a thin chunker; the feed itself lives in the shared
  // source module so the sitemap index can count the same URLs the chunks
  // emit, from one upstream read rather than two.
  assert.match(route, /loadFixtureUrls/);
  assert.match(route, /sitemapChunk\(urls, sitemapPageFrom\(request, urls\.length\)\)/);
  assert.match(route, /s-maxage=300, must-revalidate/);
  assert.doesNotMatch(route, /stale-while-revalidate/);

  // The feed now comes from seo_urls rather than the prediction endpoint.
  //
  // The old assertions pinned that upstream read, and pinning it was what
  // kept the ceiling in place: the prediction feed is paginated for readers,
  // and filtering it to the 1x2 market meant a fixture predicted in any other
  // market never reached the sitemap. 3,152 fixtures were declared where
  // 7,626 qualified.
  assert.match(sources, /loadSeoUrls\("fixture"\)/);
  assert.doesNotMatch(sources, /fetchAllTodayPredictionsWithProvider/);
  assert.doesNotMatch(sources, /fetchAllMatches/);
  // The day-by-day fan-out is gone with it: one request replaces ~21.
  assert.doesNotMatch(sources, /sitemapWindow/);
  assert.match(sources, /import \{ unstable_cache \} from "next\/cache"/);
  assert.match(sources, /const loadFixtureUrlsCached = unstable_cache/);
  assert.match(sources, /revalidate: 300/);
});

test("the fixture sitemap holds no opinion about the indexable window", async () => {
  const sources = await readFile(new URL("../../lib/sitemapSources.ts", import.meta.url), "utf8");

  // This used to assert the lookback equalled the retirement constant,
  // because the two had been chosen independently and drifted -- the sitemap
  // reached back 30 days while the page stopped indexing at 14.
  //
  // Tying them together narrowed the gap without closing it: the window
  // counted whole days and the page archived on an hour-granular age, so a
  // fixture 14.16 days old was declared and noindexed at the same time. The
  // deploy gate caught that one.
  //
  // Now the window does not exist here. Age is decided once, in the worker
  // sweep, against the same threshold the page uses, and this file only reads
  // the answer. The invariant is therefore the absence of a second opinion.
  for (const symbol of ["FIXTURE_DAYS_BACK", "FIXTURE_DAYS_FORWARD", "SETTLED_INDEXABLE_DAYS", "sitemapWindow"]) {
    assert.ok(
      !sources.includes(symbol),
      `${symbol} is back in sitemapSources.ts; indexability must be decided in one place`,
    );
  }
});

test("every sitemap set is reachable from the index", async () => {
  const index = await readFile(new URL("../../sitemap.xml/route.ts", import.meta.url), "utf8");

  for (const name of ["static", "date-hubs", "markets", "leagues", "teams", "fixtures"]) {
    assert.match(index, new RegExp(`/sitemaps/${name}\.xml`), `${name}.xml missing from the sitemap index`);
  }
  // Team pages were 414 of the top 1,000 URLs by impressions and appeared in
  // no sitemap at all.
  assert.match(index, /loadTeamUrls/);
  assert.match(index, /sitemapPageCount/);
});
