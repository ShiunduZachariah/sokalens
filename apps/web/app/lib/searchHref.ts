import { leaguePredictionPath, matchPath, teamPath } from "./catalogSlug.ts";

/**
 * Where a search hit goes when you click it.
 *
 * This lived twice: once in the /search results page and once in the
 * autocomplete dropdown. The two copies drifted. The results page learned to
 * route `prediction` hits to their match and `acca` hits to their slip; the
 * dropdown never did, so a prediction row in the dropdown fell through to the
 * `/search?q=…` fallback. Clicking a match in the dropdown dumped you on the
 * results page, and you then had to click the same match again to open it —
 * which is exactly what QA reported as "the dropdown doesn't open".
 *
 * One function, both callers. A new result type is now wrong in one place
 * instead of silently wrong in the one nobody tested.
 */
export type SearchHit = {
  type: string;
  id?: number;
  key?: string;
  match_id?: number;
  code?: string;
  name: string;
  slug?: string;
};

export function searchHitHref(hit: SearchHit): string {
  const fallback = `/search?q=${encodeURIComponent(hit.name)}`;
  switch (hit.type) {
    case "team":
      return teamPath(hit.slug) ?? fallback;
    case "league":
      return leaguePredictionPath(hit.slug) ?? fallback;
    case "match":
      return matchPath(hit.id) ?? fallback;
    case "prediction":
      // A prediction is a market on a fixture. The fixture is the thing the
      // user pictured when they clicked, so open it.
      return matchPath(hit.match_id) ?? matchPath(hit.id) ?? fallback;
    case "acca":
      return hit.key ? `/acca-predictions#acca-${encodeURIComponent(hit.key)}` : fallback;
    case "country":
    case "player":
      // Neither has a page of its own yet, so the search results for that name
      // are the closest real destination. Listed explicitly: a type that falls
      // through silently is indistinguishable from one nobody remembered, and
      // that is how the prediction case went missing.
      return fallback;
    default:
      return fallback;
  }
}

/**
 * Whether a hit has a destination of its own, as opposed to bouncing back to
 * the results page. The dropdown uses this to avoid promising a match page it
 * cannot deliver.
 */
export function hasOwnDestination(hit: SearchHit): boolean {
  return !searchHitHref(hit).startsWith("/search?");
}
