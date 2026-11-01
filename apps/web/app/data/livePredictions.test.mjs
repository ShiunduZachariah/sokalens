import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

// livePredictions.ts imports the API client with extensionless specifiers, so
// the runner cannot load it. Behaviour that deserves direct testing lives in
// app/lib/mergePredictions.ts; what remains here is structural.
const source = await readFile(new URL("./livePredictions.ts", import.meta.url), "utf8");
const wholeDayHelper = source.slice(
  source.indexOf("export async function fetchAllTodayPredictionsWithProvider"),
  source.indexOf("// providerToLivePrediction"),
);
const marketHelper = source.slice(
  source.indexOf("export async function fetchMarketPredictions"),
  source.indexOf("export async function fetchAllTodayPredictionsWithProvider"),
);

test("market hubs request one bounded page for each date, avoiding failing deep offsets", () => {
  assert.match(marketHelper, /marketFeedDates\(date, timezone, days\)/);
  assert.match(marketHelper, /dates\.map\(\(day\) =>/);
  assert.match(marketHelper, /MARKET_HUB_DAILY_LIMIT, day, 1, timezone, market/);
  assert.doesNotMatch(marketHelper, /pageCount|first\.total/);
});

test("full prediction pagination is sequential to protect PostgreSQL", () => {
  assert.doesNotMatch(wholeDayHelper, /Promise\.all/);
  assert.match(wholeDayHelper, /for \(let page = 2; page <= pageCount; page \+= 1\)/);
});

test("whole-day paging is bounded, so a bad total cannot fan out forever", () => {
  // The homepage now depends on this helper, so an unbounded page count would
  // put an unbounded number of sequential upstream calls on the busiest page.
  assert.match(wholeDayHelper, /PREDICTION_POOL_MAX_PAGES/);
  // Bounded, and large enough for a real day. The old ceiling of five was
  // written for "three pages of 100" and showed 501 of 1,439 predictions on
  // 2026-10-03; anything under ten is back to hiding most of a Saturday.
  const bound = Number(/const PREDICTION_POOL_MAX_PAGES = (\d+);/.exec(source)?.[1]);
  assert.ok(Number.isInteger(bound), "the bound must be a literal the test can read");
  assert.ok(bound >= 15 && bound <= 30, `page bound ${bound} is outside the usable range`);
});

test("the merge rule is shared, not duplicated per caller", () => {
  assert.match(source, /mergeByMatchId/);
});

test("the homepage ranks the whole day, never a kickoff-ordered prefix", async () => {
  // Europe's big five kick off at 17:00-19:05Z, which puts them at index
  // 211-247 of 258 in kickoff order. Ranking any prefix shorter than the day
  // excludes exactly the leagues the ranking exists to promote.
  const page = await readFile(new URL("../page.tsx", import.meta.url), "utf8");

  assert.match(page, /fetchTodayPredictionPool/);
  assert.doesNotMatch(page, /fetchMergedTodayPredictions\(\s*\d+\s*\)/);
});

test("Best Bets considers the whole day, as the page promises", async () => {
  const page = await readFile(new URL("../best-bets/page.tsx", import.meta.url), "utf8");

  assert.match(page, /fetchTodayPredictionPool/);
  assert.doesNotMatch(page, /fetchMergedTodayPredictions\(\s*\d+\s*\)/);
});
