import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

/**
 * The "Latest settled day" line is the only place this site states how much it
 * settled on a given day, and it was reporting neither.
 *
 * It summed the rows the page happened to receive. The request asks for one
 * page of 100, so on any busy day the line read "100 settled" -- the page size,
 * not a measurement. Production on 2026-10-03 showed 100 against an API total
 * of 15,053.
 *
 * Separately, a failed request was caught into `{ total: 0, rows: [] }` and
 * rendered identically to a genuinely empty day. On 2026-10-04 the date query
 * timed out and the page published "0 settled" for a day with roughly nineteen
 * thousand settled predictions.
 *
 * Asserted on the source because the page is an async server component whose
 * render needs the API; what matters is which value reaches the label and that
 * failure stays distinguishable from emptiness. QA cannot cover this -- its
 * results API returns `total: 0` for every date -- so the guard has to live
 * somewhere that runs everywhere.
 */

const source = await readFile(new URL("./page.tsx", import.meta.url), "utf8");

test("the settled count comes from the API total, not from the rows received", () => {
  assert.match(source, /const settledOnDay = daily\?\.total \?\? 0/,
    "the day's settled count is no longer taken from the API's own total");
  assert.doesNotMatch(source, /const settledToday =[\s\S]{0,120}matches\.reduce/,
    "the settled count is being summed from the fetched page again, which reports the page size");
});

test("a failed request is not rendered as an empty day", () => {
  assert.match(source, /fetchPredictionResults\([^)]*\)\.catch\(\(\) => null\)/,
    "a failed results request is being collapsed into a value that looks like an empty day");
  assert.match(source, /dayUnavailable/,
    "nothing distinguishes a failed request from a day with nothing settled");
  assert.match(source, /could not be loaded/,
    "a failed request no longer tells the reader it failed");
});

test("a partial list says that it is partial", () => {
  // One page of 100, ordered by kickoff. Without this the list reads as the
  // whole day, which is how eight fixtures passed for 2,756.
  assert.match(source, /listIsPartial/,
    "a truncated match list is presented as if it were the complete day");
});
