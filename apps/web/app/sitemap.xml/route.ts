import { loadFixtureUrls, loadLeagueUrls, loadTeamUrls } from "../lib/sitemapSources";
import { sitemapIndex, sitemapPageCount, type SitemapIndexEntry, type SitemapUrl } from "../lib/sitemapXml";

/**
 * The sitemap index.
 *
 * It counts the URLs each dynamic set holds so the chunk documents can be
 * enumerated. The counts come from the same cached loaders the chunks read, so
 * the index never advertises a page the chunk route would answer empty.
 */
/**
 * The newest real change in a set, or undefined when the set carries no
 * timestamps.
 *
 * Deliberately not `now()`. A lastmod that moves on every generation is a lie
 * Google detects, and once detected it discounts the field across the whole
 * property -- which costs more than having no lastmod at all. Sets whose
 * entries have no genuine timestamp (static pages, league hubs) therefore get
 * no lastmod rather than an invented one.
 */
function newestLastModified(urls: SitemapUrl[]): string | undefined {
  let newest: number | undefined;
  for (const url of urls) {
    if (!url.lastModified) continue;
    const parsed = Date.parse(url.lastModified);
    if (Number.isNaN(parsed)) continue;
    if (newest === undefined || parsed > newest) newest = parsed;
  }
  return newest === undefined ? undefined : new Date(newest).toISOString();
}

function pages(path: string, total: number, lastModified?: string): SitemapIndexEntry[] {
  const count = sitemapPageCount(total);
  if (count === 1) return [{ path, lastModified }];
  return Array.from({ length: count }, (_, index) => ({ path: `${path}?page=${index + 1}`, lastModified }));
}

// Never prerendered. This reads the live catalogue, which does not exist
// during `next build` -- the build container has no prediction API, so
// prerendering either fails the build outright or, worse, succeeds and bakes
// an empty sitemap that tells Google the property has no URLs.
export const dynamic = "force-dynamic";

export async function GET() {
  const [fixtures, leagues, teams] = await Promise.all([loadFixtureUrls(), loadLeagueUrls(), loadTeamUrls()]);
  return sitemapIndex([
    { path: "/sitemaps/static.xml" },
    { path: "/sitemaps/date-hubs.xml" },
    { path: "/sitemaps/markets.xml" },
    ...pages("/sitemaps/leagues.xml", leagues.length, newestLastModified(leagues)),
    ...pages("/sitemaps/teams.xml", teams.length, newestLastModified(teams)),
    ...pages("/sitemaps/fixtures.xml", fixtures.length, newestLastModified(fixtures)),
  ]);
}
