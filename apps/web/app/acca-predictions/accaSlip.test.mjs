import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import path from "node:path";
import {
  accaSlipView,
  groupLegsByDay,
  legDisplayState,
  legStateLabels,
  settlingOdds,
  slipMarketLabel,
  slipWindowLabel,
} from "./accaSlip.ts";

const appDir = path.resolve(import.meta.dirname, "..");
const read = (relative) => readFile(path.join(appDir, relative), "utf8");

/*
 * The ACCA detail page's reading of a slip.
 *
 * Three of these assertions exist because the thing they pin is invisible when
 * it breaks. A leg mis-graded as `live` still renders a pill; a `void` leg left
 * in the settling price still renders a number; a day grouping that reads the
 * local clock still renders days. All three produce a page that looks correct
 * and says something false about a slip whose outcome a reader can check for
 * themselves.
 */

const NOW = Date.parse("2026-10-02T18:00:00Z");
const leg = (overrides = {}) => ({
  id: 1,
  matchId: 1,
  homeTeam: "Home",
  awayTeam: "Away",
  homeTeamLogo: "",
  awayTeamLogo: "",
  leagueName: "League",
  country: "England",
  kickoffAt: "2026-10-02T19:00:00Z",
  matchStatus: "scheduled",
  marketKey: "btts",
  selection: "yes",
  confidence: 70,
  probability: 0.6,
  isBestBet: false,
  fairOdds: 1.67,
  ...overrides,
});

test("a graded leg keeps its grade, whatever the clock says", () => {
  for (const status of ["won", "lost", "void"]) {
    // Kick-off long past: the clock would call a pending leg live here, and the
    // grade must still win. Reading the clock first would show a settled leg as
    // in progress for as long as the match window lasted.
    assert.equal(legDisplayState(leg({ status, kickoffAt: "2026-10-02T17:00:00Z" }), NOW), status);
  }
});

test("a pending leg is live only inside the match window after kick-off", () => {
  // Still to come.
  assert.equal(legDisplayState(leg({ kickoffAt: "2026-10-02T19:00:00Z" }), NOW), "pending");
  // Kicked off an hour ago: being played.
  assert.equal(legDisplayState(leg({ kickoffAt: "2026-10-02T17:00:00Z" }), NOW), "live");
  // Kicked off two days ago and still ungraded. Calling that "live" would be a
  // claim the reader can disprove by looking at the fixture.
  assert.equal(legDisplayState(leg({ kickoffAt: "2026-09-30T17:00:00Z" }), NOW), "pending");
  // An unparseable kick-off cannot place the leg in time at all.
  assert.equal(legDisplayState(leg({ kickoffAt: "" }), NOW), "pending");
});

test("an unknown status is not passed through as a state", async () => {
  // The parse layer is what rejects it; this pins that the two agree, because a
  // status the page has no pill for renders an unstyled empty pill.
  const api = await read("data/accaApi.ts");
  assert.match(api, /legStatuses: AccaLegStatus\[\] = \["pending", "won", "lost", "void"\]/);
  assert.match(api, /status: legStatus\(raw\.status\)/);
  assert.deepEqual(Object.keys(legStateLabels).sort(), ["live", "lost", "pending", "void", "won"]);
});

test("a slip with nothing started reports no result at all", () => {
  const view = accaSlipView({ legs: [leg({ matchId: 1 }), leg({ matchId: 2 })] }, NOW);
  assert.equal(view.result, null);
});

test("a slip in progress counts won, live and still-to-play separately", () => {
  const view = accaSlipView({
    legs: [
      leg({ matchId: 1, status: "won", kickoffAt: "2026-10-02T12:00:00Z" }),
      leg({ matchId: 2, kickoffAt: "2026-10-02T17:00:00Z" }),
      leg({ matchId: 3, kickoffAt: "2026-10-02T21:00:00Z" }),
    ],
  }, NOW);
  assert.equal(view.result.tone, "live");
  assert.equal(view.result.label, "In progress");
  assert.equal(view.result.line, "1 won · 1 live · 1 to play");
});

test("one lost leg loses the slip, however many legs won", () => {
  const view = accaSlipView({
    legs: [
      leg({ matchId: 1, status: "won", kickoffAt: "2026-10-01T12:00:00Z" }),
      leg({ matchId: 2, status: "won", kickoffAt: "2026-10-01T14:00:00Z" }),
      leg({ matchId: 3, status: "lost", kickoffAt: "2026-10-01T16:00:00Z" }),
    ],
  }, NOW);
  assert.equal(view.result.tone, "lost");
  assert.equal(view.result.label, "Lost");
  assert.equal(view.result.line, "2 won · 1 lost · 0 void");
});

