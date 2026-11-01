import assert from "node:assert/strict";
import test from "node:test";

import { CARD_UNIT_FIXED_AT, READABLE_MARKETS, isDescribable, lineOutcome, possessive, readPick } from "./marketReads.ts";

const teams = { homeTeam: "Arsenal", awayTeam: "Chelsea" };

const stats = (overrides = {}) => ({
  matches: 10, won: 5, drawn: 3, lost: 2,
  goalsFor: 16, goalsAgainst: 9, cleanSheets: 4, bothTeamsScored: 7, overTwoFive: 7,
  ...overrides,
});

const read = (marketKey, selection, context = teams, confidence = 62) =>
  readPick(context, { marketKey, selection, confidence });

// ------------------------------------------------------------ positive cases

const expectedOutcomes = [
  ["1x2", "home", "Arsenal to win"],
  ["1x2", "away", "Chelsea to win"],
  ["1x2", "draw", "a draw"],
  ["1x2", "1", "Arsenal to win"],
  ["1x2", "X", "a draw"],
  ["match_result", "2", "Chelsea to win"],
  ["double_chance", "home_or_draw", "Arsenal to avoid defeat"],
  ["double_chance", "draw_or_away", "Chelsea to avoid defeat"],
  ["double_chance", "home_or_away", "a winner, not a draw"],
  ["double_chance", "home_draw", "Arsenal to avoid defeat"],
  ["double_chance", "1x", "Arsenal to avoid defeat"],
  ["double_chance", "x2", "Chelsea to avoid defeat"],
  ["double_chance", "12", "a winner, not a draw"],
  ["draw_no_bet", "home", "Arsenal to win, with a draw voiding the pick"],
  ["total_goals", "over_2.5", "3 or more goals in the match"],
  ["total_goals", "under_2.5", "2 or fewer goals in the match"],
  ["total_goals", "under_0.5", "no goals in the match"],
  ["total_goals", "over_0.5", "1 or more goals in the match"],
  ["total_goals", "over_3", "more than 3 goals in the match"],
  ["btts", "yes", "both teams to score"],
  ["btts", "no", "at least one side to fail to score"],
  ["btts_total_goals", "yes_over_2.5", "both teams to score, with 3 or more goals"],
  ["btts_total_goals", "no_under_2.5", "only one side (or neither) to score, with 2 or fewer goals"],
  ["btts_total_goals", "yes_under_2.5", "which only a 1-1 satisfies"],
  ["btts_match_result", "home_yes", "Arsenal to win with both teams scoring"],
  ["btts_match_result", "away_no", "Chelsea to win without conceding"],
  ["btts_match_result", "draw_yes", "a score draw (both teams scoring)"],
  ["btts_match_result", "draw_no", "a goalless draw"],
  ["result_total_goals", "home_over_2.5", "Arsenal to win in a match with 3 or more goals"],
  ["result_total_goals", "draw_under_2.5", "a draw in a match with 2 or fewer goals"],
  ["team_ou_goals", "away_over_1.5", "Chelsea to score 2 or more goals"],
  ["team_over_under_goals", "home_under_0.5", "Arsenal to score no goals"],
  ["team_to_score_first", "home", "Arsenal to score first"],
  ["team_to_score_first", "no_goals", "no goals in the match"],
  ["team_to_score_first", "no_goal", "no goals in the match"],
  ["clean_sheet", "home_yes", "Arsenal to keep a clean sheet"],
  ["clean_sheet", "away_no", "Chelsea to concede at least once"],
  ["win_to_nil", "away", "Chelsea to win without conceding"],
  ["win_either_half", "home", "Arsenal to win at least one half"],
  ["half_time_result", "draw", "level at half-time"],
  ["half_time_result", "away", "Chelsea ahead at half-time"],
  ["ht_ft", "ht_home_ft_home", "Arsenal to lead at half-time and win"],
  ["ht_ft", "ht_draw_ft_away", "level at half-time, then Chelsea to win"],
  ["half_time_full_time", "home_draw", "Arsenal ahead at half-time, then a draw at full time"],
  ["halftime_fulltime", "draw_draw", "level at half-time and at full time"],
  ["highest_scoring_half", "second_half", "more goals in the second half than the first"],
  ["highest_scoring_half", "equal", "the same number of goals in each half"],
  ["odd_even_total_goals", "odd", "an odd number of goals (1, 3, 5...)"],
  ["btts_both_halves", "yes", "both teams to score in each half"],
  ["possession_winner", "away", "Chelsea to have more of the ball"],
  ["total_corners", "over_9.5", "10 or more corners in the match"],
  ["team_corners_home", "under_4.5", "4 or fewer corners for Arsenal"],
  ["team_corners_away", "over_3.5", "4 or more corners for Chelsea"],
  ["total_fouls", "over_21.5", "22 or more fouls in the match"],
  ["total_shots", "under_18.5", "18 or fewer shots in the match"],
  ["shots_on_target", "over_8.5", "9 or more shots on target in the match"],
];

