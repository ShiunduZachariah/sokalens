import math
import unittest

from prediction_engine.challenger_markets import count_document, goal_document, under_probability
from prediction_engine.model_a import ModelAInputs, run_model_a
from prediction_engine.promoted_markets import (
    CHAMPION,
    MIX,
    MODEL_B,
    MODEL_C,
    ModelForecasts,
    forecasts_from_rows,
    published_count_candidates,
    published_goal_markets,
)


def _document(home: float, away: float) -> dict[str, dict[str, float]]:
    result = run_model_a(ModelAInputs(home_lambda=home, away_lambda=away))
    return goal_document(result.markets, result.score_matrix)


class PublishedGoalMarketsTests(unittest.TestCase):
    def setUp(self) -> None:
        self.champion = run_model_a(ModelAInputs(home_lambda=1.5, away_lambda=1.1))
        self.model_b = _document(1.9, 0.8)
        self.model_c = _document(1.2, 1.4)
        self.forecasts = ModelForecasts(model_b=self.model_b, model_c=self.model_c)

    def published(self, forecasts: ModelForecasts | None = None):
        return published_goal_markets(self.champion.markets, self.champion.score_matrix, forecasts)

    def test_no_forecast_publishes_the_champion_untouched(self) -> None:
        result = self.published(None)
        self.assertEqual(result.markets["total_goals"], self.champion.markets["total_goals"])
        self.assertIs(result.score_matrix, self.champion.score_matrix)
        self.assertEqual(result.sources, {})

    def test_the_champion_keeps_the_markets_it_wins(self) -> None:
        result = self.published(self.forecasts).markets
        for market in ("match_result", "double_chance", "draw_no_bet", "odd_even_total_goals", "team_to_score_first"):
            self.assertEqual(result[market], self.champion.markets[market], market)

    def test_totals_and_btts_are_the_mean_of_all_three_models(self) -> None:
        result = self.published(self.forecasts)
        over = self.champion.markets["total_goals"]["over_2.5"]
        self.assertAlmostEqual(
            result.markets["total_goals"]["over_2.5"],
            (over + self.model_b["total_goals:2.5"]["over_2.5"] + self.model_c["total_goals:2.5"]["over_2.5"]) / 3,
        )
        self.assertAlmostEqual(
            result.markets["btts"]["yes"],
            (self.champion.markets["btts"]["yes"] + self.model_b["btts"]["yes"] + self.model_c["btts"]["yes"]) / 3,
        )
        self.assertEqual(result.sources["total_goals"], MIX)
        self.assertEqual(result.sources["btts"], MIX)
        self.assertEqual(result.sources["btts_match_result"], MIX)

    def test_half_markets_come_from_model_b(self) -> None:
        result = self.published(self.forecasts)
        self.assertEqual(result.markets["half_time_result"], self.model_b["half_time_result"])
        self.assertEqual(result.markets["highest_scoring_half"], self.model_b["highest_scoring_half"])
        self.assertEqual(result.sources["half_time_result"], MODEL_B)

    def test_the_mixed_grid_is_a_distribution_and_matches_the_mixed_markets(self) -> None:
        result = self.published(self.forecasts)
        grid = result.score_matrix
        self.assertAlmostEqual(sum(sum(row) for row in grid), 1.0, places=2)
        # Scores inside the stored box are exact, so a line wholly inside it agrees
        # with the mean of the three documents.
        under = sum(grid[h][a] for h in range(len(grid)) for a in range(len(grid[h])) if h + a < 2.5)
        expected = (
            sum(
                self.champion.score_matrix[h][a]
                for h in range(len(grid))
                for a in range(len(grid[h]))
                if h + a < 2.5
            )
            + self.model_b["total_goals:2.5"]["under_2.5"]
            + self.model_c["total_goals:2.5"]["under_2.5"]
        ) / 3
        self.assertAlmostEqual(under, expected, places=3)
        # The published scoreline is the grid's own most likely cell.
        top = result.markets["correct_score"][0]
        home, away = (int(value) for value in str(top["score"]).split("-"))
        self.assertAlmostEqual(float(top["probability"]), grid[home][away])
        self.assertEqual(float(top["probability"]), max(max(row) for row in grid))
        self.assertEqual(result.sources["correct_score"], MIX)

    def test_btts_total_goals_keeps_every_published_line(self) -> None:
        result = self.published(self.forecasts).markets["btts_total_goals"]
        self.assertEqual(set(result), set(self.champion.markets["btts_total_goals"]))

    def test_the_mix_needs_both_models(self) -> None:
        result = self.published(ModelForecasts(model_b=self.model_b))
        self.assertEqual(result.markets["total_goals"], self.champion.markets["total_goals"])
        self.assertIs(result.score_matrix, self.champion.score_matrix)
        self.assertEqual(result.sources, {"half_time_result": MODEL_B, "highest_scoring_half": MODEL_B})

    def test_a_malformed_forecast_falls_back_market_by_market(self) -> None:
        broken = {**self.model_c, "btts": {"yes": math.nan, "no": 0.5}}
        result = self.published(ModelForecasts(model_b=self.model_b, model_c=broken))
        self.assertEqual(result.markets["btts"], self.champion.markets["btts"])
        self.assertNotIn("btts", result.sources)
        self.assertEqual(result.sources["total_goals"], MIX)

    def test_a_probability_outside_zero_and_one_is_refused(self) -> None:
        broken = {**self.model_b, "half_time_result": {"home": 1.4, "draw": 0.1, "away": 0.1}}
        result = self.published(ModelForecasts(model_b=broken, model_c=self.model_c))
        self.assertEqual(result.markets["half_time_result"], self.champion.markets["half_time_result"])


