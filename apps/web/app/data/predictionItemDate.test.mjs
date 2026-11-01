import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import path from "node:path";
import ts from "typescript";

/**
 * `viewerDate` is what makes the explorer's day grouping work at all.
 *
 * The control, the date filter and the grouping key all read `item.date`, and
 * nothing populated it -- so `spansDays` was permanently false, the "Day"
 * button could never render, and grouping by day would have filed every row
 * under "Date to be confirmed". The feature was built and then starved.
 *
 * Extracted and transpiled rather than imported: the module pulls in i18n and
 * SEO helpers that a unit test has no business loading.
 */
const source = await readFile(
  path.join(import.meta.dirname, "predictionItems.ts"),
  "utf8",
);
const fn = source.match(/function viewerDate\([\s\S]*?\n}/);
assert.ok(fn, "viewerDate not found -- renamed or inlined?");

const viewerDate = new Function(
  `${ts.transpileModule(fn[0], { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText}; return viewerDate;`,
)();

test("the date is the one the reader is standing in, not UTC", () => {
  // 23:30 UTC on the 11th is already the 12th in Nairobi (UTC+3). Taking the
  // date off the ISO string would file this fixture under the wrong day for
  // every reader east of Greenwich.
  const instant = "2026-10-11T23:30:00Z";
  assert.equal(viewerDate(instant, "UTC"), "2026-10-11");
  assert.equal(viewerDate(instant, "Africa/Nairobi"), "2026-10-12");
});

test("a zone behind UTC pulls the date back", () => {
  // 01:00 UTC on the 12th is still the 11th in New York.
  assert.equal(viewerDate("2026-10-12T01:00:00Z", "America/New_York"), "2026-10-11");
});

test("a missing or unparseable kick-off yields no date", () => {
  // Undefined, not a guess: the explorer treats an absent date as "to be
  // confirmed" and filters it out of the day list, which is correct. Inventing
  // today's date here would file an undated fixture under today.
  assert.equal(viewerDate(null, "UTC"), undefined);
  assert.equal(viewerDate(undefined, "UTC"), undefined);
  assert.equal(viewerDate("", "UTC"), undefined);
  assert.equal(viewerDate("not a date", "UTC"), undefined);
});

test("the shape is the ISO calendar date the explorer compares on", () => {
  // The filter compares `item.date === deferredDate` against values it derived
  // from this same function, so the format has to be stable and zero-padded.
  assert.match(viewerDate("2026-01-05T12:00:00Z", "UTC"), /^\d{4}-\d{2}-\d{2}$/);
  assert.equal(viewerDate("2026-01-05T12:00:00Z", "UTC"), "2026-01-05");
});
