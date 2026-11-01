import {
  matchLabel,
  marketLabel,
  providerSelectionText,
  type LivePrediction,
  type ProviderLivePrediction,
} from "./livePredictions";
import type { PredictionExplorerItem } from "../components/PredictionExplorer";
import { predictionLabel } from "../i18n/catalogs";
import { predictionFixturePath } from "../lib/seo";
import { fixtureHref } from "../lib/fixtureHref";
import { DEFAULT_VIEWER_TIMEZONE } from "../lib/viewerTimezone";

type Locale = Parameters<typeof predictionLabel>[0];

/**
 * The calendar date a kick-off falls on, in the viewer's zone.
 *
 * `PredictionExplorer` keys its day grouping, its "Day" control and its date
 * filter on this field, and nothing has ever populated it -- so `spansDays` was
 * permanently false, the control could not render, and grouping by day would
 * have filed every row under "Date to be confirmed". The feature was written
 * and then starved of its input.
 *
 * Computed from the instant rather than taken from the API, because the date a
 * 23:30 UTC kick-off belongs to depends on where the reader is.
 */
function viewerDate(kickoffAt: string | null | undefined, timezone: string): string | undefined {
  if (!kickoffAt) return undefined;
  const instant = new Date(kickoffAt);
  if (Number.isNaN(instant.getTime())) return undefined;
  const parts = new Intl.DateTimeFormat("en", {
    timeZone: timezone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(instant);
  const part = (type: Intl.DateTimeFormatPartTypes) =>
    parts.find((item) => item.type === type)?.value ?? "";
  const [year, month, day] = [part("year"), part("month"), part("day")];
  return year && month && day ? `${year}-${month}-${day}` : undefined;
}

/*
 * Mapping a prediction onto a feed row.
 *
 * These two functions were local to the Today page while Today was the only
 * refreshed list. Now that Tomorrow, Weekend, the market hubs and Best Bets all
 * render the same `PredictionListShell`, they have to produce identical rows
 * from identical input -- and the way that fails is subtle: one page forgets
 * `homeTeamLogo` and its crests silently fall back to initials, or one page
 * routes engine predictions through `fixtureHref` and quietly publishes the
 * legacy URL as canonical. Shared, there is one answer to both.
 */

export function fromLivePrediction(
  prediction: LivePrediction,
  locale: Locale,
  timezone: string = DEFAULT_VIEWER_TIMEZONE,
): PredictionExplorerItem {
  const localizedSelection = predictionLabel(locale, prediction.marketKey, prediction.selection);
  return {
    availability: "full",
    confidence: Math.round(prediction.confidence),
    date: viewerDate(prediction.kickoffAt, timezone),
    href: predictionFixturePath(prediction),
    id: prediction.id,
    isBestBet: false,
    kickoffAt: prediction.kickoffAt ?? null,
    league: prediction.leagueName || "Competition",
    country: prediction.country || "International",
    market: marketLabel(prediction.marketKey, locale),
    marketKey: prediction.marketKey,
    rawSelection: prediction.selection,
    match: matchLabel(prediction),
    matchId: prediction.matchId,
    homeTeam: prediction.homeTeam,
    awayTeam: prediction.awayTeam,
    homeTeamLogo: prediction.homeTeamLogo,
    awayTeamLogo: prediction.awayTeamLogo,
    prediction: localizedSelection,
    markets: prediction.markets.map((market) => ({
      market: marketLabel(market.marketKey, locale),
      prediction: predictionLabel(locale, market.marketKey, market.selection),
      confidence: Math.round(market.confidence),
    })),
  };
}

// Provider fallback items carry the same canonical primitives (market_key,
// selection, confidence) after the API contract unification, so they render in
// the same feed without a public source label. Provenance stays in Admin.
export function fromProviderLivePrediction(
  prediction: ProviderLivePrediction,
  locale: Locale,
  timezone: string = DEFAULT_VIEWER_TIMEZONE,
): PredictionExplorerItem {
  const localizedSelection = predictionLabel(locale, prediction.marketKey || "1x2", prediction.selection);
  const selection = prediction.selection.trim()
    ? localizedSelection
    : providerSelectionText(prediction) || "Projection";
  return {
    availability: "full",
    confidence: Math.round(prediction.confidence),
    date: viewerDate(prediction.kickoffAt, timezone),
    // Provider fallback details are still served by the legacy match route;
    // only engine-published markets qualify for the canonical SEO route.
    href: fixtureHref(prediction),
    id: `provider-${prediction.matchId}`,
    isBestBet: false,
    kickoffAt: prediction.kickoffAt ?? null,
    league: prediction.leagueName || "Competition",
    country: prediction.country || "International",
    market: marketLabel(prediction.marketKey || "1x2", locale),
    marketKey: prediction.marketKey || "1x2",
    rawSelection: prediction.selection,
    match: matchLabel(prediction),
    matchId: prediction.matchId,
    homeTeam: prediction.homeTeam,
    awayTeam: prediction.awayTeam,
    // Crests now arrive on the provider payload too. Still possibly empty --
    // a team may have no logo on record -- and TeamCrest draws initials from
    // the name when they are, so these rows render like engine ones either way.
    homeTeamLogo: prediction.homeTeamLogo,
    awayTeamLogo: prediction.awayTeamLogo,
    prediction: selection,
    markets: [{
      market: marketLabel(prediction.marketKey || "1x2", locale),
      prediction: selection,
      confidence: Math.round(prediction.confidence),
    }],
  };
}


