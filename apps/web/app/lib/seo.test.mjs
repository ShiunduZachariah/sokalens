import assert from "node:assert/strict";
import test from "node:test";

import {
  fixtureIdFromSeoSlug,
  matchPredictionSharePath,
  marketSeoPath,
  predictionFixturePath,
  seoFixturePath,
  seoSlug,
} from "./seo.ts";

test("seoSlug produces stable ASCII slugs", () => {
  assert.equal(seoSlug("UEFA Champions League"), "uefa-champions-league");
  assert.equal(seoSlug("São Tomé & Príncipe"), "sao-tome-principe");
});

test("fixture paths retain the immutable fixture id", () => {
  const match = { id: 88213, leagueName: "Premier League", homeTeam: "Arsenal", awayTeam: "Chelsea" };
  assert.equal(seoFixturePath(match), "/predictions/premier-league/arsenal-vs-chelsea-88213");
  assert.equal(fixtureIdFromSeoSlug("arsenal-vs-chelsea-88213"), 88213);
  assert.equal(predictionFixturePath({ matchId: 88213, leagueName: match.leagueName, homeTeam: match.homeTeam, awayTeam: match.awayTeam }), seoFixturePath(match));
});

test("fixture path builders never serialize invalid IDs", () => {
  assert.equal(predictionFixturePath({ matchId: Number.NaN, leagueName: "League", homeTeam: "Home", awayTeam: "Away" }), "/predictions/today");
  assert.equal(matchPredictionSharePath("null", "Winner", "Home"), "/matches/today");
});

test("market paths come from the canonical catalogue", () => {
  assert.equal(marketSeoPath("total_goals", "over_2.5"), "/predictions/markets/over-2-5-goals");
  assert.equal(marketSeoPath("btts", "yes"), "/predictions/markets/btts");
  assert.equal(marketSeoPath("correct_score", "2-1"), null);
});

test("shared predictions deep-link to a readable market and pick", () => {
  assert.equal(
    matchPredictionSharePath(88213, "Both Teams to Score", "Yes & Over 0.5 Goals"),
    "/matches/88213?market=both-teams-to-score&pick=yes-over-0-5-goals#prediction-both-teams-to-score-yes-over-0-5-goals",
  );
});
