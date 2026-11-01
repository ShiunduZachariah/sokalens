import { mergeByMatchId } from "../lib/mergePredictions";
import { fetchPredictionApi } from "./apiClient";
import { marketFeedDates } from "./marketFeedDates";
import { HIDDEN_PUBLIC_MARKETS, marketLabel, marketSelectionLabel, selectionLabel } from "../lib/markets";

import { validEntityId, validRouteText } from "../lib/catalogSlug.ts";
import { DEFAULT_VIEWER_TIMEZONE } from "../lib/viewerTimezone.ts";

/**
 * Upper bound on sequential pages when building a whole-day candidate pool.
 *
 * Five was written when "a busy Saturday is three pages of 100". That stopped
 * being true: on 2026-10-03 production carried 1,344 fixtures for the day and
 * 1,439 of them had predictions, so five pages showed **501** and the Today
 * page silently dropped nine hundred predicted matches. The stat tile read
 * "Predictions 501" beside "Fixtures 1344" and both numbers were honest about
 * what the page held -- the page was simply holding a third of the day.
 *
 * Sixteen covers 1,600 predictions, which is comfortably past the largest day
 * observed, and it stays a bound rather than a licence: a nonsense `total` from
 * the API still cannot fan out forever.
 *
 * It costs latency, and that is the trade being made deliberately. Paging is
 * sequential on purpose -- a test pins it, because a wide day fanned out in
 * parallel stampedes PostgreSQL -- so a full day is now up to sixteen
 * round-trips rather than five on the busiest page of the site. The API caps
 * `limit` at 100 server-side, so fewer, larger pages is not available without
 * a backend change.
 */
const PREDICTION_POOL_MAX_PAGES = 16;

export { HIDDEN_PUBLIC_MARKETS, marketLabel, marketSelectionLabel, selectionLabel };

type TodayPredictionsResponse = {
  total?: number;
  page?: number;
  limit?: number;
  predictions?: Array<{
    id?: number;
    match_id?: number;
    match_title?: string;
    home_team?: string;
    away_team?: string;
    home_team_logo?: string;
    away_team_logo?: string;
    league_name?: string;
    country?: string;
    kickoff_at?: string;
    match_status?: string;
    market_key?: string;
    selection?: string;
    confidence?: number;
    probability?: number;
    is_best_bet?: boolean;
    published_version?: number;
    published_at?: string;
    selection_odds?: string;
    markets?: Array<{ market_key?: string; selection?: string; confidence?: number; probability?: number; is_best_bet?: boolean }>;
  }>;
  provider_predictions?: Array<{
    match_id?: number;
    match_title?: string;
    home_team?: string;
    away_team?: string;
    home_team_logo?: string;
    away_team_logo?: string;
    league_name?: string;
    country?: string;
    kickoff_at?: string;
    match_status?: string;
    predicted_winner?: string;
    win_or_draw?: boolean;
    under_over?: string;
    percent_home?: number;
    percent_draw?: number;
    percent_away?: number;
    advice?: string;
    market_key?: string;
    selection?: string;
    confidence?: number;
    published_version?: number;
    published_at?: string;
  }>;
};

export type LivePrediction = {
  id: number;
  matchId: number;
  matchTitle: string;
  homeTeam: string;
  awayTeam: string;
  homeTeamLogo: string;
  awayTeamLogo: string;
  leagueName: string;
  country: string;
  kickoffAt: string;
  matchStatus: string;
  marketKey: string;
  selection: string;
  confidence: number;
  probability: number;
  isBestBet: boolean;
  publishedVersion: number;
  publishedAt: string;
  selectionOdds: number | null;
  markets: LivePredictionMarket[];
};

export type LivePredictionMarket = {
  marketKey: string;
  selection: string;
  confidence: number;
  probability: number;
  isBestBet: boolean;
};

