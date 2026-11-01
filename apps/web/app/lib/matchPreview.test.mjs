import assert from "node:assert/strict";
import test from "node:test";

import { MAX_MARKET_READS, MIN_SENTENCES, buildMatchPreview } from "./matchPreview.ts";

const stats = (overrides = {}) => ({
  matches: 10, won: 5, drawn: 3, lost: 2,
  goalsFor: 16, goalsAgainst: 9, cleanSheets: 4, bothTeamsScored: 7, overTwoFive: 7,
  ...overrides,
});

const fixture = (overrides = {}) => ({
  id: 4517267,
  homeTeam: "Kahraba Ismailia",
  awayTeam: "Pharco",
  homeForm: "W W L W D",
  awayForm: "W W D W W",
  homeStats: stats(),
  awayStats: stats({ goalsFor: 21, goalsAgainst: 6, cleanSheets: 5 }),
  prediction: { marketKey: "1x2", selection: "home", confidence: 58 },
  predictions: [
    { marketKey: "1x2", selection: "home", confidence: 58 },
    { marketKey: "total_goals", selection: "under_3.5", confidence: 74 },
    { marketKey: "btts", selection: "yes", confidence: 63 },
    { marketKey: "double_chance", selection: "home_or_draw", confidence: 81 },
    { marketKey: "total_corners", selection: "over_8.5", confidence: 66 },
  ],
  odds: [{ label: "Home", value: "2.10" }, { label: "Draw", value: "3.20" }, { label: "Away", value: "3.40" }],
  ...overrides,
});

const section = (sections, heading) => sections?.find((item) => item.heading === heading);
const text = (sections) => (sections ?? []).flatMap((item) => item.sentences).join(" ");

// ------------------------------------------------------------ shape

test("the same fixture always reads the same", () => {
  assert.deepEqual(buildMatchPreview(fixture()), buildMatchPreview(fixture()));
});

test("sections appear in reading order and the market list is a list", () => {
  const sections = buildMatchPreview(fixture());
  assert.deepEqual(sections.map((item) => item.heading), ["Form", "Goals", "The model's view", "Market by market", "The market"]);
  assert.equal(section(sections, "Market by market").style, "list");
});

test("different fixtures do not all share one phrasing", () => {
  const phrasings = new Set();
  for (let id = 1; id <= 40; id += 1) {
    phrasings.add(buildMatchPreview(fixture({ id }))[0].sentences[0].replace(/\d+/g, "#"));
  }
  assert.ok(phrasings.size > 1, "form sentence never varied across 40 fixtures");
});

// ------------------------------------------------------------ headline

test("the headline names the team and cites form evidence", () => {
  const model = section(buildMatchPreview(fixture()), "The model's view").sentences.join(" ");
  assert.match(model, /Kahraba Ismailia to win/);
  assert.match(model, /58%/);
  assert.match(model, /points from their last 5/);
});

test("a low-confidence headline is described as a close call", () => {
  const prediction = { marketKey: "1x2", selection: "draw", confidence: 38 };
  const model = section(buildMatchPreview(fixture({ prediction, predictions: [prediction] })), "The model's view").sentences.join(" ");
  assert.match(model, /close call|no clear favourite/);
  assert.match(model, /a draw/);
});

test("a high-confidence headline says so", () => {
  const prediction = { marketKey: "double_chance", selection: "draw_or_away", confidence: 82 };
  const model = section(buildMatchPreview(fixture({ prediction })), "The model's view").sentences.join(" ");
  assert.match(model, /strongest call|high reading/);
  assert.match(model, /Pharco to avoid defeat/);
});

test("a generic headline names its market so it still makes sense", () => {
  const prediction = { marketKey: "asian_handicap", selection: "home_minus_1", confidence: 64 };
  const model = section(buildMatchPreview(fixture({ prediction })), "The model's view").sentences.join(" ");
  assert.match(model, /Home Minus 1 \(Asian Handicap\)/);
});

