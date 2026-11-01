import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import path from "node:path";
import ts from "typescript";

/**
 * Day group headings read "Sun 12 Oct", not "2026-10-12".
 *
 * The grouping key has to stay the ISO date -- the date filter compares against
 * it and React keys on it -- so the label is a separate concern, and the raw key
 * leaked into the heading when day grouping first rendered.
 */
const source = await readFile(
  path.join(import.meta.dirname, "PredictionExplorer.tsx"),
  "utf8",
);
const fn = source.match(/function dayGroupLabel\([\s\S]*?\n}/);
assert.ok(fn, "dayGroupLabel not found -- renamed or inlined?");

const dayGroupLabel = new Function(
  `const UNDATED_GROUP = "Date to be confirmed";
   ${ts.transpileModule(fn[0], { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText}
   return dayGroupLabel;`,
)();

test("an ISO key becomes a heading a reader scans", () => {
  // 2026-10-12 is a Monday. Day-first, matching the design's "Sun 4 Oct".
  assert.equal(dayGroupLabel("2026-10-12", "en"), "Mon 12 Oct");
});

test("the date never shifts to its neighbour", () => {
  // The key is already a calendar date in the viewer's zone. Re-interpreting it
  // against a browser timezone is how a Sunday heading becomes Saturday for
  // anyone west of Greenwich, so it is anchored and formatted in UTC.
  for (const iso of ["2026-01-01", "2026-10-12", "2026-12-31"]) {
    assert.match(dayGroupLabel(iso, "en"), new RegExp(String(Number(iso.slice(8)))));
  }
});

test("the undated bucket keeps its words", () => {
  assert.equal(dayGroupLabel("Date to be confirmed", "en"), "Date to be confirmed");
});

test("an unparseable key falls back to itself rather than Invalid Date", () => {
  assert.equal(dayGroupLabel("not-a-date", "en"), "not-a-date");
});
