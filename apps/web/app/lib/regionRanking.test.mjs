import assert from "node:assert/strict";
import test from "node:test";

import { isSeniorMens, isTopLeague, rankPredictions, scorePrediction } from "./regionRanking.ts";

const NOW = new Date("2026-09-04T12:00:00Z");

function p(overrides = {}) {
  return {
    confidence: 50,
    kickoffAt: "2026-09-04T18:00:00Z",
    leagueCountry: "KE",
    leagueName: "Kenyan Premier League",
    ...overrides,
  };
}

test("league names alone never identify a top league", () => {
  // "Premier League" exists for Armenia, Barbados, Bangladesh, Bahrain,
  // Bermuda and Bhutan as well as England.
  assert.equal(isTopLeague("Premier League", "GB"), true);
  for (const country of ["AM", "BB", "BD", "BH", "BM", "BT"]) {
    assert.equal(isTopLeague("Premier League", country), false, `${country} is not a top league`);
  }
  assert.equal(isTopLeague("Bundesliga", "DE"), true);
  assert.equal(isTopLeague("Bundesliga", "AT"), false, "Austria is not the Bundesliga");
  assert.equal(isTopLeague("Ligue 1", "FR"), true);
  assert.equal(isTopLeague("Ligue 1", "BF"), false, "Burkina Faso is not Ligue 1");
});

test("continental competitions are top leagues despite having no country", () => {
  assert.equal(isTopLeague("UEFA Champions League", null), true);
  assert.equal(isTopLeague("UEFA Champions League", ""), true);
});

test("the viewer's own country outranks a merely notable foreign league", () => {
  // Home country beats MAJOR, but not ELITE -- Europe's big five and the World
  // Cup lead everywhere, which is the explicit product requirement.
  const home = scorePrediction(p({ leagueCountry: "KE" }), "KE", NOW);
  const major = scorePrediction(p({ leagueCountry: "NL", leagueName: "Eredivisie" }), "KE", NOW);
  const elite = scorePrediction(p({ leagueCountry: "GB", leagueName: "Premier League" }), "KE", NOW);

  assert.ok(home > major, "the Kenyan league beats the Eredivisie for a Kenyan");
  assert.ok(elite > home, "but the Premier League leads");
});

test("a domestic top flight still leads for its own audience", () => {
  // Brazil's Serie A is home (1000) plus major (600), so it outranks the EPL
  // for a Brazilian even though the EPL is elite.
  const brasileirao = scorePrediction(p({ leagueCountry: "BR", leagueName: "Serie A" }), "BR", NOW);
  const epl = scorePrediction(p({ leagueCountry: "GB", leagueName: "Premier League" }), "BR", NOW);
  assert.ok(brasileirao > epl, "a Brazilian leads with Brazilian football");
});

test("a global top league outranks an obscure local one for a foreign viewer", () => {
  // The reported defect: a viewer in Brazil should not lead with Gor Mahia.
  const gorMahia = scorePrediction(p({ leagueCountry: "KE" }), "BR", NOW);
  const premier = scorePrediction(
    p({ leagueCountry: "GB", leagueName: "Premier League" }),
    "BR",
    NOW,
  );
  assert.ok(premier > gorMahia, "Premier League beats Kenyan Premier League for a Brazilian");
});

test("same continent beats a different continent", () => {
  const sameContinent = scorePrediction(
    p({ leagueCountry: "AR", leagueName: "Liga Profesional Argentina" }),
    "BR",
    NOW,
  );
  const otherContinent = scorePrediction(p({ leagueCountry: "KE" }), "BR", NOW);
  assert.ok(sameContinent > otherContinent, "Argentina ranks above Kenya for a Brazilian");
});

test("imminence can no longer outrank a top league", () => {
  // This is the exact defect: kickoff time was the only signal.
  const soonObscure = scorePrediction(
    p({ kickoffAt: "2026-09-04T12:05:00Z", leagueCountry: "KE" }),
    "BR",
    NOW,
  );
  const laterTop = scorePrediction(
    p({ kickoffAt: "2026-09-04T21:00:00Z", leagueCountry: "GB", leagueName: "Premier League" }),
    "BR",
    NOW,
  );
  assert.ok(laterTop > soonObscure, "a later top-league fixture still leads");
});

