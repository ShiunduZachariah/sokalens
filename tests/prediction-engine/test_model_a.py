from __future__ import annotations

import math
import unittest

from prediction_engine.model_a import (
    ModelAInputs,
    apply_dixon_coles_correction,
    derive_tier_a_markets,
    poisson_score_matrix,
    run_model_a,
)


def _matrix_total(matrix: list[list[float]]) -> float:
    return sum(sum(row) for row in matrix)


class ApproxAssertions:
    def assertFloatApprox(self, actual: float, expected: float, places: int = 7) -> None:
        self.assertAlmostEqual(actual, expected, places=places)  # type: ignore[attr-defined]

    def assertDictApprox(self, actual: dict[str, float], expected: dict[str, float], places: int = 7) -> None:
        self.assertEqual(set(actual), set(expected))  # type: ignore[attr-defined]
        for key, expected_value in expected.items():
            self.assertFloatApprox(actual[key], expected_value, places)

    def assertListApprox(self, actual: list[float], expected: list[float], places: int = 7) -> None:
        self.assertEqual(len(actual), len(expected))  # type: ignore[attr-defined]
        for actual_value, expected_value in zip(actual, expected, strict=True):
            self.assertFloatApprox(actual_value, expected_value, places)


class ModelATests(ApproxAssertions, unittest.TestCase):
    def test_poisson_score_matrix_is_normalized_and_matches_hand_computed_cell(self) -> None:
        matrix = poisson_score_matrix(home_lambda=1.5, away_lambda=1.2, max_goals=2)

        raw = [
            [
                math.exp(-1.5) * (1.5**home_goals) / math.factorial(home_goals)
                * math.exp(-1.2) * (1.2**away_goals) / math.factorial(away_goals)
                for away_goals in range(3)
            ]
            for home_goals in range(3)
        ]
        raw_total = _matrix_total(raw)

        self.assertFloatApprox(_matrix_total(matrix), 1.0)
        self.assertFloatApprox(matrix[1][1], raw[1][1] / raw_total)

    def test_dixon_coles_correction_changes_low_score_cells_and_renormalizes(self) -> None:
        original = poisson_score_matrix(home_lambda=1.1, away_lambda=0.9, max_goals=3)

        corrected = apply_dixon_coles_correction(
            original,
            home_lambda=1.1,
            away_lambda=0.9,
            rho=-0.1,
        )

        self.assertFloatApprox(_matrix_total(corrected), 1.0)
        for cell in [(0, 0), (0, 1), (1, 0), (1, 1)]:
            self.assertNotAlmostEqual(corrected[cell[0]][cell[1]], original[cell[0]][cell[1]])

        non_low_ratios = [
            corrected[home][away] / original[home][away]
            for home in range(4)
            for away in range(4)
            if (home, away) not in {(0, 0), (0, 1), (1, 0), (1, 1)}
        ]
        self.assertEqual(len(set(round(ratio, 12) for ratio in non_low_ratios)), 1)

    def test_tier_a_market_derivations_are_summed_from_fixed_matrices(self) -> None:
        full_time_matrix = [
            [0.08, 0.07, 0.05, 0.02],
            [0.10, 0.12, 0.08, 0.03],
            [0.09, 0.11, 0.07, 0.04],
            [0.04, 0.05, 0.03, 0.02],
        ]
        half_time_matrix = [
            [0.30, 0.15, 0.05, 0.00],
            [0.20, 0.10, 0.04, 0.00],
            [0.08, 0.04, 0.02, 0.00],
            [0.01, 0.005, 0.005, 0.00],
        ]
        second_half_matrix = [
            [0.25, 0.10, 0.05, 0.00],
            [0.18, 0.12, 0.06, 0.00],
            [0.10, 0.07, 0.04, 0.00],
            [0.02, 0.01, 0.00, 0.00],
        ]

        markets = derive_tier_a_markets(
            full_time_matrix,
            half_time_matrix=half_time_matrix,
            second_half_matrix=second_half_matrix,
            top_correct_scores=3,
        )

        self.assertDictApprox(markets["match_result"], {"home": 0.42, "draw": 0.29, "away": 0.29})
        self.assertDictApprox(
            markets["double_chance"],
            {"home_or_draw": 0.71, "draw_or_away": 0.58, "home_or_away": 0.71},
        )
        self.assertDictApprox(markets["draw_no_bet"], {"home": 0.42 / 0.71, "away": 0.29 / 0.71})
        self.assertFloatApprox(markets["total_goals"]["over_2.5"], 0.49)
        self.assertFloatApprox(markets["total_goals"]["under_2.5"], 0.51)
        self.assertFloatApprox(markets["total_goals"]["over_5.5"], 0.02)
        self.assertDictApprox(markets["btts"], {"yes": 0.55, "no": 0.45})
        self.assertFloatApprox(markets["btts_total_goals"]["yes_over_2.5"], 0.43)
        self.assertFloatApprox(markets["btts_match_result"]["home_yes"], 0.19)
        self.assertEqual([scoreline["score"] for scoreline in markets["correct_score"]], ["1-1", "2-1", "1-0"])
        self.assertListApprox(
            [float(scoreline["probability"]) for scoreline in markets["correct_score"]],
            [0.12, 0.11, 0.10],
        )
        self.assertDictApprox(markets["half_time_result"], {"home": 0.34, "draw": 0.42, "away": 0.24})
        self.assertFloatApprox(markets["half_time_full_time"]["draw_away"], 0.42 * 0.29)
        self.assertDictApprox(
            markets["highest_scoring_half"],
            {"first_half": 0.31015, "second_half": 0.44, "equal": 0.24985},
        )
        self.assertDictApprox(
            markets["team_to_score_first"],
            {"home": 0.5021513944223107, "away": 0.41784860557768923, "no_goals": 0.08},
        )
        self.assertFloatApprox(markets["team_total_goals"]["home"]["over_1.5"], 0.45)
        self.assertFloatApprox(markets["team_total_goals"]["away"]["under_2.5"], 0.89)
        self.assertDictApprox(markets["win_to_nil"], {"home": 0.23, "away": 0.14})
        self.assertDictApprox(
            markets["win_either_half"],
            {"home": 0.34 + 0.38 - (0.34 * 0.38), "away": 0.24 + 0.21 - (0.24 * 0.21)},
        )
        self.assertDictApprox(
            markets["clean_sheet"],
            {"home_yes": 0.31, "home_no": 0.69, "away_yes": 0.22, "away_no": 0.78},
        )
        self.assertDictApprox(markets["btts_both_halves"], {"yes": 0.063, "no": 0.937})
        self.assertDictApprox(markets["odd_even_total_goals"], {"odd": 0.49, "even": 0.51})

    def test_model_a_uses_half_time_specific_lambdas_not_half_of_full_time(self) -> None:
        result = run_model_a(
            ModelAInputs(
                home_lambda=2.4,
                away_lambda=1.6,
                half_time_home_lambda=0.4,
                half_time_away_lambda=0.3,
                max_goals=4,
                rho=0.0,
            )
        )

        self.assertFloatApprox(result.lambdas["half_time_home"], 0.4)
        self.assertFloatApprox(result.lambdas["half_time_away"], 0.3)
        self.assertNotAlmostEqual(result.lambdas["half_time_home"], result.lambdas["home"] / 2)
        self.assertNotAlmostEqual(result.lambdas["half_time_away"], result.lambdas["away"] / 2)

    def test_match_importance_widens_matrix_without_changing_lambdas(self) -> None:
        baseline = run_model_a(
            ModelAInputs(home_lambda=1.4, away_lambda=1.0, max_goals=4, rho=0.0, importance_widening=0.0)
        )
        widened = run_model_a(
            ModelAInputs(home_lambda=1.4, away_lambda=1.0, max_goals=4, rho=0.0, importance_widening=0.25)
        )

        self.assertEqual(widened.lambdas, baseline.lambdas)
        self.assertNotEqual(widened.score_matrix, baseline.score_matrix)
        self.assertFloatApprox(_matrix_total(widened.score_matrix), 1.0)

    def test_model_a_result_preserves_full_normalized_corrected_score_matrix(self) -> None:
        result = run_model_a(ModelAInputs(home_lambda=1.5, away_lambda=1.2, max_goals=3, rho=-0.08))

        self.assertEqual(result.score_matrix, result.features_used["model_a_score_matrix"])
        self.assertEqual(len(result.score_matrix), 4)
        self.assertTrue(all(len(row) == 4 for row in result.score_matrix))
        self.assertFloatApprox(_matrix_total(result.score_matrix), 1.0)


if __name__ == "__main__":
    unittest.main()
