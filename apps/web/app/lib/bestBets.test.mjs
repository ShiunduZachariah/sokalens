import assert from "node:assert/strict";
import test from "node:test";

import { BEST_BET_MAX, BEST_BET_MIN, bestBetCount, expandMarkets, selectBestBets } from "./bestBets.ts";

const market = (marketKey, confidence, selection = "yes") => ({ marketKey, selection, confidence });
const fixture = (matchId, markets) => ({
  matchId,
  marketKey: markets[0]?.marketKey ?? "1x2",
  selection: markets[0]?.selection ?? "home",
  confidence: markets[0]?.confidence ?? 0,
  markets,
});

test("the count follows the volume of fixtures, not the market count", () => {
  // 258 fixtures carrying nine markets each is still a 258-fixture day.
  assert.equal(bestBetCount(258), 13);
  assert.equal(bestBetCount(200), 10);
  assert.equal(bestBetCount(120), 6);
});

test("the count stays inside its bounds", () => {
  assert.equal(bestBetCount(40), BEST_BET_MIN, "a thin Tuesday still shows a shortlist");
  assert.equal(bestBetCount(2000), BEST_BET_MAX, "a huge day does not become an endless page");
});

test("the count never exceeds what the day actually has", () => {
  assert.equal(bestBetCount(3), 3);
  assert.equal(bestBetCount(0), 0);
});

test("every market becomes its own candidate", () => {
  const expanded = expandMarkets([
    fixture(1, [market("1x2", 43, "away"), market("btts", 47, "no"), market("total_goals", 51, "under_2.5")]),
  ]);

  assert.deepEqual(expanded.map((item) => item.marketKey), ["1x2", "btts", "total_goals"]);
  assert.deepEqual(expanded.map((item) => item.confidence), [43, 47, 51]);
  assert.deepEqual(expanded.map((item) => item.selection), ["away", "no", "under_2.5"]);
});

test("a fixture with no market list falls back to its primary market", () => {
  const expanded = expandMarkets([{ matchId: 7, marketKey: "1x2", selection: "home", confidence: 44 }]);

  assert.equal(expanded.length, 1);
  assert.equal(expanded[0].marketKey, "1x2");
});

test("the same fixture and market is never listed twice", () => {
  const expanded = expandMarkets([fixture(1, [market("btts", 47), market("btts", 47)])]);

  assert.equal(expanded.length, 1);
});

test("a match can appear several times, once per ranked market", () => {
  // Explicitly wanted: a strong match earns more than one place.
  const pool = [
    fixture(1, [market("1x2", 52), market("btts", 51), market("total_goals", 50)]),
    ...Array.from({ length: 99 }, (_, index) => fixture(index + 2, [market("1x2", 30 - index * 0.1)])),
  ];
  const chosen = selectBestBets(pool);
  const fromMatchOne = chosen.filter((item) => item.matchId === 1);

  assert.ok(fromMatchOne.length > 1, "a strongly ranked match should appear more than once");
  assert.deepEqual(new Set(fromMatchOne.map((item) => item.marketKey)).size, fromMatchOne.length);
});

test("no threshold: low confidence is ranked, not discarded", () => {
  // Every market on every fixture sat between 38% and 54% on 2026-09-04, so a
  // filter is the difference between a page and an empty state.
  const pool = Array.from({ length: 60 }, (_, index) => fixture(index + 1, [market("1x2", 40 + (index % 10))]));

  assert.equal(selectBestBets(pool).length, bestBetCount(pool.length));
});

test("ordered by confidence, highest first", () => {
  const chosen = selectBestBets([
    fixture(1, [market("1x2", 44)]),
    fixture(2, [market("1x2", 51)]),
    fixture(3, [market("1x2", 47)]),
  ]);

  assert.deepEqual(chosen.map((item) => item.matchId), [2, 3, 1]);
});

test("a single-market feed is not truncated by the market cap", () => {
  const pool = Array.from({ length: 200 }, (_, index) => fixture(index + 1, [market("1x2", 50 - index * 0.1)]));

  assert.equal(selectBestBets(pool).length, bestBetCount(pool.length));
});

test("a mixed feed is not dominated by one market", () => {
  const pool = Array.from({ length: 200 }, (_, index) =>
    fixture(index + 1, [market("1x2", 60 - index * 0.1), market("btts", 45 - index * 0.1)]),
  );
  const chosen = selectBestBets(pool);
  const fromOneMarket = chosen.filter((item) => item.marketKey === "1x2").length;

  assert.ok(fromOneMarket < chosen.length, "one market took the entire list");
  assert.ok(fromOneMarket <= Math.ceil(chosen.length / 2), `one market took ${fromOneMarket}`);
});

test("an empty pool selects nothing rather than throwing", () => {
  assert.deepEqual(selectBestBets([]), []);
});
