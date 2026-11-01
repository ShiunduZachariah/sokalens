import assert from "node:assert/strict";
import test from "node:test";

import { upcomingFixtures } from "./upcoming.ts";

const now = new Date("2026-09-04T12:00:00Z");
const fixture = (kickoffAt, matchStatus = "upcoming") => ({ kickoffAt, matchStatus });

test("a fixture that has already kicked off is not a pick", () => {
  const kept = upcomingFixtures([fixture("2026-09-04T02:00:00Z"), fixture("2026-09-04T18:00:00Z")], now);

  assert.deepEqual(kept.map((item) => item.kickoffAt), ["2026-09-04T18:00:00Z"]);
});

test("matchStatus is not trusted, because it goes stale", () => {
  // Observed in the live feed: a match that kicked off at 22:30 the previous
  // day still carried matchStatus "upcoming". Kickoff time is the only signal
  // that cannot lie.
  const kept = upcomingFixtures([fixture("2026-09-03T22:30:00Z", "upcoming")], now);

  assert.deepEqual(kept, []);
});

test("in-play and postponed fixtures are dropped even with a future kickoff", () => {
  const kept = upcomingFixtures(
    [
      fixture("2026-09-04T18:00:00Z", "postponed"),
      fixture("2026-09-04T18:00:00Z", "second_half"),
      fixture("2026-09-04T18:00:00Z", "live"),
      fixture("2026-09-04T18:00:00Z", "upcoming"),
    ],
    now,
  );

  assert.equal(kept.length, 1);
  assert.equal(kept[0].matchStatus, "upcoming");
});

test("an unparseable kickoff is kept rather than silently dropped", () => {
  // Dropping on a parse failure would empty the page for a formatting change.
  const kept = upcomingFixtures([fixture("")], now);

  assert.equal(kept.length, 1);
});

test("order is preserved", () => {
  const kept = upcomingFixtures(
    [fixture("2026-09-04T20:00:00Z"), fixture("2026-09-04T02:00:00Z"), fixture("2026-09-04T18:00:00Z")],
    now,
  );

  assert.deepEqual(kept.map((item) => item.kickoffAt), ["2026-09-04T20:00:00Z", "2026-09-04T18:00:00Z"]);
});
