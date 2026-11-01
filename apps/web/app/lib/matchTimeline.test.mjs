import assert from "node:assert/strict";
import test from "node:test";

import { benefitingSide, eventDetailLine, eventLabel, eventPeriod, groupEventsByPeriod, isScoringEvent, periodScore, runningScores, timelineHighlights, toTimelineEvents } from "./matchTimeline.ts";

const event = (overrides = {}) => ({
  minute: 23,
  clock: "23'",
  kind: "goal",
  side: "home",
  player: "Kane",
  assist: "",
  comments: "",
  retracted: false,
  ...overrides,
});

test("a cancelled goal says so in its own label", () => {
  // The reader scans labels. "Goal" with a strike-through somewhere else is
  // not a sentence that explains a score they watched being celebrated.
  assert.equal(eventLabel(event({ retracted: true })), "Goal cancelled");
  assert.equal(eventLabel(event({ kind: "var_retraction", player: "" })), "Goal cancelled");
});

test("each kind reads as the thing it is", () => {
  assert.equal(eventLabel(event()), "Goal");
  assert.equal(eventLabel(event({ kind: "own_goal" })), "Own goal");
  // A scored penalty must read as a goal first; "Penalty" alone made it look
  // like a separate event beside the goals.
  assert.equal(eventLabel(event({ kind: "penalty_goal" })), "Goal (Penalty)");
  assert.equal(eventLabel(event({ kind: "penalty_missed" })), "Penalty missed");
  assert.equal(eventLabel(event({ kind: "second_yellow_card" })), "Second yellow");
  assert.equal(eventLabel(event({ kind: "substitution" })), "Substitution");
});

test("an unrecognised kind is labelled, not blank", () => {
  // A provider that invents an event type must not produce an empty row.
  assert.equal(eventLabel(event({ kind: "penalty_shootout_miss" })), "Event");
});

test("an own goal stays with the provider's beneficiary team", () => {
  // The upstream feed attributed Cobreloa's 87' own goal to Coquimbo (away), the
  // beneficiary. Reversing that side turns a 1-2 match into a 2-1 timeline.
  assert.equal(benefitingSide(event({ kind: "own_goal", side: "home" })), "home");
  assert.equal(benefitingSide(event({ kind: "own_goal", side: "away" })), "away");
  assert.equal(benefitingSide(event({ kind: "goal", side: "home" })), "home");
});

test("an unmapped team stays unattributed", () => {
  assert.equal(benefitingSide(event({ side: null })), null);
  assert.equal(benefitingSide(event({ kind: "own_goal", side: null })), null);
});

test("a retracted goal does not count as a score", () => {
  assert.equal(isScoringEvent(event()), true);
  assert.equal(isScoringEvent(event({ retracted: true })), false);
  assert.equal(isScoringEvent(event({ kind: "yellow_card" })), false);
});

test("the board shows goals and dismissals, not substitutions", () => {
  const highlights = timelineHighlights([
    event({ kind: "goal", clock: "12'" }),
    event({ kind: "substitution", clock: "60'" }),
    event({ kind: "yellow_card", clock: "62'" }),
    event({ kind: "red_card", clock: "80'" }),
  ]);
  assert.deepEqual(
    highlights.map((item) => item.clock),
    ["12'", "80'"],
  );
});

test("a cancelled goal survives the board filter", () => {
  // Precisely the event a reader needs told about: they may have seen it given.
  const highlights = timelineHighlights([event({ kind: "goal", retracted: true, clock: "41'" })]);
  assert.equal(highlights.length, 1);
  assert.equal(highlights[0].clock, "41'");
});

test("a goal glut keeps the most recent events", () => {
  const goals = Array.from({ length: 9 }, (_, index) => event({ clock: `${index + 1}'` }));
  const highlights = timelineHighlights(goals);
  assert.equal(highlights.length, 6);
  assert.equal(highlights[0].clock, "4'");
  assert.equal(highlights[5].clock, "9'");
});

test("a goalless match yields nothing rather than an empty shell", () => {
  assert.deepEqual(timelineHighlights([]), []);
  assert.deepEqual(timelineHighlights([event({ kind: "substitution" })]), []);
});

