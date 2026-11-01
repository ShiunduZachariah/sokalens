from __future__ import annotations

import unittest

from prediction_engine.config import (
    ConfigValidationError,
    V2_ENSEMBLE_MARKET_FAMILIES,
    V2_MARKET_FAMILIES,
    default_config,
    load_config,
)


class V2ConfigTests(unittest.TestCase):
    def test_documented_defaults_validate_without_changing_v1_blend(self) -> None:
        config = default_config()

        config.validate()
        self.assertEqual(config.model_weights["1x2"].statistical_model, 0.55)
        self.assertEqual(config.form.decay_rate, 0.85)
        self.assertEqual(config.v2.market_tier_strictness.tier_b_completeness_addition, 0.10)
        self.assertEqual(config.v2.market_tier_strictness.tier_c_completeness_addition, 0.15)
        self.assertEqual(config.v2.season_recent_weights.season_long, 0.70)
        self.assertEqual(config.v2.season_recent_weights.recent_form, 0.30)
        self.assertEqual(config.v2.ranking_weights.probability, 0.30)
        self.assertEqual(config.v2.ranking_weights.confidence, 0.25)
        self.assertEqual(config.v2.ranking_weights.data_completeness, 0.20)
        self.assertEqual(config.v2.ranking_weights.market_accuracy, 0.10)
        self.assertEqual(config.v2.ranking_weights.league_accuracy, 0.10)
        self.assertEqual(config.v2.ranking_weights.edge_vs_odds, 0.05)
        self.assertEqual(config.v2.feature_decay_rates.match_result, 0.90)
        self.assertEqual(config.v2.feature_decay_rates.expected_goals, 0.88)
        self.assertEqual(config.v2.feature_decay_rates.discipline, 0.80)
        self.assertEqual(config.v2.feature_decay_rates.corners, 0.85)
        self.assertEqual(config.v2.feature_decay_rates.fouls, 0.80)
        self.assertEqual(config.v2.feature_decay_rates.lineup_availability, 0.25)
        self.assertEqual(config.v2.ood_thresholds.confidence_cap, 0.60)
        self.assertEqual(config.v2.ood_thresholds.force_basic, 0.80)
        self.assertEqual(config.v2.drift_windows.accuracy_predictions, 200)
        self.assertEqual(config.v2.drift_windows.calibration_days, 30)
        self.assertEqual(config.v2.drift_windows.accuracy_alert_drop, 0.05)
        self.assertEqual(config.v2.drift_windows.accuracy_retraining_drop, 0.10)
        self.assertEqual(config.v2.drift_windows.calibration_alert_error, 0.05)
        self.assertEqual(config.v2.drift_windows.calibration_retraining_error, 0.10)
        self.assertEqual(config.v2.retraining.minimum_new_settled_samples, 500)
        self.assertEqual(config.v2.promotion.minimum_shadow_settled_samples, 500)
        self.assertEqual(config.v2.promotion.significance_confidence_level, 0.95)
        self.assertEqual(config.v2.lifecycle_stability.tolerance, 0.20)
        self.assertEqual(config.v2.lifecycle_stability.direction_change_penalty, 0.50)
        self.assertEqual(config.v2.lifecycle_stability.neutral_score, 0.50)

        self.assertEqual(set(config.v2.ensemble_weights), set(V2_ENSEMBLE_MARKET_FAMILIES))
        self.assertEqual(set(config.v2.recommendation_tier_by_market), set(V2_MARKET_FAMILIES))
        self.assertTrue(
            all(
                weights.model_a == 1.0 and weights.model_b == 0.0 and weights.model_c == 0.0
                for weights in config.v2.ensemble_weights.values()
            )
        )
        self.assertEqual(config.v2.calibration.primary_method_by_market["possession"], "beta")
        self.assertEqual(config.v2.calibration.primary_method_by_market["pass_accuracy"], "beta")
        self.assertEqual(config.v2.calibration.primary_method_by_market["1x2"], "isotonic")
        self.assertEqual(config.v2.calibration.low_sample_fallback_method, "platt")
        self.assertTrue(
            all(sample == 500 for sample in config.v2.calibration.minimum_sample_by_market.values())
        )

    def test_valid_v2_overrides_are_merged_and_validated(self) -> None:
        config = load_config(
            {
                "v2": {
                    "ensemble_weights": {
                        "1x2": {"model_a": 0.50, "model_b": 0.20, "model_c": 0.30}
                    },
                    "feature_decay_rates": {"corners": 0.82},
                    "recommendation_tier_by_market": {"cards": "strong"},
                    "calibration": {
                        "primary_method_by_market": {"1x2": "platt"},
                        "minimum_sample_by_market": {"1x2": 750},
                    },
                    "lifecycle_stability": {"neutral_score": 0.65},
                }
            }
        )

        self.assertEqual(config.v2.ensemble_weights["1x2"].model_c, 0.30)
        self.assertEqual(config.v2.ensemble_weights["btts"].model_a, 1.0)
        self.assertEqual(config.v2.feature_decay_rates.corners, 0.82)
        self.assertEqual(config.v2.recommendation_tier_by_market["cards"], "strong")
        self.assertEqual(config.v2.calibration.primary_method_by_market["1x2"], "platt")
        self.assertEqual(config.v2.calibration.minimum_sample_by_market["1x2"], 750)
        self.assertEqual(config.v2.lifecycle_stability.neutral_score, 0.65)

    def test_invalid_v2_threshold_decay_sample_and_weight_values_fail_fast(self) -> None:
        cases = [
            (
                {"market_tier_strictness": {"tier_b_completeness_addition": 0.20, "tier_c_completeness_addition": 0.10}},
                "Tier C completeness addition",
            ),
            (
                {"ensemble_weights": {"1x2": {"model_a": 0.60, "model_b": 0.30, "model_c": 0.20}}},
                "must sum to 1.0",
            ),
            (
                {"ensemble_weights": {"1x2": {"model_a": -0.10, "model_b": 0.50, "model_c": 0.60}}},
                "finite non-negative weights",
            ),
            (
                {"season_recent_weights": {"season_long": 0.80, "recent_form": 0.30}},
                "must sum to 1.0",
            ),
            (
                {"ranking_weights": {"probability": 0.40}},
                "v2 ranking weights must sum to 1.0",
            ),
            (
                {"ranking_weights": {"probability": -0.10, "confidence": 0.65}},
                "v2 ranking weights must contain only finite non-negative weights",
            ),
            ({"feature_decay_rates": {"match_result": 1.0}}, "strictly between 0 and 1"),
            (
                {"recommendation_tier_by_market": {"cards": "guaranteed"}},
                "must be low, medium, or strong",
            ),
            (
                {"calibration": {"primary_method_by_market": {"1x2": "spline"}}},
                "must be isotonic, platt, or beta",
            ),
            (
                {"calibration": {"minimum_sample_by_market": {"1x2": 0}}},
                "must be a positive integer",
            ),
            (
                {"ood_thresholds": {"confidence_cap": 0.80, "force_basic": 0.80}},
                "OOD thresholds must satisfy",
            ),
            ({"drift_windows": {"accuracy_predictions": 0}}, "accuracy drift window"),
            (
                {"drift_windows": {"accuracy_alert_drop": 0.10, "accuracy_retraining_drop": 0.05}},
                "accuracy drift thresholds",
            ),
            ({"retraining": {"minimum_new_settled_samples": False}}, "retraining minimum"),
            ({"promotion": {"minimum_shadow_settled_samples": -1}}, "promotion minimum"),
            ({"promotion": {"significance_confidence_level": 1.0}}, "significance confidence level"),
            ({"lifecycle_stability": {"tolerance": 0.0}}, "lifecycle stability tolerance"),
            ({"lifecycle_stability": {"direction_change_penalty": 1.1}}, "direction-change penalty"),
            (
                {"ensemble_weights": {"unknown_market": {"model_a": 1.0, "model_b": 0.0, "model_c": 0.0}}},
                "must cover exactly the v2 market families",
            ),
        ]

        for v2_overrides, expected_message in cases:
            with self.subTest(expected_message=expected_message):
                with self.assertRaisesRegex(ConfigValidationError, expected_message):
                    load_config({"v2": v2_overrides})

    def test_malformed_v2_override_shape_has_a_config_error(self) -> None:
        with self.assertRaisesRegex(ConfigValidationError, "prediction engine v2 config must be an object"):
            load_config({"v2": []})

        with self.assertRaisesRegex(ConfigValidationError, "feature decay rate"):
            load_config({"v2": {"feature_decay_rates": {"match_result": "fast"}}})


if __name__ == "__main__":
    unittest.main()