export type ProviderLivePrediction = {
  matchId: number;
  matchTitle: string;
  homeTeam: string;
  awayTeam: string;
  /**
   * Crest URLs, empty when the team has no logo on record.
   *
   * Provider fallback rows used to carry none at all, so every one of them fell
   * back to the drawn initials shield beside engine rows that had a real crest
   * -- a visible inconsistency that read as a rendering bug. The API now serves
   * them from the same team join it already made for the names.
   */
  homeTeamLogo: string;
  awayTeamLogo: string;
  leagueName: string;
  country: string;
  kickoffAt: string;
  matchStatus: string;
  predictedWinner: string;
  winOrDraw: boolean;
  underOver: string;
  percentHome: number;
  percentDraw: number;
  percentAway: number;
  advice: string;
  marketKey: string;
  selection: string;
  confidence: number;
  publishedVersion: number;
  publishedAt: string;
};

function isLivePrediction(value: unknown): value is LivePrediction {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const candidate = value as Record<string, unknown>;
  return (
    validEntityId(candidate.id) &&
    validEntityId(candidate.matchId) &&
    validRouteText(candidate.matchTitle) &&
    validRouteText(candidate.homeTeam) &&
    validRouteText(candidate.awayTeam) &&
    validRouteText(candidate.leagueName) &&
    validRouteText(candidate.kickoffAt) &&
    Number.isFinite(Date.parse(candidate.kickoffAt)) &&
    typeof candidate.matchStatus === "string" &&
    validRouteText(candidate.marketKey) &&
    validRouteText(candidate.selection) &&
    typeof candidate.confidence === "number" && Number.isFinite(candidate.confidence) &&
    typeof candidate.probability === "number" && Number.isFinite(candidate.probability) &&
    typeof candidate.isBestBet === "boolean" &&
    validEntityId(candidate.publishedVersion) &&
    validRouteText(candidate.publishedAt) &&
    (candidate.selectionOdds === null || (typeof candidate.selectionOdds === "number" && Number.isFinite(candidate.selectionOdds)))
  );
}

function toOptionalOdds(value: unknown): number | null {
  if (typeof value === "number" && Number.isFinite(value) && value > 0) {
    return value;
  }
  if (typeof value === "string") {
    const parsed = Number.parseFloat(value);
    if (Number.isFinite(parsed) && parsed > 0) {
      return parsed;
    }
  }
  return null;
}

function normalizeMarkets(
  value: Array<{ market_key?: string; selection?: string; confidence?: number; probability?: number; is_best_bet?: boolean }> | undefined,
  fallback: LivePredictionMarket,
) {
  const markets = (value ?? [])
    .map((market) => ({
      marketKey: String(market.market_key ?? ""),
      selection: String(market.selection ?? ""),
      confidence: Number(market.confidence ?? 0),
      probability: Number(market.probability ?? 0),
      isBestBet: Boolean(market.is_best_bet),
    }))
    .filter((market) => market.marketKey && market.selection && !HIDDEN_PUBLIC_MARKETS.has(market.marketKey));
  return markets.length > 0 ? markets : [fallback];
}

function normalizePrediction(
  prediction: NonNullable<TodayPredictionsResponse["predictions"]>[number],
): LivePrediction | null {
  if (!prediction) {
    return null;
  }
  const primaryMarket = {
    marketKey: String(prediction.market_key ?? ""),
    selection: String(prediction.selection ?? ""),
    confidence: Number(prediction.confidence),
    probability: Number(prediction.probability ?? 0),
    isBestBet: Boolean(prediction.is_best_bet),
  };
  const normalized = {
    id: Number(prediction.id),
    matchId: Number(prediction.match_id),
    matchTitle: String(prediction.match_title ?? ""),
    homeTeam: String(prediction.home_team ?? ""),
    awayTeam: String(prediction.away_team ?? ""),
    homeTeamLogo: String(prediction.home_team_logo ?? ""),
    awayTeamLogo: String(prediction.away_team_logo ?? ""),
    leagueName: String(prediction.league_name ?? ""),
    country: String(prediction.country ?? "International"),
    kickoffAt: String(prediction.kickoff_at ?? ""),
    matchStatus: String(prediction.match_status ?? ""),
    ...primaryMarket,
    publishedVersion: Number(prediction.published_version),
    publishedAt: String(prediction.published_at ?? ""),
    selectionOdds: toOptionalOdds(prediction.selection_odds),
    markets: normalizeMarkets(prediction.markets, primaryMarket),
  };

  if (HIDDEN_PUBLIC_MARKETS.has(normalized.marketKey)) {
    return null;
  }
  return isLivePrediction(normalized) ? normalized : null;
}