test("the detail line carries who, the assist and the reason", () => {
  assert.equal(eventDetailLine(event({ player: "Kane", assist: "Musiala" })), "Kane · assist Musiala");
  assert.equal(eventDetailLine(event({ player: "", comments: "Offside" })), "Offside");
  assert.equal(eventDetailLine(event({ player: "", assist: "", comments: "" })), "");
});

/*
 * Payload mapping. The API sends snake_case and omits every optional field;
 * the UI expects camelCase with all of them present.
 */

test("a full event maps every field", () => {
  const [mapped] = toTimelineEvents([
    { minute: 90, extra: 3, clock: "90+3'", event_kind: "penalty_goal", type: "Goal", detail: "Penalty", side: "away", player: "Hauge", assist: "Berg", comments: "Confirmed by VAR", retracted: false },
  ]);
  assert.equal(mapped.minute, 90);
  assert.equal(mapped.clock, "90+3'");
  assert.equal(mapped.kind, "penalty_goal");
  assert.equal(mapped.side, "away");
  assert.equal(mapped.player, "Hauge");
  assert.equal(mapped.assist, "Berg");
  assert.equal(mapped.comments, "Confirmed by VAR");
  assert.equal(mapped.retracted, false);
});

test("the fields the API omits become empty, not the string 'undefined'", () => {
  // String(undefined) is "undefined", and it renders.
  const [mapped] = toTimelineEvents([{ minute: 12, clock: "12'", event_kind: "yellow_card" }]);
  assert.equal(mapped.player, "");
  assert.equal(mapped.assist, "");
  assert.equal(mapped.comments, "");
});

test("a side that is missing or not home/away stays null", () => {
  assert.equal(toTimelineEvents([{ minute: 12, event_kind: "goal" }])[0].side, null);
  assert.equal(toTimelineEvents([{ minute: 12, event_kind: "goal", side: "neutral" }])[0].side, null);
});

test("retraction only comes from a true boolean", () => {
  // A truthy string from a malformed payload must not cancel a real goal.
  assert.equal(toTimelineEvents([{ minute: 1, event_kind: "goal", retracted: "false" }])[0].retracted, false);
  assert.equal(toTimelineEvents([{ minute: 1, event_kind: "goal", retracted: true }])[0].retracted, true);
});

test("a missing clock falls back to the minute rather than rendering blank", () => {
  assert.equal(toTimelineEvents([{ minute: 67, event_kind: "goal" }])[0].clock, "67'");
});

test("an event with no minute is dropped, not placed at kickoff", () => {
  assert.deepEqual(toTimelineEvents([{ event_kind: "goal", clock: "?" }]), []);
});

test("a missing or malformed timeline is an empty list", () => {
  assert.deepEqual(toTimelineEvents(undefined), []);
  assert.deepEqual(toTimelineEvents(null), []);
  assert.deepEqual(toTimelineEvents("not a list"), []);
  assert.deepEqual(toTimelineEvents([null, "event"]), []);
});

test("added time stays in the half it was played in", () => {
  // The trap: the provider sends 45+2 as minute 45 with extra 2. Summing them
  // first puts a first-half stoppage goal at minute 47 and files it under the
  // second half -- before that half has kicked off.
  assert.equal(eventPeriod({ minute: 45 }), "first_half");
  assert.equal(eventPeriod({ minute: 46 }), "second_half");
  assert.equal(eventPeriod({ minute: 90 }), "second_half");
  assert.equal(eventPeriod({ minute: 91 }), "extra_time");
  assert.equal(eventPeriod({ minute: 120 }), "extra_time");
  assert.equal(eventPeriod({ minute: 121 }), "penalties");
});

