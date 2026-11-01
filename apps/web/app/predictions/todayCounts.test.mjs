import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const PAGE = readFileSync(
  path.resolve(path.dirname(fileURLToPath(import.meta.url)), "today", "page.tsx"),
  "utf8",
);

/**
 * Two defects on one card, both visible to any reader who checks the
 * arithmetic.
 *
 * On 2026-09-21 the slate read 205 "Predicted matches" against 200 "Total
 * fixtures" -- a number that cannot exist.
 *
 * The count came from the prediction feed's `total`, which is neither
 * deduplicated nor filtered the way the fixture count is: 16 matches that day
 * were `unknown` or `postponed`, which /v1/matches/today excludes and
 * /v1/predictions/today does not.
 *
 * Underneath it, the feed was deduplicated on `item.id`. An engine item
 * carries the prediction's numeric id; a provider fallback carries the string
 * `provider-<matchId>`; and the filter only acted when the id was a number.
 * Provider fallbacks were therefore never deduplicated against anything, so a
 * match with both an engine prediction and a fallback rendered twice.
 */

test("the feed is deduplicated on matchId, the only key both sources share", () => {
  assert.match(
    PAGE,
    /seen\.has\(item\.matchId\)/,
    "the dedupe no longer keys on matchId; engine ids and provider ids differ in type, so a match can render twice",
  );
  assert.ok(
    !/seen\.has\(item\.id\)/.test(PAGE),
    "the dedupe keys on item.id again, which never matches a provider fallback against an engine prediction",
  );
});

test("the dedupe is not gated on the id being a number", () => {
  // The exact shape of the original bug: a type guard that silently exempted
  // every provider item from deduplication.
  assert.ok(
    !/typeof item\.id === "number" && seen\.has/.test(PAGE),
    "provider items are exempt from deduplication again",
  );
});

/*
 * The two figures are asserted on the expression behind each one, never on the
 * element drawing it -- which is now the third markup this guard has outlived.
 * The 2026-09-27 refresh replaced three StatCards with a <dl>; the 2026-10-01
 * refresh moved the <dl> into PredictionListShell and left the page declaring
 * the figures as `stats` entries. The property being guarded is unchanged
 * through all three: the predicted count is the RENDERED set, so it cannot
 * exceed the fixture count, and it has.
 */
const statValue = (label) =>
  PAGE.match(new RegExp(`\\{\\s*label:\\s*"${label}",\\s*value:\\s*([^,}]+)`));

test("the predicted-matches stat counts the rendered set, not the API total", () => {
  const predicted = statValue("Predictions");
  assert.ok(predicted, "the predictions figure is no longer rendered");
  assert.equal(
    predicted[1].trim(),
    "merged.length",
    "the stat reads an API total again; it can exceed the fixture count and has",
  );
});

test("total fixtures still comes from the fixture count", () => {
  // The other half of the comparison must stay what it says it is.
  const fixtures = statValue("Fixtures");
  assert.ok(fixtures, "the fixtures figure is no longer rendered");
  assert.equal(
    fixtures[1].trim(),
    "totalFixtures",
    "the fixtures stat no longer reads the fixture count",
  );
});
