"""The in-play model time series (000079) and its client-facing projections.

Snapshots are a different record from `engine_predictions`: one row per fixture
per tick with its markets as JSONB, append-only, never settled. They are what a
client graphs. Keeping the two apart is what makes a 60-second timer affordable
alongside a dynamic market ladder.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
import json
import unittest

from prediction_engine.db_repository import InPlaySnapshotRepository
from prediction_engine.inplay_features import MatchPeriod
from prediction_engine.inplay_model import (
    BASIS_POINT_TOTAL,
    DEFAULT_IN_PLAY_CONFIG,
    DEFAULT_SNAPSHOT_INTERVAL,
    SNAPSHOT_TRIGGER_PERIOD_CHANGE,
    SNAPSHOT_TRIGGER_POST_GOAL,
    SNAPSHOT_TRIGGER_POST_RED_CARD,
    SNAPSHOT_TRIGGER_TIMER,
    SnapshotState,
    should_snapshot,
    MODEL_HEALTH_OK,
    MODEL_HEALTH_STALE,
    MODEL_HEALTH_SUSPENDED,
    SuppressionReason,
    basis_points,
    model_health_for,
    snapshot_markets,
)
from test_db_repository import FakeConnection, FakeCursor
from test_inplay_model import features

CAPTURED_AT = datetime(2026, 9, 30, 15, 30, tzinfo=UTC)


class ModelHealthTests(unittest.TestCase):
    def test_no_suppression_is_ok(self) -> None:
        self.assertEqual(model_health_for(SuppressionReason.NONE), MODEL_HEALTH_OK)

    def test_stale_data_is_stale_not_suspended(self) -> None:
        # A client showing a stale figure and a client showing no figure are
        # different behaviours. Collapsing them would let a suspended fixture
        # look current.
        self.assertEqual(model_health_for(SuppressionReason.STALE_DATA), MODEL_HEALTH_STALE)

    def test_every_other_suppression_suspends(self) -> None:
        for reason in (
            SuppressionReason.NOT_IN_PLAY,
            SuppressionReason.EXTRA_TIME_NOT_MODELLED,
            SuppressionReason.MISSING_SCORE,
            SuppressionReason.MISSING_PRE_MATCH_RATES,
        ):
            with self.subTest(reason=reason):
                self.assertEqual(model_health_for(reason), MODEL_HEALTH_SUSPENDED)

    def test_every_reason_is_mapped(self) -> None:
        # A new suppression reason must not silently fall through to a health a
        # client would read as "show the numbers".
        for reason in SuppressionReason:
            self.assertIn(
                model_health_for(reason),
                {MODEL_HEALTH_OK, MODEL_HEALTH_STALE, MODEL_HEALTH_SUSPENDED},
            )


class BasisPointTests(unittest.TestCase):
    """Must agree with services/api/internal/predictions/basispoints."""

    def test_selections_always_sum_to_the_total(self) -> None:
        for name, selections in (
            ("thirds", {"home": 1 / 3, "draw": 1 / 3, "away": 1 / 3}),
            ("typical 1x2", {"home": 0.5123, "draw": 0.2456, "away": 0.2421}),
            ("two way", {"yes": 0.6667, "no": 0.3333}),
            ("sevenths", {"a": 1 / 7, "b": 2 / 7, "c": 4 / 7}),
            ("near certain", {"home": 0.99991, "draw": 0.00005, "away": 0.00004}),
            ("unnormalised", {"home": 0.4, "draw": 0.2, "away": 0.2}),
        ):
            with self.subTest(name):
                got = basis_points(selections)
                self.assertEqual(sum(got.values()), BASIS_POINT_TOTAL, got)
                self.assertTrue(all(value >= 0 for value in got.values()), got)

    def test_the_apportionment_matches_the_go_implementation(self) -> None:
        # The Go test asserts exactly this for equal thirds. If the two diverge,
        # a snapshot and a live payload disagree about the same market.
        self.assertEqual(
            basis_points({"home": 1 / 3, "draw": 1 / 3, "away": 1 / 3}),
            {"away": 3334, "draw": 3333, "home": 3333},
        )
        self.assertEqual(
            basis_points({"home": 0.5123, "draw": 0.2456, "away": 0.2421}),
            {"away": 2421, "draw": 2456, "home": 5123},
        )

    def test_edge_cases(self) -> None:
        self.assertEqual(basis_points({}), {})
        self.assertEqual(basis_points({"yes": 0, "no": 0}), {"yes": 0, "no": 0})
        self.assertEqual(basis_points({"yes": -0.5, "no": 1.0}), {"no": BASIS_POINT_TOTAL, "yes": 0})
        self.assertEqual(basis_points({"only": 0.42}), {"only": BASIS_POINT_TOTAL})

    def test_it_is_deterministic_under_ties(self) -> None:
        selections = {"home": 1 / 3, "draw": 1 / 3, "away": 1 / 3}
        first = basis_points(selections)
        for _ in range(100):
            self.assertEqual(basis_points(selections), first)


class SnapshotMarketsTests(unittest.TestCase):
    def test_each_market_is_converted_independently(self) -> None:
        class Market:
            def __init__(self, selections: dict[str, float]) -> None:
                self.selections = selections

        got = snapshot_markets(
            {
                "match_result_in_play": Market({"home": 0.6412, "draw": 0.21, "away": 0.1488}),
                "btts_in_play": Market({"yes": 0.7, "no": 0.3}),
            }
        )
        self.assertEqual(sum(got["match_result_in_play"].values()), BASIS_POINT_TOTAL)
        self.assertEqual(sum(got["btts_in_play"].values()), BASIS_POINT_TOTAL)
        self.assertEqual(got["btts_in_play"], {"no": 3000, "yes": 7000})


class SnapshotWriteTests(unittest.TestCase):
    def repository(self, *cursors: FakeCursor):
        connection = FakeConnection(results=list(cursors))
        return connection, InPlaySnapshotRepository(connection)

    def test_a_timer_snapshot_writes_one_row_and_returns_its_cursor(self) -> None:
        connection, repository = self.repository(FakeCursor(one={"id": 11, "sequence": 4242}))
        ref = repository.write_snapshot(
            fixture_id=42,
            captured_at=CAPTURED_AT,
            trigger="timer",
            status="2H",
            model_health=MODEL_HEALTH_OK,
            model_version="inplay-analytic-v1",
            markets={"match_result_in_play": {"home": 6412, "draw": 2100, "away": 1488}},
        )
        self.assertEqual((ref.id, ref.sequence), (11, 4242))
        self.assertEqual(len(connection.executed), 1)
        sql, params = connection.executed[0]
        self.assertIn("INSERT INTO inplay_snapshots", sql)
        # sequence is never supplied: the column default allocates it, so no
        # writer can forget it and two writers cannot collide.
        self.assertNotIn("sequence", params)
        self.assertIn("RETURNING id, sequence", sql)
        self.assertEqual(json.loads(params["markets"])["match_result_in_play"]["home"], 6412)
        self.assertTrue(connection.committed)

    def test_a_final_snapshot_also_writes_the_permanent_copy(self) -> None:
        # In the same transaction: the frozen record and the series row must not
        # be able to disagree, and a separate pass could crash between them.
        connection, repository = self.repository(FakeCursor(one={"id": 12, "sequence": 99}))
        repository.write_snapshot(
            fixture_id=42,
            captured_at=CAPTURED_AT,
            trigger="final",
            status="FT",
            model_health=MODEL_HEALTH_OK,
            model_version="inplay-analytic-v1",
        )
        self.assertEqual(len(connection.executed), 2)
        self.assertIn("INSERT INTO inplay_final_snapshots", connection.executed[1][0])
        # The permanent copy carries the series row's cursor, not a fresh one.
        self.assertEqual(connection.executed[1][1]["sequence"], 99)
        # Repeating the job must not overwrite the permanent record with a later,
        # worse observation.
        self.assertIn("ON CONFLICT (fixture_id) DO NOTHING", connection.executed[1][0])

    def test_a_non_final_snapshot_writes_only_the_series_row(self) -> None:
        connection, repository = self.repository(FakeCursor(one={"id": 13, "sequence": 5}))
        repository.write_snapshot(
            fixture_id=42,
            captured_at=CAPTURED_AT,
            trigger="post_goal",
            triggered_by_event_key="g3",
            status="2H",
            model_health=MODEL_HEALTH_OK,
            model_version="inplay-analytic-v1",
        )
        self.assertEqual(len(connection.executed), 1)
        self.assertEqual(connection.executed[0][1]["triggered_by_event_key"], "g3")

    def test_a_failed_write_rolls_back(self) -> None:
        connection = FakeConnection(fail_on_call=1)
        with self.assertRaises(RuntimeError):
            InPlaySnapshotRepository(connection).write_snapshot(
                fixture_id=42,
                captured_at=CAPTURED_AT,
                trigger="timer",
                status="2H",
                model_health=MODEL_HEALTH_OK,
                model_version="v1",
            )
        self.assertTrue(connection.rolled_back)
        self.assertFalse(connection.committed)

    def test_a_missing_returning_row_is_an_error(self) -> None:
        connection, repository = self.repository(FakeCursor(one=None))
        with self.assertRaises(RuntimeError):
            repository.write_snapshot(
                fixture_id=42,
                captured_at=CAPTURED_AT,
                trigger="timer",
                status="2H",
                model_health=MODEL_HEALTH_OK,
                model_version="v1",
            )

    def test_a_non_positive_fixture_id_is_refused(self) -> None:
        _, repository = self.repository()
        with self.assertRaises(ValueError):
            repository.write_snapshot(
                fixture_id=0,
                captured_at=CAPTURED_AT,
                trigger="timer",
                status="2H",
                model_health=MODEL_HEALTH_OK,
                model_version="v1",
            )


class SnapshotBeforeEventTests(unittest.TestCase):
    def test_it_links_to_the_newest_recent_snapshot(self) -> None:
        # "Just before" is a link, not a synthesis: with a 60-second timer the
        # newest prior snapshot is at most a minute stale and is the before-state.
        connection = FakeConnection(results=[FakeCursor(one={"id": 7, "sequence": 70, "captured_at": CAPTURED_AT})])
        ref = InPlaySnapshotRepository(connection).snapshot_before_event(
            fixture_id=42, event_at=CAPTURED_AT + timedelta(seconds=30)
        )
        self.assertEqual((ref.id, ref.sequence), (7, 70))
        sql, params = connection.executed[0]
        self.assertIn("captured_at <", sql)
        self.assertIn("ORDER BY captured_at DESC", sql)
        self.assertEqual(params["max_age_seconds"], InPlaySnapshotRepository.DEFAULT_BEFORE_MAX_AGE_SECONDS)

    def test_nothing_recent_enough_means_the_caller_must_write_one(self) -> None:
        connection = FakeConnection(results=[FakeCursor(one=None)])
        self.assertIsNone(
            InPlaySnapshotRepository(connection).snapshot_before_event(
                fixture_id=42, event_at=CAPTURED_AT
            )
        )

    def test_the_staleness_bound_is_overridable(self) -> None:
        connection = FakeConnection(results=[FakeCursor(one=None)])
        InPlaySnapshotRepository(connection).snapshot_before_event(
            fixture_id=42, event_at=CAPTURED_AT, max_age_seconds=15
        )
        self.assertEqual(connection.executed[0][1]["max_age_seconds"], 15)


class FinaliseSnapshotTests(unittest.TestCase):
    """Freezing the full-time record needs its own sweep.

    The in-play candidate scope only holds live fixtures and `fixtures?live=all`
    drops a match the moment it ends, so no live pass ever sees a fixture at the
    final whistle. Without this the permanent record would never be written.
    """

    def test_it_reports_what_it_examined_and_froze(self) -> None:
        connection = FakeConnection(
            results=[FakeCursor(one={"fixtures_examined": 4, "snapshots_frozen": 3})]
        )
        examined, frozen = InPlaySnapshotRepository(connection).finalise_snapshots(limit=100)
        self.assertEqual((examined, frozen), (4, 3))
        self.assertTrue(connection.committed)

    def test_it_copies_the_newest_point_and_is_idempotent(self) -> None:
        connection = FakeConnection(
            results=[FakeCursor(one={"fixtures_examined": 1, "snapshots_frozen": 1})]
        )
        InPlaySnapshotRepository(connection).finalise_snapshots(limit=10)
        sql = connection.executed[0][0]
        # Newest series row, not the first: the model cannot be re-run for a
        # finished match, so what it last said IS the full-time record.
        self.assertIn("ORDER BY s.fixture_id, s.captured_at DESC", sql)
        self.assertIn("ON CONFLICT (fixture_id) DO NOTHING", sql)
        # Only finished fixtures, and only those not already frozen.
        self.assertIn("lower(f.status) IN ('ft', 'aet', 'pen', 'finished')", sql)
        self.assertIn("NOT EXISTS", sql)

    def test_a_missing_summary_is_an_error(self) -> None:
        connection = FakeConnection(results=[FakeCursor(one=None)])
        with self.assertRaises(RuntimeError):
            InPlaySnapshotRepository(connection).finalise_snapshots(limit=10)
        self.assertTrue(connection.rolled_back)

    def test_a_non_positive_limit_is_refused(self) -> None:
        with self.assertRaises(ValueError):
            InPlaySnapshotRepository(FakeConnection()).finalise_snapshots(limit=0)


class SnapshotCadenceTests(unittest.TestCase):
    """`should_snapshot` is deliberately not `should_regenerate`.

    Publication is gated on the match state moving; the graph needs a point every
    60 seconds regardless, or a quiet passage of play draws as a gap and reads as
    "the model stopped" rather than "nothing changed".
    """

    def state(self, **overrides) -> SnapshotState:
        base = {
            "captured_at": CAPTURED_AT,
            "home_goals": 1,
            "away_goals": 1,
            "home_red_cards": 0,
            "away_red_cards": 0,
            "period": MatchPeriod.SECOND_HALF,
        }
        base.update(overrides)
        return SnapshotState(**base)

    def decide(self, previous, *, after_seconds: int = 10, **feature_overrides):
        return should_snapshot(
            features=features(**feature_overrides),
            previous=previous,
            now=CAPTURED_AT + timedelta(seconds=after_seconds),
        )

    def test_the_first_snapshot_is_always_due(self) -> None:
        decision = self.decide(None)
        self.assertTrue(decision.should_snapshot)
        self.assertEqual(decision.reason, "first_snapshot")

    def test_a_quiet_interval_still_snapshots_on_the_timer(self) -> None:
        # The whole point: nothing changed, and a point is still recorded.
        unchanged = {"home_goals": 1, "away_goals": 1, "period": MatchPeriod.SECOND_HALF}
        self.assertFalse(self.decide(self.state(), after_seconds=30, **unchanged).should_snapshot)
        due = self.decide(self.state(), after_seconds=60, **unchanged)
        self.assertTrue(due.should_snapshot)
        self.assertEqual((due.trigger, due.reason), (SNAPSHOT_TRIGGER_TIMER, "timer"))

    def test_a_goal_snapshots_immediately_and_is_labelled(self) -> None:
        decision = self.decide(
            self.state(), after_seconds=1, home_goals=2, away_goals=1, period=MatchPeriod.SECOND_HALF
        )
        self.assertTrue(decision.should_snapshot)
        self.assertEqual((decision.trigger, decision.reason), (SNAPSHOT_TRIGGER_POST_GOAL, "goal"))

    def test_a_red_card_snapshots_immediately(self) -> None:
        decision = self.decide(
            self.state(),
            after_seconds=1,
            home_goals=1,
            away_goals=1,
            home_red_cards=1,
            period=MatchPeriod.SECOND_HALF,
        )
        self.assertTrue(decision.should_snapshot)
        self.assertEqual(decision.trigger, SNAPSHOT_TRIGGER_POST_RED_CARD)

    def test_a_period_change_snapshots_immediately(self) -> None:
        decision = self.decide(
            self.state(period=MatchPeriod.FIRST_HALF),
            after_seconds=1,
            home_goals=1,
            away_goals=1,
            period=MatchPeriod.SECOND_HALF,
        )
        self.assertTrue(decision.should_snapshot)
        self.assertEqual(decision.trigger, SNAPSHOT_TRIGGER_PERIOD_CHANGE)

    def test_the_snapshot_interval_is_finer_than_either_regeneration_interval(self) -> None:
        # If it were not, the graph would inherit the publication cadence and the
        # 60-second requirement could never be met.
        self.assertLess(DEFAULT_SNAPSHOT_INTERVAL, DEFAULT_IN_PLAY_CONFIG.tier_a_interval)
        self.assertLess(DEFAULT_SNAPSHOT_INTERVAL, DEFAULT_IN_PLAY_CONFIG.tier_b_interval)


if __name__ == "__main__":
    unittest.main()