export async function fetchTodayPredictions(limit = 6): Promise<LivePrediction[]> {
  if (!Number.isFinite(limit) || limit <= 0) {
    return [];
  }
  const boundedLimit = Math.min(100, Math.max(1, Math.floor(limit)));
  const params = new URLSearchParams({ limit: String(boundedLimit) });
  const response = await fetchPredictionApi(`/v1/predictions/today?${params.toString()}`, {
    revalidate: 15,
  });
  if (!response.ok) {
    throw new Error(`Prediction feed request failed with status ${response.status}`);
  }

  const payload = (await response.json()) as TodayPredictionsResponse;
  const now = Date.now();
  const freshnessWindowMs = 48 * 60 * 60 * 1000;
  const normalized = (payload.predictions ?? [])
    .map((prediction) => normalizePrediction(prediction))
    .filter((prediction): prediction is LivePrediction => prediction !== null)
    .filter((prediction) => {
      const kickoffTime = Date.parse(prediction.kickoffAt);
      if (Number.isNaN(kickoffTime)) {
        return true;
      }
      return Math.abs(kickoffTime - now) <= freshnessWindowMs;
    })
    .sort((left, right) => {
      if (right.confidence !== left.confidence) {
        return right.confidence - left.confidence;
      }
      return Date.parse(right.publishedAt) - Date.parse(left.publishedAt);
    });

  return normalized.slice(0, boundedLimit);
}

// providerSelectionText builds the prediction text for a provider fallback item
// when the canonical selection is missing (older payloads).
export function providerSelectionText(prediction: ProviderLivePrediction) {
  const parts: string[] = [];
  if (prediction.predictedWinner.trim()) {
    parts.push(prediction.predictedWinner);
  } else if (prediction.winOrDraw) {
    parts.push("Home or draw");
  }
  if (prediction.underOver.trim()) {
    parts.push(prediction.underOver.trim());
  }
  return parts.join(" · ");
}

export function matchLabel(prediction: LivePrediction | ProviderLivePrediction) {
  if (prediction.matchTitle.trim().length > 0) {
    return prediction.matchTitle;
  }
  if (prediction.homeTeam.trim().length > 0 || prediction.awayTeam.trim().length > 0) {
    return `${prediction.homeTeam} vs ${prediction.awayTeam}`.trim();
  }
  return `Match #${prediction.matchId}`;
}

function normalizeProviderPrediction(
  prediction: NonNullable<NonNullable<TodayPredictionsResponse["provider_predictions"]>[number]>,
): ProviderLivePrediction | null {
  if (!prediction) {
    return null;
  }
  const normalized: ProviderLivePrediction = {
    matchId: Number(prediction.match_id),
    matchTitle: String(prediction.match_title ?? ""),
    homeTeam: String(prediction.home_team ?? ""),
    awayTeam: String(prediction.away_team ?? ""),
    homeTeamLogo: String(prediction.home_team_logo ?? ""),
    awayTeamLogo: String(prediction.away_team_logo ?? ""),
    leagueName: String(prediction.league_name ?? ""),
    country: String(prediction.country ?? "International"),
    kickoffAt: String(prediction.kickoff_at ?? ""),
    matchStatus: String(prediction.match_status ?? ""),
    predictedWinner: String(prediction.predicted_winner ?? ""),
    winOrDraw: Boolean(prediction.win_or_draw),
    underOver: String(prediction.under_over ?? ""),
    percentHome: Number(prediction.percent_home ?? 0),
    percentDraw: Number(prediction.percent_draw ?? 0),
    percentAway: Number(prediction.percent_away ?? 0),
    advice: String(prediction.advice ?? ""),
    marketKey: String(prediction.market_key ?? "1x2"),
    selection: String(prediction.selection ?? ""),
    confidence: Number(prediction.confidence),
    publishedVersion: Number(prediction.published_version),
    publishedAt: String(prediction.published_at ?? ""),
  };
  if (HIDDEN_PUBLIC_MARKETS.has(normalized.marketKey)) {
    return null;
  }
  return normalized.matchId > 0 && (normalized.homeTeam.trim() !== "" || normalized.awayTeam.trim() !== "")
    ? normalized
    : null;
}

