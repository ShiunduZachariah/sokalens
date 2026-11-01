import { unstable_cache } from "next/cache";
import { loadSeoUrls } from "./seoUrlFeed";
import type { SitemapUrl } from "./sitemapXml";

/**
 * Every URL the sitemaps declare, resolved once and shared.
 *
 * Search Console knew about ~16,930 URLs while the sitemaps declared roughly
 * 1,000 -- about 6% coverage -- and 1,308 pages sat in "Discovered, currently
 * not indexed" as a direct result. Team pages, 414 of the top 1,000 by
 * impressions, appeared in no sitemap at all.
 *
 * The loaders live here rather than in the route handlers because the sitemap
 * index has to know how many chunks each set needs, which means counting the
 * same URLs the chunk documents will emit. Sharing one cached loader keeps the
 * index and its chunks consistent and costs one upstream read, not two.
 */

const loadFixtureUrlsCached = unstable_cache(
  () => loadSeoUrls("fixture"),
  ["sitemap-fixtures-v3"],
  { revalidate: 300 },
);

const loadLeagueUrlsCached = unstable_cache(
  () => loadSeoUrls("league"),
  ["sitemap-leagues-v3"],
  { revalidate: 300 },
);

// Only clubs with a played fixture are listed, and that decision now lives in
// the sweep rather than here: a team page noindexes itself until it has one,
// and a sitemap that declares a noindexed URL is a contradiction the
// seo-invariants gate fails the build over -- correctly, because it is also
// how a property teaches Google to distrust the file.
const loadTeamUrlsCached = unstable_cache(
  () => loadSeoUrls("team"),
  ["sitemap-teams-v3"],
  { revalidate: 300 },
);

// unstable_cache does not de-duplicate concurrent misses, and a cold cache hit
// by several crawler threads at once would fan out the league walk many times.
function once<T>(load: () => Promise<T>) {
  let inFlight: Promise<T> | null = null;
  return () => {
    inFlight ??= load().finally(() => {
      inFlight = null;
    });
    return inFlight;
  };
}

export const loadFixtureUrls = once(loadFixtureUrlsCached);
export const loadLeagueUrls = once(loadLeagueUrlsCached);
export const loadTeamUrls = once(loadTeamUrlsCached);

/**
 * Hand-maintained routes. `lastModified` is deliberately absent rather than
 * set to "now": a `lastmod` that changes on every fetch teaches Google to stop
 * trusting the field across the whole property.
 */
export const staticUrls: SitemapUrl[] = [
  { path: "/" },
  { path: "/football" },
  { path: "/matches/today" },
  { path: "/in-play" },
  { path: "/best-bets" },
  { path: "/acca-predictions" },
  // `/acca-generator` is not listed: the design merges it into the Builder, it
  // now canonicalises to `/multi-match-builder`, and submitting a URL that
  // declares another canonical asks Google to crawl a page in order to be told
  // to ignore it.
  { path: "/acca-predictions/1x2-accas" },
  { path: "/acca-predictions/over-2-5-accas" },
  { path: "/acca-predictions/btts-accas" },
  { path: "/acca-predictions/corners-accas" },
  { path: "/acca-predictions/cards-accas" },
  { path: "/multi-match-builder" },
  { path: "/leagues" },
  { path: "/results" },
  { path: "/about" },
  { path: "/responsible-gambling" },
  { path: "/terms" },
  { path: "/privacy" },
  { path: "/cookies" },
];

export const dateHubUrls: SitemapUrl[] = [
  { path: "/predictions/today" },
  { path: "/predictions/tomorrow" },
  { path: "/predictions/weekend" },
];
