import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { HIGH_CONFIDENCE, matchTags, selectionTags, slipTags } from "./recommendationTags.ts";

const appDir = path.resolve(import.meta.dirname, "..");
const read = (relative) => readFile(path.join(appDir, relative), "utf8");

/*
 * The recommendation tags.
 *
 * A tag is a claim about a prediction, printed next to it. The risk this file
 * guards is not a rendering bug -- it is a tag that keeps rendering after the
 * thing it asserts has stopped being true, which looks exactly like a working
 * tag.
 */

const selection = (overrides = {}) => ({
  confidence: 72,
  country: "GB",
  isBestBet: false,
  leagueName: "Premier League",
  probability: 0.5,
  ...overrides,
});

test("the high-confidence threshold is the one the prose already uses", async () => {
  // matchPreview.ts writes "a high reading for this model" at 70. A chip
  // drawing the line elsewhere would contradict the paragraph beneath it on
  // the same page.
  const preview = await read("lib/matchPreview.ts");
  assert.match(preview, new RegExp(`confidence >= ${HIGH_CONFIDENCE}`),
    "the tag threshold and the prose threshold must be the same number");
  assert.equal(HIGH_CONFIDENCE, 70);
});

test("there is no value tag, because nothing here can support one", async () => {
  // Value means the price is better than it should be, which needs a
  // bookmaker's odds to compare against. Fair odds alone cannot say it.
  const source = await read("lib/recommendationTags.ts");
  assert.doesNotMatch(source, /label: "Value/);
  assert.doesNotMatch(source, /label: "[^"]*Edge/i);
});

test("a Best Bet and a high reading are the only tags that get the accent fill", () => {
  const tags = selectionTags(selection({ isBestBet: true, confidence: 90, probability: 0.9 }));
  const strong = tags.filter((tag) => tag.tone === "strong").map((tag) => tag.key);
  assert.deepEqual(strong, ["best-bet", "high-confidence"]);
  // Everything else is derived, and a card of four filled chips ranks nothing.
  assert.ok(tags.filter((tag) => tag.tone === "quiet").length > 0);
});

test("every tag names the figure it came from", () => {
  const all = [
    ...selectionTags(selection({ isBestBet: true, probability: 0.9 })),
    ...selectionTags(selection({ confidence: 20, probability: 0.1 })),
    ...matchTags([selection({ isBestBet: true }), selection(), selection(), selection(), selection()]),
    ...slipTags({
      combinedFairOdds: 12,
      legs: [
        selection({ kickoffAt: "2026-10-02T12:00:00Z", marketKey: "btts" }),
        selection({ kickoffAt: "2026-10-03T12:00:00Z", marketKey: "1x2" }),
      ],
    }),
  ];
  assert.ok(all.length > 8, "the fixtures above should exercise most tags");
  for (const tag of all) {
    assert.ok(tag.detail.trim().length > 20, `${tag.key} needs a detail that explains it`);
    assert.ok(tag.label.trim().length > 0);
  }
});

test("confidence below the threshold earns no confidence tag", () => {
  const keys = selectionTags(selection({ confidence: HIGH_CONFIDENCE - 1 })).map((tag) => tag.key);
  assert.ok(!keys.includes("high-confidence"));
  assert.ok(selectionTags(selection({ confidence: HIGH_CONFIDENCE })).map((tag) => tag.key).includes("high-confidence"),
    "the threshold is inclusive, as the prose version is");
});

test("a league outside the ranked list is not called a top competition", () => {
  // The bug this pins is the one competitionOrder.ts was written to fix: a
  // substring match made "Kenyan Premier League" rank with England's.
  const ranked = selectionTags(selection({ country: "GB", leagueName: "Premier League" }));
  const unranked = selectionTags(selection({ country: "KE", leagueName: "Premier League" }));
  assert.ok(ranked.map((tag) => tag.key).includes("top-competition"));
  assert.ok(!unranked.map((tag) => tag.key).includes("top-competition"));
});

test("short price and long shot are exclusive, and neither fires in the middle", () => {
  const keys = (probability) => selectionTags(selection({ probability })).map((tag) => tag.key);
  assert.ok(keys(0.8).includes("short-price"));      // 1.25
  assert.ok(keys(0.1).includes("long-shot"));        // 10.0
  const middle = keys(0.4);                          // 2.50
  assert.ok(!middle.includes("short-price"));
  assert.ok(!middle.includes("long-shot"));
  // A selection with no probability has no price, so it gets neither.
  const none = selectionTags(selection({ probability: 0 })).map((tag) => tag.key);
  assert.ok(!none.includes("short-price") && !none.includes("long-shot"));
});