export async function fetchTodayPredictionsWithProvider(
  limit = 40,
  date?: string,
  page = 1,
  timezone = DEFAULT_VIEWER_TIMEZONE,
  market = "1x2",
  /**
   * How many calendar days to cover, starting at `date` or today. 1 keeps the
   * single-day feed every existing caller expects.
   */
  days = 1,
): Promise<{
  engine: LivePrediction[];
  provider: ProviderLivePrediction[];
  total: number;
  page: number;
  limit: number;
}> {
  const boundedLimit = Math.min(100, Math.max(1, Math.floor(limit)));
  const boundedPage = Math.max(1, Math.floor(page));
  // The public feed is one row per match. Every other market belongs on the
  // match-detail page; requesting all markets here made a 40-row response
  // represent only four fixtures because the engine publishes ten markets.
  const params = new URLSearchParams({
    limit: String(boundedLimit),
    market,
    page: String(boundedPage),
  });
  if (date) {
    params.set("date", date);
  }
  if (days > 1) {
    params.set("days", String(days));
  }
  params.set("timezone", timezone);
  const response = await fetchPredictionApi(`/v1/predictions/today?${params.toString()}`, {
    // Published predictions are worker-generated, not live-score data. A
    // one-minute shared cache avoids a seven-day ACCA/Builder refresh stampede
    // while keeping newly published selections reasonably fresh.
    revalidate: 60,
  });
  if (!response.ok) {
    throw new Error(`Prediction feed request failed with status ${response.status}`);
  }
  const payload = (await response.json()) as TodayPredictionsResponse;
  const now = Date.now();
  const freshnessWindowMs = 48 * 60 * 60 * 1000;
  const engine = (payload.predictions ?? [])
    .map((prediction) => normalizePrediction(prediction))
    .filter((prediction): prediction is LivePrediction => prediction !== null)
    .filter((prediction) => {
      // An explicit date or horizon has already bounded the window server-side.
      // Without this the 48-hour guard below silently clips a seven-day request
      // back to two, and the horizon looks like it does nothing.
      if (date || days > 1) {
        return true;
      }
      const kickoffTime = Date.parse(prediction.kickoffAt);
      if (Number.isNaN(kickoffTime)) {
        return true;
      }
      return Math.abs(kickoffTime - now) <= freshnessWindowMs;
    })
    .sort((left, right) => Date.parse(left.kickoffAt) - Date.parse(right.kickoffAt))
    .slice(0, boundedLimit);
  const provider = (payload.provider_predictions ?? [])
    .map((prediction) => normalizeProviderPrediction(prediction))
    .filter((prediction): prediction is ProviderLivePrediction => prediction !== null)
    .filter((prediction) => {
      // An explicit date or horizon has already bounded the window server-side.
      // Without this the 48-hour guard below silently clips a seven-day request
      // back to two, and the horizon looks like it does nothing.
      if (date || days > 1) {
        return true;
      }
      const kickoffTime = Date.parse(prediction.kickoffAt);
      if (Number.isNaN(kickoffTime)) {
        return true;
      }
      return Math.abs(kickoffTime - now) <= freshnessWindowMs;
    })
    .sort((left, right) => Date.parse(left.kickoffAt) - Date.parse(right.kickoffAt))
    .slice(0, boundedLimit);
  return {
    engine,
    provider,
    total: Math.max(0, Number(payload.total) || 0),
    page: Math.max(1, Number(payload.page) || boundedPage),
    limit: Math.max(1, Number(payload.limit) || boundedLimit),
  };
}

