from __future__ import annotations

import unittest
from datetime import UTC, datetime, timedelta

from prediction_engine.lifecycle import (
    LifecycleConfig,
    LifecycleStage,
    PredictionVersion,
    PredictionWorkItem,
    assign_lifecycle_stage,
    calculate_lifecycle_stability,
    process_fixture_batch,
    supersede_previous_versions,
)


class LifecycleTests(unittest.TestCase):
    def test_lifecycle_stability_is_neutral_without_a_predecessor(self) -> None:
        stability = calculate_lifecycle_stability(
            probability=0.64,
            prediction="home",
            previous_probability=None,
            previous_prediction=None,
        )

        self.assertIsNone(stability.previous_probability)
        self.assertIsNone(stability.probability_delta)
        self.assertEqual(stability.max_lifecycle_probability_shift, 0.0)
        self.assertFalse(stability.prediction_direction_changed)
        self.assertEqual(stability.lifecycle_stability_score, 0.5)

    def test_lifecycle_stability_tracks_signed_delta_running_max_and_direction(self) -> None:
        stable = calculate_lifecycle_stability(
            probability=0.63,
            prediction="home",
            previous_probability=0.64,
            previous_prediction="home",
            previous_max_shift=0.03,
            tolerance=0.20,
        )
        changed = calculate_lifecycle_stability(
            probability=0.49,
            prediction="draw",
            previous_probability=0.63,
            previous_prediction="home",
            previous_max_shift=stable.max_lifecycle_probability_shift,
            tolerance=0.20,
            direction_change_penalty=0.50,
        )

        self.assertAlmostEqual(stable.probability_delta or 0, -0.01)
        self.assertAlmostEqual(stable.max_lifecycle_probability_shift, 0.03)
        self.assertAlmostEqual(stable.lifecycle_stability_score, 0.85)
        self.assertAlmostEqual(changed.probability_delta or 0, -0.14)
        self.assertAlmostEqual(changed.max_lifecycle_probability_shift, 0.14)
        self.assertTrue(changed.prediction_direction_changed)
        self.assertAlmostEqual(changed.lifecycle_stability_score, 0.15)

    def test_assign_lifecycle_stage_from_time_and_lineup_state(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        config = LifecycleConfig()

        self.assertEqual(assign_lifecycle_stage(now, now + timedelta(days=2), False, config), LifecycleStage.EARLY)
        self.assertEqual(assign_lifecycle_stage(now, now + timedelta(hours=5), False, config), LifecycleStage.PRE_MATCH)
        self.assertEqual(
            assign_lifecycle_stage(now, now + timedelta(minutes=75), True, config),
            LifecycleStage.LINEUP_CONFIRMED,
        )
        self.assertEqual(assign_lifecycle_stage(now, now - timedelta(seconds=1), True, config), LifecycleStage.LOCKED)

    def test_prediction_versioning_inserts_new_rows_and_supersedes_current_rows(self) -> None:
        original = PredictionVersion(
            fixture_id=42,
            market="1x2",
            lifecycle_stage=LifecycleStage.EARLY,
            generated_at=datetime(2026, 7, 1, 8, 0, tzinfo=UTC),
        )
        replacement = PredictionVersion(
            fixture_id=42,
            market="1x2",
            lifecycle_stage=LifecycleStage.PRE_MATCH,
            generated_at=datetime(2026, 7, 1, 14, 0, tzinfo=UTC),
        )

        versions = supersede_previous_versions([original], [replacement])

        self.assertEqual(versions[0].superseded_at, replacement.generated_at)
        self.assertIsNone(versions[1].superseded_at)
        self.assertIsNot(versions[1], original)

    def test_priority_order_processes_lineup_confirmed_and_nearest_kickoff_first(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        items = [
            PredictionWorkItem(fixture_id=1, kickoff_at=now + timedelta(days=2), stage=LifecycleStage.EARLY),
            PredictionWorkItem(fixture_id=2, kickoff_at=now + timedelta(hours=5), stage=LifecycleStage.PRE_MATCH),
            PredictionWorkItem(fixture_id=3, kickoff_at=now + timedelta(hours=4), stage=LifecycleStage.PRE_MATCH),
            PredictionWorkItem(fixture_id=4, kickoff_at=now + timedelta(minutes=70), stage=LifecycleStage.LINEUP_CONFIRMED),
        ]

        processed = process_fixture_batch(items, max_workers=1, processor=lambda item: item.fixture_id)

        self.assertEqual([result.value for result in processed], [4, 3, 2, 1])
        self.assertTrue(all(result.error is None for result in processed))

    def test_worker_pool_failure_isolation_keeps_valid_fixture_results(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        items = [
            PredictionWorkItem(fixture_id=1, kickoff_at=now + timedelta(hours=1), stage=LifecycleStage.PRE_MATCH),
            PredictionWorkItem(fixture_id=2, kickoff_at=now + timedelta(hours=2), stage=LifecycleStage.PRE_MATCH),
            PredictionWorkItem(fixture_id=3, kickoff_at=now + timedelta(hours=3), stage=LifecycleStage.PRE_MATCH),
        ]

        def processor(item: PredictionWorkItem) -> int:
            if item.fixture_id == 2:
                raise ValueError("bad fixture")
            return item.fixture_id * 10

        processed = process_fixture_batch(items, max_workers=2, processor=processor)

        by_fixture = {result.fixture_id: result for result in processed}
        self.assertEqual(by_fixture[1].value, 10)
        self.assertIsNone(by_fixture[2].value)
        self.assertEqual(by_fixture[2].error, "bad fixture")
        self.assertEqual(by_fixture[3].value, 30)


if __name__ == "__main__":
    unittest.main()
