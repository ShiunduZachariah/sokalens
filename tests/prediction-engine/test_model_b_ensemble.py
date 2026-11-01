from __future__ import annotations

import unittest

from prediction_engine.config import BlendWeights, ProbabilityClamp, default_config, load_config
from prediction_engine.ensemble import blend_probability, blend_tier_a_markets
from prediction_engine.model_b import ModelBConfig, ModelBInputs, run_model_b


class ApproxAssertions:
    def assertFloatApprox(self, actual: float, expected: float, places: int = 7) -> None:
        self.assertAlmostEqual(actual, expected, places=places)  # type: ignore[attr-defined]

    def assertDictApprox(self, actual: dict[str, float], expected: dict[str, float], places: int = 7) -> None:
        self.assertEqual(set(actual), set(expected))  # type: ignore[attr-defined]
        for key, expected_value in expected.items():
            self.assertFloatApprox(actual[key], expected_value, places)


class ModelBEnsembleTests(ApproxAssertions, unittest.TestCase):
    def test_model_b_maps_strength_differential_to_hand_computed_tier_a_deltas(self) -> None:
        result = run_model_b(
            ModelBInputs(
                home_recent_ppg=2.25,
                away_recent_ppg=0.75,
                home_btts_rate=0.60,
                away_btts_rate=0.50,
                model_a_btts_yes=0.52,
                home_over_2_5_rate=0.70,
                away_over_2_5_rate=0.60,
                model_a_over_2_5=0.58,
            )
        )

        self.assertDictApprox(result.current_strength, {"home": 0.75, "away": 0.25})
        self.assertFloatApprox(result.strength_differential, 0.50)
        self.assertDictApprox(
            result.market_deltas["match_result"],
            {"home": 0.08, "draw": -0.04, "away": -0.04},
        )
        self.assertDictApprox(result.market_deltas["btts"], {"yes": 0.03, "no": -0.03})
        self.assertDictApprox(result.market_deltas["total_goals"], {"over_2.5": 0.07, "under_2.5": -0.07})

    def test_model_b_caps_extreme_form_adjustments(self) -> None:
        result = run_model_b(
            ModelBInputs(
                home_recent_ppg=3.00,
                away_recent_ppg=0.00,
                home_btts_rate=1.00,
                away_btts_rate=1.00,
                model_a_btts_yes=0.20,
                home_over_2_5_rate=0.00,
                away_over_2_5_rate=0.00,
                model_a_over_2_5=0.90,
            ),
            ModelBConfig(
                one_x_two_shift_cap=0.15,
                btts_shift_cap=0.04,
                total_goals_shift_cap=0.05,
            ),
        )

        self.assertDictApprox(
            result.market_deltas["match_result"],
            {"home": 0.15, "draw": -0.075, "away": -0.075},
        )
        self.assertDictApprox(result.market_deltas["btts"], {"yes": 0.04, "no": -0.04})
        self.assertDictApprox(result.market_deltas["total_goals"], {"over_2.5": -0.05, "under_2.5": 0.05})
        self.assertTrue(all(abs(delta) <= 0.15 for delta in result.market_deltas["match_result"].values()))

    def test_blend_probability_matches_hand_calculated_formula(self) -> None:
        blended = blend_probability(
            raw_probability=0.40,
            adjustment_delta=0.10,
            weights=BlendWeights(statistical_model=0.55, form_adjustment=0.45),
            clamp=ProbabilityClamp(minimum=0.01, maximum=0.99),
        )

        self.assertFloatApprox(blended, (0.55 * 0.40) + (0.45 * (0.40 + 0.10)))

    def test_ensemble_renormalizes_one_x_two_after_independent_blending(self) -> None:
        markets = blend_tier_a_markets(
            {"match_result": {"home": 0.50, "draw": 0.25, "away": 0.25}},
            {"match_result": {"home": 0.01, "draw": 0.00, "away": 0.00}},
            default_config(),
        )

        independently_blended = {"home": 0.5045, "draw": 0.25, "away": 0.25}
        total = sum(independently_blended.values())
        expected = {key: value / total for key, value in independently_blended.items()}

        self.assertFloatApprox(sum(markets["match_result"].values()), 1.0)
        self.assertDictApprox(markets["match_result"], expected)

    def test_ensemble_clamps_extreme_outputs_to_configured_safe_range(self) -> None:
        markets = blend_tier_a_markets(
            {"btts": {"yes": 0.98, "no": 0.02}},
            {"btts": {"yes": 0.10, "no": -0.10}},
            default_config(),
        )

        self.assertDictApprox(markets["btts"], {"yes": 0.99, "no": 0.01})

    def test_ensemble_uses_per_market_blend_weights(self) -> None:
        config = load_config(
            {
                "model_weights": {
                    "btts": {"statistical_model": 0.65, "form_adjustment": 0.35},
                    "total_goals": {"statistical_model": 0.70, "form_adjustment": 0.30},
                }
            }
        )

        markets = blend_tier_a_markets(
            {
                "btts": {"yes": 0.40},
                "total_goals": {"over_2.5": 0.40},
            },
            {
                "btts": {"yes": 0.20},
                "total_goals": {"over_2.5": 0.20},
            },
            config,
        )

        self.assertFloatApprox(markets["btts"]["yes"], 0.40 + (0.35 * 0.20))
        self.assertFloatApprox(markets["total_goals"]["over_2.5"], 0.40 + (0.30 * 0.20))


if __name__ == "__main__":
    unittest.main()
