import assert from "node:assert/strict";
import test from "node:test";
/*
 * The in-feed cadence.
 *
 * Asserted on the arithmetic rather than on markup, because every way this goes
 * wrong is quiet: an off-by-one puts the first ad one row early, a hidden cap
 * ends the cadence while the reader is still scrolling, and
 * a non-deterministic position reshuffles ads under the reader's thumb every time
 * infinite scroll appends a page. None of those throw.
 *
 * ## Updated 2026-10-15 for the ad placement spec
 *
 * These assertions used to pin the cadence to TEN rows, with the comment "the
 * product decision is an ad after every ten matches". Section 6 of the
 * 2026-10-15 ad spec replaces that decision with "at most one in-feed unit per
 * 8 rows", and section 3.1 names an exact first position per page instead of
 * letting the cadence pick it.
 *
 * Both halves are kept here rather than relaxed: the cadence is pinned to 8,
 * and `MIN_FEED_AD_SPACING` is pinned as a FLOOR, so a caller that asks for a
 * denser cadence is clamped instead of honoured. Note that 8 is denser than the
 * 10 it replaced -- the number is a ceiling derived from section 5's 30% page
 * density cap, not a loosening of one.
 */
import {
  FEED_AD_CADENCE,
  MIN_FEED_AD_SPACING,
  feedAdPositions,
  splitForFeedAds,
} from "./feedAds.ts";

test("the first ad comes after the eighth row, not the seventh", () => {
  assert.equal(FEED_AD_CADENCE, 8, "ad spec section 6: at most one in-feed unit per 8 rows");
  // Zero-based: index 7 IS the eighth row.
  assert.deepEqual([...feedAdPositions(25)], [7, 15, 23]);
});

test("a short feed gets no ads at all", () => {
  assert.deepEqual([...feedAdPositions(7)], [], "seven rows cannot carry an ad after the eighth");
  assert.deepEqual([...feedAdPositions(0)], []);
});

test("an ad never lands under the last row", () => {
  // Exactly eight rows would put the slot directly above the footer, reading as
  // page furniture rather than as part of the feed.
  assert.deepEqual([...feedAdPositions(8)], [], "an ad after the final row is page furniture");
  assert.deepEqual([...feedAdPositions(9)], [7], "one row below is enough to read as in-feed");
});

test("a long feed keeps its cadence to the end", () => {
  assert.deepEqual([...feedAdPositions(81)], [7, 15, 23, 31, 39, 47, 55, 63, 71, 79]);
  assert.deepEqual([...feedAdPositions(49)], [7, 15, 23, 31, 39, 47]);
  assert.deepEqual([...feedAdPositions(48)], [7, 15, 23, 31, 39]);
});

test("a caller can still impose a finite cap", () => {
  assert.deepEqual([...feedAdPositions(81, { maxBlocks: 2 })], [7, 15]);
  assert.deepEqual([...feedAdPositions(81, { maxBlocks: 0 })], []);
});

/*
 * The density cap is a promise about the RESULT, so it is enforced rather than
 * documented. A caller passing a tighter cadence -- by mistake, or because a
 * page wanted more inventory -- would otherwise break section 6 silently, and
 * an over-dense feed is exactly the kind of ad defect nobody files a bug about.
 */
test("no caller can ask for a denser cadence than the spec allows", () => {
  assert.equal(MIN_FEED_AD_SPACING, 8);
  assert.deepEqual([...feedAdPositions(25, { cadence: 2 })], [7, 15, 23],
    "a cadence tighter than one per 8 rows must be clamped, not honoured");
  assert.deepEqual([...feedAdPositions(25, { cadence: 12 })], [11, 23],
    "a sparser cadence is a caller's business and is left alone");
});

/*
 * Section 3.1 names the first position per page. Best Bets is "after the 5th
 * ranked row" and a prediction list is "after the first competition group", and
 * neither of those is eight rows down.
 */
