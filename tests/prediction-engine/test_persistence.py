from __future__ import annotations

from dataclasses import replace
from datetime import UTC, datetime, timedelta
import json
import unittest

from prediction_engine.persistence import (
    ConfidenceTier,
    EnginePrediction,
    GateTier,
    PredictionLifecycleStage,
    qualifies_for_best_bet_pool,
    supersede_current_predictions,
)


class PersistenceTests(unittest.TestCase):
    def test_prediction_row_validation_and_json_snapshot_serialization(self) -> None:
        prediction = valid_prediction()

        row = prediction.to_db_row()

        self.assertEqual(row["fixture_id"], 42)
        self.assertEqual(row["confidence_tier"], "medium")
        self.assertEqual(row["lifecycle_stage"], "PRE_MATCH")
        self.assertEqual(row["gate_tier"], "FULL")
        self.assertEqual(json.loads(row["features_used"])["model_a_score_matrix"], [[0.6, 0.4]])

    def test_validation_rejects_invalid_probability_and_non_json_features(self) -> None:
        invalid_probability = replace(valid_prediction(), probability=1.5)
        with self.assertRaisesRegex(ValueError, "probability must be between"):
            invalid_probability.validate()

        invalid_snapshot = replace(valid_prediction(), features_used={"bad": object()})
        with self.assertRaisesRegex(ValueError, "features_used must be JSON serializable"):
            invalid_snapshot.validate()

        incomplete_stability = replace(valid_prediction(), previous_probability=0.6)
        with self.assertRaisesRegex(ValueError, "previous_probability and probability_delta"):
            incomplete_stability.validate()

        inconsistent_shift = replace(
            valid_prediction(),
            previous_probability=0.6,
            probability_delta=0.2,
            max_lifecycle_probability_shift=0.1,
        )
        with self.assertRaisesRegex(ValueError, "must cover probability_delta"):
            inconsistent_shift.validate()

        partial_metadata = replace(valid_prediction(), lifecycle_stability_score=0.5)
        with self.assertRaisesRegex(ValueError, "metadata must be entirely set"):
            partial_metadata.validate()

        invalid_first = replace(
            valid_prediction(),
            max_lifecycle_probability_shift=0.1,
            prediction_direction_changed=False,
            lifecycle_stability_score=0.5,
        )
        with self.assertRaisesRegex(ValueError, "first lifecycle version"):
            invalid_first.validate()

    def test_supersede_current_predictions_inserts_new_versions_without_overwriting(self) -> None:
        original = valid_prediction(generated_at=datetime(2026, 7, 1, 9, 0, tzinfo=UTC))
        replacement = valid_prediction(
            prediction="away",
            generated_at=datetime(2026, 7, 1, 14, 0, tzinfo=UTC),
            lifecycle_stage=PredictionLifecycleStage.LINEUP_CONFIRMED,
        )

        versions = supersede_current_predictions([original], [replacement])

        self.assertEqual(len(versions), 2)
        self.assertEqual(versions[0].superseded_at, replacement.generated_at)
        self.assertIsNone(versions[1].superseded_at)
        self.assertEqual(versions[1].prediction, "away")
        self.assertIsNotNone(versions[1].id)

    def test_best_bet_pool_filter_matches_architecture_conditions(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        kickoff = now + timedelta(hours=3)

        self.assertTrue(qualifies_for_best_bet_pool(valid_prediction(), kickoff_at=kickoff, now=now))

        cases = [
            replace(valid_prediction(), superseded_at=now),
            replace(valid_prediction(), gate_tier=GateTier.BASIC),
            replace(valid_prediction(), horizon_eligible=False),
            replace(valid_prediction(), embargo_active=True),
            replace(valid_prediction(), probability=0.54),
            replace(valid_prediction(), confidence_tier=ConfidenceTier.LOW),
            replace(valid_prediction(), market="correct_score"),
        ]
        for case in cases:
            with self.subTest(case=case):
                self.assertFalse(qualifies_for_best_bet_pool(case, kickoff_at=kickoff, now=now))

        self.assertFalse(qualifies_for_best_bet_pool(valid_prediction(), kickoff_at=now, now=now))


def valid_prediction(**overrides: object) -> EnginePrediction:
    values = {
        "fixture_id": 42,
        "market": "1x2",
        "prediction": "home",
        "probability": 0.62,
        "confidence": 0.68,
        "confidence_tier": ConfidenceTier.MEDIUM,
        "is_best_bet": True,
        "lifecycle_stage": PredictionLifecycleStage.PRE_MATCH,
        "data_completeness": 0.81,
        "features_used": {"model_a_score_matrix": [[0.6, 0.4]]},
        "explanation": "The data suggests the home side is favored.",
        "generated_at": datetime(2026, 7, 1, 12, 0, tzinfo=UTC),
        "gate_tier": GateTier.FULL,
        "horizon_eligible": True,
        "embargo_active": False,
        "model_version": "python-engine-v1",
    }
    values.update(overrides)
    return EnginePrediction(**values)


if __name__ == "__main__":
    unittest.main()
