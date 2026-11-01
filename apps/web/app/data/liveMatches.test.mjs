import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

import { matchClockLabel, matchStatusLabel, toPredictions } from "./liveMatches.ts";

test("full match pagination is sequential to protect PostgreSQL", async () => {
  const source = await readFile(new URL("./liveMatches.ts", import.meta.url), "utf8");
  assert.doesNotMatch(source, /const remaining = await Promise\.all/);
  assert.match(source, /for \(let page = 2; page <= pageCount; page \+= 1\)/);
});

function buildMatch(overrides = {}) {
  return {
    id: 1,
    leagueName: "Premier League",
    homeTeam: "Arsenal",
    awayTeam: "Chelsea",
    kickoffAt: "2026-05-14T18:00:00Z",
    status: "upcoming",
    rawStatus: "",
    rawStatusLabel: "",
    statusElapsed: null,
    venueName: "",
    venueCity: "",
    homeScore: null,
    awayScore: null,
    halfTimeHomeScore: null,
    halfTimeAwayScore: null,
    odds: [],
    predictionState: "pending",
    predictionStateReason: "pending_generation",
    prediction: null,
    ...overrides,
  };
}

test("matchStatusLabel uses human labels for terminal statuses", () => {
  assert.equal(matchStatusLabel(buildMatch({ status: "finished", rawStatus: "FT", rawStatusLabel: "Match Finished" })), "Full time");
  assert.equal(matchStatusLabel(buildMatch({ status: "finished", rawStatus: "AET", rawStatusLabel: "After Extra Time" })), "After extra time");
  assert.equal(matchStatusLabel(buildMatch({ status: "penalties", rawStatus: "P", rawStatusLabel: "Penalty In Progress" })), "Penalties");
  assert.equal(matchStatusLabel(buildMatch({ status: "halftime", rawStatus: "HT", rawStatusLabel: "Half Time" })), "Half-time");
});

test("matchStatusLabel falls back to the canonical status label", () => {
  assert.equal(matchStatusLabel(buildMatch({ status: "upcoming" })), "Upcoming");
  assert.equal(matchStatusLabel(buildMatch({ status: "second_half" })), "Second half");
  assert.equal(matchStatusLabel(buildMatch({ status: "postponed", rawStatus: "PST" })), "Postponed");
});

test("an unresolved result does not inherit a stale provider status", () => {
  for (const rawStatus of ["NS", "2H", "FT", ""]) {
    assert.equal(matchStatusLabel(buildMatch({ status: "unknown", rawStatus })), "Result unconfirmed");
  }
});

test("match detail renders the actual terminal label, not a blanket full-time label", async () => {
  const source = await readFile(new URL("../components/MatchDetail.tsx", import.meta.url), "utf8");
  assert.match(source, /label=\{live \? matchClockLabel\(match\) : matchStatusLabel\(match\)\}/);
});

test("matchClockLabel formats first-half and second-half added time", () => {
  assert.equal(matchClockLabel(buildMatch({ status: "live", rawStatus: "1H", statusElapsed: 48 })), "45 + 3'");
  assert.equal(matchClockLabel(buildMatch({ status: "second_half", rawStatus: "2H", statusElapsed: 93 })), "90 + 3'");
});

test("matchClockLabel shows human terminal labels instead of raw elapsed minutes", () => {
  assert.equal(matchClockLabel(buildMatch({ status: "finished", rawStatus: "FT", statusElapsed: 90 })), "Full time");
  assert.equal(matchClockLabel(buildMatch({ status: "finished", rawStatus: "AET", statusElapsed: 120 })), "After extra time");
  assert.equal(matchClockLabel(buildMatch({ status: "penalties", rawStatus: "PEN", statusElapsed: 120 })), "Penalties");
});

test("toPredictions retains over 2.5 and hides unsupported correct score", () => {
  assert.deepEqual(toPredictions([
    { id: 101, market_key: "1x2", selection: "Home", confidence: 55, published_version: 3, published_at: "2026-08-25T08:00:00Z" },
    { id: 102, market_key: "total_goals", selection: "over_2.5", confidence: 52, published_version: 3, published_at: "2026-08-25T08:00:00Z" },
    { id: 103, market_key: "correct_score", selection: "2-1", confidence: 12, published_version: 3, published_at: "2026-08-25T08:00:00Z" },
  ]), [
    { id: 101, source: "engine", marketKey: "1x2", selection: "Home", confidence: 55, publishedVersion: 3, publishedAt: "2026-08-25T08:00:00Z", outcome: "pending", actual: "", voidReason: "" },
    { id: 102, source: "engine", marketKey: "total_goals", selection: "over_2.5", confidence: 52, publishedVersion: 3, publishedAt: "2026-08-25T08:00:00Z", outcome: "pending", actual: "", voidReason: "" },
  ]);
});

test("toPredictions retains only explicitly marked provider rows with no engine id", () => {
  const rows = toPredictions([
    { id: 0, source: "provider", market_key: "1x2", selection: "Home", confidence: 55 },
    { id: 0, market_key: "1x2", selection: "Home", confidence: 55 },
  ]);
  assert.equal(rows.length, 1);
  assert.equal(rows[0].source, "provider");
});

test("toPredictions carries the void reason through, and defaults it to empty", () => {
  const [expired, graded] = toPredictions([
    // No result ever arrived: the page has to be able to say so.
    { id: 201, market_key: "total_goals", selection: "over_2.5", confidence: 52, published_version: 1, published_at: "2026-09-20T08:00:00Z", outcome: "void", void_reason: "result_never_arrived" },
    // A market-rule void is a graded outcome and needs no explanation.
    { id: 202, market_key: "draw_no_bet", selection: "home", confidence: 51, published_version: 1, published_at: "2026-09-20T08:00:00Z", outcome: "void", actual: "draw" },
  ]);

  assert.equal(expired.voidReason, "result_never_arrived");
  assert.equal(graded.voidReason, "");
});
