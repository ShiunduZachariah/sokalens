import { unstable_cache } from "next/cache";
import { fetchMarketPredictions } from "../data/livePredictions";
import { evergreenMarketHub, marketSeoDefinitions } from "./seo";
import { DEFAULT_VIEWER_TIMEZONE } from "./viewerTimezone.ts";

/**
 * How many selections each market hub currently has published, or `null` when
 * the upstream read failed and we genuinely do not know.
 *
 * Two callers need this and must agree: the hub itself, deciding whether to be
 * indexable, and `markets.xml`, deciding whether to declare it. If they
 * disagree the sitemap advertises a noindexed URL, which teaches Google to
 * discount the whole sitemap, not just that row.
 *
 * The distinction between "zero" and "unknown" is the whole point of this
 * file. It used to `.catch(() => [])`, so an upstream failure became a count
 * of zero, the hub emitted `noindex`, and `unstable_cache` pinned that answer
 * for ten minutes. The prediction API was failing 56% of requests at the time,
 * which means transient infrastructure trouble was quietly instructing Google
 * to drop pages that were perfectly fine -- and the post-deploy sitemap check
 * caught it as six hubs in the sitemap answering noindex.
 *
 * So a failed read is `null`, and null resolves in the direction that cannot
 * cost an indexed page:
 *
 *   - the hub stays indexable, because a page wrongly left indexable recovers
 *     on the next crawl, while one wrongly noindexed has to be rediscovered;
 *   - the sitemap omits it, because declaring a URL we cannot vouch for is the
 *     one failure mode that damages the whole file.
 *
 * Those two are deliberately asymmetric and still consistent: a hub may be
 * indexable without being declared, never the reverse.
 *
 * One upstream read per market, cached for ten minutes. Inventory changes with
 * the publish cycle, not with page views.
 */
const SITEMAP_TIMEZONE = DEFAULT_VIEWER_TIMEZONE;

export type MarketInventory = Record<string, number | null>;

/**
 * The last counts that were actually observed, per market.
 *
 * A failed read falls back to the last real answer before it falls back to
 * "unknown", so a blip does not discard inventory the process already knows.
 * Module state, so it is lost on restart -- which is correct: after a restart
 * we genuinely have not observed anything yet.
 */
const lastObserved: Record<string, number> = {};

const loadMarketInventoryCached = unstable_cache(async (): Promise<MarketInventory> => {
  const counts: MarketInventory = {};
  for (const definition of marketSeoDefinitions) {
    const predictions = await fetchMarketPredictions(definition.marketKey, undefined, SITEMAP_TIMEZONE)
      .catch(() => null);
    if (predictions === null) {
      counts[definition.slug] = lastObserved[definition.slug] ?? null;
      continue;
    }
    const count = predictions.filter((item) => !definition.selection || item.selection === definition.selection).length;
    lastObserved[definition.slug] = count;
    counts[definition.slug] = count;
  }
  return counts;
}, ["market-hub-inventory-v2"], { revalidate: 600 });

let inventoryInFlight: Promise<MarketInventory> | null = null;

function loadMarketInventory() {
  inventoryInFlight ??= loadMarketInventoryCached().finally(() => {
    inventoryInFlight = null;
  });
  return inventoryInFlight;
}

/** The count for one hub, or `null` when the upstream read failed. */
export async function marketHubInventory(slug: string): Promise<number | null> {
  const counts = await loadMarketInventory();
  return slug in counts ? counts[slug] : null;
}

/** Current hubs, plus the two with durable guides that survive an empty feed. */
export async function indexableMarketHubs() {
  const counts = await loadMarketInventory();
  return marketSeoDefinitions.filter((definition) => {
    const count = counts[definition.slug];
    return evergreenMarketHub(definition.slug) || (typeof count === "number" && count > 0);
  });
}
