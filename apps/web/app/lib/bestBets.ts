/**
 * Selection for the Best Bets page.
 *
 * Two decisions are baked in here.
 *
 * **No threshold.** The page used to filter at 65% confidence. That was
 * unreachable rather than strict: across the whole day of 2026-09-04, no market
 * on any of 258 fixtures exceeded 53.8%, and the model's published confidence
 * sits in a 38-54 band. A three-way market such as 1x2 splits probability three
 * ways and structurally cannot reach 65, so the page rendered its empty state
 * every day while the data was fine.
 *
 * **Every market, and a fixture may appear more than once.** The feed's primary
 * row is always 1x2, but each fixture carries its full market set. Ranking only
 * the primary row threw away the other eight, and the strongest read on a match
 * is often "under 2.5" rather than the outright result. So each market is its
 * own candidate, and a match with two strongly ranked markets earns two places.
 */

export type SelectableMarket = {
  marketKey: string;
  selection: string;
  confidence: number;
  probability?: number;
};

export type SelectableBet = {
  matchId: number;
  marketKey?: string | null;
  selection?: string | null;
  confidence: number;
  markets?: readonly SelectableMarket[];
};

/** Roughly one pick per twenty fixtures, bounded at both ends. */
const FIXTURES_PER_PICK = 20;
export const BEST_BET_MIN = 5;
export const BEST_BET_MAX = 20;

/**
 * No market may take more than this share of the list -- a preference, not a
 * ceiling. See selectBestBets: when honouring it would leave the list short,
 * the remainder is filled by confidence alone.
 */
const MARKET_SHARE = 0.5;

/**
 * How many picks a day of this size deserves. Counted in FIXTURES, not
 * candidates: the page's size should follow how much football is on, not how
 * many markets the engine happens to publish per match.
 */
export function bestBetCount(fixtureCount: number): number {
  if (!Number.isFinite(fixtureCount) || fixtureCount <= 0) return 0;
  const scaled = Math.round(fixtureCount / FIXTURES_PER_PICK);
  return Math.min(fixtureCount, Math.max(BEST_BET_MIN, Math.min(BEST_BET_MAX, scaled)));
}

/**
 * One candidate per (fixture, market). Falls back to the row's own primary
 * market when the engine published no market list for it.
 */
export function expandMarkets<T extends SelectableBet>(pool: readonly T[]): Array<T & SelectableMarket> {
  const seen = new Set<string>();
  const candidates: Array<T & SelectableMarket> = [];

  for (const item of pool) {
    const markets = item.markets?.length
      ? item.markets
      : [
          {
            marketKey: String(item.marketKey ?? ""),
            selection: String(item.selection ?? ""),
            confidence: Number(item.confidence ?? 0),
          },
        ];

    for (const market of markets) {
      if (!market.marketKey || !market.selection) continue;
      const key = `${item.matchId}|${market.marketKey}`;
      if (seen.has(key)) continue;
      seen.add(key);
      candidates.push({
        ...item,
        marketKey: market.marketKey,
        selection: market.selection,
        confidence: Number(market.confidence ?? 0),
        probability: market.probability,
      });
    }
  }

  return candidates;
}

/**
 * Highest confidence first across every market. A fixture may appear more than
 * once -- that is the point -- while the market share stops any one market
 * filling the page.
 */
export function selectBestBets<T extends SelectableBet>(pool: readonly T[]): Array<T & SelectableMarket> {
  const limit = bestBetCount(pool.length);
  if (limit === 0) return [];

  const ranked = expandMarkets(pool).sort(
    (left, right) => (right.confidence ?? 0) - (left.confidence ?? 0),
  );

  const perMarket = Math.max(1, Math.ceil(limit * MARKET_SHARE));
  const takenPerMarket = new Map<string, number>();
  const chosen: Array<T & SelectableMarket> = [];
  const deferred: Array<T & SelectableMarket> = [];

  for (const candidate of ranked) {
    if (chosen.length >= limit) break;
    const taken = takenPerMarket.get(candidate.marketKey) ?? 0;
    if (taken >= perMarket) {
      deferred.push(candidate);
      continue;
    }
    takenPerMarket.set(candidate.marketKey, taken + 1);
    chosen.push(candidate);
  }

  // Top up from what the cap held back, still strongest first.
  for (const candidate of deferred) {
    if (chosen.length >= limit) break;
    chosen.push(candidate);
  }

  return chosen.sort((left, right) => (right.confidence ?? 0) - (left.confidence ?? 0));
}