test("a void leg is dropped from the settling price, not counted as a win", () => {
  // 2.0 * 2.0 with a void 5.0 in the middle settles at 4.0. Leaving the void leg
  // in would advertise 20.0 -- five times the return, on a page a reader opens
  // precisely to see what their slip did.
  const legs = [
    { ...leg({ matchId: 1, fairOdds: 2 }), state: "won" },
    { ...leg({ matchId: 2, fairOdds: 5 }), state: "void" },
    { ...leg({ matchId: 3, fairOdds: 2 }), state: "won" },
  ];
  assert.equal(settlingOdds(legs), 4);

  const view = accaSlipView({
    legs: [
      leg({ matchId: 1, status: "won", fairOdds: 2, kickoffAt: "2026-10-01T12:00:00Z" }),
      leg({ matchId: 2, status: "void", fairOdds: 5, kickoffAt: "2026-10-01T14:00:00Z" }),
      leg({ matchId: 3, status: "won", fairOdds: 2, kickoffAt: "2026-10-01T16:00:00Z" }),
    ],
  }, NOW);
  assert.equal(view.result.tone, "won");
  assert.equal(view.result.line, "2 won · 1 void · settles at 4.00");
});

test("a slip of nothing but void legs is won, not lost", () => {
  // Every match abandoned. No leg lost, so the ticket cannot have.
  const view = accaSlipView({
    legs: [
      leg({ matchId: 1, status: "void", kickoffAt: "2026-10-01T12:00:00Z" }),
      leg({ matchId: 2, status: "void", kickoffAt: "2026-10-01T14:00:00Z" }),
    ],
  }, NOW);
  assert.equal(view.result.label, "Won");
  assert.equal(view.result.line, "0 won · 2 void · settles at 1.00");
});

test("legs are ordered by kick-off and grouped by UTC day", () => {
  const view = accaSlipView({
    legs: [
      leg({ matchId: 3, kickoffAt: "2026-10-03T11:00:00Z" }),
      leg({ matchId: 1, kickoffAt: "2026-10-02T19:00:00Z" }),
      leg({ matchId: 2, kickoffAt: "2026-10-02T23:30:00Z" }),
    ],
  }, NOW);
  assert.deepEqual(view.legs.map((item) => item.matchId), [1, 2, 3]);
  assert.deepEqual(view.days.map((day) => [day.key, day.legs.length]), [
    ["2026-10-02", 2],
    ["2026-10-03", 1],
  ]);
  assert.equal(view.firstKickoffAt, "2026-10-02T19:00:00Z");
  assert.equal(view.lastKickoffAt, "2026-10-03T11:00:00Z");
});

test("the day key comes from the ISO string, not from a parsed local date", () => {
  // A 23:30 UTC kick-off is the NEXT day in any timezone east of UTC. Grouping
  // through `new Date(...).getDate()` would put it in a different group on the
  // server than in the browser, which hydrates as a different tree.
  const legs = [
    { ...leg({ kickoffAt: "2026-10-02T23:30:00Z" }), state: "pending" },
    { ...leg({ kickoffAt: "2026-10-02T23:45:00Z" }), state: "pending" },
  ];
  assert.deepEqual(groupLegsByDay(legs).map((day) => day.key), ["2026-10-02"]);
});

test("a mixed-market slip is called Mixed, not All", () => {
  assert.equal(slipMarketLabel("all", "en"), "Mixed");
  assert.equal(slipMarketLabel("mixed", "en"), "Mixed");
  assert.equal(slipMarketLabel("", "en"), "Mixed");
  assert.equal(slipMarketLabel("btts", "en"), "Both Teams to Score");
});

test("the window label collapses to one date when every leg is on one day", () => {
  assert.equal(slipWindowLabel("2026-10-02T19:00:00Z", "2026-10-02T21:00:00Z"), "2 Oct");
  assert.equal(slipWindowLabel("2026-10-02T19:00:00Z", "2026-10-04T13:00:00Z"), "2 Oct – 4 Oct");
  assert.equal(slipWindowLabel("", ""), "");
});
