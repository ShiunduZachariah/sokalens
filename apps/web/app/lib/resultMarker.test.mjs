import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import path from "node:path";

// The module is TypeScript; strip the types to exercise the real logic rather
// than assert on source text, since these mappings are the whole point.
const source = await readFile(path.join(import.meta.dirname, "resultMarker.ts"), "utf8");
const js = source
  .replace(/^export type [\s\S]*?;$/gm, "")
  .replace(/^export type .*$/gm, "")
  .replace(/: Record<string, ResultSide>/g, "")
  .replace(/: ResultSide \| null/g, "")
  .replace(/: ResultCell\[\] \| null/g, "")
  .replace(/\(marketKey: string, selection: string\)/g, "(marketKey, selection)")
  .replace(/^export /gm, "");
const { resultSide, resultCells } = await import(
  `data:text/javascript,${encodeURIComponent(`${js}\nexport { resultSide, resultCells };`)}`
);

test("match-result selections map to a side across provider spellings", () => {
  for (const value of ["1", "home", "Home", "HOME_WIN", "Home win"]) {
    assert.equal(resultSide("1x2", value), "home", value);
  }
  for (const value of ["x", "X", "draw", "Tie"]) {
    assert.equal(resultSide("1x2", value), "draw", value);
  }
  for (const value of ["2", "away", "AWAY_WIN", "Away win"]) {
    assert.equal(resultSide("1x2", value), "away", value);
  }
});

test("a non-result market has no 1/X/2 marker", () => {
  // The regression: drawing three empty cells beside a BTTS pick, which reads
  // as "none of these outcomes" -- a claim about the match that was not made.
  for (const market of ["btts", "total_goals", "total_corners", "total_cards", "double_chance"]) {
    assert.equal(resultCells(market, "yes"), null, market);
    assert.equal(resultSide(market, "over_2.5"), null, market);
  }
});

test("an unrecognised selection returns no marker rather than guessing", () => {
  assert.equal(resultSide("1x2", "home_or_draw"), null);
  assert.equal(resultSide("1x2", ""), null);
  assert.equal(resultSide("1x2", "3"), null);
});

test("exactly one cell is picked, and the order is always 1 X 2", () => {
  const cells = resultCells("1x2", "draw");
  assert.deepEqual(cells.map((cell) => cell.key), ["1", "X", "2"]);
  assert.deepEqual(cells.map((cell) => cell.picked), [false, true, false]);
  assert.equal(cells.filter((cell) => cell.picked).length, 1);
});

test("every cell carries an accessible label, not just a character", () => {
  for (const cell of resultCells("1x2", "home")) {
    assert.ok(cell.label.length > 1, `cell ${cell.key} needs a readable label`);
  }
});
