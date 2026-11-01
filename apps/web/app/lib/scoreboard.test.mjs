import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { halfTimeLabel, scorersFor } from "./scoreboard.ts";

const appDir = path.resolve(import.meta.dirname, "..");
const read = (relative) => readFile(path.join(appDir, relative), "utf8");

/*
 * The scoreboard's two pieces of arithmetic, and the one presentation rule that
 * matters more than either.
 *
 * Half-time and the scorers line are both derived -- the API sends no half-time
 * score and no scorers list -- so both are computed from the timeline, and both
 * fail by printing a plausible wrong number rather than by throwing.
 */

const goal = (minute, side, player = "Player", kind = "goal") => ({
  minute, side, player, kind, clock: `${minute}'`, assist: "", comments: "", retracted: false,
});

test("half-time counts goals at or before minute 45", () => {
  const events = [goal(12, "home"), goal(45, "away"), goal(70, "home")];
  // 45+2 arrives as minute 45, so `<= 45` is deliberate: that goal belongs to the
  // first half, and `< 45` would drop it.
  assert.equal(halfTimeLabel(events, "finished"), "HT 1–1");
});

test("half-time appears only once the match has gone past it", () => {
  // A 0–0 first half has not necessarily happened yet. Printing "HT 0–0" at
  // kick-off would claim an interval the match has not reached.
  assert.equal(halfTimeLabel([], "live"), null);
  assert.equal(halfTimeLabel([goal(20, "home")], "live"), null, "still in the first half");
  assert.equal(halfTimeLabel([goal(20, "home"), goal(52, "away")], "live"), "HT 1–0");
});

test("half-time is never shown before kick-off", () => {
  // A fixture with a stale timeline from the provider must not show a half-time
  // score on a match that has not started.
  assert.equal(halfTimeLabel([goal(20, "home"), goal(52, "away")], "prematch"), null);
});

test("a retracted goal counts for nothing", () => {
  const events = [goal(10, "home"), { ...goal(30, "home"), retracted: true }];
  assert.equal(halfTimeLabel([...events, goal(60, "away")], "finished"), "HT 1–0",
    "a withdrawn goal is still in the half-time score");
  assert.equal(scorersFor(events, "home"), "Player 10'",
    "a withdrawn goal is still named as a scorer, contradicting the score above it");
});

test("an own goal is credited to the side it benefits, and says so", () => {
  // The API maps the event's team to the BENEFITING side, so an own goal already
  // sits on the scoring side -- without the marker the line reads as that team's
  // own player scoring for them.
  const events = [goal(22, "away", "Maguire", "own_goal")];
  assert.equal(scorersFor(events, "away"), "Maguire 22' (og)");
  assert.equal(scorersFor(events, "home"), "");
});

test("a penalty is marked, and a nameless goal still shows its minute", () => {
  assert.equal(scorersFor([goal(80, "home", "Salah", "penalty_goal")], "home"), "Salah 80' (pen)");
  assert.equal(scorersFor([goal(80, "home", "")], "home"), "80'",
    "a goal the provider gave no player for must not render an empty name");
});

test("two goals by one player read as two goals", () => {
  // "Haaland, Haaland" reads like a bug, which is why minutes are appended.
  assert.equal(
    scorersFor([goal(12, "home", "Haaland"), goal(64, "home", "Haaland")], "home"),
    "Haaland 12', Haaland 64'",
  );
});

test("a kick-off time is never drawn as a score", async () => {
  const source = await read("components/match/Scoreboard.tsx");
  const css = await read("globals.css");

  // The page previously rendered the kick-off time through the score's own class,
  // at the score's own size and weight, so "15:00" looked like a 15-0 scoreline.
  assert.match(source, /className="mp-figure mp-figure--time"/,
    "the kick-off time no longer gets its own treatment");
  assert.match(css, /\.mp-figure--time \{[^}]*color: var\(--sl-text-3\)/,
    "the time is no longer muted against the score");
  assert.match(css, /\.mp-figure--time \{[^}]*font-weight: 500/,
    "the time is no longer lighter than the score");

  // And a live match with no score yet falls back to the time rather than to 0-0.
  assert.match(source, /phase === "prematch" \|\| score === null/,
    "a live match without a score no longer falls back to the kick-off time");
});

test("each phase has its own status pill", async () => {
  const source = await read("components/match/Scoreboard.tsx");
  const css = await read("globals.css");
  for (const modifier of ["live", "done", "pre"]) {
    assert.match(css, new RegExp(`\\.mp-pill--${modifier}\\b`), `no style for .mp-pill--${modifier}`);
  }
  assert.match(source, /mp-pill--live/);
  assert.match(source, /mp-pill--done/);
  assert.match(source, /mp-pill--pre/);
  // The pulse is the only moving thing on the page; it has to respect the setting.
  assert.match(css, /@media \(prefers-reduced-motion: reduce\) \{[\s\S]{0,200}\.mp-pill__dot \{ animation: none; \}/,
    "the live pulse ignores prefers-reduced-motion");
});

test("the status is not also shown in the hero meta line", async () => {
  const source = await read("components/MatchDetail.tsx");
  // Two status indicators a few pixels apart invite a reader to check whether they
  // agree, and they are derived from different expressions.
  assert.ok(!/mp-status/.test(source), "the hero meta line shows the status again");
});
