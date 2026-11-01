/**
 * Filtering for the Best Bets shortlist.
 *
 * Kept as pure functions, separate from the client component, so the rules are
 * testable without rendering. Mirrors the Prediction Explorer's controls --
 * search, country, league, market -- so the two pages behave the same way.
 *
 * The list is already ranked by confidence, so filtering must PRESERVE order
 * rather than re-sort: the whole point of the page is the ranking.
 */

export type FilterableBet = {
  match: string;
  league: string;
  country: string;
  market: string;
  prediction?: string;
};

export type BestBetFilters = {
  search: string;
  country: string;
  league: string;
  market: string;
};

export const EMPTY_BEST_BET_FILTERS: BestBetFilters = {
  search: "",
  country: "all",
  league: "all",
  market: "all",
};

const matchesFacet = (value: string, selected: string) => selected === "all" || value === selected;

export function applyBestBetFilters<T extends FilterableBet>(
  items: readonly T[],
  filters: BestBetFilters,
): T[] {
  const needle = filters.search.trim().toLowerCase();

  return items.filter((item) => {
    if (!matchesFacet(item.country, filters.country)) return false;
    if (!matchesFacet(item.league, filters.league)) return false;
    if (!matchesFacet(item.market, filters.market)) return false;
    if (!needle) return true;

    return [item.match, item.league, item.market, item.prediction ?? ""]
      .join(" ")
      .toLowerCase()
      .includes(needle);
  });
}

/**
 * The options to offer. Leagues are narrowed to the selected country: offering
 * every league regardless is how a reader picks two sensible-looking values and
 * gets an empty page.
 */
export function bestBetFacets<T extends FilterableBet>(items: readonly T[], country = "all") {
  const unique = (values: string[]) => [...new Set(values.filter(Boolean))].sort((a, b) => a.localeCompare(b));

  return {
    countries: unique(items.map((item) => item.country)),
    leagues: unique(
      items.filter((item) => matchesFacet(item.country, country)).map((item) => item.league),
    ),
    markets: unique(items.map((item) => item.market)),
  };
}
