import assert from "node:assert/strict";
import test from "node:test";
import { buildSportsEventStructuredData } from "./sportsEventStructuredData.ts";

function match(overrides = {}) {
  return { id: 1, leagueName: "Premier League", country: "England", homeTeam: "Chelsea", awayTeam: "Fulham", homeTeamLogo: "https://img.example/chelsea.png", awayTeamLogo: "https://img.example/fulham.png", kickoffAt: "2026-08-24T19:00:00Z", status: "upcoming", venueName: "Stamford Bridge", venueCity: "London", ...overrides };
}

test("complete SportsEvent includes truthful required and recommended fields", () => {
  const event = buildSportsEventStructuredData(match(), "https://sokalens.com/predictions/match");
  assert.equal(event.location.name, "Stamford Bridge");
  assert.equal(event.location.address.addressCountry, "England");
  assert.equal(event.endDate, "2026-08-24T21:00:00.000Z");
  assert.equal(event.organizer.name, "Premier League");
  assert.equal(event.organizer.url, "https://sokalens.com/predictions/leagues/premier-league");
  assert.deepEqual(event.performer.map((item) => item.name), ["Chelsea", "Fulham"]);
  assert.equal(event.location.address.addressLocality, "London");
  assert.equal(event.image.length, 2);
  assert.match(event.description, /Chelsea play Fulham/);
  assert.equal("offers" in event, false, "prediction odds must never be misrepresented as ticket offers");
});

test("missing venue suppresses invalid Event markup instead of inventing a location", () => {
  assert.equal(buildSportsEventStructuredData(match({ venueName: "" }), "https://sokalens.com/predictions/match"), null);
});

test("missing physical address suppresses invalid Event markup instead of emitting a Place name alone", () => {
  assert.equal(
    buildSportsEventStructuredData(
      match({ country: "International", venueCity: "" }),
      "https://sokalens.com/predictions/match",
    ),
    null,
  );
});

test("sparse optional data does not invent an organizer URL or emit empty schema values", () => {
  const event = JSON.parse(JSON.stringify(buildSportsEventStructuredData(
    match({
      leagueName: "   ",
      country: "International",
      venueCity: "London",
      homeTeamLogo: "",
      awayTeamLogo: "",
    }),
    "https://sokalens.com/predictions/match",
  )));

  assert.deepEqual(event.organizer, { "@type": "SportsOrganization", name: "Football" });
  assert.deepEqual(event.location.address, { "@type": "PostalAddress", addressLocality: "London" });
  assert.equal("image" in event, false);
  assert.doesNotMatch(event.description, / in\s+at /);
});

test("postponed fixtures use the schema.org postponed status", () => {
  assert.equal(buildSportsEventStructuredData(match({ status: "postponed" }), "https://sokalens.com/predictions/match").eventStatus, "https://schema.org/EventPostponed");
});

test("invalid kickoff suppresses Event markup instead of emitting an unusable startDate", () => {
  assert.equal(
    buildSportsEventStructuredData(match({ kickoffAt: "not-a-date" }), "https://sokalens.com/predictions/match"),
    null,
  );
});

test("interrupted and suspended fixtures use the schema.org postponed status", () => {
  for (const status of ["interrupted", "suspended"]) {
    assert.equal(
      buildSportsEventStructuredData(match({ status }), "https://sokalens.com/predictions/match").eventStatus,
      "https://schema.org/EventPostponed",
    );
  }
});

test("awarded and walkover fixtures use the schema.org completed status", () => {
  for (const status of ["awarded", "walkover"]) {
    assert.equal(
      buildSportsEventStructuredData(match({ status }), "https://sokalens.com/predictions/match").eventStatus,
      "https://schema.org/EventCompleted",
    );
  }
});
