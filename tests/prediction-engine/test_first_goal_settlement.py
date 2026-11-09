"""`team_to_score_first` grades on the goal timeline, or closes honestly.

The market used to grade only on 0-0 matches, because nothing ever set
`first_goal_team`: every match with a goal closed as
`unsettled_data_unavailable`. That made the market's recorded accuracy the hit
rate on goalless matches alone, about 3%, and that number fed its confidence.
"""

from __future__ import annotations

from datetime import UTC, datetime
import unittest

from prediction_engine.db_repository import PredictionSettlementRepository
from prediction_engine.settlement import REVISION_RESCORED, STATUS_REVERSED
from test_db_repository import FakeConnection, FakeCursor

SETTLED_AT = datetime(2026, 11, 4, 18, 0, tzinfo=UTC)
HOME, AWAY = 10, 20


def goal(key: str, team: int | None, minute: int, extra: int | None = None, observed_minute: int | None = None):
    return {
        "observed_at": datetime(2026, 11, 4, 15, 0, tzinfo=UTC).replace(minute=(observed_minute or minute) % 60),
        "elapsed_minute": minute,
        "elapsed_extra": extra,
        "team_id": team,
        "identity_key": key,
    }


def fixture_row(home_goals: int, away_goals: int) -> dict[str, object]:
    return {
        "id": 42,
        "league_id": 39,
        "home_team_id": HOME,
        "away_team_id": AWAY,
        "home_goals_ft": home_goals,
        "away_goals_ft": away_goals,
        "home_goals_ht": None,
        "away_goals_ht": None,
    }


def prediction_row(selection: str = "away", market: str = "team_to_score_first", **changes) -> dict[str, object]:
    return {
        "id": "uuid-1",
        "fixture_id": 42,
        "market": market,
        "prediction": selection,
        "probability": 0.57,
        "confidence": 0.41,
        "confidence_tier": "none",
        "lifecycle_stage": "PRE_MATCH",
        "league_id": 39,
        "model_registry_id": "model-1",
        "generated_at": datetime(2026, 11, 4, 12, 0, tzinfo=UTC),
        "data_cutoff_at": datetime(2026, 11, 4, 11, 55, tzinfo=UTC),
        "feature_version": "v1-approximate",
        "raw_probability": 0.57,
        "calibrated_probability": None,
        "odds_snapshot_id": None,
        **changes,
    }


