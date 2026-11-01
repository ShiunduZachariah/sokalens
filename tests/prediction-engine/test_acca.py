from __future__ import annotations

from datetime import UTC, datetime, timedelta
import unittest

from prediction_engine.acca import AccaRequest, select_acca
from prediction_engine.persistence import ConfidenceTier, EnginePrediction, GateTier, PredictionLifecycleStage


class AccaTests(unittest.TestCase):
    def test_filters_eligible_legs_and_respects_per_fixture_cap(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        rows = [
            leg(1, "1x2", "home", 0.70, ranking_score=0.9, kickoff_at=now + timedelta(hours=2)),
            leg(1, "btts", "yes", 0.62, ranking_score=0.8, kickoff_at=now + timedelta(hours=2)),
            leg(1, "total_goals", "over_2.5", 0.60, ranking_score=0.7, kickoff_at=now + timedelta(hours=2)),
            leg(2, "1x2", "away", 0.58, ranking_score=0.6, kickoff_at=now + timedelta(hours=3)),
            leg(3, "1x2", "home", 0.57, ranking_score=0.5, kickoff_at=now - timedelta(hours=1)),
        ]

        selection = select_acca(rows, AccaRequest(start_datetime=now, end_datetime=now + timedelta(days=1), legs_requested=3))

        self.assertEqual(selection.leg_count, 2)
        self.assertEqual([row.market for row in selection.legs if row.fixture_id == 1], ["1x2"])
        self.assertTrue(any(row.fixture_id == 2 for row in selection.legs))
        self.assertFalse(any(row.fixture_id == 3 for row in selection.legs))

    def test_unsupported_same_fixture_pair_is_denied_and_cross_fixture_leg_is_used(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        rows = [
            leg(1, "1x2", "home", 0.70, ranking_score=0.9, kickoff_at=now + timedelta(hours=2)),
            leg(1, "corners", "over_9.5", 0.65, ranking_score=0.8, kickoff_at=now + timedelta(hours=2)),
            leg(2, "btts", "yes", 0.60, ranking_score=0.7, kickoff_at=now + timedelta(hours=3)),
        ]

        selection = select_acca(rows, AccaRequest(start_datetime=now, end_datetime=now + timedelta(days=1), legs_requested=2))

        self.assertEqual([(row.fixture_id, row.market) for row in selection.legs], [(1, "1x2"), (2, "btts")])
        self.assertAlmostEqual(selection.combined_fair_probability, 0.42)

    def test_goals_derived_pair_is_denied_until_typed_joint_price_contract_exists(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        rows = [
            leg(1, "1x2", "home", 0.60, ranking_score=0.9, kickoff_at=now + timedelta(hours=2)),
            leg(1, "total_goals", "under_2.5", 0.50, ranking_score=0.8, kickoff_at=now + timedelta(hours=2)),
        ]

        selection = select_acca(rows, AccaRequest(start_datetime=now, end_datetime=now + timedelta(days=1), legs_requested=2))

        self.assertEqual(selection.leg_count, 1)
        self.assertAlmostEqual(selection.combined_fair_probability, 0.60)

    def test_precomputed_goals_combination_market_remains_eligible_as_one_leg(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        rows = [
            leg(1, "btts_total_goals", "yes_over_2.5", 0.64, ranking_score=0.9, kickoff_at=now + timedelta(hours=2)),
            leg(1, "1x2", "home", 0.60, ranking_score=0.8, kickoff_at=now + timedelta(hours=2)),
        ]

        selection = select_acca(rows, AccaRequest(start_datetime=now, end_datetime=now + timedelta(days=1), legs_requested=2))

        self.assertEqual(selection.leg_count, 1)
        self.assertEqual(selection.legs[0].market, "btts_total_goals")
        self.assertAlmostEqual(selection.combined_fair_probability, 0.64)

    def test_combined_fair_odds_are_inverse_of_combined_probability(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        rows = [
            leg(1, "1x2", "home", 0.50, ranking_score=0.9, kickoff_at=now + timedelta(hours=2)),
            leg(2, "btts", "yes", 0.40, ranking_score=0.8, kickoff_at=now + timedelta(hours=3)),
        ]

        selection = select_acca(rows, AccaRequest(start_datetime=now, end_datetime=now + timedelta(days=1), legs_requested=2))

        self.assertAlmostEqual(selection.combined_fair_probability, 0.20)
        self.assertAlmostEqual(selection.combined_fair_odds, 5.0)
        self.assertTrue(selection.target_met)

    def test_target_odds_shortfall_is_reported_without_degrading_to_lower_confidence_legs(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        rows = [
            leg(1, "1x2", "home", 0.70, ranking_score=0.9, kickoff_at=now + timedelta(hours=2)),
            leg(2, "btts", "yes", 0.60, ranking_score=0.8, kickoff_at=now + timedelta(hours=3)),
            leg(3, "1x2", "away", 0.50, ranking_score=0.1, kickoff_at=now + timedelta(hours=4), confidence_tier=ConfidenceTier.NONE),
        ]

        selection = select_acca(
            rows,
            AccaRequest(start_datetime=now, end_datetime=now + timedelta(days=1), legs_requested=2, target_odds=6.0),
        )

        self.assertEqual(selection.leg_count, 2)
        self.assertFalse(selection.target_met)
        self.assertIn("target could not be met", selection.explanation)
        self.assertFalse(any(row.fixture_id == 3 for row in selection.legs))


def leg(
    fixture_id: int,
    market: str,
    prediction: str,
    probability: float,
    *,
    ranking_score: float,
    kickoff_at: datetime,
    confidence_tier: ConfidenceTier = ConfidenceTier.MEDIUM,
) -> EnginePrediction:
    return EnginePrediction(
        fixture_id=fixture_id,
        market=market,
        prediction=prediction,
        probability=probability,
        confidence=0.7,
        confidence_tier=confidence_tier,
        is_best_bet=True,
        lifecycle_stage=PredictionLifecycleStage.PRE_MATCH,
        data_completeness=0.8,
        features_used={"kickoff_at": kickoff_at.isoformat(), "league_id": 39, "ranking_score": ranking_score},
        explanation=f"{market} {prediction}",
        generated_at=datetime(2026, 7, 1, 10, 0, tzinfo=UTC),
        gate_tier=GateTier.FULL,
        horizon_eligible=True,
        embargo_active=False,
        model_version="python-engine-v1",
    )


if __name__ == "__main__":
    unittest.main()
