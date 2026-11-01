from __future__ import annotations

from datetime import UTC, datetime, timedelta
import unittest

from prediction_engine.horizon import FixtureScheduleItem, horizon_embargo_check


class HorizonEmbargoTests(unittest.TestCase):
    def test_fixture_outside_seven_day_window_is_not_horizon_eligible(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        fixture = FixtureScheduleItem(1, now + timedelta(days=8), 10, 20, "scheduled")

        decision = horizon_embargo_check(fixture, now=now, team_schedule=[fixture])

        self.assertFalse(decision.horizon_eligible)
        self.assertFalse(decision.embargo_active)
        self.assertEqual(decision.embargo_reason, "outside_prediction_window")

    def test_fixture_inside_window_without_interleaving_match_is_eligible(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        fixture = FixtureScheduleItem(1, now + timedelta(days=3), 10, 20, "scheduled")

        decision = horizon_embargo_check(fixture, now=now, team_schedule=[fixture])

        self.assertTrue(decision.horizon_eligible)
        self.assertFalse(decision.embargo_active)
        self.assertEqual(decision.embargo_blocking_fixture_id, None)

    def test_unfinished_interleaving_fixture_embargoes_later_fixture(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureScheduleItem(10, now + timedelta(days=5), 1, 2, "scheduled")
        midweek = FixtureScheduleItem(5, now + timedelta(days=2), 1, 9, "scheduled")

        decision = horizon_embargo_check(target, now=now, team_schedule=[target, midweek])

        self.assertTrue(decision.horizon_eligible)
        self.assertTrue(decision.embargo_active)
        self.assertEqual(decision.embargo_blocking_fixture_id, 5)
        self.assertIn("Team 1", decision.embargo_reason or "")

    def test_finished_interleaving_fixture_does_not_embargo_later_fixture(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureScheduleItem(10, now + timedelta(days=5), 1, 2, "scheduled")
        midweek = FixtureScheduleItem(5, now + timedelta(days=2), 1, 9, "FT")

        decision = horizon_embargo_check(target, now=now, team_schedule=[target, midweek])

        self.assertTrue(decision.horizon_eligible)
        self.assertFalse(decision.embargo_active)


if __name__ == "__main__":
    unittest.main()
