import { loadFixtureUrls } from "../../lib/sitemapSources";
import { sitemapChunk, sitemapPageFrom, urlSet } from "../../lib/sitemapXml";

export async function GET(request: Request) {
  const urls = await loadFixtureUrls();
  const response = urlSet(sitemapChunk(urls, sitemapPageFrom(request, urls.length)));
  // Prediction eligibility and canonical team names can change between worker
  // cycles. Never serve the stale fixture sitemap for an additional hour.
  response.headers.set("Cache-Control", "public, max-age=0, s-maxage=300, must-revalidate");
  return response;
}