class PublishedCountCandidatesTests(unittest.TestCase):
    def setUp(self) -> None:
        self.document = count_document("total_corners", 10.4, 0.04)

    def test_replaces_only_a_market_the_champion_would_publish(self) -> None:
        candidates = [("total_corners", "over_7.5", 0.6), ("1x2", "home", 0.5)]
        forecast = ModelForecasts(model_c={**self.document, **count_document("total_cards", 4.1, 0.1)})
        result, sources = published_count_candidates(candidates, forecast)
        self.assertEqual([row[0] for row in result], ["total_corners", "1x2"])
        self.assertEqual(sources, {"total_corners": MODEL_C})
        self.assertEqual(result[1], candidates[1])

    def test_takes_the_side_and_line_model_c_is_surest_of(self) -> None:
        result, _ = published_count_candidates([("total_corners", "over_7.5", 0.6)], ModelForecasts(model_c=self.document))
        options = {
            selection: probability
            for market in self.document.values()
            for selection, probability in market.items()
        }
        best = max(options, key=lambda name: options[name])
        self.assertEqual(result[0][1], best)
        self.assertAlmostEqual(result[0][2], options[best])

    def test_keeps_the_champion_when_a_line_is_missing(self) -> None:
        document = dict(self.document)
        document.pop("total_corners:9.5")
        candidate = ("total_corners", "over_7.5", 0.6)
        result, sources = published_count_candidates([candidate], ModelForecasts(model_c=document))
        self.assertEqual(result, [candidate])
        self.assertEqual(sources, {})

    def test_no_model_c_forecast_keeps_the_champion(self) -> None:
        candidate = ("total_cards", "over_2.5", 0.7)
        self.assertEqual(published_count_candidates([candidate], ModelForecasts()), ([candidate], {}))
        self.assertEqual(published_count_candidates([candidate], None), ([candidate], {}))

    def test_negative_binomial_is_wider_than_poisson(self) -> None:
        self.assertGreater(under_probability(10.0, 0.1, 6.5), under_probability(10.0, 0.0, 6.5))


class ForecastRowsTests(unittest.TestCase):
    def test_no_rows_means_no_forecast(self) -> None:
        self.assertIsNone(forecasts_from_rows([]))

    def test_rows_map_to_their_models_with_provenance(self) -> None:
        from datetime import UTC, datetime

        made = datetime(2026, 11, 5, 8, tzinfo=UTC)
        forecasts = forecasts_from_rows(
            [
                {"model_key": "elo-markets", "model_version": "elo-markets-a", "markets": {"btts": {}}, "as_of_timestamp": made},
                {"model_key": "model-c-markets", "model_version": "model-c-b", "markets": {"x": {}}, "as_of_timestamp": made},
            ]
        )
        self.assertEqual(forecasts.model_b, {"btts": {}})
        self.assertEqual(forecasts.model_c, {"x": {}})
        self.assertEqual(forecasts.provenance["model-c-b"], made.isoformat())


class ChampionSourceConstantTests(unittest.TestCase):
    def test_the_default_source_name_is_stable(self) -> None:
        # Stored in features_used, so reports can group on it.
        self.assertEqual(CHAMPION, "champion")


if __name__ == "__main__":
    unittest.main()
