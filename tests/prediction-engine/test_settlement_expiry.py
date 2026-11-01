"""The bound: no prediction stays pending forever.

Settlement only ever wrote a `prediction_results` row for a fixture with a
full-time status *and* both full-time goals. A fixture that never reaches that
state produced no row at all, and the API renders a missing row as `pending` --
which is how fixture 3735781 came to show twenty pending cards a day after it
was played.

These tests pin the two halves of the fix that can silently regress: that the
sweep finds those rows and gives them a terminal state, and that it does so
*without* grading them against the score that happens to be stored.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
import unittest

import prediction_engine.db_repository as db_repository_module
from prediction_engine.db_repository import (
    DEFAULT_MAX_PENDING_AGE_HOURS,
    DEFAULT_NOT_PLAYED_GRACE_HOURS,
    PredictionSettlementRepository,
)
from prediction_engine.settlement import (
    VOID_FIXTURE_NOT_PLAYED,
    VOID_RESULT_NEVER_ARRIVED,
    VOID_SETTLEMENT_UNAVAILABLE,
    ActualFixtureResult,
    expire_prediction,
    settle_prediction,
)


NOW = datetime(2026, 9, 21, 12, 0, tzinfo=UTC)


def expired_row(
    prediction_id: str = "11111111-1111-1111-1111-111111111111",
    *,
    market: str = "total_goals",
    prediction: str = "over_2.5",
    void_reason: str = VOID_RESULT_NEVER_ARRIVED,
    model_registry_id: str | None = "22222222-2222-2222-2222-222222222222",
) -> dict[str, object]:
    return {
        "id": prediction_id,
        "fixture_id": 3735781,
        "market": market,
        "prediction": prediction,
        "probability": 0.61,
        "confidence": 0.58,
        "confidence_tier": "medium",
        "lifecycle_stage": "PRE_MATCH",
        "model_version": "python-engine-v1",
        "model_registry_id": model_registry_id,
        "generated_at": NOW - timedelta(days=9),
        "data_cutoff_at": NOW - timedelta(days=9),
        "feature_version": "features-v1",
        "raw_probability": 0.61,
        "calibrated_probability": 0.60,
        "odds_snapshot_id": None,
        "league_id": 10,
        "void_reason": void_reason,
    }


class FakeCursor:
    def __init__(self, many: list[dict[str, object]] | None = None) -> None:
        self.many = many or []

    def fetchone(self) -> dict[str, object] | None:
        return None

    def fetchall(self) -> list[dict[str, object]]:
        return self.many


class FakeConnection:
    def __init__(self, rows: list[dict[str, object]] | None = None, fail_on_call: int | None = None) -> None:
        self.rows = rows or []
        self.fail_on_call = fail_on_call
        self.executed: list[tuple[str, dict[str, object]]] = []
        self.committed = False
        self.rolled_back = False

    def execute(self, sql: str, params: dict[str, object]) -> FakeCursor:
        if self.fail_on_call == len(self.executed) + 1:
            raise RuntimeError("database write failed")
        self.executed.append((sql, params))
        if sql is db_repository_module._EXPIRED_PREDICTIONS_SQL:
            return FakeCursor(many=list(self.rows))
        return FakeCursor()

    def commit(self) -> None:
        self.committed = True

    def rollback(self) -> None:
        self.rolled_back = True

    def written(self, sql: str) -> list[dict[str, object]]:
        return [params for statement, params in self.executed if statement is sql]


class ExpirePredictionTests(unittest.TestCase):
    def test_expiry_never_grades_against_the_stored_score(self) -> None:
        # The defect this guards. Fixture 3735781 carries 1-1 -- but the last
        # provider observation was `1H` at 45 minutes, so that is a half-time
        # snapshot, not a result. Grading "over 2.5 goals" against it would
        # report a loss the match never produced.
        graded = settle_prediction("total_goals", "over_2.5", ActualFixtureResult(home_goals=1, away_goals=1))
        self.assertEqual(graded.is_correct, False)

        expired = expire_prediction("total_goals", "over_2.5", VOID_RESULT_NEVER_ARRIVED)
        self.assertEqual(expired.status, "void")
        self.assertIsNone(expired.actual)
        self.assertIsNone(expired.is_correct)
        self.assertFalse(expired.settled)
        self.assertEqual(expired.void_reason, VOID_RESULT_NEVER_ARRIVED)

    def test_market_rule_voids_stay_unlabelled(self) -> None:
        # A drawn draw-no-bet is a graded outcome. It must not pick up an
        # explanation that says the feed failed, because the feed did not.
        rule_void = settle_prediction("draw_no_bet", "home", ActualFixtureResult(home_goals=1, away_goals=1))
        self.assertEqual(rule_void.status, "void")
        self.assertIsNone(rule_void.void_reason)

    def test_unknown_reason_is_refused(self) -> None:
        with self.assertRaises(ValueError):
            expire_prediction("total_goals", "over_2.5", "felt_like_it")

    def test_a_gradeable_fixture_that_never_settled_is_labelled_as_a_defect(self) -> None:
        # The result exists, so "no result arrived" would be a lie the reader
        # could check against the score on the same page. This reason means
        # "go add the market mapping".
        expired = expire_prediction("inplay_next_goal", "home", VOID_SETTLEMENT_UNAVAILABLE)
        self.assertEqual(expired.status, "void")
        self.assertEqual(expired.void_reason, VOID_SETTLEMENT_UNAVAILABLE)
        self.assertIsNone(expired.is_correct)


class ExpirySweepSQLTests(unittest.TestCase):
    def test_already_terminal_predictions_are_never_revisited(self) -> None:
        sql = db_repository_module._EXPIRED_PREDICTIONS_SQL
        self.assertIn("pr.prediction_id IS NULL", sql)
        self.assertIn("rollup.prediction_id IS NULL", sql)
        self.assertIn("ep.superseded_at IS NULL", sql)

    def test_no_fixture_is_exempt_from_the_bound(self) -> None:
        # The sweep must not carve out "settlement could still grade this".
        # An exemption is how a prediction stays pending forever: a market
        # with no mapping raises and rolls the whole fixture back, so a
        # perfectly settleable fixture can still strand every prediction on
        # it. Such rows are classified, not skipped.
        sql = db_repository_module._EXPIRED_PREDICTIONS_SQL
        self.assertNotIn("NOT (", sql)
        self.assertIn("'settlement_unavailable'", sql)

    def test_oldest_first(self) -> None:
        self.assertIn("ORDER BY f.kickoff_at ASC", db_repository_module._EXPIRED_PREDICTIONS_SQL)


class ExpirySweepTests(unittest.TestCase):
    def test_sweep_writes_a_void_result_and_an_excluded_evaluation_fact(self) -> None:
        connection = FakeConnection(rows=[expired_row()])
        summary = PredictionSettlementRepository(connection).void_expired_predictions(now=NOW, limit=100)

        self.assertEqual(summary.examined, 1)
        self.assertEqual(summary.voided, 1)
        self.assertEqual(summary.facts_written, 1)
        self.assertEqual(summary.fixtures_touched, 1)
        self.assertEqual(summary.reasons, {VOID_RESULT_NEVER_ARRIVED: 1})
        self.assertTrue(connection.committed)

        result = connection.written(db_repository_module._UPSERT_PREDICTION_RESULT_SQL)[0]
        self.assertEqual(result["settlement_status"], "void")
        self.assertEqual(result["void_reason"], VOID_RESULT_NEVER_ARRIVED)
        self.assertIsNone(result["actual"])
        self.assertIsNone(result["is_correct"])
        self.assertEqual(result["settled_at"], NOW)

        fact = connection.written(db_repository_module._INSERT_MODEL_EVALUATION_FACT_SQL)[0]
        self.assertEqual(fact["settlement_classification"], "void")
        self.assertFalse(fact["evaluation_eligible"])
        # The ledger must be able to tell a model failure from a feed failure.
        self.assertEqual(fact["exclusion_reason"], f"settlement_void:{VOID_RESULT_NEVER_ARRIVED}")
        self.assertEqual(fact["final_outcome"], "data_unavailable")

    def test_the_two_clocks_are_independent(self) -> None:
        connection = FakeConnection(rows=[])
        PredictionSettlementRepository(connection).void_expired_predictions(
            now=NOW, limit=50, max_pending_age_hours=168, not_played_grace_hours=12
        )
        params = connection.executed[0][1]
        self.assertEqual(params["limit"], 50)
        self.assertEqual(params["stale_cutoff"], NOW - timedelta(hours=168))
        self.assertEqual(params["not_played_cutoff"], NOW - timedelta(hours=12))

    def test_defaults_keep_reconciliation_ahead_of_the_bound(self) -> None:
        # Results reconciliation runs hourly. The bound has to sit far enough
        # past its reach that a void never lands on a fixture whose real
        # result was about to arrive -- a void is terminal.
        self.assertEqual(DEFAULT_MAX_PENDING_AGE_HOURS, 24 * 7)
        self.assertLess(DEFAULT_NOT_PLAYED_GRACE_HOURS, DEFAULT_MAX_PENDING_AGE_HOURS)

    def test_a_prediction_with_no_model_still_gets_its_terminal_state(self) -> None:
        # The evaluation fact needs a model to attribute the outcome to. The
        # bound does not: dropping the whole row because the audit half cannot
        # be written would leave it pending forever, which is the defect.
        connection = FakeConnection(rows=[expired_row(model_registry_id=None)])
        summary = PredictionSettlementRepository(connection).void_expired_predictions(now=NOW, limit=100)

        self.assertEqual(summary.voided, 1)
        self.assertEqual(summary.facts_written, 0)
        self.assertEqual(len(summary.unevaluable_prediction_ids), 1)
        self.assertEqual(len(connection.written(db_repository_module._UPSERT_PREDICTION_RESULT_SQL)), 1)
        self.assertEqual(connection.written(db_repository_module._INSERT_MODEL_EVALUATION_FACT_SQL), [])

    def test_reasons_are_counted_separately(self) -> None:
        connection = FakeConnection(
            rows=[
                expired_row("aaaaaaaa-0000-0000-0000-000000000001"),
                expired_row("aaaaaaaa-0000-0000-0000-000000000002", void_reason=VOID_FIXTURE_NOT_PLAYED),
                expired_row("aaaaaaaa-0000-0000-0000-000000000003", void_reason=VOID_FIXTURE_NOT_PLAYED),
            ]
        )
        summary = PredictionSettlementRepository(connection).void_expired_predictions(now=NOW, limit=100)
        self.assertEqual(
            summary.reasons,
            {VOID_RESULT_NEVER_ARRIVED: 1, VOID_FIXTURE_NOT_PLAYED: 2},
        )

    def test_a_failed_write_rolls_the_whole_sweep_back(self) -> None:
        connection = FakeConnection(rows=[expired_row()], fail_on_call=2)
        with self.assertRaises(RuntimeError):
            PredictionSettlementRepository(connection).void_expired_predictions(now=NOW, limit=100)
        self.assertTrue(connection.rolled_back)
        self.assertFalse(connection.committed)

    def test_thresholds_must_be_positive(self) -> None:
        repository = PredictionSettlementRepository(FakeConnection())
        with self.assertRaises(ValueError):
            repository.void_expired_predictions(now=NOW, limit=0)
        with self.assertRaises(ValueError):
            repository.void_expired_predictions(now=NOW, limit=10, max_pending_age_hours=0)
        with self.assertRaises(ValueError):
            repository.void_expired_predictions(now=NOW, limit=10, not_played_grace_hours=0)


if __name__ == "__main__":
    unittest.main()
