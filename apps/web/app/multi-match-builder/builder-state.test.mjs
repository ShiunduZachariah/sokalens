import assert from "node:assert/strict";
import test from "node:test";
import { builderLegId, decodeBuilderSelection, encodeBuilderSelection, formatOdds, hasBuilderLegStarted, pruneBuilderLegs, pruneBuilderSelection, readBuilderSelection, totalOdds, writeBuilderSelection } from "./builder-state.ts";

const leg = (id, odds) => ({ id: String(id), predictionId: id, matchId: id, matchTitle: `Match ${id}`, marketKey: "1x2", selection: "home", odds, oddsType: "market", kickoffAt: "2026-08-30T12:00:00Z", matchStatus: "scheduled", source: "manual" });

test("ticket totals every leg without a six-selection cap", () => {
  const legs = Array.from({ length: 20 }, (_, index) => leg(index + 1, 1.1));
  assert.ok(totalOdds(legs) > 6);
});

test("selection identity separates markets on the same match", () => {
  assert.notEqual(builderLegId(1, 42, "1x2", "home"), builderLegId(1, 42, "total_goals", "over_2.5"));
  assert.equal(formatOdds(2), "2.00");
});

test("builder legs close as soon as kickoff arrives or a match becomes live", () => {
  const scheduled = leg(1, 1.8);
  assert.equal(hasBuilderLegStarted(scheduled, Date.parse("2026-08-30T11:59:59Z")), false);
  assert.equal(hasBuilderLegStarted(scheduled, Date.parse("2026-08-30T12:00:00Z")), true);
  assert.equal(hasBuilderLegStarted({ ...scheduled, matchStatus: "live" }, Date.parse("2026-08-30T11:00:00Z")), true);
});

test("shared builder links contain only canonical selection references", () => {
  const selected = [
    leg(1, 1.8),
    { ...leg(2, 2.1), matchTitle: "This text must not be trusted", marketKey: "total_goals", selection: "over_2.5" },
  ];
  assert.deepEqual(decodeBuilderSelection(encodeBuilderSelection(selected, Date.parse("2026-08-30T11:59:59Z"))), [
    { predictionId: 1, matchId: 1, marketKey: "1x2", selection: "home" },
    { predictionId: 2, matchId: 2, marketKey: "total_goals", selection: "over_2.5" },
  ]);
  assert.equal(decodeBuilderSelection("not+a+valid+token").length, 0);
});

test("pruning reports started legs and shared links never retain their stale odds", () => {
  const now = Date.parse("2026-08-30T12:00:00Z");
  const active = { ...leg(1, 1.8), kickoffAt: "2026-08-30T12:00:01Z" };
  const expired = { ...leg(2, 8.5), kickoffAt: "2026-08-30T12:00:00Z" };

  assert.deepEqual(pruneBuilderLegs([active, expired], now), {
    active: [active],
    expired: [expired],
  });
  assert.deepEqual(decodeBuilderSelection(encodeBuilderSelection([active, expired], now)), [
    { predictionId: 1, matchId: 1, marketKey: "1x2", selection: "home" },
  ]);
});

test("pruning removes started legs from session state", () => {
  const previousWindow = globalThis.window;
  const storage = new Map();
  globalThis.window = {
    dispatchEvent() {},
    sessionStorage: {
      getItem(key) { return storage.get(key) ?? null; },
      removeItem(key) { storage.delete(key); },
      setItem(key, value) { storage.set(key, value); },
    },
  };
  try {
    const now = Date.parse("2026-08-30T12:00:00Z");
    const active = { ...leg(1, 1.8), kickoffAt: "2026-08-30T12:00:01Z" };
    const expired = { ...leg(2, 8.5), kickoffAt: "2026-08-30T12:00:00Z" };
    writeBuilderSelection([active, expired]);

    const result = pruneBuilderSelection(now);
    assert.deepEqual(result.active.map((item) => item.id), [active.id]);
    assert.deepEqual(result.expired.map((item) => item.id), [expired.id]);
    assert.deepEqual(readBuilderSelection().map((item) => item.id), [active.id]);
  } finally {
    if (previousWindow === undefined) delete globalThis.window;
    else globalThis.window = previousWindow;
  }
});