test("an unreadable headline is dropped, and its market is not hidden from the list", () => {
  const prediction = { marketKey: "1x2", selection: "banana", confidence: 70 };
  const sections = buildMatchPreview(fixture({ prediction, predictions: [prediction, ...fixture().predictions.slice(1)] }));
  assert.equal(section(sections, "The model's view"), undefined);
  assert.doesNotMatch(text(sections), /banana/i);
});

test("a headline on the wrong confidence scale is not quoted", () => {
  const prediction = { marketKey: "1x2", selection: "home", confidence: 0.58 };
  const sections = buildMatchPreview(fixture({ prediction, predictions: [prediction] }));
  assert.equal(section(sections, "The model's view"), undefined);
  assert.doesNotMatch(text(sections), /\b1%|\b0%/);
});

// ------------------------------------------------------------ market by market

test("every other market gets one line, strongest first, in plain words", () => {
  const lines = section(buildMatchPreview(fixture()), "Market by market").sentences;
  assert.deepEqual(lines.map((line) => line.split(":")[0]), ["Double Chance", "Total Goals", "Total Corners", "Both Teams to Score"]);
  assert.match(lines[0], /Kahraba Ismailia to avoid defeat, 81%/);
  assert.match(lines[1], /3 or fewer goals in the match, 74%/);
  assert.match(lines[2], /9 or more corners in the match, 66%/);
});

test("the headline's market is not repeated in the list", () => {
  const lines = section(buildMatchPreview(fixture()), "Market by market").sentences;
  assert.ok(lines.every((line) => !line.startsWith("Match Result")));
});

test("contradictory picks in one market collapse to the stronger", () => {
  const predictions = [
    ...fixture().predictions,
    { marketKey: "total_goals", selection: "over_3.5", confidence: 40 },
    { marketKey: "TOTAL_GOALS", selection: "over_1.5", confidence: 30 },
  ];
  const lines = section(buildMatchPreview(fixture({ predictions })), "Market by market").sentences.filter((line) => line.startsWith("Total Goals"));
  assert.equal(lines.length, 1);
  assert.match(lines[0], /3 or fewer goals/);
});

test("the list is capped", () => {
  const markets = ["double_chance", "btts", "total_goals", "total_corners", "total_cards", "total_fouls", "total_shots", "shots_on_target", "odd_even_total_goals", "highest_scoring_half", "possession_winner"];
  const selections = { double_chance: "home_or_draw", btts: "no", total_goals: "over_2.5", odd_even_total_goals: "odd", highest_scoring_half: "second_half", possession_winner: "home" };
  const predictions = markets.map((marketKey, index) => ({ marketKey, selection: selections[marketKey] ?? "over_4.5", confidence: 90 - index }));
  const lines = section(buildMatchPreview(fixture({ predictions })), "Market by market").sentences;
  assert.equal(lines.length, MAX_MARKET_READS);
  assert.match(lines[0], /^Double Chance/);
});

test("hidden, malformed and badly-scaled picks never reach the list", () => {
  const predictions = [
    fixture().prediction,
    { marketKey: "correct_score", selection: "2-1", confidence: 14 },
    { marketKey: "total_goals", selection: "over_abc", confidence: 70 },
    { marketKey: "btts", selection: "yes", confidence: Number.NaN },
    { marketKey: "total_cards", selection: "under_3.5", confidence: 0.7 },
    { marketKey: "total_fouls", selection: "over_21.5", confidence: 250 },
    { marketKey: "", selection: "home", confidence: 60 },
    { marketKey: "total_corners", selection: "over_8.5", confidence: 66 },
  ];
  const lines = section(buildMatchPreview(fixture({ predictions })), "Market by market").sentences;
  assert.deepEqual(lines, ["Total Corners: 9 or more corners in the match, 66%."]);
});

// ------------------------------------------------------------ goals

test("rates are withheld below the minimum sample", () => {
  const body = text(buildMatchPreview(fixture({ homeStats: stats({ matches: 2 }), awayStats: stats({ matches: 3 }) })));
  assert.doesNotMatch(body, /per match|per game|clean sheet|over 2\.5 goals came|average \d/i);
});

