from __future__ import annotations

from dataclasses import replace
from datetime import UTC, date, datetime, timedelta
import unittest

from prediction_engine.persistence import ConfidenceTier, EnginePrediction, GateTier, PredictionLifecycleStage
from prediction_engine.ranking import (
    ExplanationInput,
    PredictionSlice,
    RankingInput,
    assign_best_bet_flags,
    best_bet_pool,
    explain_prediction,
    rank_score,
    slice_predictions,
)


class RankingExplanationTests(unittest.TestCase):
    def test_composite_ranking_score_matches_hand_calculated_weighted_sum(self) -> None:
        score = rank_score(
            RankingInput(
                probability=0.70,
                confidence=0.80,
                data_completeness=0.90,
                market_accuracy=0.60,
                league_accuracy=0.50,
                edge_vs_odds=0.10,
            )
        )

        self.assertAlmostEqual(score, 0.70 * 0.30 + 0.80 * 0.25 + 0.90 * 0.20 + 0.60 * 0.10 + 0.50 * 0.10 + 0.10 * 0.05)

    def test_best_bet_pool_filters_only_architecture_conditions_and_has_no_cap(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        rows = [
            prediction(1, "1x2", "home", generated_at=now, kickoff_at=now + timedelta(hours=2)),
            prediction(1, "btts", "yes", generated_at=now, kickoff_at=now + timedelta(hours=2)),
            prediction(2, "total_goals", "over_2.5", generated_at=now, kickoff_at=now + timedelta(hours=3)),
            prediction(3, "1x2", "away", generated_at=now, kickoff_at=now + timedelta(hours=4), gate_tier=GateTier.BASIC),
            prediction(4, "1x2", "home", generated_at=now, kickoff_at=now + timedelta(hours=5), embargo_active=True),
            prediction(5, "1x2", "home", generated_at=now, kickoff_at=now + timedelta(hours=6), superseded_at=now + timedelta(minutes=1)),
            *[
                prediction(100 + index, "1x2", "home", generated_at=now, kickoff_at=now + timedelta(hours=8))
                for index in range(80)
            ],
        ]

        pool = best_bet_pool(rows, now=now)

        self.assertEqual(len(pool), 83)
        self.assertTrue(all(row.is_best_bet for row in pool))
        self.assertEqual(sum(1 for row in pool if row.fixture_id >= 100), 80)

    def test_assign_best_bet_flags_stores_pool_membership_without_changing_other_fields(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        qualifying = replace(
            prediction(1, "1x2", "home", generated_at=now, kickoff_at=now + timedelta(hours=2)),
            is_best_bet=False,
        )
        low_confidence = replace(
            prediction(
                2,
                "1x2",
                "home",
                generated_at=now,
                kickoff_at=now + timedelta(hours=2),
                confidence_tier=ConfidenceTier.LOW,
            ),
            is_best_bet=True,
        )
        excluded_market = replace(
            prediction(3, "correct_score", "1-0", generated_at=now, kickoff_at=now + timedelta(hours=2)),
            is_best_bet=True,
        )

        rows = assign_best_bet_flags([qualifying, low_confidence, excluded_market], now=now)

        self.assertEqual([row.is_best_bet for row in rows], [True, False, False])
        self.assertEqual(rows[0].market, qualifying.market)
        self.assertFalse(qualifying.is_best_bet)

    def test_slice_predictions_filters_by_day_league_market_and_confidence_tier(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        rows = [
            prediction(1, "1x2", "home", kickoff_at=datetime(2026, 7, 2, 15, tzinfo=UTC), league_id=39),
            prediction(2, "btts", "yes", kickoff_at=datetime(2026, 7, 2, 17, tzinfo=UTC), league_id=39, confidence_tier=ConfidenceTier.STRONG),
            prediction(3, "1x2", "away", kickoff_at=datetime(2026, 7, 3, 15, tzinfo=UTC), league_id=140),
        ]

        sliced = slice_predictions(
            rows,
            PredictionSlice(
                start_date=date(2026, 7, 2),
                end_date=date(2026, 7, 3),
                league_id=39,
                market="btts",
                confidence_tier=ConfidenceTier.STRONG,
            ),
        )

        self.assertEqual([row.fixture_id for row in sliced], [2])

    def test_explanation_uses_feature_values_caveats_and_avoids_certainty_language(self) -> None:
        explanation = explain_prediction(
            ExplanationInput(
                market="total_goals",
                prediction="over_2.5",
                probability=0.64,
                drivers={
                    "home goals per match": 1.8,
                    "away goals conceded per match": 1.6,
                    "combined over 2.5 rate": 0.7,
                },
                fallback_flags=("xg_fallback_used",),
                max_confidence_tier=ConfidenceTier.MEDIUM,
                embargo_recently_lifted=True,
            )
        )

        self.assertIn("over_2.5", explanation)
        self.assertIn("1.8", explanation)
        self.assertIn("xg_fallback_used", explanation)
        self.assertIn("harder to call", explanation)
        self.assertIn("recently lifted embargo", explanation)
        banned = ("guaranteed", "certain", "sure thing", "can't lose")
        self.assertFalse(any(word in explanation.lower() for word in banned))


def prediction(
    fixture_id: int,
    market: str,
    selection: str,
    *,
    kickoff_at: datetime | None = None,
    generated_at: datetime | None = None,
    league_id: int = 39,
    confidence_tier: ConfidenceTier = ConfidenceTier.MEDIUM,
    gate_tier: GateTier = GateTier.FULL,
    embargo_active: bool = False,
    superseded_at: datetime | None = None,
) -> EnginePrediction:
    generated = generated_at or datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
    row = EnginePrediction(
        fixture_id=fixture_id,
        market=market,
        prediction=selection,
        probability=0.62,
        confidence=0.68 if confidence_tier is ConfidenceTier.MEDIUM else 0.80,
        confidence_tier=confidence_tier,
        is_best_bet=True,
        lifecycle_stage=PredictionLifecycleStage.PRE_MATCH,
        data_completeness=0.85,
        features_used={"ranking_score": 0.5},
        explanation="The data suggests this outcome is favored.",
        generated_at=generated,
        gate_tier=gate_tier,
        horizon_eligible=True,
        embargo_active=embargo_active,
        model_version="python-engine-v1",
        superseded_at=superseded_at,
    )
    row = replace(row, features_used={**row.features_used, "kickoff_at": (kickoff_at or generated + timedelta(hours=2)).isoformat(), "league_id": league_id})
    return row


if __name__ == "__main__":
    unittest.main()