test("every market family reads as the outcome it describes", () => {
  for (const [market, selection, expected] of expectedOutcomes) {
    const result = read(market, selection);
    assert.ok(result, `${market}/${selection} was not readable`);
    assert.ok(result.outcome.includes(expected), `${market}/${selection}: "${result.outcome}" lacks "${expected}"`);
    assert.equal(result.generic, false, `${market}/${selection} fell back to the generic label`);
  }
});

test("market keys and selections are case- and whitespace-insensitive", () => {
  assert.equal(read("  1X2 ", " HOME ").outcome, "Arsenal to win");
  assert.equal(read("Total_Goals", "OVER_2.5").outcome, "3 or more goals in the match");
});

test("the market name and rounded confidence travel with the read", () => {
  const result = readPick(teams, { marketKey: "btts", selection: "yes", confidence: 63.6 });
  assert.equal(result.market, "Both Teams to Score");
  assert.equal(result.confidence, 64);
});

test("an unknown market falls back to its catalogue label, marked generic", () => {
  const result = read("asian_handicap", "home_minus_1");
  assert.equal(result.generic, true);
  assert.equal(result.outcome, "Home Minus 1");
  assert.equal(result.market, "Asian Handicap");
});

// Card picks forecast before the engine counted cards in one unit are not
// described; one published after CARD_UNIT_FIXED_AT is.
const AFTER_FIX = "2026-09-25T06:00:00Z";
const cardRecord = (overrides = {}) => ({ matches: 10, yellowCards: 20, redCards: 1, matchCards: 42, ...overrides });
const cards = (selection, context = teams, publishedAt = AFTER_FIX) =>
  readPick(context, { marketKey: "total_cards", selection, confidence: 64, publishedAt });

test("card picks from before the unit fix, or undated, are withheld", () => {
  for (const publishedAt of [undefined, "", "not a date", "2026-09-24T23:59:59Z", "2026-09-01T00:00:00Z"]) {
    assert.equal(readPick(teams, { marketKey: "total_cards", selection: "over_5.5", confidence: 64, publishedAt }), null, `published ${publishedAt} was described`);
    assert.equal(readPick(teams, { marketKey: "cards", selection: "under_3.5", confidence: 60, publishedAt }), null);
  }
});

test("card picks from after the fix read as a card count", () => {
  assert.equal(cards("over_3.5").outcome, "4 or more cards in the match");
  assert.equal(cards("under_4.5").outcome, "4 or fewer cards in the match");
  assert.equal(readPick(teams, { marketKey: "cards", selection: "over_5.5", confidence: 60, publishedAt: CARD_UNIT_FIXED_AT }).outcome, "6 or more cards in the match");
});

test("card evidence compares the line with the sides' real card averages", () => {
  const both = { ...teams, homeDiscipline: cardRecord(), awayDiscipline: cardRecord({ matchCards: 30 }) };
  // (42/10 + 30/10) / 2 = 3.6 cards a match.
  assert.match(cards("over_3.5", both).evidence, /average 3\.6 cards, in line with the pick/);
  assert.match(cards("over_4.5", both).evidence, /average 3\.6 cards, which points the other way/);
  const one = { ...teams, homeDiscipline: null, awayDiscipline: cardRecord({ matches: 5, matchCards: 25 }) };
  assert.match(cards("under_5.5", one).evidence, /average 5\.0 cards, in line with the pick/);
});

test("card evidence needs four recorded matches", () => {
  const thin = { ...teams, homeDiscipline: cardRecord({ matches: 3, matchCards: 12 }), awayDiscipline: null };
  const read = cards("over_3.5", thin);
  assert.equal(read.outcome, "4 or more cards in the match");
  assert.equal(read.evidence, undefined);
});

test("malformed card lines are refused even after the fix", () => {
  for (const selection of ["over_", "over_abc", "lots", "over_-1.5", "yes"]) {
    assert.equal(cards(selection), null, selection);
  }
});

// ------------------------------------------------------------ line outcomes