class FirstGoalSettlementTests(unittest.TestCase):
    def settle(self, score: tuple[int, int], goals: list[dict[str, object]], row: dict[str, object] | None = None):
        connection = FakeConnection(
            results=[
                FakeCursor(one=fixture_row(*score)),
                FakeCursor(many=[]),
                FakeCursor(many=[row or prediction_row()]),
                FakeCursor(many=goals),
            ]
        )
        self.connection = connection
        return PredictionSettlementRepository(connection).settle_fixture(42, settled_at=SETTLED_AT)[0]

    def test_the_side_that_scored_first_grades_the_market(self) -> None:
        result = self.settle(
            (2, 1),
            [goal("a", AWAY, 10), goal("h1", HOME, 40), goal("h2", HOME, 70)],
        )

        self.assertEqual(result.status, "settled")
        self.assertEqual(result.actual, "away")
        self.assertIs(result.is_correct, True)

    def test_the_wrong_side_loses(self) -> None:
        result = self.settle((2, 1), [goal("a", AWAY, 10), goal("h1", HOME, 40), goal("h2", HOME, 70)], prediction_row("home"))

        self.assertEqual(result.actual, "away")
        self.assertIs(result.is_correct, False)

    def test_the_settlement_names_the_goal_that_decided_it(self) -> None:
        # The column reads NULL as "decided by the full-time score", which is
        # not true of this market. A later retraction has to be traceable.
        self.settle((2, 1), [goal("a", AWAY, 10), goal("h1", HOME, 40), goal("h2", HOME, 70)])

        params = next(params for sql, params in self.connection.executed if "settled_by_event_keys" in params)
        self.assertEqual(params["settled_by_event_keys"], ["a"])

    def test_match_clock_decides_not_when_the_poll_saw_it(self) -> None:
        # A backfilled or batched timeline observes goals out of order, and the
        # query sorts by observed_at. The away goal really came first.
        result = self.settle(
            (1, 1),
            [goal("h", HOME, 60, observed_minute=5), goal("a", AWAY, 20, observed_minute=6)],
        )

        self.assertEqual(result.actual, "away")

    def test_stoppage_time_is_earlier_than_the_next_half(self) -> None:
        result = self.settle((1, 1), [goal("h", HOME, 46), goal("a", AWAY, 45, extra=3)])

        self.assertEqual(result.actual, "away")

    def test_same_minute_goals_for_the_same_side_still_name_it(self) -> None:
        result = self.settle((2, 0), [goal("h1", HOME, 12), goal("h2", HOME, 12)], prediction_row("home"))

        self.assertEqual(result.actual, "home")
        params = next(params for sql, params in self.connection.executed if "settled_by_event_keys" in params)
        self.assertEqual(params["settled_by_event_keys"], ["h1", "h2"])

    def test_same_minute_goals_for_both_sides_are_not_guessed(self) -> None:
        result = self.settle((1, 1), [goal("h", HOME, 12), goal("a", AWAY, 12, extra=0)])

        self.assertEqual(result.status, "unsettled_data_unavailable")
        self.assertIsNone(result.is_correct)

    def test_a_timeline_that_misses_a_goal_is_not_trusted(self) -> None:
        # The missing goal could be the first one.
        result = self.settle((2, 1), [goal("h1", HOME, 40), goal("a", AWAY, 50)])

        self.assertEqual(result.status, "unsettled_data_unavailable")
        self.assertIsNone(result.actual)

    def test_a_timeline_with_the_wrong_split_is_not_trusted(self) -> None:
        result = self.settle((2, 0), [goal("a", AWAY, 10), goal("h", HOME, 40)])

        self.assertEqual(result.status, "unsettled_data_unavailable")

    def test_an_empty_timeline_closes_the_market(self) -> None:
        # Most fixtures with no event coverage end here, and that is the contract.
        result = self.settle((1, 0), [])

        self.assertEqual(result.status, "unsettled_data_unavailable")

    def test_an_unattributable_goal_is_not_guessed(self) -> None:
        result = self.settle((1, 0), [goal("x", None, 10)])

        self.assertEqual(result.status, "unsettled_data_unavailable")

    def test_a_goal_for_some_other_team_is_not_trusted(self) -> None:
        result = self.settle((1, 0), [goal("x", 999, 10)])

        self.assertEqual(result.status, "unsettled_data_unavailable")

    def test_a_goalless_match_needs_no_timeline(self) -> None:
        connection = FakeConnection(
            results=[
                FakeCursor(one=fixture_row(0, 0)),
                FakeCursor(many=[]),
                FakeCursor(many=[prediction_row("no_goals")]),
            ]
        )

        result = PredictionSettlementRepository(connection).settle_fixture(42, settled_at=SETTLED_AT)[0]

        self.assertEqual(result.actual, "no_goals")
        self.assertIs(result.is_correct, True)
        self.assertFalse(any("fixture_events" in sql for sql, _ in connection.executed))

    def test_a_fixture_without_the_market_does_not_pay_for_the_query(self) -> None:
        connection = FakeConnection(
            results=[
                FakeCursor(one=fixture_row(1, 0)),
                FakeCursor(many=[]),
                FakeCursor(many=[prediction_row("home", market="1x2")]),
            ]
        )

        PredictionSettlementRepository(connection).settle_fixture(42, settled_at=SETTLED_AT)

        self.assertFalse(any("fixture_events" in sql for sql, _ in connection.executed))

    def test_the_timeline_query_keeps_withdrawn_goals_out(self) -> None:
        self.settle((1, 0), [goal("h", HOME, 10)])

        sql = next(sql for sql, _ in self.connection.executed if "fixture_events" in sql)
        self.assertIn("is_retracted = false", sql)
        self.assertIn("missed penalty", sql)
        self.assertIn("elapsed_minute", sql)
        self.assertIn("elapsed_extra", sql)


