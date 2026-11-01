import type { LivePrediction } from "../data/livePredictions";
import { formatMarket } from "../lib/markets.ts";
import { footballCountry } from "../lib/competitionOrder.ts";
import type { Locale } from "../i18n/config.ts";
import type { AccaLegStatus } from "./accaSlip.ts";

export type AccaRisk = "Lower" | "Balanced" | "Higher";

export type AccaLeg = Pick<LivePrediction,
  "id" | "matchId" | "homeTeam" | "awayTeam" | "homeTeamLogo" | "awayTeamLogo" |
  "leagueName" | "country" | "kickoffAt" | "matchStatus" | "marketKey" | "selection" |
  "confidence" | "probability" | "isBestBet"
> & {
  fairOdds: number;
  /**
   * How the leg finished, present only on `GET /v1/accas/{id}`.
   *
   * The list endpoint omits it because it only serves slips whose earliest
   * kick-off is still ahead, where every leg is `pending` by construction.
   * Absent therefore means "not told", which the detail page reads as pending
   * rather than inventing a state.
   */
  status?: AccaLegStatus;
};

export type AccaBundle = {
  id: string;
  label: string;
  description: string;
  legs: AccaLeg[];
  combinedFairProbability: number;
  combinedFairOdds: number;
  marketKey: string;
  country: string;
  risk: AccaRisk;
  // "on_demand" marks a slip composed for this request from the vetted pool
  // rather than read from the precomputed inventory. It is not stored.
  origin?: string;
};

export type AccaFilters = {
  market?: string;
  country?: string;
  legs?: number;
  risk?: AccaRisk;
};

export type AccaPage = {
  bundles: AccaBundle[];
  total: number;
  page: number;
  pageSize: number;
  nextCursor?: string;
  hasMore?: boolean;
  facets: {
    markets: string[];
    countries: string[];
    leagues: string[];
    legCounts: number[];
    leagueCountries: Record<string, string[]>;
  };
};

export const ACCA_PAGE_SIZE = 10;
export const MIN_ACCA_LEG_FAIR_ODDS = 1.10;
export const MIN_ACCA_COMBINED_FAIR_ODDS = 1.50;

export function minimumSystemAccaCombinedFairOdds(legCount: number) {
  return Math.pow(MIN_ACCA_COMBINED_FAIR_ODDS, Math.max(2, legCount) / 2);
}

type Scope = { key: string; marketKey: string; country: string; items: AccaLeg[] };
const LEG_COUNTS = [2, 3, 5, 8] as const;

function eligibleSelections(predictions: LivePrediction[]) {
  return predictions.flatMap((prediction) => prediction.markets
    .filter((market) => market.confidence > 0 && market.probability > 0 && market.probability <= (1 / MIN_ACCA_LEG_FAIR_ODDS))
    .map((market): AccaLeg => ({
      id: prediction.id,
      matchId: prediction.matchId,
      homeTeam: prediction.homeTeam,
      awayTeam: prediction.awayTeam,
      homeTeamLogo: prediction.homeTeamLogo,
      awayTeamLogo: prediction.awayTeamLogo,
      leagueName: prediction.leagueName,
      country: footballCountry(prediction.country, prediction.leagueName),
      kickoffAt: prediction.kickoffAt,
      matchStatus: prediction.matchStatus,
      ...market,
      fairOdds: market.probability > 0 ? 1 / market.probability : 0,
    })))
    .sort((left, right) => (right.probability * Math.max(right.confidence, 1)) - (left.probability * Math.max(left.confidence, 1)));
}

function uniqueFixtures(items: AccaLeg[]) {
  const seen = new Set<number>();
  return items.filter((item) => !seen.has(item.matchId) && Boolean(seen.add(item.matchId)));
}

function scopeList(items: AccaLeg[]): Scope[] {
  const scopes: Scope[] = [{ key: "mixed", marketKey: "all", country: "all", items: uniqueFixtures(items) }];
  const markets = [...new Set(items.map((item) => item.marketKey))];
  const countries = [...new Set(items.map((item) => item.country))];
  for (const marketKey of markets) scopes.push({ key: `market-${marketKey}`, marketKey, country: "all", items: uniqueFixtures(items.filter((item) => item.marketKey === marketKey)) });
  for (const country of countries) {
    const countryItems = items.filter((item) => item.country === country);
    scopes.push({ key: `country-${country}`, marketKey: "all", country, items: uniqueFixtures(countryItems) });
    for (const marketKey of markets) scopes.push({ key: `country-${country}-market-${marketKey}`, marketKey, country, items: uniqueFixtures(countryItems.filter((item) => item.marketKey === marketKey)) });
  }
  return scopes;
}