/**
 * A market hub covers the week, not today.
 *
 * These pages are standing entries about a market rather than about a date --
 * someone arriving on "Over 2.5 Goals" wants the fixtures it applies to, and a
 * Tuesday-evening visitor was being shown whatever happened to kick off that
 * day. It also unlocks the explorer's "Day" grouping, which renders only when
 * the slate spans more than one date and so could never appear here.
 *
 * `MARKET_HUB_DAYS` is the API's own ceiling. Each calendar day contributes a
 * bounded sample, so a busy weekend cannot force deep offset pages that fail
 * or make the browser render thousands of cards.
 */
export const MARKET_HUB_DAYS = 7;

// A week can contain thousands of fixtures. Deep pages of the public feed
// currently return 503 in production, and fetching every page also sends a
// multi-megabyte card list to the browser. One page per calendar day keeps all
// seven days represented with a fixed upper bound of seven API requests.
export const MARKET_HUB_DAILY_LIMIT = 100;

export async function fetchMarketPredictions(
  market: string,
  date?: string,
  timezone = DEFAULT_VIEWER_TIMEZONE,
  days = MARKET_HUB_DAYS,
) {
  const dates = marketFeedDates(date, timezone, days);
  const pages = await Promise.all(dates.map((day) =>
    fetchTodayPredictionsWithProvider(MARKET_HUB_DAILY_LIMIT, day, 1, timezone, market),
  ));
  return pages.flatMap((page) => page.engine)
    .sort((left, right) => Date.parse(left.kickoffAt) - Date.parse(right.kickoffAt));
}

/**
 * The whole prediction feed for a day, paged.
 *
 * `maxPages` exists because two callers want different things from the word
 * "all". A reader-facing page wants enough to fill a screen and must not
 * stampede PostgreSQL on a busy Saturday, so it takes the default. The sitemap
 * wants every fixture that can rank, and silently getting five pages of them is
 * not a smaller sitemap, it is a wrong one.
 *
 * That is not hypothetical. The sitemap shared the reader's cap until
 * 2026-09-21 and advertised 2,307 fixtures when 7,323 had current predictions
 * -- roughly five thousand rankable pages Google was never told about, at a
 * time when it was crawling around 275 URLs a day and had to find them by
 * luck instead.
 */
export async function fetchAllTodayPredictionsWithProvider(
  date?: string,
  timezone = DEFAULT_VIEWER_TIMEZONE,
  maxPages = PREDICTION_POOL_MAX_PAGES,
) {
  const first = await fetchTodayPredictionsWithProvider(100, date, 1, timezone);
  // Bounded because the homepage depends on this too, and paging stays
  // sequential -- see the test that pins it -- so a wide day cannot stampede
  // PostgreSQL. The bound is sized from the real day, not from a guess: see
  // PREDICTION_POOL_MAX_PAGES.
  const pageCount = Math.min(
    Math.max(1, maxPages),
    Math.max(1, Math.ceil(first.total / first.limit)),
  );
  const pages = [first];
  for (let page = 2; page <= pageCount; page += 1) {
    pages.push(await fetchTodayPredictionsWithProvider(first.limit, date, page, timezone));
  }
  return {
    engine: pages.flatMap((page) => page.engine),
    provider: pages.flatMap((page) => page.provider),
    total: first.total,
  };
}

