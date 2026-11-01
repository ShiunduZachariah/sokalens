import assert from "node:assert/strict";
import test from "node:test";
import { summarizeFeaturedAcca } from "./featuredAcca.ts";

const bundleWithLegs = (count, overrides = {}) => ({
  id: `bundle-${count}`, legs: Array.from({ length: count }, (_, i) => ({ leagueName: i % 2 ? "Premier League" : "La Liga", marketKey: i % 2 ? "btts" : "1x2", ...overrides })),
  ...overrides,
});

test("2-leg ACCA shows both legs and no artificial remainder", () => {
  const summary = summarizeFeaturedAcca(bundleWithLegs(2));
  assert.equal(summary.previewLegs.length, 2);
  assert.equal(summary.remainderLabel, "All 2 selections shown");
});

test("20-leg ACCA stays bounded to two previews", () => {
  const summary = summarizeFeaturedAcca(bundleWithLegs(20));
  assert.equal(summary.previewLegs.length, 2);
  assert.equal(summary.remainderLabel, "+18 more selections");
});

test("summary counts unique factual league and market metadata", () => {
  const summary = summarizeFeaturedAcca(bundleWithLegs(5));
  assert.equal(summary.previewLegs.length, 2);
  assert.equal(summary.remainderLabel, "+3 more selections");
  assert.equal(summary.leagueCount, 2);
  assert.equal(summary.marketCount, 2);
});

test("blank optional metadata does not create fake league or market counts", () => {
  const summary = summarizeFeaturedAcca(bundleWithLegs(2, { leagueName: "", marketKey: "" }));
  assert.equal(summary.previewLegs.length, 2);
  assert.equal(summary.leagueCount, 0);
  assert.equal(summary.marketCount, 0);
  assert.equal(summary.remainderLabel, "All 2 selections shown");
});