function bundleRisk(legs: AccaLeg[]): AccaRisk {
  const average = legs.reduce((sum, leg) => sum + leg.confidence, 0) / legs.length;
  if (legs.length <= 2 && average >= 65) return "Lower";
  if (legs.length <= 5 && average >= 55) return "Balanced";
  return "Higher";
}

/**
 * Produces a growing catalogue rather than three hard-coded ACCAs. Every
 * published, probability-bearing start position becomes a bundle for each shape;
 * no global bundle-count cap is applied.
 */
export function buildAccaBundles(predictions: LivePrediction[], locale: Locale = "en"): AccaBundle[] {
  const output = new Map<string, AccaBundle>();
  for (const scope of scopeList(eligibleSelections(predictions))) {
    for (const legCount of LEG_COUNTS) {
      if (scope.items.length < legCount) continue;
      // Non-overlapping windows keep the payload linear as the fixture slate
      // grows while still applying no global bundle-count ceiling.
      for (let start = 0; start <= scope.items.length - legCount; start += legCount) {
        const legs = scope.items.slice(start, start + legCount);
        const signature = legs.map((leg) => `${leg.matchId}:${leg.marketKey}:${leg.selection}`).sort().join("|");
        const id = `${scope.key}-${legCount}-${signature}`;
        if (output.has(id)) continue;
        const combinedFairProbability = legs.reduce((combined, leg) => combined * leg.probability, 1);
        const combinedFairOdds = combinedFairProbability > 0 ? 1 / combinedFairProbability : 0;
        if (combinedFairOdds < minimumSystemAccaCombinedFairOdds(legCount)) continue;
        const scopeLabel = scope.marketKey !== "all" ? formatMarket(scope.marketKey, locale) : scope.country !== "all" ? scope.country : "Mixed markets";
        output.set(id, {
          id,
          label: `${legCount}-leg ${scopeLabel} ACCA`,
          description: scope.country !== "all" ? `${scope.country} selections from separate fixtures.` : "Engine-qualified selections from separate fixtures.",
          legs,
          combinedFairProbability,
          combinedFairOdds,
          marketKey: scope.marketKey,
          country: scope.country,
          risk: bundleRisk(legs),
        });
      }
    }
  }
  return [...output.values()].sort((left, right) => left.risk.localeCompare(right.risk) || left.legs.length - right.legs.length || right.combinedFairProbability - left.combinedFairProbability);
}

export function paginateAccaBundles(
  bundles: AccaBundle[],
  page = 1,
  filters: AccaFilters = {},
): AccaPage {
  const safePage = Number.isSafeInteger(page) && page > 0 ? page : 1;
  const filtered = bundles.filter((bundle) =>
    (!filters.market || filters.market === "all" || bundle.marketKey === filters.market) &&
    (!filters.country || filters.country === "all" || bundle.country === filters.country) &&
    (!filters.legs || bundle.legs.length === filters.legs) &&
    (!filters.risk || bundle.risk === filters.risk));
  const offset = (safePage - 1) * ACCA_PAGE_SIZE;
  return {
    bundles: filtered.slice(offset, offset + ACCA_PAGE_SIZE),
    total: filtered.length,
    page: safePage,
    pageSize: ACCA_PAGE_SIZE,
    facets: {
      markets: [...new Set(bundles.map((bundle) => bundle.marketKey).filter((item) => item !== "all"))].sort(),
      countries: [...new Set(bundles.map((bundle) => bundle.country).filter((item) => item !== "all"))].sort(),
      leagues: [...new Set(bundles.flatMap((bundle) => bundle.legs.map((leg) => leg.leagueName)).filter(Boolean))].sort(),
      legCounts: [...new Set(bundles.map((bundle) => bundle.legs.length))].sort((a, b) => a - b),
      leagueCountries: bundles.reduce<Record<string, string[]>>((map, bundle) => { for (const leg of bundle.legs) if (leg.country && leg.leagueName) map[leg.country] = [...new Set([...(map[leg.country] ?? []), leg.leagueName])].sort(); return map; }, {}),
    },
  };
}