// providerToLivePrediction shapes a provider fallback item as a LivePrediction
// so merged-feed consumers (PredictionCard) render engine + provider alike. The
// synthesized negative id never collides with engine public ids.
function providerToLivePrediction(prediction: ProviderLivePrediction): LivePrediction {
  return {
    id: -Math.abs(prediction.matchId),
    matchId: prediction.matchId,
    matchTitle: prediction.matchTitle,
    homeTeam: prediction.homeTeam,
    awayTeam: prediction.awayTeam,
    homeTeamLogo: prediction.homeTeamLogo,
    awayTeamLogo: prediction.awayTeamLogo,
    leagueName: prediction.leagueName,
    country: prediction.country,
    kickoffAt: prediction.kickoffAt,
    matchStatus: prediction.matchStatus,
    marketKey: prediction.marketKey,
    selection: prediction.selection,
    confidence: prediction.confidence,
    probability: Math.max(0, Math.min(1, prediction.confidence / 100)),
    isBestBet: false,
    publishedVersion: prediction.publishedVersion,
    publishedAt: prediction.publishedAt,
    selectionOdds: null,
    markets: [{ marketKey: prediction.marketKey, selection: prediction.selection, confidence: prediction.confidence, probability: Math.max(0, Math.min(1, prediction.confidence / 100)), isBestBet: false }],
  };
}

// fetchMergedTodayPredictions returns one unified prediction list: engine
// predictions first (dedup by match), provider fallbacks filling gaps, sorted
// by kickoff ascending.
export async function fetchMergedTodayPredictions(limit = 40): Promise<LivePrediction[]> {
  const { engine, provider } = await fetchTodayPredictionsWithProvider(limit);
  return mergeByMatchId(engine, provider, providerToLivePrediction);
}

/**
 * The whole day's fixtures, merged, for surfaces that RANK before they
 * truncate.
 *
 * `fetchMergedTodayPredictions(n)` returns the first n fixtures *by kickoff*.
 * That is the wrong input for a ranked surface: Europe's big five kick off at
 * 17:00-19:05 UTC, which is the tail of the day, so they sat at index 211-247
 * of 258 on 2026-09-04 and no prefix short of the full day contained them. The
 * homepage was ranking 24 fixtures that could not include the leagues the
 * ranking exists to promote.
 *
 * Cost is a handful of extra upstream calls behind the existing 60s shared
 * cache, not a call per reader.
 */
export async function fetchTodayPredictionPool(
  date?: string,
  timezone = DEFAULT_VIEWER_TIMEZONE,
): Promise<LivePrediction[]> {
  const { engine, provider } = await fetchAllTodayPredictionsWithProvider(date, timezone);
  return mergeByMatchId(engine, provider, providerToLivePrediction);
}

export function nairobiDate(offsetDays: number) {
  const shifted = new Date(Date.now() + offsetDays * 86_400_000);
  const parts = new Intl.DateTimeFormat("en", {
    timeZone: DEFAULT_VIEWER_TIMEZONE,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(shifted);
  const part = (type: Intl.DateTimeFormatPartTypes) => parts.find((item) => item.type === type)?.value ?? "";
  return `${part("year")}-${part("month")}-${part("day")}`;
}

/** Fetch one engine row per fixture across the rolling public horizon. */
export async function fetchPredictionHorizon(days = 7, allPages = true, firstPageLimit = 100): Promise<LivePrediction[]> {
  const boundedDays = Math.min(7, Math.max(1, Math.floor(days)));
  const boundedFirstPageLimit = Math.min(100, Math.max(1, Math.floor(firstPageLimit)));
  const pages = await Promise.allSettled(
    Array.from({ length: boundedDays }, (_, offset) =>
      allPages
        ? fetchAllTodayPredictionsWithProvider(nairobiDate(offset))
        : fetchTodayPredictionsWithProvider(boundedFirstPageLimit, nairobiDate(offset)),
    ),
  );
  const byFixture = new Map<number, LivePrediction>();
  for (const page of pages) {
    if (page.status !== "fulfilled") continue;
    for (const prediction of page.value.engine) {
      const current = byFixture.get(prediction.matchId);
      if (!current || prediction.confidence > current.confidence) {
        byFixture.set(prediction.matchId, prediction);
      }
    }
  }
  return [...byFixture.values()].sort((left, right) => Date.parse(left.kickoffAt) - Date.parse(right.kickoffAt));
}
