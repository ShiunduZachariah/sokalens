from __future__ import annotations

import unittest

from prediction_engine.confidence import (
    CalibrationBin,
    CalibrationProfile,
    ConfidenceInput,
    ConfidenceScorer,
    ConfidenceTier,
    ConfidenceWeights,
)


class ApproxAssertions:
    def assertFloatApprox(self, actual: float, expected: float, places: int = 7) -> None:
        self.assertAlmostEqual(actual, expected, places=places)  # type: ignore[attr-defined]


class ConfidenceScoringTests(ApproxAssertions, unittest.TestCase):
    def test_data_completeness_contribution_scales_directly(self) -> None:
        result = ConfidenceScorer().score(
            confidence_input(data_completeness=0.80),
            weights=single_factor_weights(data_completeness=25),
        )

        self.assertFloatApprox(result.factor_points["data_completeness"], 20.0)
        self.assertFloatApprox(result.raw_score, 0.80)

    def test_sample_size_contribution_scales_from_smaller_team_sample(self) -> None:
        result = ConfidenceScorer().score(
            confidence_input(home_sample_size=12, away_sample_size=9),
            weights=single_factor_weights(sample_size=15),
        )

        self.assertFloatApprox(result.factor_points["sample_size"], 9.0)
        self.assertFloatApprox(result.raw_score, 0.60)

    def test_model_agreement_contribution_rewards_close_model_outputs(self) -> None:
        result = ConfidenceScorer().score(
            confidence_input(model_a_probability=0.60, blended_probability=0.65),
            weights=single_factor_weights(model_agreement=20),
        )

        self.assertFloatApprox(result.factor_points["model_agreement"], 16.0)
        self.assertFloatApprox(result.raw_score, 0.80)

    def test_market_cross_validation_contribution_uses_external_reference_alignment(self) -> None:
        result = ConfidenceScorer().score(
            confidence_input(
                blended_probability=0.60,
                external_probabilities=(0.55, 0.70),
            ),
            weights=single_factor_weights(market_cross_validation=15),
        )

        self.assertFloatApprox(result.factor_points["market_cross_validation"], 9.0)
        self.assertFloatApprox(result.raw_score, 0.60)

    def test_lineup_confirmation_contribution_requires_confirmed_lineups(self) -> None:
        scorer = ConfidenceScorer()

        unconfirmed = scorer.score(
            confidence_input(lineups_confirmed=False),
            weights=single_factor_weights(lineup_confirmation=10),
        )
        confirmed = scorer.score(
            confidence_input(lineups_confirmed=True),
            weights=single_factor_weights(lineup_confirmation=10),
        )

        self.assertFloatApprox(unconfirmed.factor_points["lineup_confirmation"], 0.0)
        self.assertFloatApprox(confirmed.factor_points["lineup_confirmation"], 10.0)

    def test_market_volatility_contribution_rewards_stable_markets(self) -> None:
        result = ConfidenceScorer().score(
            confidence_input(market_volatility=0.30),
            weights=single_factor_weights(market_volatility=5),
        )

        self.assertFloatApprox(result.factor_points["market_volatility"], 3.5)
        self.assertFloatApprox(result.raw_score, 0.70)

    def test_historical_accuracy_contribution_scales_from_accuracy_rate(self) -> None:
        result = ConfidenceScorer().score(
            confidence_input(historical_accuracy=0.72),
            weights=single_factor_weights(historical_accuracy=10),
        )

        self.assertFloatApprox(result.factor_points["historical_accuracy"], 7.2)
        self.assertFloatApprox(result.raw_score, 0.72)

    def test_tier_assignment_uses_inclusive_threshold_boundaries(self) -> None:
        scorer = ConfidenceScorer()

        cases = [
            (0.75, ConfidenceTier.STRONG),
            (0.749, ConfidenceTier.MEDIUM),
            (0.65, ConfidenceTier.MEDIUM),
            (0.649, ConfidenceTier.LOW),
            (0.55, ConfidenceTier.LOW),
            (0.549, ConfidenceTier.NONE),
        ]

        for score, tier in cases:
            with self.subTest(score=score):
                result = scorer.score(
                    confidence_input(data_completeness=score),
                    weights=single_factor_weights(data_completeness=100),
                )

                self.assertEqual(result.tier, tier)

    def test_max_confidence_tier_cap_limits_final_tier_without_hiding_raw_score(self) -> None:
        result = ConfidenceScorer().score(
            confidence_input(data_completeness=1.0, max_confidence_tier=ConfidenceTier.MEDIUM),
            weights=single_factor_weights(data_completeness=100),
        )

        self.assertFloatApprox(result.raw_score, 1.0)
        self.assertEqual(result.uncapped_tier, ConfidenceTier.STRONG)
        self.assertEqual(result.tier, ConfidenceTier.MEDIUM)

    def test_calibration_profile_adjusts_confidence_toward_observed_hit_rate(self) -> None:
        profile = CalibrationProfile(
            bins=(
                CalibrationBin(lower=0.70, upper=0.90, total=60, correct=36),
                CalibrationBin(lower=0.90, upper=1.0, total=20, correct=18),
            ),
            minimum_bin_size=10,
        )

        result = ConfidenceScorer().score(
            confidence_input(data_completeness=0.80),
            weights=single_factor_weights(data_completeness=100),
            calibration_profile=profile,
        )

        self.assertFloatApprox(result.raw_score, 0.80)
        self.assertFloatApprox(result.calibrated_score, 0.60)
        self.assertEqual(result.tier, ConfidenceTier.LOW)


def confidence_input(**overrides: object) -> ConfidenceInput:
    values = {
        "data_completeness": 0.0,
        "home_sample_size": 0,
        "away_sample_size": 0,
        "model_a_probability": 0.50,
        "blended_probability": 0.50,
        "external_probabilities": (),
        "lineups_confirmed": False,
        "market_volatility": 1.0,
        "historical_accuracy": 0.0,
        "max_confidence_tier": None,
    }
    values.update(overrides)
    return ConfidenceInput(**values)


def single_factor_weights(**overrides: int) -> ConfidenceWeights:
    values = {
        "data_completeness": 0,
        "sample_size": 0,
        "model_agreement": 0,
        "market_cross_validation": 0,
        "lineup_confirmation": 0,
        "market_volatility": 0,
        "historical_accuracy": 0,
    }
    values.update(overrides)
    return ConfidenceWeights(**values)


if __name__ == "__main__":
    unittest.main()
