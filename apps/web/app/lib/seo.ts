import type { LiveMatch } from "../data/liveMatches";
import type { LivePrediction } from "../data/livePredictions";
import { matchPath, validEntityId, validRouteText } from "./catalogSlug.ts";

export type MarketSeoDefinition = {
  slug: string;
  marketKey: string;
  title: string;
  heading: string;
  description: string;
  selection?: string;
};

export const marketSeoDefinitions: MarketSeoDefinition[] = [
  {
    slug: "over-2-5-goals",
    marketKey: "total_goals",
    selection: "over_2.5",
    title: "Over 2.5 Goals Predictions",
    heading: "Over 2.5 goals predictions",
    description: "Data-led Over 2.5 goals predictions, settled results, and current market accuracy from SokaLens.",
  },
  {
    slug: "btts",
    marketKey: "btts",
    title: "Both Teams to Score Predictions",
    heading: "Both teams to score predictions",
    description: "BTTS football predictions with confidence, upcoming fixtures, and transparent settled performance.",
  },
  {
    slug: "match-result",
    marketKey: "1x2",
    title: "Match Result Predictions",
    heading: "Match result predictions",
    description: "Home, draw, and away football predictions with model confidence and settled performance.",
  },
  {
    slug: "corners",
    marketKey: "total_corners",
    title: "Football Corners Predictions",
    heading: "Football corners predictions",
    description: "Upcoming football corners predictions published only when the supporting match data is available.",
  },
  {
    slug: "cards",
    marketKey: "total_cards",
    title: "Football Cards Predictions",
    heading: "Football cards predictions",
    description: "Upcoming football cards predictions with clear availability and transparent settled results.",
  },
  {
    slug: "over-1-5-goals",
    marketKey: "total_goals",
    selection: "over_1.5",
    title: "Over 1.5 Goals Predictions",
    heading: "Over 1.5 goals predictions",
    description: "The lowest goals line SokaLens publishes: fixtures the model expects to produce at least two goals, with the settled hit rate for the line.",
  },
  {
    slug: "under-2-5-goals",
    marketKey: "total_goals",
    selection: "under_2.5",
    title: "Under 2.5 Goals Predictions",
    heading: "Under 2.5 goals predictions",
    description: "Low-scoring fixtures: matches where the model puts more probability on two goals or fewer than on the over. Published with the same settled record as every other line.",
  },
  {
    slug: "over-3-5-goals",
    marketKey: "total_goals",
    selection: "over_3.5",
    title: "Over 3.5 Goals Predictions",
    heading: "Over 3.5 goals predictions",
    description: "High-scoring fixtures. This line clears less often than it feels like it should, so every selection here carries its confidence score and the settled hit rate beside it.",
  },
  {
    slug: "double-chance",
    marketKey: "double_chance",
    title: "Double Chance Predictions",
    heading: "Double chance predictions",
    description: "Two of the three match outcomes in one selection. Lower variance than a straight result call, and priced accordingly. Each selection shows its confidence score, which is not its chance of winning.",
  },
  {
    slug: "half-time-full-time",
    marketKey: "ht_ft",
    title: "Half-Time / Full-Time Predictions",
    heading: "Half-time / full-time predictions",
    description: "The score state at the interval paired with the final result. A narrow market with long prices, published only where the model has enough in-match history to separate the nine combinations.",
  },
  {
    slug: "odd-or-even-goals",
    marketKey: "odd_even_total_goals",
    title: "Odd or Even Goals Predictions",
    heading: "Odd or even goals predictions",
    description: "Whether the combined goal count lands odd or even. Close to a coin flip by construction, which is exactly why the published probability and settled record matter more here than anywhere else.",
  },
  {
    slug: "btts-and-match-result",
    marketKey: "btts_match_result",
    title: "BTTS & Match Result Predictions",
    heading: "Both teams to score and match result",
    description: "A combined selection: who wins, and whether both sides find the net. Published only when the model supports both legs independently.",
  },
  {
    slug: "result-and-total-goals",
    marketKey: "result_total_goals",
    title: "Result & Total Goals Predictions",
    heading: "Result and total goals predictions",
    description: "The match result paired with a goals line. The combination pays more than either leg alone and clears less often -- both figures are published.",
  },
  {
    slug: "half-time-result",
    marketKey: "half_time_result",
    title: "Half-Time Result Predictions",
    heading: "Half-time result predictions",
    description: "The score state at the interval on its own. Roughly a third of matches are level at half-time, so the draw is live here in a way it rarely is at full time.",
  },
  {
    slug: "team-goals",
    marketKey: "team_over_under_goals",
    title: "Team Goals Predictions",
    heading: "Team goals predictions",
    description: "Goals lines for one side rather than the match total, for fixtures where the model separates the two attacks with confidence.",
  },
];

