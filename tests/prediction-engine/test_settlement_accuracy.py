from __future__ import annotations

from datetime import UTC, date, datetime
import unittest

from prediction_engine.persistence import ConfidenceTier
from prediction_engine.settlement import (
    ActualFixtureResult,
    SettledPrediction,
    accuracy_snapshot,
    settle_prediction,
)


class SettlementAccuracyTests(unittest.TestCase):
    def test_settles_1x2_total_goals_and_btts_predictions(self) -> None:
        actual = ActualFixtureResult(home_goals=2, away_goals=1)

        self.assertTrue(settle_prediction("1x2", "home", actual).is_correct)
        self.assertFalse(settle_prediction("1x2", "draw", actual).is_correct)
        self.assertTrue(settle_prediction("total_goals", "over_2.5", actual).is_correct)
        self.assertFalse(settle_prediction("total_goals", "under_2.5", actual).is_correct)
        self.assertTrue(settle_prediction("btts", "yes", actual).is_correct)

    def test_unavailable_stat_market_is_marked_unsettled_not_guessed(self) -> None:
        actual = ActualFixtureResult(home_goals=1, away_goals=0, home_corners=None, away_corners=None)

        result = settle_prediction("total_corners", "over_8.5", actual)

        self.assertFalse(result.settled)
        self.assertEqual(result.status, "unsettled_data_unavailable")
        self.assertIsNone(result.is_correct)

    def test_settles_tier_b_and_tier_c_stat_markets_when_statistics_exist(self) -> None:
        actual = ActualFixtureResult(
            home_goals=1,
            away_goals=0,
            home_corners=6,
            away_corners=4,
            home_card_points=2,
            away_card_points=3,
            home_fouls=12,
            away_fouls=11,
            home_shots_total=13,
            away_shots_total=9,
            home_shots_on_target=5,
            away_shots_on_target=4,
            home_possession=55.0,
            away_possession=45.0,
        )

        self.assertTrue(settle_prediction("total_corners", "over_9.5", actual).is_correct)
        self.assertTrue(settle_prediction("team_corners_home", "over_5.5", actual).is_correct)
        self.assertTrue(settle_prediction("team_corners_away", "under_4.5", actual).is_correct)
        self.assertTrue(settle_prediction("total_cards", "over_4.5", actual).is_correct)
        self.assertTrue(settle_prediction("total_fouls", "over_21.5", actual).is_correct)
        self.assertTrue(settle_prediction("total_shots", "over_21.5", actual).is_correct)
        self.assertTrue(settle_prediction("shots_on_target", "over_8.5", actual).is_correct)
        self.assertTrue(settle_prediction("possession_winner", "home_more", actual).is_correct)

    def test_settles_tier_d_goal_combination_markets(self) -> None:
        actual = ActualFixtureResult(home_goals=2, away_goals=1)

        self.assertTrue(settle_prediction("btts_total_goals", "yes_over_2.5", actual).is_correct)
        self.assertTrue(settle_prediction("btts_match_result", "home_yes", actual).is_correct)
        self.assertTrue(settle_prediction("result_total_goals", "home_over_2.5", actual).is_correct)
        self.assertFalse(settle_prediction("result_total_goals", "away_under_2.5", actual).is_correct)

    def test_btts_total_goals_grades_each_published_line(self) -> None:
        # Include both sides of every half-goal boundary, goalless/one-sided
        # results, and the live 6-2 regression. Only one of four selections
        # at a given line can win.
        scores = [(0, 0), (0, 1), (1, 0), (1, 1), (2, 1), (2, 2), (3, 2), (3, 3), (6, 2)]
        for home, away in scores:
            actual = ActualFixtureResult(home_goals=home, away_goals=away)
            for line in (0.5, 1.5, 2.5, 3.5, 4.5, 5.5):
                btts = "yes" if home and away else "no"
                side = "over" if home + away > line else "under"
                expected = f"{btts}_{side}_{line:g}"
                for selected_btts in ("yes", "no"):
                    for selected_side in ("over", "under"):
                        prediction = f"{selected_btts}_{selected_side}_{line:g}"
                        with self.subTest(score=(home, away), prediction=prediction):
                            result = settle_prediction("btts_total_goals", prediction, actual)
                            self.assertEqual(result.actual, expected)
                            self.assertEqual(result.is_correct, prediction == expected)
                            self.assertTrue(result.settled)
                            self.assertEqual(result.status, "settled")

    def test_settles_additional_tier_a_markets_and_voids_draw_no_bet(self) -> None:
        actual = ActualFixtureResult(
            home_goals=2,
            away_goals=2,
            home_goals_ht=1,
            away_goals_ht=0,
            first_goal_team="home",
        )

        self.assertTrue(settle_prediction("double_chance", "home_or_draw", actual).is_correct)
        self.assertTrue(settle_prediction("correct_score", "2-2", actual).is_correct)
        self.assertTrue(settle_prediction("half_time_result", "home", actual).is_correct)
        self.assertTrue(settle_prediction("highest_scoring_half", "second_half", actual).is_correct)
        self.assertTrue(settle_prediction("team_to_score_first", "home", actual).is_correct)
        self.assertTrue(settle_prediction("odd_even_total_goals", "even", actual).is_correct)

        draw_no_bet = settle_prediction("draw_no_bet", "home", actual)
        self.assertEqual(draw_no_bet.status, "void")
        self.assertTrue(draw_no_bet.settled)
        self.assertIsNone(draw_no_bet.is_correct)

    def test_first_goal_market_requires_event_data_unless_no_goals(self) -> None:
        scored_without_event = ActualFixtureResult(home_goals=1, away_goals=0)
        goalless = ActualFixtureResult(home_goals=0, away_goals=0)

        self.assertEqual(
            settle_prediction("team_to_score_first", "home", scored_without_event).status,
            "unsettled_data_unavailable",
        )
        self.assertTrue(settle_prediction("team_to_score_first", "no_goals", goalless).is_correct)

    def test_accuracy_snapshot_matches_hand_calculated_totals(self) -> None:
        rows = [
            SettledPrediction("1x2", ConfidenceTier.STRONG, 39, True),
            SettledPrediction("1x2", ConfidenceTier.STRONG, 39, False),
            SettledPrediction("btts", ConfidenceTier.MEDIUM, 39, True),
        ]

        snapshot = accuracy_snapshot(rows, snapshot_date=date(2026, 7, 1), scope="overall")

        self.assertEqual(snapshot.total_predictions, 3)
        self.assertEqual(snapshot.correct, 2)
        self.assertAlmostEqual(snapshot.accuracy, 2 / 3)
        self.assertEqual(snapshot.scope, "overall")


if __name__ == "__main__":
    unittest.main()
