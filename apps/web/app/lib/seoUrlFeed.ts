import { fetchPredictionApi } from "../data/apiClient";
import type { SitemapUrl } from "./sitemapXml";

/**
 * The crawlable URL set, read from `seo_urls` via the API.
 *
 * This replaces three loaders that assembled sitemaps out of reader-facing
 * endpoints. Those endpoints carry pagination limits that exist for readers
 * and mean nothing to a crawler, and the result was a sitemap declaring a
 * fifth of the property:
 *
 *                 declared   actually indexable
 *   fixtures         3,152                7,626
 *   teams            1,194               12,906
 *   leagues             82                  845
 *                    -----               ------
 *                    4,462               21,377
 *
 * The league loader asked for 100 leagues and got 82 with content, of 845 that
 * qualify. The team loader fanned out over those same 100 and inherited the
 * ceiling. The fixture loader filtered to the 1x2 market, so a fixture
 * predicted in any other market was invisible to it.
 *
 * None of those numbers encoded a decision about what should rank. The worker
 * already derives that answer directly from the database every fifteen
 * minutes, with no cap, and this reads it.
 */

const PAGE_SIZE = 10000;

// A page count, not a URL count. Twelve thousand teams is two pages; the bound
// exists so a bug in `total` cannot spin this loop forever, not to limit the
// sitemap. Exceeding it must fail instead of publishing a truncated document.
const MAX_PAGES = 12;

type SeoUrlItem = {
  path?: unknown;
  last_modified?: unknown;
};

type SeoUrlResponse = {
  total?: unknown;
  urls?: unknown;
};

/**
 * Load every indexable path of one entity type.
 *
 * Reject unavailable or incomplete inventories. The routes propagate this as
 * a server error, and the shared cache can retain its previous successful set
 * instead of caching an outage as a deliberate removal of all URLs.
 */
export async function loadSeoUrls(entityType: string): Promise<SitemapUrl[]> {
  const collected: SitemapUrl[] = [];
  let offset = 0;
  let expectedTotal: number | undefined;

  for (let page = 0; page < MAX_PAGES; page += 1) {
    const response = await fetchPredictionApi(
      `/v1/seo/urls?type=${encodeURIComponent(entityType)}&limit=${PAGE_SIZE}&offset=${offset}`,
      // Matches the worker's fifteen-minute sweep closely enough that a newly
      // published fixture reaches the sitemap within one cycle, without
      // rebuilding a 12,906-row document for every crawler thread.
      { revalidate: 300 },
    );
    if (!response.ok) {
      throw new Error(`[sitemap] ${entityType} feed returned HTTP ${response.status} at offset ${offset}`);
    }

    const payload = (await response.json()) as SeoUrlResponse;
    if (!payload || !Array.isArray(payload.urls) ||
        typeof payload.total !== "number" || !Number.isSafeInteger(payload.total) || payload.total < 0) {
      throw new Error(`[sitemap] ${entityType} feed returned an invalid inventory`);
    }
    const total = payload.total;
    expectedTotal ??= total;
    const items = payload.urls;
    if (total !== expectedTotal || items.length !== Math.min(PAGE_SIZE, total - offset)) {
      throw new Error(`[sitemap] ${entityType} feed returned an incomplete or changing inventory`);
    }
    for (const item of items as SeoUrlItem[]) {
      if (!item || typeof item.path !== "string" || item.path === "") {
        throw new Error(`[sitemap] ${entityType} feed returned an invalid URL`);
      }
      const url: SitemapUrl = { path: item.path };
      // Only when the API sends one. A lastmod that appears and disappears
      // between crawls teaches Google to stop trusting the field.
      if (typeof item.last_modified === "string" && item.last_modified !== "") {
        url.lastModified = item.last_modified;
      }
      collected.push(url);
    }

    offset += PAGE_SIZE;
    if (collected.length === total) {
      return collected;
    }
  }

  // Reaching the ceiling means the set outgrew MAX_PAGES and the sitemap is
  // now incomplete -- the exact condition this replacement exists to end.
  throw new Error(`[sitemap] ${entityType} hit the page ceiling at ${collected.length} URLs; raise MAX_PAGES`);
}
