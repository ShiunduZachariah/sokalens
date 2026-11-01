import assert from "node:assert/strict";
import test from "node:test";
import { marketFeedDates } from "./marketFeedDates.ts";

test("market hub dates cover the viewer's seven calendar days across month end", () => {
  const dates = marketFeedDates(undefined, "Africa/Nairobi", 7, new Date("2026-10-18T21:30:00Z"));
  assert.deepEqual(dates, [
    "2026-10-19", "2026-10-20", "2026-10-21", "2026-10-22",
    "2026-10-23", "2026-10-24", "2026-10-25",
  ]);
  assert.deepEqual(marketFeedDates("2026-10-29", "Europe/London", 5), [
    "2026-10-29", "2026-10-30", "2026-10-31", "2026-11-01", "2026-11-02",
  ]);
});

test("the horizon request remains bounded", () => {
  assert.equal(marketFeedDates("2026-10-19", "Africa/Nairobi", 100).length, 7);
  assert.equal(marketFeedDates("2026-10-19", "Africa/Nairobi", 0).length, 1);
});
