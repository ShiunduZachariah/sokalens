import { loadTeamUrls } from "../../lib/sitemapSources";
import { sitemapChunk, sitemapPageFrom, urlSet } from "../../lib/sitemapXml";

/**
 * Team pages were in no sitemap at all, despite being 414 of the top 1,000
 * URLs by impressions. Google had to find every one of them by following an
 * internal link, which is why so many sat in "Discovered, currently not
 * indexed".
 */
export async function GET(request: Request) {
  const urls = await loadTeamUrls();
  return urlSet(sitemapChunk(urls, sitemapPageFrom(request, urls.length)));
}
