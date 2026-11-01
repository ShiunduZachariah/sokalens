import assert from "node:assert/strict";
import test from "node:test";

import { fixtureHref } from "./fixtureHref.ts";

/*
 * The id a fixture link is built from.
 *
 * This is pinned because the failure mode is a URL that looks completely
 * normal. On 2026-10-03 the #1 Best Bet pointed at `...-2935987` -- a
 * prediction id -- and returned 404, while the same match linked correctly from
 * the homepage as `...-5133154`. Nothing in the markup looked wrong.
 */
const match = { leagueName: "Liga Profesional Argentina", homeTeam: "Defensa Y Justicia", awayTeam: "San Lorenzo" };

test("a prediction links to its match, not to itself", () => {
  // The live feed shape: `id` is the prediction, `matchId` is the fixture.
  const href = fixtureHref({ ...match, id: 2936003, matchId: 5133154 });
  assert.match(href, /5133154$/, "the fixture id must win over the prediction id");
  assert.doesNotMatch(href, /2936003/);
});

test("a match object still links by its own id", () => {
  // A match carries no `matchId`, so the preference cannot strand it.
  assert.match(fixtureHref({ ...match, id: 5133154 }), /5133154$/);
});

test("an ACCA leg with a zeroed id links to its match", () => {
  /*
   * `??` falls back only on null and undefined, so a leg whose `id` parsed to
   * 0 -- which every leg's did, because the API sends `prediction_id` as a
   * UUID and the web parsed it as a number -- took 0, failed validation, and
   * linked to `/predictions/today`. A 200, so no link checker saw it.
   */
  const href = fixtureHref({ ...match, id: 0, matchId: 5133154 });
  assert.match(href, /5133154$/);
  assert.notEqual(href, "/predictions/today");
});

test("no usable id still falls back rather than inventing a URL", () => {
  assert.equal(fixtureHref({ ...match, id: 0, matchId: 0 }), "/predictions/today");
  assert.equal(fixtureHref({ ...match }), "/predictions/today");
});