test("a team with zero recorded matches cannot divide by zero", () => {
  const body = text(buildMatchPreview(fixture({ homeStats: stats({ matches: 0, goalsFor: 0 }), awayStats: stats({ matches: 0 }) })));
  assert.doesNotMatch(body, /NaN|Infinity/);
});

// ------------------------------------------------------------ odds

test("the market sentence names the favourite and compares the model", () => {
  const market = section(buildMatchPreview(fixture()), "The market").sentences.join(" ");
  assert.match(market, /Kahraba Ismailia (are the bookmakers' favourites|favourites)/);
  // 1 / 2.10 = 47.6%: the model's 58% differs by more than five points.
  assert.match(market, /48%/);
});

test("equal prices name no favourite", () => {
  const odds = [{ label: "Home", value: "2.60" }, { label: "Draw", value: "3.10" }, { label: "Away", value: "2.60" }];
  const market = section(buildMatchPreview(fixture({ odds })), "The market").sentences.join(" ");
  assert.match(market, /cannot separate the sides/);
  assert.doesNotMatch(market, /favourite/);
});

test("a shortest-priced draw is described as a tight game", () => {
  const odds = [{ label: "Home", value: "3.30" }, { label: "Draw", value: "2.80" }, { label: "Away", value: "3.10" }];
  assert.match(section(buildMatchPreview(fixture({ odds })), "The market").sentences[0], /draw is the shortest price at 2\.80/);
});

test("broken prices produce no market section", () => {
  for (const [home, away] of [["1.00", "3.00"], ["0", "2.0"], ["abc", "2.0"], ["", "2.0"], ["-2.1", "2.0"], ["2.1", "1e3"], ["NaN", "2"]]) {
    const odds = [{ label: "Home", value: home }, { label: "Away", value: away }];
    assert.equal(section(buildMatchPreview(fixture({ odds })), "The market"), undefined, `prices ${home}/${away} accepted`);
  }
  assert.equal(section(buildMatchPreview(fixture({ odds: [{ label: "Away", value: "2.0" }] })), "The market"), undefined);
});

test("prices are matched on label case- and whitespace-insensitively", () => {
  const odds = [{ label: " HOME ", value: "1.90" }, { label: "away", value: " 4.20 " }];
  assert.match(section(buildMatchPreview(fixture({ odds })), "The market").sentences[0], /Kahraba Ismailia/);
});

// ------------------------------------------------------------ thin and hostile input

test("a fixture with nothing to say gets no preview rather than filler", () => {
  const thin = fixture({ homeForm: "", awayForm: "W", homeStats: null, awayStats: null, prediction: null, predictions: [], odds: [] });
  assert.equal(buildMatchPreview(thin), null);
});

test("exactly the minimum is enough, one fewer is not", () => {
  const base = { homeStats: null, awayStats: null, odds: [], prediction: null, predictions: [] };
  // Home form only: one form sentence. Not enough.
  assert.equal(buildMatchPreview(fixture({ ...base, awayForm: "" })), null);
  // Both forms: two sentences plus the comparison makes three.
  const minimal = buildMatchPreview(fixture(base));
  assert.equal(text(minimal).split(/(?<=\.)\s/).length >= MIN_SENTENCES, true);
});

test("garbage in the form strings is ignored, not echoed", () => {
  const body = text(buildMatchPreview(fixture({ homeForm: "w, d; X ?? L w", awayForm: "<b>W</b> W D" })));
  assert.doesNotMatch(body, /<b>|\?\?|;|X /);
});

test("team names with apostrophes and accents read correctly", () => {
  const body = text(buildMatchPreview(fixture({ homeTeam: "O'Higgins", awayTeam: "Rangers" })));
  assert.match(body, /O'Higgins/);
  assert.doesNotMatch(body, /Rangers's/);
});

test("no combination of inputs leaks undefined, NaN or null", () => {
  const forms = ["", "W", "W W W W W", "L L L L L", "D D D", "w l d w l"];
  const statOptions = [null, stats(), stats({ matches: 0 }), stats({ matches: 4, goalsFor: 0, goalsAgainst: 0, bothTeamsScored: 0, overTwoFive: 0, cleanSheets: 4 })];
  const oddsOptions = [[], [{ label: "Home", value: "1.50" }, { label: "Away", value: "6.00" }], [{ label: "Home", value: "x" }]];
  let built = 0;
  for (const homeForm of forms) {
    for (const homeStats of statOptions) {
      for (const odds of oddsOptions) {
        const sections = buildMatchPreview(fixture({ homeForm, awayForm: forms[(forms.indexOf(homeForm) + 2) % forms.length], homeStats, awayStats: homeStats, odds }));
        if (!sections) continue;
        built += 1;
        for (const item of sections) {
          assert.ok(item.sentences.length > 0, `empty section ${item.heading}`);
          for (const sentence of item.sentences) {
            assert.doesNotMatch(sentence, /undefined|NaN|null|Infinity|\$\{/, sentence);
            assert.match(sentence, /[.!?)]$/, `unterminated: ${sentence}`);
          }
        }
      }
    }
  }
  assert.ok(built > 0);
});

test("one piece of evidence is cited once, not under every pick it touches", () => {
  const prediction = { marketKey: "1x2", selection: "away", confidence: 58 };
  const predictions = [
    prediction,
    { marketKey: "double_chance", selection: "draw_or_away", confidence: 81 },
    { marketKey: "win_either_half", selection: "away", confidence: 66 },
    { marketKey: "draw_no_bet", selection: "away", confidence: 64 },
  ];
  const body = text(buildMatchPreview(fixture({ prediction, predictions, homeForm: "L L D L W", awayForm: "W W W D W" })));
  // The evidence sentence: "{home} have N points from their last 5, {away} M".
  assert.equal(body.match(/have \d+ points from their last 5, Pharco \d+/g).length, 1);
});

test("the Form section and the evidence agree on what counts as a gap", () => {
  // Three points apart over five games: "little between" in Form, so the
  // evidence must not call it support.
  const body = text(buildMatchPreview(fixture({ homeForm: "W W L W D", awayForm: "W W D W W", prediction: { marketKey: "1x2", selection: "away", confidence: 58 } })));
  assert.match(body, /little between the sides|Recent form is close/);
  assert.match(body, /Pharco 13, so recent form barely separates them/);
  assert.doesNotMatch(body, /Pharco 13, which (supports|cuts against) it/);
});

test("missing form strings and arrays from an older API do not throw", () => {
  assert.doesNotThrow(() => buildMatchPreview(fixture({ homeForm: undefined, awayForm: null })));
});

// ------------------------------------------------------------ discipline

const cards = (overrides = {}) => ({ matches: 10, yellowCards: 21, redCards: 1, matchCards: 44, ...overrides });

test("card records get their own section, before the model's view", () => {
  const sections = buildMatchPreview(fixture({ homeDiscipline: cards(), awayDiscipline: cards({ yellowCards: 12, matchCards: 30 }) }));
  const headings = sections.map((item) => item.heading);
  assert.ok(headings.indexOf("Discipline") > -1);
  assert.ok(headings.indexOf("Discipline") < headings.indexOf("The model's view"));
  const body = section(sections, "Discipline").sentences.join(" ");
  assert.match(body, /2\.1 a game|2\.1 yellow cards a game/);
  assert.match(body, /1\.2/);
  // (44/10 + 30/10) / 2 = 3.7 cards a game counting both sides.
  assert.match(body, /3\.7 cards/);
});

test("the more booked side is named only when the gap is real", () => {
  const wide = section(buildMatchPreview(fixture({ homeDiscipline: cards({ yellowCards: 25, matchCards: 45 }), awayDiscipline: cards({ yellowCards: 12, matchCards: 30 }) })), "Discipline").sentences.join(" ");
  assert.match(wide, /Kahraba Ismailia are the more booked side, by 1\.3 yellow cards a game over Pharco/);
  const close = section(buildMatchPreview(fixture({ homeDiscipline: cards({ yellowCards: 18 }), awayDiscipline: cards({ yellowCards: 15 }) })), "Discipline").sentences.join(" ");
  assert.doesNotMatch(close, /more booked/);
});

test("red cards are mentioned only as a pattern, not a one-off", () => {
  const one = section(buildMatchPreview(fixture({ homeDiscipline: cards({ redCards: 1 }), awayDiscipline: null })), "Discipline").sentences.join(" ");
  assert.doesNotMatch(one, /sent off/);
  const two = section(buildMatchPreview(fixture({ homeDiscipline: cards({ redCards: 3 }), awayDiscipline: null })), "Discipline").sentences.join(" ");
  assert.match(two, /Kahraba Ismailia have had 3 players sent off/);
});

test("one team's record is described alone, with its own match total", () => {
  const body = section(buildMatchPreview(fixture({ homeDiscipline: null, awayDiscipline: cards({ matches: 6, yellowCards: 9, matchCards: 20 }) })), "Discipline").sentences.join(" ");
  assert.match(body, /Pharco/);
  assert.doesNotMatch(body, /Kahraba/);
  assert.match(body, /3\.3 cards in total/);
});

test("card records from fewer than four matches are not quoted", () => {
  const sections = buildMatchPreview(fixture({ homeDiscipline: cards({ matches: 3, yellowCards: 6, matchCards: 12 }), awayDiscipline: cards({ matches: 1, yellowCards: 1, redCards: 0, matchCards: 2 }) }));
  assert.equal(section(sections, "Discipline"), undefined);
});

test("no card record, no Discipline section", () => {
  assert.equal(section(buildMatchPreview(fixture()), "Discipline"), undefined);
  assert.equal(section(buildMatchPreview(fixture({ homeDiscipline: null, awayDiscipline: undefined })), "Discipline"), undefined);
});

test("a single yellow card is singular", () => {
  for (let id = 1; id <= 20; id += 1) {
    const body = section(buildMatchPreview(fixture({ id, homeDiscipline: cards({ matches: 4, yellowCards: 1, redCards: 0, matchCards: 5 }), awayDiscipline: null })), "Discipline").sentences.join(" ");
    assert.doesNotMatch(body, /\b1 yellow cards\b/);
  }
});

test("a clean record reads as zero, not as missing", () => {
  const body = section(buildMatchPreview(fixture({ homeDiscipline: cards({ matches: 5, yellowCards: 0, redCards: 0, matchCards: 3 }), awayDiscipline: null })), "Discipline").sentences.join(" ");
  assert.match(body, /0 yellow cards|0\.0 yellow cards/);
});

test("card data alone can carry a thin preview over the minimum", () => {
  const thin = fixture({ homeForm: "", awayForm: "", homeStats: null, awayStats: null, prediction: null, predictions: [], odds: [] });
  assert.equal(buildMatchPreview(thin), null);
  const withCards = buildMatchPreview({ ...thin, homeDiscipline: cards(), awayDiscipline: cards() });
  assert.deepEqual(withCards.map((item) => item.heading), ["Discipline"]);
});

test("a card pick appears in the market list only once published after the unit fix", () => {
  const discipline = { homeDiscipline: cards(), awayDiscipline: cards({ yellowCards: 12, matchCards: 30 }) };
  const before = { marketKey: "total_cards", selection: "over_3.5", confidence: 68, publishedAt: "2026-09-24T09:00:00Z" };
  const after = { ...before, publishedAt: "2026-09-25T09:00:00Z" };
  const stale = section(buildMatchPreview(fixture({ ...discipline, predictions: [...fixture().predictions, before] })), "Market by market").sentences.join(" ");
  assert.doesNotMatch(stale, /Total Cards/);
  const fresh = section(buildMatchPreview(fixture({ ...discipline, predictions: [...fixture().predictions, after] })), "Market by market").sentences.join(" ");
  // (44/10 + 30/10) / 2 = 3.7 cards a match, above the 3.5 line.
  assert.match(fresh, /Total Cards: 4 or more cards in the match, 68%\. Recent matches involving these sides average 3\.7 cards, in line with the pick\./);
});
