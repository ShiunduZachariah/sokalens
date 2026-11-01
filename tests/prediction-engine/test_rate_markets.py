from __future__ import annotations

import math
import unittest

from prediction_engine.rate_markets import (
    combined_total_market,
    poisson_probability,
    rare_event_market,
    team_total_market,
)


class ApproxAssertions:
    def assertFloatApprox(self, actual: float, expected: float, places: int = 7) -> None:
        self.assertAlmostEqual(actual, expected, places=places)  # type: ignore[attr-defined]


class RateMarketTests(ApproxAssertions, unittest.TestCase):
    def test_poisson_probability_matches_hand_computed_values(self) -> None:
        self.assertFloatApprox(poisson_probability(2.0, 0), math.exp(-2.0))
        self.assertFloatApprox(poisson_probability(2.0, 1), 2.0 * math.exp(-2.0))
        self.assertFloatApprox(poisson_probability(2.0, 2), 2.0 * math.exp(-2.0))
        self.assertFloatApprox(poisson_probability(2.0, 3), (8.0 / 6.0) * math.exp(-2.0))

    def test_combined_total_market_derives_over_under_lines_from_poisson_cumulative_probability(self) -> None:
        result = combined_total_market(
            market_key="total_corners",
            expected_total_lambda=2.0,
            lines=(1.5, 2.5),
        )

        p0 = math.exp(-2.0)
        p1 = 2.0 * math.exp(-2.0)
        p2 = 2.0 * math.exp(-2.0)

        self.assertEqual(result.market_key, "total_corners")
        self.assertEqual(result.method, "combined_total_poisson")
        self.assertFloatApprox(result.lambda_value, 2.0)
        self.assertFloatApprox(result.lines[1.5].under, p0 + p1)
        self.assertFloatApprox(result.lines[1.5].over, 1 - (p0 + p1))
        self.assertFloatApprox(result.lines[2.5].under, p0 + p1 + p2)
        self.assertFloatApprox(result.lines[2.5].over, 1 - (p0 + p1 + p2))

    def test_combined_total_market_applies_referee_modifier_when_referee_rate_is_available(self) -> None:
        scaled = combined_total_market(
            market_key="total_cards",
            expected_total_lambda=4.0,
            lines=(5.5,),
            referee_stat_average=6.0,
            league_stat_average=4.0,
        )
        fallback = combined_total_market(
            market_key="total_cards",
            expected_total_lambda=4.0,
            lines=(5.5,),
            referee_stat_average=None,
            league_stat_average=4.0,
        )

        self.assertFloatApprox(scaled.referee_modifier, 1.5)
        self.assertFloatApprox(scaled.lambda_value, 6.0)
        self.assertFloatApprox(fallback.referee_modifier, 1.0)
        self.assertFloatApprox(fallback.lambda_value, 4.0)

    def test_team_total_market_uses_direct_team_lambda_for_team_specific_lines(self) -> None:
        result = team_total_market(
            market_key="home_team_corners",
            team_key="home",
            team_lambda=3.0,
            lines=(4.5,),
        )

        expected_under_4_5 = math.exp(-3.0) * (1 + 3.0 + 4.5 + 4.5 + 3.375)

        self.assertEqual(result.market_key, "home_team_corners")
        self.assertEqual(result.team_key, "home")
        self.assertEqual(result.method, "team_total_poisson")
        self.assertFloatApprox(result.lambda_value, 3.0)
        self.assertFloatApprox(result.lines[4.5].under, expected_under_4_5)
        self.assertFloatApprox(result.lines[4.5].over, 1 - expected_under_4_5)

    def test_rare_event_market_uses_direct_probability_and_carries_confidence_cap(self) -> None:
        result = rare_event_market(
            market_key="red_card_in_match",
            probability_yes=0.18,
            max_confidence_tier="low",
        )

        poisson_yes_if_misused = 1 - math.exp(-0.18)

        self.assertEqual(result.market_key, "red_card_in_match")
        self.assertEqual(result.method, "direct_probability")
        self.assertEqual(result.max_confidence_tier, "low")
        self.assertFloatApprox(result.probability_yes, 0.18)
        self.assertFloatApprox(result.probability_no, 0.82)
        self.assertNotAlmostEqual(result.probability_yes, poisson_yes_if_misused)


if __name__ == "__main__":
    unittest.main()
