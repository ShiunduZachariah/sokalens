import assert from "node:assert/strict";
import test from "node:test";

import { parseMatchDiscipline, parseTeamDiscipline } from "./discipline.ts";

const record = (overrides = {}) => ({ matches: 10, yellow_cards: 21, red_cards: 1, match_cards: 44, ...overrides });

test("a well-formed record parses to camelCase totals", () => {
  assert.deepEqual(parseTeamDiscipline(record()), { matches: 10, yellowCards: 21, redCards: 1, matchCards: 44 });
});

test("zero cards is a real record, not a missing one", () => {
  assert.deepEqual(parseTeamDiscipline(record({ yellow_cards: 0, red_cards: 0, match_cards: 0 })), { matches: 10, yellowCards: 0, redCards: 0, matchCards: 0 });
});

test("missing, negative, fractional or non-numeric fields reject the record", () => {
  for (const overrides of [
    { matches: undefined }, { yellow_cards: null }, { red_cards: "1" }, { match_cards: -3 },
    { matches: 2.5 }, { yellow_cards: Number.NaN }, { red_cards: Number.POSITIVE_INFINITY }, { match_cards: true },
  ]) {
    assert.equal(parseTeamDiscipline(record(overrides)), null, JSON.stringify(overrides));
  }
});

test("a record from zero matches is not a record", () => {
  assert.equal(parseTeamDiscipline(record({ matches: 0, yellow_cards: 0, red_cards: 0, match_cards: 0 })), null);
});

test("a team cannot have more cards than its matches produced", () => {
  assert.equal(parseTeamDiscipline(record({ yellow_cards: 40, red_cards: 5, match_cards: 44 })), null);
});

test("non-object payloads are ignored", () => {
  for (const raw of [undefined, null, "", 0, "cards", [], [record()]]) {
    assert.equal(parseTeamDiscipline(raw), null, JSON.stringify(raw));
  }
});

test("the match payload keeps whichever side is usable", () => {
  assert.deepEqual(parseMatchDiscipline({ home: record(), away: record({ matches: 0 }) }), {
    home: { matches: 10, yellowCards: 21, redCards: 1, matchCards: 44 },
    away: null,
  });
  assert.equal(parseMatchDiscipline({ home: null, away: "x" }), null);
  assert.equal(parseMatchDiscipline(undefined), null);
  assert.equal(parseMatchDiscipline("discipline"), null);
});