test("with equal standing, sooner kickoff and higher confidence win", () => {
  const sooner = scorePrediction(p({ kickoffAt: "2026-09-04T13:00:00Z" }), "KE", NOW);
  const later = scorePrediction(p({ kickoffAt: "2026-09-04T22:00:00Z" }), "KE", NOW);
  assert.ok(sooner > later, "sooner wins when everything else is equal");

  const confident = scorePrediction(p({ confidence: 80 }), "KE", NOW);
  const unsure = scorePrediction(p({ confidence: 20 }), "KE", NOW);
  assert.ok(confident > unsure, "higher confidence wins when everything else is equal");
});

test("an unknown viewer country still prefers top leagues over obscure ones", () => {
  for (const unknown of [null, "", undefined, "ZZ"]) {
    const top = scorePrediction(
      p({ leagueCountry: "ES", leagueName: "La Liga" }),
      unknown,
      NOW,
    );
    const obscure = scorePrediction(
      p({ leagueCountry: "GR", leagueName: "Gamma Ethniki - Group 10" }),
      unknown,
      NOW,
    );
    assert.ok(top > obscure, `falls back sensibly for ${String(unknown)}`);
  }
});

test("rankPredictions orders a realistic mixed feed for a Brazilian viewer", () => {
  const feed = [
    p({ id: "gor", kickoffAt: "2026-09-04T12:30:00Z", leagueCountry: "KE" }),
    p({ id: "epl", kickoffAt: "2026-09-04T19:00:00Z", leagueCountry: "GB", leagueName: "Premier League" }),
    p({ id: "bra", kickoffAt: "2026-09-04T23:00:00Z", leagueCountry: "BR", leagueName: "Serie A" }),
    p({ id: "obs", kickoffAt: "2026-09-04T12:10:00Z", leagueCountry: "GR", leagueName: "Gamma Ethniki - Group 10" }),
  ];
  const order = rankPredictions(feed, "BR", NOW).map((row) => row.id);

  assert.equal(order[0], "bra", "home country first");
  assert.equal(order[1], "epl", "then the global top league");
  // Both remaining fixtures are equally irrelevant to a Brazilian: neither is a
  // top league and neither shares the continent. They tie on standing and split
  // on imminence, so assert they are both BELOW the relevant ones rather than
  // pinning which of the two lands last.
  assert.deepEqual(order.slice(0, 2), ["bra", "epl"]);
  assert.deepEqual(order.slice(2).sort(), ["gor", "obs"], "both obscure leagues rank last");
  assert.ok(order.indexOf("gor") > order.indexOf("epl"), "Gor Mahia is not near the top");
});

test("rankPredictions does not mutate its input", () => {
  const feed = [p({ id: "a" }), p({ id: "b", leagueCountry: "GB", leagueName: "Premier League" })];
  const before = feed.map((row) => row.id);
  rankPredictions(feed, "BR", NOW);
  assert.deepEqual(feed.map((row) => row.id), before);
});

// --- Elite tiering, tournaments, and senior men's priority -----------------

test("Europe's big leagues outrank the viewer's own domestic league", () => {
  // Requested explicitly: EPL/La Liga/Serie A/Ligue 1 should lead when they
  // play. A Kenyan viewer cares more about the Premier League than about a
  // mid-table FKF fixture.
  const fkf = scorePrediction(p({ leagueCountry: "KE", leagueName: "FKF Premier League" }), "KE", NOW);
  for (const [country, league] of [
    ["GB", "Premier League"],
    ["ES", "La Liga"],
    ["IT", "Serie A"],
    ["DE", "Bundesliga"],
    ["FR", "Ligue 1"],
  ]) {
    assert.ok(
      scorePrediction(p({ leagueCountry: country, leagueName: league }), "KE", NOW) > fkf,
      `${league} should outrank the viewer's domestic league`,
    );
  }
});

