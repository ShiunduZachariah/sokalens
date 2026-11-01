import assert from "node:assert/strict";
import test from "node:test";

import { applyBestBetFilters, bestBetFacets, EMPTY_BEST_BET_FILTERS } from "./bestBetFilters.ts";

const bet = (overrides = {}) => ({
  matchId: 1,
  match: "Arsenal v Chelsea",
  league: "Premier League",
  country: "GB",
  market: "Match Result",
  prediction: "Home win",
  confidence: 50,
  ...overrides,
});

const pool = [
  bet({ matchId: 1, match: "Arsenal v Chelsea", league: "Premier League", country: "GB", market: "Match Result", confidence: 52 }),
  bet({ matchId: 1, match: "Arsenal v Chelsea", league: "Premier League", country: "GB", market: "Total Goals", confidence: 51 }),
  bet({ matchId: 2, match: "Genoa v Como", league: "Serie A", country: "IT", market: "Total Goals", confidence: 50 }),
  bet({ matchId: 3, match: "Al-Ahli v Al Riyadh", league: "Pro League", country: "SA", market: "Double Chance", confidence: 49 }),
];

test("no filters returns the list untouched and in order", () => {
  assert.deepEqual(applyBestBetFilters(pool, EMPTY_BEST_BET_FILTERS), pool);
});

test("filtering by country narrows the list", () => {
  const filtered = applyBestBetFilters(pool, { ...EMPTY_BEST_BET_FILTERS, country: "GB" });

  assert.equal(filtered.length, 2);
  assert.ok(filtered.every((item) => item.country === "GB"));
});

test("filtering by market keeps a match that qualifies on that market only", () => {
  // Arsenal appears twice on two markets; filtering to Total Goals must keep
  // the Total Goals row and drop the Match Result one.
  const filtered = applyBestBetFilters(pool, { ...EMPTY_BEST_BET_FILTERS, market: "Total Goals" });

  assert.deepEqual(filtered.map((item) => item.matchId), [1, 2]);
  assert.ok(filtered.every((item) => item.market === "Total Goals"));
});

test("league and country combine", () => {
  const filtered = applyBestBetFilters(pool, { ...EMPTY_BEST_BET_FILTERS, country: "IT", league: "Serie A" });

  assert.equal(filtered.length, 1);
  assert.equal(filtered[0].matchId, 2);
});

test("search matches team, league or market, case-insensitively", () => {
  assert.equal(applyBestBetFilters(pool, { ...EMPTY_BEST_BET_FILTERS, search: "chelsea" }).length, 2);
  assert.equal(applyBestBetFilters(pool, { ...EMPTY_BEST_BET_FILTERS, search: "SERIE" }).length, 1);
  assert.equal(applyBestBetFilters(pool, { ...EMPTY_BEST_BET_FILTERS, search: "double" }).length, 1);
});

test("a filter that matches nothing returns empty rather than everything", () => {
  assert.deepEqual(applyBestBetFilters(pool, { ...EMPTY_BEST_BET_FILTERS, search: "nothing here" }), []);
});

test("facets are derived from the list, deduplicated and sorted", () => {
  const facets = bestBetFacets(pool);

  assert.deepEqual(facets.countries, ["GB", "IT", "SA"]);
  assert.deepEqual(facets.markets, ["Double Chance", "Match Result", "Total Goals"]);
});

test("league facets follow the selected country", () => {
  // Offering every league regardless of country is how you get an empty result
  // from two sensible-looking choices.
  assert.deepEqual(bestBetFacets(pool, "IT").leagues, ["Serie A"]);
  assert.deepEqual(bestBetFacets(pool, "all").leagues, ["Premier League", "Pro League", "Serie A"]);
});