test("periods keep match order and drop the ones never played", () => {
  const groups = groupEventsByPeriod([
    { minute: 70, clock: "70'", kind: "goal", side: "home", player: "B", assist: "", comments: "", retracted: false },
    { minute: 12, clock: "12'", kind: "yellow_card", side: "away", player: "A", assist: "", comments: "", retracted: false },
    { minute: 45, clock: "45+2'", kind: "goal", side: "home", player: "C", assist: "", comments: "", retracted: false },
  ]);
  assert.deepEqual(groups.map((g) => g.key), ["first_half", "second_half"]);
  // Most matches never reach extra time; a permanent empty heading would read
  // as missing data rather than a match that finished in ninety minutes.
  assert.equal(groups.length, 2);
  assert.deepEqual(groups[0].events.map((e) => e.player), ["A", "C"]);
  assert.deepEqual(groups[1].events.map((e) => e.player), ["B"]);
});

test("grouping loses no events", () => {
  const events = [5, 45, 46, 90, 95, 130].map((minute) => ({
    minute, clock: `${minute}'`, kind: "goal", side: "home", player: String(minute),
    assist: "", comments: "", retracted: false,
  }));
  const total = groupEventsByPeriod(events).reduce((sum, g) => sum + g.events.length, 0);
  assert.equal(total, events.length);
});

const goal = (over = {}) => event({ kind: "goal", side: "home", ...over });

test("the running score counts a goal to the side it benefited", () => {
  const events = [
    goal({ side: "home" }),
    goal({ side: "away" }),
    // The provider's team is already the beneficiary, including on own goals.
    goal({ kind: "own_goal", side: "away" }),
  ];
  const scores = runningScores(events);
  assert.deepEqual(scores.get(0), { home: 1, away: 0 });
  assert.deepEqual(scores.get(1), { home: 1, away: 1 });
  assert.deepEqual(scores.get(2), { home: 1, away: 2 });
});

test("second-half goal badges continue the first-half running score", () => {
  const firstHalf = [goal({ side: "away" }), goal({ side: "home" })];
  const secondHalf = [goal({ side: "away" })];
  const first = periodScore(firstHalf);
  assert.deepEqual(runningScores(secondHalf, first).get(0), { home: 1, away: 2 });
});

test("a superseded 6' own goal cannot turn Lesotho-Angola 0-4 into 1-4", () => {
  const events = [
    goal({ minute: 6, kind: "own_goal", side: "away", player: "B. Ntsenko", retracted: true }),
    goal({ minute: 6, side: "away", player: "Jairo Muanha" }),
    goal({ minute: 13, side: "away", player: "L. Panda" }),
    goal({ minute: 19, kind: "penalty_goal", side: "away", player: "L. Panda" }),
    goal({ minute: 37, side: "away", player: "Romano" }),
  ];
  assert.deepEqual(periodScore(events), { home: 0, away: 4 });
  assert.deepEqual(runningScores(events).get(4), { home: 0, away: 4 });
});

test("a retracted goal does not move the score", () => {
  // The row stays on the timeline struck through, because a cancellation is
  // what the reader came back for -- but the score beside the goals after it
  // has to be the score that actually stood.
  const events = [goal(), goal({ retracted: true }), goal()];
  const scores = runningScores(events);
  assert.deepEqual(scores.get(0), { home: 1, away: 0 });
  assert.equal(scores.has(1), false, "a cancelled goal gets no score badge");
  assert.deepEqual(scores.get(2), { home: 2, away: 0 });
});

test("a goal with no mapped side is shown but cannot move the score", () => {
  const events = [goal({ side: null }), goal({ side: "home" })];
  const scores = runningScores(events);
  assert.equal(scores.has(0), false, "an unattributable goal must not guess a side");
  assert.deepEqual(scores.get(1), { home: 1, away: 0 });
});

test("a half's header shows that half's goals, not the match total", () => {
  // "2ND HALF 1-0" means one goal in the second half. Reading the last running
  // score in the period would print the match aggregate instead.
  const secondHalf = [goal({ side: "home" })];
  assert.deepEqual(periodScore(secondHalf), { home: 1, away: 0 });
  assert.deepEqual(periodScore([]), { home: 0, away: 0 });
  assert.deepEqual(
    periodScore([goal({ side: "home" }), goal({ retracted: true })]),
    { home: 1, away: 0 },
  );
});