class FirstGoalRetractionTests(unittest.TestCase):
    """A withdrawn first goal has to move the grade, visibly."""

    def resettle(self, score: tuple[int, int], goals: list[dict[str, object]], previous_actual: str, previous_correct: bool):
        row = prediction_row(
            "away",
            settlement_status="settled",
            previous_actual=previous_actual,
            previous_is_correct=previous_correct,
        )
        results = [FakeCursor(one=fixture_row(*score)), FakeCursor(many=[]), FakeCursor(many=[row])]
        if sum(score) > 0:
            results.append(FakeCursor(many=goals))
        results.append(FakeCursor(one={"identity_key": "withdrawn"}))
        connection = FakeConnection(results=results)
        self.connection = connection
        return PredictionSettlementRepository(connection).resettle_fixture(42, recorded_at=SETTLED_AT)[0]

    def test_a_withdrawn_opener_hands_the_market_to_the_new_first_scorer(self) -> None:
        outcome = self.resettle((1, 1), [goal("h", HOME, 30), goal("a", AWAY, 50)], "away", True)

        self.assertTrue(outcome.changed)
        self.assertEqual(outcome.revision_reason, REVISION_RESCORED)
        self.assertEqual(outcome.result.actual, "home")
        self.assertIs(outcome.result.is_correct, False)

    def test_a_withdrawn_only_goal_grades_on_the_goalless_score(self) -> None:
        outcome = self.resettle((0, 0), [], "away", True)

        self.assertTrue(outcome.changed)
        self.assertEqual(outcome.result.actual, "no_goals")
        self.assertIs(outcome.result.is_correct, False)

    def test_a_withdrawn_goal_with_the_score_not_yet_corrected_is_a_reversal(self) -> None:
        # Goal gone from the timeline, score still counting it: nothing can be
        # graded, so the grade is withdrawn and the withdrawal is shown.
        outcome = self.resettle((1, 1), [goal("h", HOME, 30)], "away", True)

        self.assertTrue(outcome.changed)
        self.assertEqual(outcome.result.status, STATUS_REVERSED)
        self.assertIsNone(outcome.result.is_correct)

    def test_a_withdrawn_later_goal_leaves_the_first_scorer_alone(self) -> None:
        outcome = self.resettle((0, 1), [goal("a", AWAY, 10)], "away", True)

        self.assertFalse(outcome.changed)

    def test_a_closed_row_on_a_retracted_fixture_grades_with_a_revision(self) -> None:
        # The one route by which an already-closed row changes after this fix.
        # It writes a revision saying so, rather than rewriting the row quietly.
        row = prediction_row(
            "away",
            settlement_status="unsettled_data_unavailable",
            previous_actual=None,
            previous_is_correct=None,
        )
        connection = FakeConnection(
            results=[
                FakeCursor(one=fixture_row(1, 1)),
                FakeCursor(many=[]),
                FakeCursor(many=[row]),
                FakeCursor(many=[goal("a", AWAY, 10), goal("h", HOME, 30)]),
                FakeCursor(one={"identity_key": "withdrawn"}),
            ]
        )

        outcome = PredictionSettlementRepository(connection).resettle_fixture(42, recorded_at=SETTLED_AT)[0]

        self.assertTrue(outcome.changed)
        self.assertEqual(outcome.revision_reason, REVISION_RESCORED)
        self.assertEqual(outcome.result.actual, "away")
        self.assertTrue(any("INSERT INTO prediction_result_revisions" in sql for sql, _ in connection.executed))


if __name__ == "__main__":
    unittest.main()