test("half lines become counts; whole lines keep 'more than'", () => {
  assert.equal(lineOutcome("over", "2.5", "goals"), "3 or more goals");
  assert.equal(lineOutcome("under", "2.5", "goals"), "2 or fewer goals");
  assert.equal(lineOutcome("under", "0.5", "cards"), "no cards");
  assert.equal(lineOutcome("over", "10.5", "corners"), "11 or more corners");
  assert.equal(lineOutcome("over", "3", "goals"), "more than 3 goals");
  assert.equal(lineOutcome("under", "3", "goals"), "fewer than 3 goals");
  assert.equal(lineOutcome("over", "0", "goals"), "more than 0 goals");
});

test("malformed lines are refused rather than guessed", () => {
  for (const raw of ["", "abc", "-1.5", "1e3", "2.5.5", " 2.5", "Infinity", "NaN", "1001", "0x10", "2,5"]) {
    assert.equal(lineOutcome("over", raw, "goals"), null, `line "${raw}" was accepted`);
  }
  assert.equal(lineOutcome("sideways", "2.5", "goals"), null);
});

// ------------------------------------------------------------ negative cases

const malformed = [
  ["1x2", "banana"], ["1x2", ""], ["1x2", "home_draw"], ["1x2", "3"],
  ["double_chance", "home_home"], ["double_chance", "1x2"], ["double_chance", "home_or"],
  ["double_chance", "home_or_banana"], ["double_chance", "11"], ["double_chance", "home_draw_away"],
  ["draw_no_bet", "draw"], ["draw_no_bet", "x"],
  ["total_goals", "over"], ["total_goals", "over_"], ["total_goals", "over_abc"],
  ["total_goals", "sideways_2.5"], ["total_goals", "over_-1.5"], ["total_goals", "2.5"],
  ["btts", "maybe"], ["btts", "yes_over_2.5"],
  ["btts_total_goals", "yes_over_"], ["btts_total_goals", "maybe_over_2.5"], ["btts_total_goals", "yes_2.5"],
  ["btts_match_result", "home_maybe"], ["btts_match_result", "yes_home"], ["btts_match_result", "home"],
  ["result_total_goals", "home_over_x"], ["result_total_goals", "home_2.5"], ["result_total_goals", "win_over_2.5"],
  ["team_ou_goals", "draw_over_1.5"], ["team_ou_goals", "home_1.5"],
  ["team_to_score_first", "draw"], ["team_to_score_first", "nobody"],
  ["clean_sheet", "home"], ["clean_sheet", "draw_yes"],
  ["win_to_nil", "draw"], ["win_either_half", "draw"],
  ["half_time_result", "banana"],
  ["ht_ft", "home_banana"], ["ht_ft", "ht_home"], ["ht_ft", "home"],
  ["highest_scoring_half", "third_half"],
  ["odd_even_total_goals", "prime"],
  ["btts_both_halves", "sometimes"],
  ["possession_winner", "draw"],
  ["total_corners", "over_-1"], ["total_corners", "lots"], ["team_corners_home", "over_x"],
];

test("a malformed selection on a known market is refused, never half-read", () => {
  for (const [market, selection] of malformed) {
    assert.equal(read(market, selection), null, `${market}/"${selection}" produced a reading`);
  }
});

test("markup or control characters in an unknown market's selection are refused", () => {
  assert.equal(read("asian_handicap", "<script>alert(1)</script>"), null);
  assert.equal(read("asian_handicap", "home\u0000"), null);
});

test("confidence outside 1-100, non-finite, or on the 0-1 scale is refused", () => {
  for (const confidence of [Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY, 0, -5, 0.63, 100.1, 150]) {
    assert.equal(isDescribable({ marketKey: "1x2", selection: "home", confidence }), false, `confidence ${confidence} accepted`);
  }
  assert.equal(isDescribable({ marketKey: "1x2", selection: "home", confidence: 1 }), true);
  assert.equal(isDescribable({ marketKey: "1x2", selection: "home", confidence: 100 }), true);
});

test("hidden markets and empty keys are never described", () => {
  assert.equal(read("correct_score", "2-1"), null);
  assert.equal(read("", "home"), null);
  assert.equal(read("1x2", "   "), null);
});

// ------------------------------------------------------------ evidence

const withStats = (overrides = {}) => ({
  ...teams,
  homeForm: "W W W D W",
  awayForm: "L L D L W",
  homeStats: stats(),
  awayStats: stats(),
  ...overrides,
});

test("result evidence says whether form supports or cuts against the pick", () => {
  assert.match(read("1x2", "home", withStats()).evidence, /which supports it/);
  assert.match(read("1x2", "away", withStats()).evidence, /which cuts against it/);
  assert.match(read("1x2", "draw", withStats()).evidence, /wider gap than a draw usually suggests/);
  const level = read("1x2", "home", withStats({ homeForm: "W D L W D", awayForm: "D W L D W" }));
  assert.match(level.evidence, /barely separates them/);
});

