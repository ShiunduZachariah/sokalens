import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { MATCH_STAT_ROWS, parseMatchStatistics } from "../../lib/matchStatistics.ts";

/**
 * The Stats tab, and the distinction the whole contract rests on.
 *
 * MatchTabs.test.mjs withheld this tab because "the data does not exist yet,
 * and an empty tab is a promise the page cannot keep", noting that adding it
 * needs its own test. This is that test.
 *
 * The data does exist -- fixture_statistics carries 5,715 fixtures in
 * production, two rows each -- but the harder half is that ABSENT and ZERO are
 * different answers and the database proves it: red_cards is explicitly 0 in
 * 4,371 rows and NULL in 5,710. A match where nobody was sent off and a match
 * where nobody counted are not the same fixture. Rendering both as "0" invents
 * a measurement, and a reader cannot tell afterwards which they were shown.
 */

test("an unreported value stays null rather than becoming zero", () => {
  const stats = parseMatchStatistics({
    home: { possession_pct: 39, shots_total: 6, yellow_cards: 0 },
    away: { possession_pct: 61, shots_total: 14, yellow_cards: 2 },
  });
  assert.ok(stats, "a block with values parsed to nothing");
  assert.equal(stats.home.redCards, null, "an absent red card count became a number");
  assert.equal(stats.home.corners, null, "an absent corner count became a number");
  assert.equal(stats.home.yellowCards, 0, "a reported zero was lost; it is a real measurement");
  assert.equal(stats.home.possessionPct, 39);
});

test("a block where neither side reported anything is not a tab", () => {
  // Otherwise the page offers eight rows of dashes, which reads as broken
  // rather than absent -- the exact failure the original assertion guarded.
  assert.equal(parseMatchStatistics({ home: {}, away: {} }), null);
  assert.equal(parseMatchStatistics(null), null);
  assert.equal(parseMatchStatistics(undefined), null);
});

test("the rows are the eight the design draws, in its order", () => {
  assert.deepEqual(
    MATCH_STAT_ROWS.map((row) => row.label),
    ["Possession", "Total shots", "Shots on target", "Corners", "Fouls", "Offsides", "Yellow cards", "Red cards"],
  );
  // Possession is the only one carrying a unit; the rest are counts.
  assert.equal(MATCH_STAT_ROWS.find((row) => row.key === "possessionPct").unit, "%");
});

test("the table prints a dash for absent values and omits the bar", async () => {
  const source = await readFile(new URL("./MatchStats.tsx", import.meta.url), "utf8");
  assert.match(source, /=== null \? "—"/,
    "an unreported value no longer renders as a dash, so it will print a number nobody measured");
  assert.match(source, /const comparable =/,
    "the bar is drawn without checking both sides reported");
  assert.match(source, /comparable \? \(/,
    "a one-sided row still draws a bar, which reads as total dominance rather than missing data");
});
