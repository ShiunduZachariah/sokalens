import assert from "node:assert/strict";
import test from "node:test";

import { MAX_LEAGUE_FIXTURES, relatedFixtures } from "./relatedFixtures.ts";

const now = new Date("2026-09-24T12:00:00Z");
const match = (id, hoursFromNow, overrides = {}) => ({
  id,
  league_name: "Second League",
  home_team: `Home ${id}`,
  away_team: `Away ${id}`,
  kickoff_at: new Date(now.getTime() + hoursFromNow * 3_600_000).toISOString(),
  status: "scheduled",
  ...overrides,
});

test("team threads come first, then the nearest league fixtures", () => {
  const related = relatedFixtures({
    matchId: 1,
    homeNext: match(10, 72),
    awayNext: match(11, 96),
    leagueUpcoming: [match(22, 30), match(21, 5), match(23, 50)],
    now,
  });
  assert.deepEqual(related.map((item) => [item.match.id, item.reason]), [
    [10, "team"], [11, "team"], [21, "league"], [22, "league"], [23, "league"],
  ]);
});

test("the fixture itself and duplicates are never linked", () => {
  const related = relatedFixtures({
    matchId: 1,
    homeNext: match(1, 2), // an upcoming fixture's own next match is itself
    awayNext: match(5, 10),
    leagueUpcoming: [match(1, 2), match(5, 10), match(6, 12)],
    now,
  });
  assert.deepEqual(related.map((item) => item.match.id), [5, 6]);
});

test("league fixtures are capped and past kickoffs skipped", () => {
  const upcoming = [match(30, -3), ...Array.from({ length: 8 }, (_, index) => match(40 + index, index + 1))];
  const related = relatedFixtures({ matchId: 1, leagueUpcoming: upcoming, now });
  assert.equal(related.length, MAX_LEAGUE_FIXTURES);
  assert.ok(related.every((item) => item.match.id !== 30));
});

test("a fixture without names cannot become a link", () => {
  const related = relatedFixtures({ matchId: 1, leagueUpcoming: [match(50, 3, { home_team: "" })], now });
  assert.deepEqual(related, []);
});

test("missing data yields no links rather than an error", () => {
  assert.deepEqual(relatedFixtures({ matchId: 1, now }), []);
});
