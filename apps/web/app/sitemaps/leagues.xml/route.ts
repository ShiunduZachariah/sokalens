import { loadLeagueUrls } from "../../lib/sitemapSources";
import { sitemapChunk, sitemapPageFrom, urlSet } from "../../lib/sitemapXml";

export async function GET(request: Request) {
  const urls = await loadLeagueUrls();
  return urlSet(sitemapChunk(urls, sitemapPageFrom(request, urls.length)));
}