test("a page can name the first position, and the cadence follows from it", () => {
  // Best Bets: index 4 is the fifth ranked row, hero included.
  assert.deepEqual([...feedAdPositions(30, { firstAfterIndex: 4 })], [4, 12, 20, 28]);
  // A prediction list whose first competition group holds three rows.
  assert.deepEqual([...feedAdPositions(30, { firstAfterIndex: 2 })], [2, 10, 18, 26]);
  // Spacing still holds from a named start.
  const positions = [...feedAdPositions(40, { firstAfterIndex: 4 })];
  for (let index = 1; index < positions.length; index += 1) {
    assert.ok(positions[index] - positions[index - 1] >= MIN_FEED_AD_SPACING,
      `positions ${positions[index - 1]} and ${positions[index]} are closer than 8 rows apart`);
  }
});

test("a named position with nothing below it is still dropped", () => {
  // This is why section 3.1 qualifies the prediction-list position with "only
  // if there are 2+ groups": a single group's last row IS the feed's last row,
  // and an ad there is page furniture above the footer.
  assert.deepEqual([...feedAdPositions(5, { firstAfterIndex: 4 })], []);
  assert.deepEqual([...feedAdPositions(6, { firstAfterIndex: 4 })], [4]);
});

test("positions depend only on the count, so appending rows never moves an ad", () => {
  // Infinite scroll appends; the ads already on screen must not jump.
  const first = [...feedAdPositions(40)];
  const second = [...feedAdPositions(80)];
  assert.deepEqual(second.slice(0, first.length), first,
    "appending a page moved the existing ads");
  // And it is a pure function: same input, same output, no seed, no clock.
  assert.deepEqual([...feedAdPositions(57)], [...feedAdPositions(57)]);
});

test("splitting carries the cadence across group boundaries", () => {
  const positions = feedAdPositions(25);

  // Three leagues of 8, 9 and 8 rows. The ad after row 8 falls on the first
  // league's last row, after row 16 inside the second, and after row 24 inside
  // the third -- which is the whole reason `startIndex` is the position in the
  // WHOLE feed and not in the run.
  assert.deepEqual(splitForFeedAds(Array(8).fill("a"), 0, positions).map((s) => [s.rows.length, s.adAfter]), [[8, true]]);
  assert.deepEqual(splitForFeedAds(Array(9).fill("b"), 8, positions).map((s) => [s.rows.length, s.adAfter]), [[8, true], [1, false]]);
  assert.deepEqual(splitForFeedAds(Array(8).fill("c"), 17, positions).map((s) => [s.rows.length, s.adAfter]), [[7, true], [1, false]]);
});

test("an ad at a league boundary is retained without an empty trailing list", () => {
  // The ninth row is in the next league. The first league's final segment must
  // still emit its ad after row eight.
  const positions = feedAdPositions(9);
  const segments = splitForFeedAds(Array(8).fill("x"), 0, positions);
  assert.deepEqual(segments.map((s) => [s.rows.length, s.adAfter]), [[8, true]]);
  assert.deepEqual(splitForFeedAds(["next"], 8, positions), [{ rows: ["next"], adAfter: false }]);
  for (const segment of segments) assert.ok(segment.rows.length > 0);
});

test("group boundaries preserve every ad in a forty-row prediction slate", () => {
  // A live slate split into small leagues had its cadence rows exactly at group
  // ends. The old renderer dropped all of them.
  const groupSizes = [2, 2, 3, 2, 1, 4, 3, 3, 9, 1, 3, 6, 1];
  const positions = feedAdPositions(40);
  let start = 0;
  const adEnds = [];
  for (const size of groupSizes) {
    const segments = splitForFeedAds(Array(size).fill(null), start, positions);
    let offset = 0;
    for (const segment of segments) {
      offset += segment.rows.length;
      if (segment.adAfter) adEnds.push(start + offset);
    }
    start += size;
  }
  assert.equal(start, 40);
  assert.deepEqual(adEnds, [8, 16, 24, 32]);
});

test("a Best Bets hero counts as the first feed row", () => {
  // 21 feed rows: the hero plus 20 ranked rows. Section 3.1 puts the first unit
  // after the 5th ranked row, which is feed index 4, one row into the list.
  const positions = feedAdPositions(21, { firstAfterIndex: 4 });
  const segments = splitForFeedAds(Array(20).fill(null), 1, positions);
  assert.deepEqual([...positions], [4, 12],
    "index 20 is the last feed row, so no ad lands under it");
  assert.deepEqual(segments.map((segment) => [segment.rows.length, segment.adAfter]),
    [[4, true], [8, true], [8, false]]);
});
