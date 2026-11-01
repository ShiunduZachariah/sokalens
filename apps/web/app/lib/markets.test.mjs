import assert from "node:assert/strict";
import test from "node:test";

import { formatMarket, formatMarketOutcome } from "./markets.ts";

test("over-under selections use the market unit instead of a generic goals suffix", () => {
  assert.equal(formatMarketOutcome("total_goals", "under_2.5"), "Under 2.5 Goals");
  assert.equal(formatMarketOutcome("shots_on_target", "under_8.5"), "Under 8.5 Shots on Target");
  assert.equal(formatMarketOutcome("total_shots", "over_18.5"), "Over 18.5 Shots");
  assert.equal(formatMarketOutcome("total_corners", "over_7.5"), "Over 7.5 Corners");
  assert.equal(formatMarketOutcome("total_cards", "over_2.5"), "Over 2.5 Cards");
  assert.equal(formatMarketOutcome("total_fouls", "over_18.5"), "Over 18.5 Fouls");
});

test("common market and bet labels follow the selected locale", () => {
  assert.equal(formatMarket("btts", "pt"), "Ambas as equipas marcam");
  assert.equal(formatMarketOutcome("1x2", "home", "pt"), "Casa");
  assert.equal(formatMarketOutcome("1x2", "draw", "de"), "Unentschieden");
  assert.equal(formatMarketOutcome("total_goals", "over_2.5", "pt"), "Mais de 2.5 golos");
  assert.equal(formatMarketOutcome("total_corners", "under_9.5", "es"), "Menos de 9.5 córners");
});

test("every supported language localizes less common market families", () => {
  assert.equal(formatMarket("possession_winner", "es"), "Ganador de posesión");
  assert.equal(formatMarket("total_cards", "fr"), "Total de cartons");
  assert.equal(formatMarket("throw_ins", "pt"), "Lançamentos laterais");
  assert.equal(formatMarket("half_time_result", "ar"), "نتيجة الشوط الأول");
  assert.equal(formatMarket("total_corners", "de"), "Ecken gesamt");
  assert.equal(formatMarket("correct_score", "it"), "Risultato esatto");
});

test("composite and aliased outcomes localize each semantic part", () => {
  assert.equal(formatMarketOutcome("1x2", "1", "pt"), "Casa");
  assert.equal(formatMarketOutcome("ht_ft", "ht_home_ft_draw", "es"), "Local / Empate");
  assert.equal(formatMarketOutcome("team_ou_goals", "away_under_1.5", "fr"), "Extérieur Moins de 1.5 buts");
  assert.equal(formatMarketOutcome("btts_total_goals", "yes_over_2.5", "de"), "Ja & Über 2.5 Tore");
  assert.equal(formatMarketOutcome("odd_even_total_goals", "odd", "ar"), "فردي");
});
