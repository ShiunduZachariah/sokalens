from __future__ import annotations

import math
import unittest

from prediction_engine.shot_possession_markets import (
    possession_market,
    shots_on_target_market,
    tier_market_eligibility,
)


class ApproxAssertions:
    def assertFloatApprox(self, actual: float, expected: float, places: int = 7) -> None:
        self.assertAlmostEqual(actual, expected, places=places)  # type: ignore[attr-defined]


class ShotPossessionMarketTests(ApproxAssertions, unittest.TestCase):
    def test_shots_on_target_market_derives_over_under_from_hand_computed_poisson(self) -> None:
        result = shots_on_target_market(
            expected_total_lambda=2.0,
            lines=(1.5, 2.5),
        )

        p0 = math.exp(-2.0)
        p1 = 2.0 * math.exp(-2.0)
        p2 = 2.0 * math.exp(-2.0)

        self.assertEqual(result.market_key, "shots_on_target")
        self.assertEqual(result.method, "shots_on_target_poisson")
        self.assertFloatApprox(result.lambda_value, 2.0)
        self.assertFloatApprox(result.lines[1.5].under, p0 + p1)
        self.assertFloatApprox(result.lines[1.5].over, 1 - (p0 + p1))
        self.assertFloatApprox(result.lines[2.5].under, p0 + p1 + p2)
        self.assertFloatApprox(result.lines[2.5].over, 1 - (p0 + p1 + p2))

    def test_tier_c_completeness_floor_excludes_fixture_that_tier_b_allows(self) -> None:
        tier_b = tier_market_eligibility(
            market_tier="B",
            has_statistics=True,
            group_completeness=0.67,
            statistic_sample_size=8,
        )
        tier_c = tier_market_eligibility(
            market_tier="C",
            has_statistics=True,
            group_completeness=0.67,
            statistic_sample_size=8,
        )

        self.assertTrue(tier_b.eligible)
        self.assertFloatApprox(tier_b.required_completeness, 0.65)
        self.assertFalse(tier_c.eligible)
        self.assertEqual(tier_c.exclusion_reason, "insufficient_tier_completeness")
        self.assertFloatApprox(tier_c.required_completeness, 0.70)

    def test_tier_c_eligibility_requires_explicit_statistics_coverage(self) -> None:
        decision = tier_market_eligibility(
            market_tier="C",
            has_statistics=False,
            group_completeness=1.0,
            statistic_sample_size=20,
        )

        self.assertFalse(decision.eligible)
        self.assertEqual(decision.exclusion_reason, "statistics_not_supported")

    def test_possession_market_side_probabilities_favor_home_above_even_line(self) -> None:
        result = possession_market(expected_home_possession_pct=55.0)

        self.assertGreater(result.side_probabilities["home_more"], result.side_probabilities["away_more"])
        self.assertFloatApprox(sum(result.side_probabilities.values()), 1.0)


if __name__ == "__main__":
    unittest.main()
