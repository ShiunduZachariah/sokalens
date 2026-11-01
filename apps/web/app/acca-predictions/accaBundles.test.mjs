import assert from "node:assert/strict";
import test from "node:test";
import { ACCA_PAGE_SIZE, MIN_ACCA_COMBINED_FAIR_ODDS, MIN_ACCA_LEG_FAIR_ODDS, buildAccaBundles, minimumSystemAccaCombinedFairOdds, paginateAccaBundles } from "./accaBundles.ts";

function prediction(id, marketKey = "total_goals", selection = "over_2.5", country = "England") {
  return { id, matchId: id, matchTitle: `Home ${id} vs Away ${id}`, homeTeam: `Home ${id}`, awayTeam: `Away ${id}`, homeTeamLogo: "", awayTeamLogo: "", leagueName: "League", country, kickoffAt: `2026-08-${String(24 + id).padStart(2, "0")}T12:00:00Z`, matchStatus: "scheduled", marketKey, selection, confidence: 70, probability: .65, isBestBet: true, publishedVersion: 1, publishedAt: "2026-08-24T00:00:00Z", selectionOdds: null, markets: [{ marketKey, selection, confidence: 70, probability: .65, isBestBet: true }] };
}

test("catalogue grows from eligible fixtures instead of returning three hard-coded bundles", () => {
  const bundles = buildAccaBundles(Array.from({ length: 6 }, (_, index) => prediction(index + 1)));
  assert.ok(bundles.length > 3);
  assert.ok(bundles.some((bundle) => bundle.marketKey === "total_goals"));
  assert.ok(bundles.every((bundle) => new Set(bundle.legs.map((leg) => leg.matchId)).size === bundle.legs.length));
});

test("selections without a usable probability never enter a system ACCA", () => {
  const items = [prediction(1), prediction(2), prediction(3)];
  items[1].markets[0].probability = 0;
  const bundles = buildAccaBundles(items);
  assert.ok(bundles.length > 0);
  assert.ok(bundles.every((bundle) => bundle.legs.every((leg) => leg.matchId !== 2)));
});

test("catalogue pagination bounds the payload without capping the generated catalogue", () => {
  const bundles = buildAccaBundles(Array.from({ length: 80 }, (_, index) => prediction(index + 1)));
  const first = paginateAccaBundles(bundles);
  const second = paginateAccaBundles(bundles, 2);
  assert.ok(bundles.length > ACCA_PAGE_SIZE);
  assert.equal(first.bundles.length, ACCA_PAGE_SIZE);
  assert.equal(first.total, bundles.length);
  assert.equal(second.page, 2);
  assert.notEqual(first.bundles[0].id, second.bundles[0].id);
});

test("ACCA pages keep the initial interaction tree bounded", () => {
  assert.equal(ACCA_PAGE_SIZE, 10);
});

test("ACCA legs and bundles enforce meaningful fair-odds floors", () => {
  const weakLegs = Array.from({ length: 8 }, (_, index) => prediction(index + 1));
  weakLegs.forEach((item) => { item.markets[0].probability = 0.95; });
  assert.equal(buildAccaBundles(weakLegs).length, 0);

  const bundles = buildAccaBundles(Array.from({ length: 8 }, (_, index) => prediction(index + 1)));
  assert.ok(bundles.length > 0);
  assert.ok(bundles.every((bundle) => bundle.combinedFairOdds >= minimumSystemAccaCombinedFairOdds(bundle.legs.length)));
  assert.ok(bundles.every((bundle) => bundle.legs.every((leg) => (1 / leg.probability) >= MIN_ACCA_LEG_FAIR_ODDS)));
});

test("system ACCA combined-odds floor scales with leg count", () => {
  assert.equal(minimumSystemAccaCombinedFairOdds(2), MIN_ACCA_COMBINED_FAIR_ODDS);
  assert.equal(minimumSystemAccaCombinedFairOdds(4), 2.25);
  assert.equal(minimumSystemAccaCombinedFairOdds(10), 7.59375);
  assert.ok(Math.abs(minimumSystemAccaCombinedFairOdds(20) - 57.6650390625) < 1e-9);
});

test("ACCA labels never expose backend market keys", () => {
  const bundle = buildAccaBundles(Array.from({ length: 5 }, (_, index) => prediction(index + 1)))
    .find((item) => item.marketKey === "total_goals");
  assert.ok(bundle);
  assert.match(bundle.label, /Total Goals/);
  assert.doesNotMatch(bundle.label, /total_goals/);
});
