import { fetchPredictionApi } from "./apiClient.ts";
import type { AccaBundle, AccaLeg, AccaPage } from "../acca-predictions/accaBundles";
import type { AccaLegStatus } from "../acca-predictions/accaSlip.ts";

type JsonRecord = Record<string, unknown>;
const text = (v: unknown, fallback = "") => typeof v === "string" ? v : fallback;
const num = (v: unknown, fallback = 0) => typeof v === "number" && Number.isFinite(v) ? v : fallback;

const legStatuses: AccaLegStatus[] = ["pending", "won", "lost", "void"];

/**
 * A leg's grade, or undefined.
 *
 * An unrecognised word becomes undefined rather than passing through: the
 * detail page colours a leg green on "won", and a provider or a future API
 * sending "WON" or "settled_won" must read as ungraded instead of as a loss.
 */
function legStatus(v: unknown): AccaLegStatus | undefined {
  return typeof v === "string" && (legStatuses as string[]).includes(v) ? (v as AccaLegStatus) : undefined;
}

function leg(raw: JsonRecord): AccaLeg {
  const fairOdds = num(raw.fair_odds ?? raw.fairOdds);
  const explicitProbability = num(raw.probability ?? raw.selection_probability);
  const probability = explicitProbability > 0 ? explicitProbability : fairOdds > 0 ? 1 / fairOdds : 0;
  return {
    id: num(raw.id ?? raw.prediction_id ?? raw.predictionId),
    matchId: num(raw.match_id ?? raw.matchId),
    homeTeam: text(raw.home_team ?? raw.homeTeam, "Home"),
    awayTeam: text(raw.away_team ?? raw.awayTeam, "Away"),
    homeTeamLogo: text(raw.home_team_logo ?? raw.homeTeamLogo),
    awayTeamLogo: text(raw.away_team_logo ?? raw.awayTeamLogo),
    leagueName: text(raw.league_name ?? raw.leagueName ?? raw.league),
    country: text(raw.country),
    kickoffAt: text(raw.kickoff_at ?? raw.kickoffAt),
    matchStatus: text(raw.match_status ?? raw.matchStatus, "scheduled"),
    marketKey: text(raw.market_key ?? raw.marketKey ?? raw.market),
    selection: text(raw.selection),
    confidence: num(raw.confidence),
    probability,
    fairOdds: fairOdds > 0 ? fairOdds : probability > 0 ? 1 / probability : 0,
    isBestBet: Boolean(raw.is_best_bet ?? raw.isBestBet),
    status: legStatus(raw.status),
  };
}

function bundle(raw: JsonRecord): AccaBundle {
  const legs = Array.isArray(raw.legs) ? raw.legs.filter((v): v is JsonRecord => Boolean(v && typeof v === "object")).map(leg) : [];
  const combinedFairOdds = num(raw.combined_fair_odds ?? raw.combinedFairOdds);
  const probability = num(raw.combined_fair_probability ?? raw.combinedFairProbability, combinedFairOdds > 0 ? 1 / combinedFairOdds : legs.reduce((v, item) => v * item.probability, 1));
  return {
    id: text(raw.id ?? raw.composition_hash),
    label: text(raw.label, `${legs.length}-leg ACCA`),
    description: text(raw.description, "Precomputed engine-qualified selections from separate fixtures."),
    legs,
    combinedFairProbability: probability,
    combinedFairOdds: combinedFairOdds > 0 ? combinedFairOdds : probability > 0 ? 1 / probability : 0,
    marketKey: text(raw.market_tab ?? raw.marketKey, "all"),
    country: text(raw.country, "all"),
    risk: (text(raw.risk, "Balanced") as AccaBundle["risk"]),
    origin: text(raw.origin) || undefined,
  };
}

function isValidBundle(value: AccaBundle): boolean {
  return value.id.trim() !== "" && value.legs.length >= 2 && value.legs.every((item) =>
    item.matchId > 0 && item.marketKey.trim() !== "" && item.selection.trim() !== "" &&
    Number.isFinite(item.probability) && item.probability > 0 && item.probability <= 1 &&
    Number.isFinite(item.kickoffAt ? Date.parse(item.kickoffAt) : NaN)
  ) && Number.isFinite(value.combinedFairOdds) && value.combinedFairOdds > 0 && value.combinedFairOdds <= 1000 &&
    Number.isFinite(value.combinedFairProbability) && value.combinedFairProbability > 0 && value.combinedFairProbability <= 1;
}

export function normalizeAccaPage(raw: unknown): AccaPage {
  const value = (raw && typeof raw === "object" ? raw : {}) as JsonRecord;
  const rows = value.bundles ?? value.accas ?? value.items;
  const bundles = Array.isArray(rows) ? Array.from(new Map(rows
    .filter((v): v is JsonRecord => Boolean(v && typeof v === "object"))
    .map(bundle).filter(isValidBundle).map((item) => [item.id, item])).values()) : [];
  const facets = (value.facets && typeof value.facets === "object" ? value.facets : {}) as JsonRecord;
  return {
    bundles,
    total: num(value.total, bundles.length),
    page: num(value.page, 1),
    pageSize: num(value.page_size ?? value.pageSize, 10),
    nextCursor: text(value.next_cursor ?? value.nextCursor).trim() || undefined,
    hasMore: Boolean(value.has_more ?? value.hasMore ?? value.next_cursor) && Boolean(text(value.next_cursor ?? value.nextCursor).trim()),
    facets: {
      markets: Array.isArray(facets.markets) ? facets.markets.map(String) : [],
      countries: Array.isArray(facets.countries) ? facets.countries.map(String) : [],
      leagues: Array.isArray(facets.leagues) ? facets.leagues.map(String) : [],
      leagueCountries: facets.league_countries && typeof facets.league_countries === "object" ? Object.fromEntries(Object.entries(facets.league_countries).map(([country, leagues]) => [country, Array.isArray(leagues) ? leagues.map(String) : []])) : {},
    legCounts: (() => { const values = facets.leg_counts ?? facets.legCounts; return Array.isArray(values) ? values.map(Number).filter(Number.isFinite) : []; })(),
    },
  };
}

export async function fetchAccaPage(query: URLSearchParams): Promise<AccaPage> {
  const response = await fetchPredictionApi(`/v1/accas?${query}`, { revalidate: 15, staleWhileRevalidate: 30 });
  if (!response.ok) throw new Error(`ACCA API returned ${response.status}`);
  return normalizeAccaPage(await response.json());
}

/**
 * One stored ACCA, by id.
 *
 * `revalidate: 0` on purpose, and it is the whole reason this is not
 * `fetchAccaPage` with a filter: the API serves this from the UNCACHED source
 * (`services/api/internal/httpserver/server.go` says so where it wires it)
 * because leg statuses change while the matches are being played. Caching it
 * here would undo that and show a settled slip as still in progress for as long
 * as the cache held.
 *
 * Returns null for both "no such slip" and "a slip that fails the same validity
 * check the list applies". The second is not a 404, but rendering a slip whose
 * legs have no kick-off or no price would put a broken page in front of a reader
 * and in a sitemap, and the page has a not-found state already.
 */
export async function fetchAccaSlip(id: string): Promise<AccaBundle | null> {
  const response = await fetchPredictionApi(`/v1/accas/${encodeURIComponent(id)}`, { revalidate: 0 });
  if (response.status === 404) return null;
  if (!response.ok) throw new Error(`ACCA API returned ${response.status}`);
  const raw = await response.json();
  const value = (raw && typeof raw === "object" ? raw : {}) as JsonRecord;
  const slip = bundle(value);
  return isValidBundle(slip) ? slip : null;
}
