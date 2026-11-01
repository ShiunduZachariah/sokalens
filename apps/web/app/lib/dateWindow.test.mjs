import assert from "node:assert/strict";
import test from "node:test";

import { buildDayWindow, formatDayTabLabel, resolveSelectedDay } from "./dateWindow.ts";

test("buildDayWindow creates today plus the next 7 days (no past days)", () => {
  const window = buildDayWindow(new Date("2026-05-13T09:00:00.000Z"), "Africa/Nairobi");

  assert.equal(window.length, 8);
  assert.equal(window[0]?.isoDate, "2026-05-13");
  assert.equal(window[1]?.isoDate, "2026-05-14");
  assert.equal(window[7]?.isoDate, "2026-05-20");
});

test("formatDayTabLabel prefers Today and short weekday labels", () => {
  const today = new Date("2026-05-13T09:00:00.000Z");

  assert.equal(formatDayTabLabel("2026-05-13", today, "Africa/Nairobi"), "Today");
  assert.equal(formatDayTabLabel("2026-05-14", today, "Africa/Nairobi"), "Thu");
  assert.equal(formatDayTabLabel("2026-05-20", today, "Africa/Nairobi"), "next Wed");
});

test("resolveSelectedDay accepts explicit dates and relative day keywords", () => {
  const now = new Date("2026-05-13T09:00:00.000Z");

  assert.equal(resolveSelectedDay({ date: "2026-05-10" }, now, "Africa/Nairobi"), "2026-05-10");
  assert.equal(resolveSelectedDay({ day: "yesterday" }, now, "Africa/Nairobi"), "2026-05-12");
  assert.equal(resolveSelectedDay({ day: "tomorrow" }, now, "Africa/Nairobi"), "2026-05-14");
});
