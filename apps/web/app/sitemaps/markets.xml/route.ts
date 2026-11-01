import { indexableMarketHubs } from "../../lib/marketInventory";
import { urlSet } from "../../lib/sitemapXml";

/**
 * Hubs with current publications plus the two durable reading guides. The
 * page uses the same predicate, so every declared URL stays indexable.
 */
// Never prerendered. This reads the live catalogue, which does not exist
// during `next build` -- the build container has no prediction API, so
// prerendering either fails the build outright or, worse, succeeds and bakes
// an empty sitemap that tells Google the property has no URLs.
export const dynamic = "force-dynamic";

export async function GET() {
  const hubs = await indexableMarketHubs();
  return urlSet(hubs.map((market) => ({ path: `/predictions/markets/${market.slug}` })));
}
