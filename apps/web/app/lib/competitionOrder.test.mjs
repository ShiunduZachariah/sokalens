import assert from "node:assert/strict";
import test from "node:test";
import { competitionPopularity, footballCountry } from "./competitionOrder.ts";

test("British football competitions use their home association instead of United Kingdom", () => {
  assert.equal(footballCountry("GB", "Premier League"), "England");
  assert.equal(footballCountry("United Kingdom", "Scottish Premiership"), "Scotland");
  assert.equal(footballCountry("GB", "Cymru Premier"), "Wales");
  assert.equal(footballCountry("GB", "NIFL Premiership"), "Northern Ireland");
});

// Regression: popularLeaguePatterns matched by substring, so any league whose
// name CONTAINS a famous one inherited its rank. "Kenyan Premier League"
// scored 1 -- tied with England's Premier League -- which is why Gor Mahia led
// the homepage for every visitor in the world.
test("a league is not famous merely because its name contains a famous one", () => {
  const england = competitionPopularity("GB", "Premier League");

  for (const [country, league] of [
    ["KE", "Kenyan Premier League"],
    ["BD", "Bangladesh Premier League"],
    ["AM", "Premier League"],
    ["RU", "Premier League"],
  ]) {
    assert.ok(
      competitionPopularity(country, league) > england,
      `${country} ${league} must rank below England's Premier League`,
    );
  }
});

test("same-named competitions in different countries are ranked apart", () => {
  assert.ok(
    competitionPopularity("AT", "Bundesliga") > competitionPopularity("DE", "Bundesliga"),
    "Austria's Bundesliga is not Germany's",
  );
  assert.ok(
    competitionPopularity("BF", "Ligue 1") > competitionPopularity("FR", "Ligue 1"),
    "Burkina Faso's Ligue 1 is not France's",
  );
});

test("the genuine top leagues still rank at the top", () => {
  const obscure = competitionPopularity("GR", "Gamma Ethniki - Group 10");
  for (const [country, league] of [
    ["GB", "Premier League"],
    ["ES", "La Liga"],
    ["IT", "Serie A"],
    ["DE", "Bundesliga"],
    ["FR", "Ligue 1"],
    ["", "UEFA Champions League"],
  ]) {
    assert.ok(
      competitionPopularity(country, league) < obscure,
      `${league} should outrank an obscure regional division`,
    );
  }
});

/*
 * Wales is the case that proves the name alone is not enough: its top division
 * is called "Premier League", exactly like England's, so every Welsh fixture
 * was grouped under England on the today and in-play feeds. The slug carries
 * the nation and the data already had it.
 */
test("a UK competition is filed by its slug when the name does not say", () => {
  assert.equal(footballCountry("GB", "Premier League", "league-wales-premier-league"), "Wales");
  assert.equal(footballCountry("GB", "Premier League", "league-england-premier-league"), "England");
  assert.equal(footballCountry("GB", "Premiership", "league-scotland-premiership"), "Scotland");
  assert.equal(footballCountry("GB", "Premiership", "league-northern-ireland-premiership"), "Northern Ireland");
});

test("a name that does say still wins without a slug", () => {
  assert.equal(footballCountry("GB", "Welsh Cup"), "Wales");
  assert.equal(footballCountry("GB", "Scottish Cup"), "Scotland");
  // Unknown UK competition stays England, which is the historical default.
  assert.equal(footballCountry("GB", "FA Cup"), "England");
});

test("the home-nation search never reaches a non-UK country", () => {
  // Australia has a "New South Wales NPL"; its country code keeps it out.
  assert.equal(footballCountry("AU", "New South Wales NPL", "league-australia-new-south-wales-npl"), "AU");
});
