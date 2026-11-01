import assert from "node:assert/strict";
import test from "node:test";

import { SETTLED_INDEXABLE_DAYS, fixtureLifecycle, isArchivedFixture, isLiveFixture } from "./fixtureLifecycle.ts";

const now = new Date("2026-09-11T12:00:00Z");
const daysAgo = (n) => new Date(now.getTime() - n * 86_400_000).toISOString();
const inDays = (n) => new Date(now.getTime() + n * 86_400_000).toISOString();

test("a fixture moves upcoming -> live -> settled -> archived", () => {
  assert.equal(fixtureLifecycle({ kickoffAt: inDays(2), status: "scheduled" }, now), "upcoming");
  assert.equal(fixtureLifecycle({ kickoffAt: daysAgo(0.02), status: "live" }, now), "live");
  assert.equal(fixtureLifecycle({ kickoffAt: daysAgo(3), status: "finished" }, now), "settled");
  assert.equal(fixtureLifecycle({ kickoffAt: daysAgo(20), status: "finished" }, now), "archived");
});

test("retirement happens after the indexable window, not before", () => {
  // The boundary is load-bearing: inside it the page is in fixtures.xml and
  // indexable; outside it the page is noindex and absent from every sitemap.
  assert.equal(isArchivedFixture({ kickoffAt: daysAgo(SETTLED_INDEXABLE_DAYS - 1), status: "finished" }, now), false);
  assert.equal(isArchivedFixture({ kickoffAt: daysAgo(SETTLED_INDEXABLE_DAYS + 1), status: "finished" }, now), true);
});

test("a match that has kicked off but never settled is never archived", () => {
  // Otherwise a stuck provider status silently deindexes a live fixture.
  assert.equal(fixtureLifecycle({ kickoffAt: daysAgo(40), status: "scheduled" }, now), "live");
  assert.equal(isArchivedFixture({ kickoffAt: daysAgo(40), status: "scheduled" }, now), false);
});

test("an unparseable kickoff is treated as upcoming", () => {
  // Losing a page that should be indexed is worse than keeping one that should
  // not, so bad data fails towards visibility.
  assert.equal(fixtureLifecycle({ kickoffAt: "", status: "finished" }, now), "upcoming");
  assert.equal(isArchivedFixture({ kickoffAt: "not-a-date", status: "finished" }, now), false);
});

test("abandoned and postponed matches retire on the same clock", () => {
  assert.equal(fixtureLifecycle({ kickoffAt: daysAgo(30), status: "postponed" }, now), "archived");
  assert.equal(fixtureLifecycle({ kickoffAt: daysAgo(2), status: "cancelled" }, now), "settled");
});

test("every in-play phase of a match counts as live", () => {
  // A match is still being played through half-time, extra time, penalties and
  // a stoppage. The failure this guards is silent: drop one of these and the
  // match vanishes from the in-play board mid-game, with no error anywhere.
  for (const status of ["live", "halftime", "second_half", "extra_time", "penalties", "break"]) {
    assert.equal(isLiveFixture(status), true, `"${status}" must count as live`);
  }
});

test("no terminal or pre-match status counts as live", () => {
  for (const status of ["upcoming", "scheduled", "finished", "ft", "cancelled", "postponed", "abandoned"]) {
    assert.equal(isLiveFixture(status), false, `"${status}" must not count as live`);
  }
});

test("isLiveFixture tolerates provider casing and a missing status", () => {
  // Provider status casing is not guaranteed, and a fixture can reach a page
  // with no status at all. Neither may throw, and neither may be live by
  // accident.
  assert.equal(isLiveFixture("LIVE"), true);
  assert.equal(isLiveFixture("Second_Half"), true);
  assert.equal(isLiveFixture(""), false);
  assert.equal(isLiveFixture(undefined), false);
  assert.equal(isLiveFixture(null), false);
});

test("isLiveFixture is status-only, unlike fixtureLifecycle", () => {
  // These two deliberately disagree in one case, and the difference is load
  // bearing rather than an oversight.
  //
  // fixtureLifecycle infers "live" from a kickoff in the past whose status
  // never settled, so a stuck provider status cannot silently deindex a page.
  // That inference is right for indexing and wrong for the in-play board: a
  // fixture whose status has been stuck on "scheduled" for three hours is not
  // something to put in front of someone as being played right now.
  //
  // So isLiveFixture asks only what the provider currently says.
  const stale = { kickoffAt: daysAgo(0.2), status: "scheduled" };
  assert.equal(fixtureLifecycle(stale, now), "live", "indexing still treats a stuck fixture as live");
  assert.equal(isLiveFixture(stale.status), false, "the in-play board must not show a stuck fixture");

  // For every status the provider actually reports as in play, they agree.
  for (const status of ["live", "halftime", "penalties", "finished"]) {
    assert.equal(
      isLiveFixture(status),
      fixtureLifecycle({ kickoffAt: daysAgo(0.02), status }, now) === "live",
      `disagreement on "${status}"`,
    );
  }
});
