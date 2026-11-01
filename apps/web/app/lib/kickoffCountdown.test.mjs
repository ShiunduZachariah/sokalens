import assert from "node:assert/strict";
import test from "node:test";

import { kickoffCountdown, startingSoon } from "./kickoffCountdown.ts";

const now = new Date("2026-09-22T12:00:00.000Z");
const match = (over = {}) => ({
  kickoffAt: "2026-09-22T12:30:00.000Z",
  status: "upcoming",
  homeTeam: "Home",
  awayTeam: "Away",
  ...over,
});

test("the same fixture arriving under two ids is listed once", () => {
  // The catalogue holds 706 duplicated team names across 776 surplus rows, so
  // one fixture can arrive twice with different away-team ids. "Korea Republic
  // U23 v Saudi Arabia U23" appeared twice at the same minute in the same
  // competition, and both rows were real. Two fixtures between the same named
  // sides at the same instant cannot both be played.
  const listed = startingSoon([match(), match()], { now });
  assert.equal(listed.length, 1);
});

test("a genuine double-header at different times is kept", () => {
  // The guard keys on the kickoff too, so two meetings of the same sides on
  // the same day stay two rows.
  const listed = startingSoon(
    [match(), match({ kickoffAt: "2026-09-22T12:45:00.000Z" })],
    { now },
  );
  assert.equal(listed.length, 2);
});

test("a countdown never reads negative", () => {
  // Past kickoff but not yet reported live: the provider has not caught up.
  assert.equal(kickoffCountdown("2026-09-22T11:59:00.000Z", now), "Kicking off");
  assert.equal(kickoffCountdown("2026-09-22T12:25:00.000Z", now), "25 min");
  assert.equal(kickoffCountdown("2026-09-22T13:55:00.000Z", now), "1h 55m");
});