test("form of unequal length is not compared", () => {
  assert.equal(read("1x2", "home", withStats({ awayForm: "W W W" })).evidence, undefined);
});

test("goal evidence agrees or disagrees with the line honestly", () => {
  const scoring = withStats({ homeStats: stats({ goalsFor: 20, goalsAgainst: 15 }), awayStats: stats({ goalsFor: 18, goalsAgainst: 14 }) });
  assert.match(read("total_goals", "over_2.5", scoring).evidence, /average 3\.4 goals, in line with the pick/);
  assert.match(read("total_goals", "under_2.5", scoring).evidence, /which points the other way/);
});

test("BTTS evidence supports yes on high rates and cuts against it on low", () => {
  const high = withStats({ homeStats: stats({ bothTeamsScored: 8 }), awayStats: stats({ bothTeamsScored: 7 }) });
  const low = withStats({ homeStats: stats({ bothTeamsScored: 2 }), awayStats: stats({ bothTeamsScored: 3 }) });
  const mixed = withStats({ homeStats: stats({ bothTeamsScored: 8 }), awayStats: stats({ bothTeamsScored: 2 }) });
  assert.match(read("btts", "yes", high).evidence, /supports it/);
  assert.match(read("btts", "yes", low).evidence, /cuts against it/);
  assert.match(read("btts", "no", low).evidence, /supports it/);
  assert.doesNotMatch(read("btts", "yes", mixed).evidence, /supports|cuts/);
});

test("rates are not quoted from fewer than four matches, or from none", () => {
  for (const matches of [0, 1, 3]) {
    const thin = withStats({ homeStats: stats({ matches }), awayStats: stats({ matches }) });
    assert.equal(read("btts", "yes", thin).evidence, undefined);
    assert.equal(read("total_goals", "over_2.5", thin).evidence, undefined);
    assert.equal(read("clean_sheet", "home_yes", thin).evidence, undefined);
  }
  assert.equal(read("btts", "yes", withStats({ homeStats: null, awayStats: undefined })).evidence, undefined);
});

test("markets without recorded statistics carry no invented evidence", () => {
  for (const [market, selection] of [["total_corners", "over_9.5"], ["total_fouls", "under_21.5"], ["odd_even_total_goals", "even"], ["possession_winner", "home"]]) {
    assert.equal(read(market, selection, withStats()).evidence, undefined, `${market} invented evidence`);
  }
});

test("possessives follow English, not a template", () => {
  assert.equal(possessive("Pharco"), "Pharco's");
  assert.equal(possessive("Rangers"), "Rangers'");
  assert.equal(possessive("Kahraba Ismailia"), "Kahraba Ismailia's");
});

// ------------------------------------------------------------ sweep

test("no market and selection combination leaks undefined, NaN or null into text", () => {
  const selections = [
    "home", "away", "draw", "yes", "no", "odd", "even", "equal", "first_half", "second_half", "no_goals",
    "over_2.5", "under_0.5", "over_3", "home_yes", "away_no", "draw_yes", "home_over_2.5", "away_under_1.5",
    "yes_over_2.5", "no_under_3.5", "ht_home_ft_away", "draw_draw", "home_or_draw", "1x", "12", "",
    "over_", "banana", "home_over_", "yes__2.5", "__", "over_2.5_extra", "ht__ft_", "1", "x", "2",
  ];
  const contexts = [
    teams, withStats(), withStats({ homeStats: stats({ matches: 0 }), awayStats: stats({ matches: 0 }) }),
    { homeTeam: "O'Higgins", awayTeam: "Ñublense" },
    { ...teams, homeDiscipline: { matches: 10, yellowCards: 20, redCards: 1, matchCards: 42 }, awayDiscipline: { matches: 0, yellowCards: 0, redCards: 0, matchCards: 0 } },
  ];
  for (const market of [...READABLE_MARKETS, "asian_handicap", "correct_score"]) {
    for (const selection of selections) {
      for (const context of contexts) {
        const result = readPick(context, { marketKey: market, selection, confidence: 57, publishedAt: "2026-09-26T00:00:00Z" });
        if (!result) continue;
        const text = `${result.outcome} ${result.evidence ?? ""} ${result.market}`;
        assert.doesNotMatch(text, /undefined|NaN|null|Infinity/, `${market}/${selection}: ${text}`);
        assert.ok(result.outcome.trim().length > 0, `${market}/${selection}: empty outcome`);
      }
    }
  }
});
