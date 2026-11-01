import assert from "node:assert/strict";
import test from "node:test";

import { addBuilderLegs, readBuilderSelection, totalOdds, writeBuilderSelection } from "./builder-state.ts";

/**
 * The Builder tells the reader "one pick per match". It did not hold.
 *
 * Selections were keyed on `leg.id`, so two markets on the same fixture were
 * two distinct ids and both stayed in the ticket. The consequence is arithmetic,
 * not cosmetic: `totalOdds` multiplies every leg as though they were
 * independent, and two markets on one match are correlated, so the ticket
 * advertised a return it could not pay.
 */

function leg(overrides) {
  return {
    id: "leg-1", predictionId: 1, matchId: 100, matchTitle: "Home vs Away",
    marketKey: "1x2", selection: "home", odds: 2, oddsType: "market",
    // Far future: pruneBuilderLegs drops anything already kicked off.
    kickoffAt: new Date(Date.now() + 86_400_000).toISOString(),
    matchStatus: "upcoming", source: "manual",
    ...overrides,
  };
}

// sessionStorage + window shim: builder-state is browser code.
function installBrowser() {
  const store = new Map();
  globalThis.window = {
    sessionStorage: {
      getItem: (k) => (store.has(k) ? store.get(k) : null),
      setItem: (k, v) => store.set(k, v),
      removeItem: (k) => store.delete(k),
    },
    dispatchEvent: () => true,
  };
  globalThis.CustomEvent = class { constructor(type, init) { this.type = type; Object.assign(this, init); } };
  return store;
}

test("a second market on the same fixture replaces the first", () => {
  installBrowser();
  writeBuilderSelection([]);
  addBuilderLegs([leg({ id: "a", marketKey: "1x2", selection: "home", odds: 2 })]);
  addBuilderLegs([leg({ id: "b", marketKey: "btts", selection: "yes", odds: 3 })]);

  const held = readBuilderSelection();
  assert.equal(held.length, 1, "two markets from one fixture are both in the ticket");
  assert.equal(held[0].marketKey, "btts", "the later tap should win");
});

test("different fixtures are both kept", () => {
  installBrowser();
  writeBuilderSelection([]);
  addBuilderLegs([leg({ id: "a", matchId: 100 })]);
  addBuilderLegs([leg({ id: "b", matchId: 200 })]);
  assert.equal(readBuilderSelection().length, 2);
});

test("odds cannot be inflated by stacking correlated markets", () => {
  // The reason the rule exists. Two markets on one match at 2.0 and 3.0 would
  // multiply to 6.0 for a ticket that is really a single 3.0 outcome.
  installBrowser();
  writeBuilderSelection([]);
  addBuilderLegs([leg({ id: "a", marketKey: "1x2", odds: 2 })]);
  addBuilderLegs([leg({ id: "b", marketKey: "btts", odds: 3 })]);
  assert.equal(totalOdds(readBuilderSelection()), 3, "correlated legs were multiplied together");
});

test("an imported multi-leg ACCA keeps every leg", () => {
  // Published slips never repeat a fixture (verified against 16,890 production
  // slips), so collapsing by fixture must not thin a real import.
  installBrowser();
  writeBuilderSelection([]);
  addBuilderLegs([
    leg({ id: "x1", matchId: 11, source: "acca", groupId: "slip-1" }),
    leg({ id: "x2", matchId: 12, source: "acca", groupId: "slip-1" }),
    leg({ id: "x3", matchId: 13, source: "acca", groupId: "slip-1" }),
  ]);
  assert.equal(readBuilderSelection().length, 3);
});
