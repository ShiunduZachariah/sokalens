from __future__ import annotations

from dataclasses import replace
import unittest

from prediction_engine.config import (
    BlendWeights,
    ConfigValidationError,
    default_config,
    load_config,
)
from prediction_engine.gate import (
    GateOutcome,
    GateReason,
    GateSoftFlag,
    MarketTier,
    MatchQualityGate,
    MatchQualityInput,
    Phase3PrecheckInput,
    evaluate_phase3_horizon_embargo_precheck,
)


class ConfigGateTests(unittest.TestCase):
    def test_default_config_matches_documented_phase_1_values(self) -> None:
        config = default_config()

        self.assertEqual(config.confidence_thresholds.strong_minimum, 0.75)
        self.assertEqual(config.confidence_thresholds.medium_minimum, 0.65)
        self.assertEqual(config.confidence_thresholds.low_minimum, 0.55)
        self.assertEqual(config.match_gate.minimum_completeness_to_attempt, 0.35)
        self.assertEqual(config.match_gate.minimum_completeness_for_full, 0.55)
        self.assertEqual(config.match_gate.minimum_historical_matches_per_team, 3)
        self.assertEqual(config.match_gate.dual_injury_impact_skip_threshold, 0.50)
        self.assertEqual(config.form.decay_rate, 0.85)
        self.assertEqual(config.form.lookback_windows, (5, 10))
        self.assertEqual(config.model_weights["1x2"], BlendWeights(statistical_model=0.55, form_adjustment=0.45))
        self.assertEqual(config.probability_clamp.minimum, 0.01)
        self.assertEqual(config.probability_clamp.maximum, 0.99)
        self.assertEqual(config.correct_score.top_n, 8)

    def test_config_validation_rejects_invalid_overrides(self) -> None:
        cases = [
            (
                {
                    "confidence_thresholds": {
                        "strong_minimum": 0.65,
                        "medium_minimum": 0.65,
                        "low_minimum": 0.55,
                    }
                },
                "confidence thresholds must be strictly descending",
            ),
            (
                {"model_weights": {"1x2": {"statistical_model": 0.80, "form_adjustment": 0.30}}},
                'model blend ratio for "1x2" must sum to 1.0',
            ),
            (
                {"model_weights": {"1x2": {"statistical_model": -0.10, "form_adjustment": 1.10}}},
                'model blend ratio for "1x2" contains negative weights',
            ),
            ({"form": {"decay_rate": 1.0}}, "form decay rate must be strictly between 0 and 1"),
            (
                {"match_gate": {"minimum_completeness_to_attempt": 0.70, "minimum_completeness_for_full": 0.60}},
                "match gate full completeness must be greater than or equal to attempt completeness",
            ),
        ]

        for overrides, message in cases:
            with self.subTest(message=message):
                with self.assertRaisesRegex(ConfigValidationError, message):
                    load_config(overrides)

    def test_match_quality_gate_applies_ordered_skip_basic_full_checks(self) -> None:
        cases = [
            (
                lambda base: replace(
                    base,
                    fixture_status="Postponed",
                    home_recent_sample=0,
                    away_recent_sample=0,
                    completeness_score=0,
                    home_injury_impact_score=0.90,
                    away_injury_impact_score=0.90,
                ),
                GateOutcome.SKIP,
                GateReason.FIXTURE_STATUS,
            ),
            (
                lambda base: replace(
                    base,
                    home_recent_sample=2,
                    completeness_score=0,
                    home_injury_impact_score=0.90,
                    away_injury_impact_score=0.90,
                ),
                GateOutcome.SKIP,
                GateReason.INSUFFICIENT_SAMPLE,
            ),
            (
                lambda base: replace(
                    base,
                    completeness_score=0.34,
                    home_injury_impact_score=0.90,
                    away_injury_impact_score=0.90,
                ),
                GateOutcome.SKIP,
                GateReason.INSUFFICIENT_COMPLETENESS,
            ),
            (
                lambda base: replace(
                    base,
                    home_injury_impact_score=0.51,
                    away_injury_impact_score=0.51,
                ),
                GateOutcome.SKIP,
                GateReason.DUAL_INJURY_IMPACT,
            ),
            (
                lambda base: replace(base, completeness_score=0.54),
                GateOutcome.BASIC,
                GateReason.BASIC_DATA,
            ),
            (
                lambda base: replace(base, completeness_score=0.55),
                GateOutcome.FULL,
                GateReason.FULL_DATA,
            ),
        ]
        gate = MatchQualityGate(default_config())

        for vector, outcome, reason in cases:
            with self.subTest(reason=reason):
                decision = gate.evaluate(vector(gate_ready_input()))

                self.assertEqual(decision.outcome, outcome)
                self.assertEqual(decision.reason, reason)
                if outcome is GateOutcome.BASIC:
                    self.assertEqual(decision.allowed_market_tiers, (MarketTier.A,))
                    self.assertFalse(decision.best_bet_eligible)
                    self.assertFalse(decision.confidence_scoring_enabled)
                if outcome is GateOutcome.FULL:
                    self.assertEqual(decision.allowed_market_tiers, (MarketTier.A, MarketTier.B, MarketTier.C, MarketTier.D))
                    self.assertTrue(decision.best_bet_eligible)
                    self.assertTrue(decision.confidence_scoring_enabled)

    def test_soft_flags_are_recorded_without_changing_full_gate_decision(self) -> None:
        gate = MatchQualityGate(default_config())
        vector = replace(
            gate_ready_input(),
            home_fixture_congestion=0.80,
            early_season=True,
            referee_fallback_used=True,
            one_x_two_odds_present=False,
            group_completeness={
                "referee": 0,
                "market_cross_reference": 0,
            },
        )

        decision = gate.evaluate(vector)

        self.assertIs(decision.outcome, GateOutcome.FULL)
        self.assertEqual(
            decision.soft_flags,
            (
                GateSoftFlag.FIXTURE_CONGESTION,
                GateSoftFlag.EARLY_SEASON,
                GateSoftFlag.MISSING_REFEREE_DATA,
                GateSoftFlag.MISSING_ODDS_DATA,
            ),
        )

    def test_phase_3_horizon_embargo_precheck_stub_always_passes(self) -> None:
        self.assertTrue(evaluate_phase3_horizon_embargo_precheck(Phase3PrecheckInput(fixture_id=42)).passed)


def gate_ready_input() -> MatchQualityInput:
    return MatchQualityInput(
        fixture_status="scheduled",
        home_recent_sample=10,
        away_recent_sample=10,
        completeness_score=0.80,
        group_completeness={
            "referee": 1,
            "market_cross_reference": 1,
        },
        one_x_two_odds_present=True,
    )