test("a match's tags come from its strongest market, not its first", () => {
  const tags = matchTags([
    selection({ confidence: 30 }),
    selection({ confidence: 85 }),
  ]);
  const confidence = tags.find((tag) => tag.key === "high-confidence");
  assert.ok(confidence, "the 85% market should earn the tag even though the 30% one is listed first");
  assert.match(confidence.detail, /85%/);
});

test("a match with no selections has no tags", () => {
  assert.deepEqual(matchTags([]), []);
  assert.deepEqual(slipTags({ combinedFairOdds: 3, legs: [] }), []);
});

test("every leg must clear the threshold for a slip to claim it", () => {
  // The average would hide one weak leg behind four strong ones, and on an
  // ACCA the weak leg is the one that settles it.
  const leg = (confidence) => selection({ confidence, kickoffAt: "2026-10-02T12:00:00Z", marketKey: "btts" });
  const strong = slipTags({ combinedFairOdds: 3, legs: [leg(90), leg(88)] }).map((tag) => tag.key);
  const mixed = slipTags({ combinedFairOdds: 3, legs: [leg(90), leg(40)] }).map((tag) => tag.key);
  assert.ok(strong.includes("all-high-confidence"));
  assert.ok(!mixed.includes("all-high-confidence"));
});

test("a slip says how many markets and how many days it spans", () => {
  const leg = (marketKey, kickoffAt) => selection({ marketKey, kickoffAt, confidence: 40 });
  const oneDayOneMarket = slipTags({
    combinedFairOdds: 3,
    legs: [leg("btts", "2026-10-02T12:00:00Z"), leg("btts", "2026-10-02T18:00:00Z")],
  }).map((tag) => tag.key);
  assert.ok(oneDayOneMarket.includes("single-market"));
  assert.ok(oneDayOneMarket.includes("same-day"));

  const spread = slipTags({
    combinedFairOdds: 3,
    legs: [leg("btts", "2026-10-02T12:00:00Z"), leg("1x2", "2026-10-04T18:00:00Z")],
  });
  assert.ok(spread.map((tag) => tag.key).includes("mixed-markets"));
  const days = spread.find((tag) => tag.key === "spans-days");
  assert.equal(days.label, "Over 2 days");
});

test("the day count comes from the ISO string, not a parsed local date", () => {
  // Same trap as the detail page's grouping: a 23:30Z kick-off is the next day
  // in any timezone east of UTC, so a server and a browser would disagree on
  // whether this slip settles in a day.
  const tags = slipTags({
    combinedFairOdds: 3,
    legs: [
      selection({ marketKey: "btts", kickoffAt: "2026-10-02T23:30:00Z", confidence: 40 }),
      selection({ marketKey: "btts", kickoffAt: "2026-10-02T23:45:00Z", confidence: 40 }),
    ],
  }).map((tag) => tag.key);
  assert.ok(tags.includes("same-day"));
});

test("the tags are capped where they are rendered, and ordered before cutting", async () => {
  const component = await read("components/RecommendationTags.tsx");
  assert.match(component, /max = 3/);
  assert.match(component, /tags\.slice\(0, max\)/,
    "truncation must keep the first tags, which the tag functions order most-asserted first");
  assert.match(component, /if \(tags\.length === 0\) return null;/);
});

test("the builder tags read the same fields its ordering does", async () => {
  const builder = await read("multi-match-builder/BuilderClient.tsx");
  // The ranking is isBestBet + confidence + probability. Tagging from anything
  // else would describe a ranking the reader is not looking at.
  assert.match(builder, /\(item\.isBestBet \? 100 : 0\) \+ item\.confidence \+ \(item\.probability \* 20\)/);
  assert.match(builder, /<RecommendationTags tags=\{matchTags\(items\)\} \/>/);
});

test("the generator shows slip tags and per-leg tags", async () => {
  const generator = await read("components/AccaGenerator.tsx");
  assert.match(generator, /<RecommendationTags max=\{4\} tags=\{slipTags\(bundle\)\} \/>/);
  assert.match(generator, /<RecommendationTags max=\{2\} tags=\{selectionTags\(leg\)\} \/>/);
});