export function seoSlug(value: string) {
  return value
    .normalize("NFKD")
    .replace(/[\u0300-\u036f]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "") || "football";
}

export function seoFixturePath(match: Pick<LiveMatch, "id" | "leagueName" | "homeTeam" | "awayTeam">) {
  return `/predictions/${seoSlug(match.leagueName)}/${seoSlug(match.homeTeam)}-vs-${seoSlug(match.awayTeam)}-${match.id}`;
}

export function predictionFixturePath(prediction: Pick<LivePrediction, "matchId" | "leagueName" | "homeTeam" | "awayTeam">) {
  if (!validEntityId(prediction.matchId) || !validRouteText(prediction.leagueName) || !validRouteText(prediction.homeTeam) || !validRouteText(prediction.awayTeam)) {
    return "/predictions/today";
  }
  return seoFixturePath({
    id: prediction.matchId,
    leagueName: prediction.leagueName,
    homeTeam: prediction.homeTeam,
    awayTeam: prediction.awayTeam,
  });
}

export function matchPredictionSharePath(matchId: number | string, market: string, selection: string) {
  const path = matchPath(matchId);
  if (!path || !validRouteText(market) || !validRouteText(selection)) return "/matches/today";
  const marketSlug = seoSlug(market);
  const selectionSlug = seoSlug(selection);
  return `${path}?market=${encodeURIComponent(marketSlug)}&pick=${encodeURIComponent(selectionSlug)}#prediction-${marketSlug}-${selectionSlug}`;
}

export function seoLeaguePath(slug: string) {
  return `/predictions/leagues/${slug}`;
}

export function marketSeoDefinition(slug: string) {
  return marketSeoDefinitions.find((market) => market.slug === slug) ?? null;
}

/**
 * Whether a market hub has anything to rank for.
 *
 * The five original hubs had the best click-through on the property (4.7%),
 * which is the whole argument for having more of them -- but a hub with no
 * published selections is a heading and an empty state, and shipping ten of
 * those is how a site acquires ten thin pages instead of ten landing pages.
 *
 * Most hubs are indexable only while the engine publishes into them. Over 2.5
 * and BTTS have durable reading guides and keep serving useful market context
 * between publication cycles. `markets.xml` includes those two permanently
 * and only other hubs with observed inventory; it never lists a noindex URL.
 *
 * `null` means the upstream read failed and the count is unknown, and unknown
 * is treated as indexable on purpose. Answering `noindex` because an API call
 * timed out is how a transient outage becomes a deindexed page: a page wrongly
 * left indexable is corrected by the next crawl, while one wrongly noindexed
 * has to be rediscovered. This exact thing happened -- the prediction API was
 * failing 56% of requests, the inventory read caught the error as zero, and
 * six hubs went noindex while remaining in the sitemap.
 */
export function evergreenMarketHub(slug: string) {
  // These two pages have durable, market-specific reading guides. They remain
  // useful when the current prediction feed is empty or temporarily unavailable.
  return slug === "over-2-5-goals" || slug === "btts";
}

export function marketHubIsIndexable(publishedSelections: number | null, slug?: string) {
  if (slug && evergreenMarketHub(slug)) return true;
  if (publishedSelections === null) return true;
  return publishedSelections > 0;
}

export function marketSeoPath(marketKey: string, selection?: string) {
  const definition = marketSeoDefinitions.find((market) =>
    market.marketKey === marketKey && (!market.selection || market.selection === selection),
  );
  return definition ? `/predictions/markets/${definition.slug}` : null;
}

export function fixtureIdFromSeoSlug(value: string) {
  const match = value.match(/-(\d+)$/);
  if (!match) return null;
  const id = Number(match[1]);
  return Number.isInteger(id) && id > 0 ? id : null;
}

/**
 * Splits a market heading into the part drawn in body ink and the trailing word
 * drawn in accent ink.
 *
 * The market hub renders `title` followed by `accent`, and it passed the whole
 * heading as the title while also passing a literal "predictions" accent. For
 * the fourteen headings that already end in that word the page rendered
 * "Over 2.5 goals predictions predictions" -- visible in the h1 on every market
 * hub, while the <title> was correct, which is why it survived.
 *
 * Not a blanket strip: "Both teams to score and match result" does NOT end in
 * "predictions", and for that one the accent is what completes the phrase. So
 * the word is removed only when it is already there, and supplied as the accent
 * either way.
 */
export function marketHeadingParts(heading: string): { title: string; accent: string } {
  const suffix = " predictions";
  return heading.toLowerCase().endsWith(suffix)
    ? { title: heading.slice(0, -suffix.length), accent: "predictions" }
    : { title: heading, accent: "predictions" };
}
