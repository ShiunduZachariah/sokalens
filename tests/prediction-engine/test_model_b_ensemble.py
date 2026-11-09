from __future__ import annotations

import random
import unittest

from prediction_engine.config import BlendWeights, ProbabilityClamp, default_config, load_config
from prediction_engine.ensemble import blend_probability, blend_tier_a_markets, bounded_distribution
from prediction_engine.model_a import ModelAInputs, run_model_a
from prediction_engine.model_b import ModelBConfig, ModelBInputs, run_model_b
from prediction_engine.pipeline import _published_result_markets


SAFE_RANGE = ProbabilityClamp(minimum=0.01, maximum=0.99)


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

    def test_extreme_mismatch_without_odds_is_a_distribution_inside_the_clamp(self) -> None:
        # The form delta takes the away outcome below zero; clamping it back up
        # after renormalising used to publish a total above one.
        model_a = run_model_a(ModelAInputs(home_lambda=4.0, away_lambda=0.15))
        deltas = run_model_b(
            ModelBInputs(
                home_recent_ppg=3.0,
                away_recent_ppg=0.0,
                home_btts_rate=0.2,
                away_btts_rate=0.2,
                model_a_btts_yes=0.1,
                home_over_2_5_rate=0.8,
                away_over_2_5_rate=0.8,
                model_a_over_2_5=0.8,
            )
        ).market_deltas

        result = blend_tier_a_markets(model_a.markets, deltas, default_config())["match_result"]

        self.assertAlmostEqual(sum(result.values()), 1.0, delta=1e-9)
        self.assertTrue(all(0.01 <= value <= 0.99 for value in result.values()))
        self.assertEqual(result["away"], 0.01)
        self.assertGreater(result["home"], result["draw"])

    def test_bounded_distribution_only_scales_what_is_already_inside_the_clamp(self) -> None:
        probabilities = {"home": 0.5045, "draw": 0.25, "away": 0.25}
        total = sum(probabilities.values())

        self.assertEqual(
            bounded_distribution(probabilities, SAFE_RANGE),
            {outcome: value / total for outcome, value in probabilities.items()},
        )

    def test_bounded_distribution_shares_what_remains_in_proportion(self) -> None:
        result = bounded_distribution({"home": 0.983, "draw": 0.044, "away": -0.027}, SAFE_RANGE)

        self.assertDictApprox(result, {"home": 0.99 * 0.983 / 1.027, "draw": 0.99 * 0.044 / 1.027, "away": 0.01})
        self.assertEqual(list(result), ["home", "draw", "away"])

    def test_bounded_distribution_pins_every_small_outcome(self) -> None:
        # Pinning the 0.995 to the maximum as well would leave a total of 1.01.
        self.assertDictApprox(
            bounded_distribution({"home": 0.995, "draw": 0.004, "away": 0.001}, SAFE_RANGE),
            {"home": 0.98, "draw": 0.01, "away": 0.01},
        )

    def test_bounded_distribution_pins_the_top_when_that_is_the_side_overshot(self) -> None:
        self.assertDictApprox(
            bounded_distribution(
                {"home": 0.99, "draw": 0.005, "away": 0.005},
                ProbabilityClamp(minimum=0.01, maximum=0.4),
            ),
            {"home": 0.4, "draw": 0.3, "away": 0.3},
        )

    def test_bounded_distribution_holds_for_any_input(self) -> None:
        generator = random.Random(20261104)
        checked = 0
        for _ in range(2000):
            clamp = ProbabilityClamp(minimum=generator.uniform(0.001, 0.3), maximum=generator.uniform(0.4, 0.999))
            probabilities = {
                outcome: generator.choice((generator.uniform(-0.1, 1.0), generator.uniform(0.0, 0.01), 0.0))
                for outcome in ("home", "draw", "away")
            }
            if max(probabilities.values()) <= 0:
                continue
            checked += 1
            result = bounded_distribution(probabilities, clamp)
            self.assertAlmostEqual(sum(result.values()), 1.0, delta=1e-9, msg=str(probabilities))
            for value in result.values():
                self.assertGreaterEqual(value, clamp.minimum - 1e-12)
                self.assertLessEqual(value, clamp.maximum + 1e-12)
        self.assertGreater(checked, 1000)

    def test_bounded_distribution_refuses_what_no_distribution_can_satisfy(self) -> None:
        with self.assertRaises(ValueError):
            bounded_distribution({"home": 0.5, "away": 0.5}, ProbabilityClamp(minimum=0.6, maximum=0.9))
        with self.assertRaises(ValueError):
            bounded_distribution({"home": -0.1, "draw": 0.0, "away": 0.0}, SAFE_RANGE)

    def test_market_anchor_keeps_the_result_and_its_dependents_consistent(self) -> None:
        # A one-sided bookmaker line takes the small outcomes under the minimum.
        markets = {
            "match_result": {"home": 0.98, "draw": 0.01, "away": 0.01},
            # Deliberately wrong, so these can only pass if they are rederived.
            "double_chance": {"home_or_draw": 0.5, "draw_or_away": 0.5, "home_or_away": 0.5},
            "draw_no_bet": {"home": 0.5, "away": 0.5},
        }

        anchored = _published_result_markets(
            markets,
            {"home": 0.998, "draw": 0.0015, "away": 0.0005},
            SAFE_RANGE,
        )

        result = anchored["match_result"]
        self.assertAlmostEqual(sum(result.values()), 1.0, delta=1e-9)
        self.assertTrue(all(0.01 <= value <= 0.99 for value in result.values()))
        self.assertFloatApprox(anchored["double_chance"]["home_or_draw"], result["home"] + result["draw"])
        self.assertFloatApprox(anchored["double_chance"]["draw_or_away"], result["draw"] + result["away"])
        self.assertAlmostEqual(sum(anchored["draw_no_bet"].values()), 1.0, delta=1e-9)

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