test("the World Cup and continental championships rank as elite", () => {
  const epl = scorePrediction(p({ leagueCountry: "GB", leagueName: "Premier League" }), "KE", NOW);
  for (const name of ["World Cup", "UEFA Champions League", "Euro Championship", "Africa Cup of Nations"]) {
    const score = scorePrediction(p({ leagueCountry: null, leagueName: name }), "KE", NOW);
    assert.ok(score >= epl * 0.75, `${name} should rank near the top, got ${score} vs EPL ${epl}`);
  }
});

test("a qualifier does not rank as the tournament itself", () => {
  const finals = scorePrediction(p({ leagueCountry: null, leagueName: "World Cup" }), "", NOW);
  const qualifier = scorePrediction(
    p({ leagueCountry: null, leagueName: "World Cup - Qualification Asia" }),
    "",
    NOW,
  );
  assert.ok(finals > qualifier, "the finals outrank qualifiers");
});

test("every senior men's fixture precedes every women's or youth one", () => {
  // A sort band, not a weight. No penalty or cap works reliably: a senior
  // fixture can legitimately score near zero, so any non-senior score high
  // enough to stay visible would still outrank it. Banding holds for every
  // viewer, including one with no country where nothing else separates them.
  const feed = [
    p({ id: "wucl", leagueCountry: null, leagueName: "UEFA Champions League Women" }),
    p({ id: "frauen", leagueCountry: "DE", leagueName: "Frauen Bundesliga" }),
    p({ id: "u19", leagueCountry: "DE", leagueName: "U19 Bundesliga" }),
    p({ id: "obscure", leagueCountry: "GR", leagueName: "Gamma Ethniki - Group 10" }),
    p({ id: "epl", leagueCountry: "GB", leagueName: "Premier League" }),
  ];
  for (const viewer of ["KE", "GB", "BR", "DE", ""]) {
    const order = rankPredictions(feed, viewer, NOW).map((row) => row.id);
    const lastSenior = Math.max(order.indexOf("epl"), order.indexOf("obscure"));
    const firstNonSenior = Math.min(
      order.indexOf("wucl"),
      order.indexOf("frauen"),
      order.indexOf("u19"),
    );
    assert.ok(
      lastSenior < firstNonSenior,
      `viewer ${viewer || "unknown"}: senior men's must all precede the rest, got ${order.join(",")}`,
    );
  }
});

test("the non-senior marker catches the spellings that actually occur", () => {
  for (const nonSenior of [
    "UEFA Champions League Women",
    "AFC Women's Champions League",
    "Supercup der Frauen",
    "CONMEBOL U20 Femenino",
    "NWSL Women",
    "World Cup - Women - Qualification Concacaf",
    "U19 Bundesliga",
    "UEFA Youth League",
  ]) {
    assert.equal(isSeniorMens(nonSenior), false, `"${nonSenior}" is not senior men's`);
  }
  for (const mens of ["Premier League", "World Cup", "Serie A", "Ligue 1", "Copa Do Brasil"]) {
    assert.equal(isSeniorMens(mens), true, `"${mens}" is senior men's`);
  }
});

test("a women's competition is demoted, not deleted", () => {
  const feed = [
    p({ id: "minor", leagueCountry: "DE", leagueName: "Frauen Bundesliga" }),
    p({ id: "wucl", leagueCountry: null, leagueName: "UEFA Champions League Women" }),
  ];
  const order = rankPredictions(feed, "", NOW).map((row) => row.id);
  assert.equal(order.length, 2, "nothing is dropped");
  assert.deepEqual([...order].sort(), ["minor", "wucl"], "both survive the demotion");
  // Both resolve to elite standing once the women's marker is stripped -- the
  // Frauen-Bundesliga genuinely is Germany's top women's league -- so no
  // ordering between them is asserted. Their position relative to senior
  // men's football is what matters, and the banding test covers that.
});
