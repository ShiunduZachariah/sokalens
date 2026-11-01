from __future__ import annotations

import unittest

from prediction_engine.combination_markets import (
    ComponentEligibility,
    MarketLeg,
    combination_eligible,
    joint_probability,
)


class ApproxAssertions:
    def assertFloatApprox(self, actual: float, expected: float, places: int = 7) -> None:
        self.assertAlmostEqual(actual, expected, places=places)  # type: ignore[attr-defined]


class CombinationMarketTests(ApproxAssertions, unittest.TestCase):
    def setUp(self) -> None:
        self.matrix = [
            [0.08, 0.07, 0.05, 0.02],
            [0.10, 0.12, 0.08, 0.03],
            [0.09, 0.11, 0.07, 0.04],
            [0.04, 0.05, 0.03, 0.02],
        ]

    def test_winner_and_under_over_combos_sum_matching_score_cells(self) -> None:
        home_win = MarketLeg.match_result("home")
        under_2_5 = MarketLeg.total_goals("under", 2.5)
        over_2_5 = MarketLeg.total_goals("over", 2.5)

        self.assertFloatApprox(
            joint_probability(self.matrix, home_win, under_2_5),
            0.10 + 0.09,
        )
        self.assertFloatApprox(
            joint_probability(self.matrix, home_win, over_2_5),
            0.11 + 0.04 + 0.05 + 0.03,
        )

        naive_product = 0.42 * 0.51
        self.assertNotAlmostEqual(joint_probability(self.matrix, home_win, under_2_5), naive_product)

    def test_btts_and_result_combo_sums_joint_cells_not_marginals(self) -> None:
        btts_yes = MarketLeg.btts("yes")
        away_win = MarketLeg.match_result("away")

        self.assertFloatApprox(
            joint_probability(self.matrix, btts_yes, away_win),
            0.08 + 0.03 + 0.04,
        )

        naive_product = 0.55 * 0.29
        self.assertNotAlmostEqual(joint_probability(self.matrix, btts_yes, away_win), naive_product)

    def test_combination_eligibility_requires_both_components_to_be_eligible(self) -> None:
        full = ComponentEligibility(eligible=True, tier="FULL")
        blocked = ComponentEligibility(eligible=False, tier="SKIP", reason="corner history below floor")

        self.assertTrue(combination_eligible(full, full).eligible)

        inherited = combination_eligible(full, blocked)
        self.assertFalse(inherited.eligible)
        self.assertEqual(inherited.tier, "SKIP")
        self.assertEqual(inherited.reason, "component market ineligible: corner history below floor")


if __name__ == "__main__":
    unittest.main()
