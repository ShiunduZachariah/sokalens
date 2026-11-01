import assert from "node:assert/strict";
import test from "node:test";

import { LIVE_CLOCK_TRUST_WINDOW_MS, isClockBearing, liveClockIsCurrent } from "./liveClock.ts";

const at = (iso) => Date.parse(iso);

test("a freshly confirmed clock is shown", () => {
  const now = at("2026-09-06T15:10:00Z");
  assert.equal(
    liveClockIsCurrent({ status: "second_half", rawStatus: "2H", liveStateAt: "2026-09-06T15:09:30Z" }, now),
    true,
  );
});

test("the reported defect: an hours-old minute is not current", () => {
  // "Second half, 67'" three hours after the last confirmation.
  const now = at("2026-09-06T18:00:00Z");
  assert.equal(
    liveClockIsCurrent({ status: "second_half", rawStatus: "2H", liveStateAt: "2026-09-06T15:00:00Z" }, now),
    false,
  );
});

test("the window absorbs a missed poll but not a stalled feed", () => {
  const confirmed = "2026-09-06T15:00:00Z";
  const justInside = at(confirmed) + LIVE_CLOCK_TRUST_WINDOW_MS - 1000;
  const justOutside = at(confirmed) + LIVE_CLOCK_TRUST_WINDOW_MS + 1000;
  const match = { status: "second_half", rawStatus: "2H", liveStateAt: confirmed };
  assert.equal(liveClockIsCurrent(match, justInside), true);
  assert.equal(liveClockIsCurrent(match, justOutside), false);
});

test("half-time shows a label, not a minute, so it never goes stale", () => {
  // A match sitting at HT for fifteen minutes is just half-time. Reporting
  // "awaiting update" there would invent a problem.
  const now = at("2026-09-06T16:00:00Z");
  for (const rawStatus of ["HT", "BT", "P", "PEN"]) {
    assert.equal(isClockBearing({ status: "halftime", rawStatus, liveStateAt: "2026-09-06T15:00:00Z" }), false);
    assert.equal(
      liveClockIsCurrent({ status: "halftime", rawStatus, liveStateAt: "2026-09-06T15:00:00Z" }, now),
      true,
      rawStatus,
    );
  }
});

test("finished and upcoming matches make no claim about now", () => {
  const now = at("2026-09-08T12:00:00Z");
  for (const status of ["finished", "upcoming", "postponed", "unknown"]) {
    assert.equal(
      liveClockIsCurrent({ status, rawStatus: "FT", liveStateAt: "2026-09-06T15:00:00Z" }, now),
      true,
      status,
    );
  }
});

test("a row with no recorded confirmation is trusted, not blanked", () => {
  // Every row written before the freshness column existed looks like this.
  // Showing "awaiting update" across the whole back catalogue would be a worse
  // lie than the one being fixed; the server-side reaper bounds these instead.
  const now = at("2026-09-06T18:00:00Z");
  assert.equal(
    liveClockIsCurrent({ status: "second_half", rawStatus: "2H", liveStateAt: "" }, now),
    true,
  );
  assert.equal(
    liveClockIsCurrent({ status: "second_half", rawStatus: "2H", liveStateAt: "not a timestamp" }, now),
    true,
  );
});

test("clock skew does not read as staleness", () => {
  const now = at("2026-09-06T15:00:00Z");
  assert.equal(
    liveClockIsCurrent({ status: "second_half", rawStatus: "2H", liveStateAt: "2026-09-06T15:00:30Z" }, now),
    true,
  );
});
