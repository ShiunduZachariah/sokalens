from __future__ import annotations

from datetime import UTC, datetime
from io import StringIO
from types import SimpleNamespace
import json
import os
import unittest

from prediction_engine.cli import main
from prediction_engine.pipeline import BackfillDeferred


class CLITests(unittest.TestCase):
    def test_run_fixture_invokes_engine_for_single_fixture_and_prints_summary(self) -> None:
        stdout = StringIO()
        connection = FakeConnection()
        calls: list[int] = []

        code = main(
            [
                "run-fixture",
                "--fixture-id",
                "42",
                "--database-url",
                "postgres://example",
                "--generated-at",
                "2026-07-01T12:00:00+00:00",
            ],
            connect=lambda database_url: connection,
            runner=lambda fixture_id, **_: calls.append(fixture_id) or [object()],
            stdout=stdout,
        )

        self.assertEqual(code, 0)
        self.assertEqual(calls, [42])
        self.assertEqual(
            json.loads(stdout.getvalue()),
            {
                "failures": [],
                "fixture_ids": [42],
                "fixtures_failed": 0,
                "fixtures_processed": 1,
                "fixtures_skipped": 0,
                "predictions_generated": 1,
                "predictions_inserted": 1,
                "predictions_stale": 0,
                "predictions_unchanged": 0,
                "predictions_versioned": 0,
                "predictions_written": 1,
                "scope": "fixture",
                "skipped_fixture_ids": [],
            },
        )

    def test_as_of_alias_is_normalized_to_utc_and_passed_explicitly(self) -> None:
        stdout = StringIO()
        connection = FakeConnection()
        received: list[datetime] = []

        code = main(
            [
                "run-fixture",
                "--fixture-id",
                "42",
                "--database-url",
                "postgres://example",
                "--as-of-timestamp",
                "2026-07-01T15:00:00+03:00",
            ],
            connect=lambda database_url: connection,
            runner=lambda fixture_id, **kwargs: received.append(kwargs["as_of_timestamp"]) or [],
            stdout=stdout,
        )

        self.assertEqual(code, 0)
        self.assertEqual(received, [datetime(2026, 7, 1, 12, 0, tzinfo=UTC)])

    def test_replay_timestamp_rejects_naive_datetime(self) -> None:
        with self.assertRaisesRegex(ValueError, "timezone-aware"):
            main(
                [
                    "run-fixture",
                    "--fixture-id",
                    "42",
                    "--database-url",
                    "postgres://example",
                    "--as-of-timestamp",
                    "2026-07-01T12:00:00",
                ],
                connect=lambda database_url: FakeConnection(),
            )

    def test_run_scope_lists_candidate_fixtures_then_invokes_engine_for_each(self) -> None:
        stdout = StringIO()
        connection = FakeConnection(results=[FakeCursor(many=[{"id": 42}, {"id": 43}])])
        calls: list[int] = []

        code = main(
            [
                "run-scope",
                "--scope",
                "historical",
                "--limit",
                "2",
                "--database-url",
                "postgres://example",
                "--generated-at",
                "2026-07-01T12:00:00+00:00",
            ],
            connect=lambda database_url: connection,
            runner=lambda fixture_id, **_: calls.append(fixture_id) or [object(), object()],
            stdout=stdout,
        )

        self.assertEqual(code, 0)
        self.assertEqual(calls, [42, 43])
        self.assertIn("f.home_goals_ft IS NOT NULL", connection.executed[0][0])
        self.assertEqual(json.loads(stdout.getvalue())["predictions_written"], 4)

    def test_historical_scope_is_a_non_publishing_preview_by_default(self) -> None:
        stdout = StringIO()
        connection = FakeConnection(results=[FakeCursor(many=[{"id": 42}])])

        code = main(
            [
                "run-scope",
                "--scope",
                "historical",
                "--limit",
                "1",
                "--database-url",
                "postgres://example",
                "--generated-at",
                "2026-07-01T12:00:00+00:00",
            ],
            connect=lambda database_url: connection,
            preview_runner=lambda fixture_id, **_: [object(), object()],
            stdout=stdout,
        )

        self.assertEqual(code, 0)
        summary = json.loads(stdout.getvalue())
        self.assertEqual(summary["predictions_generated"], 2)
        self.assertEqual(summary["predictions_written"], 0)
        self.assertEqual(summary["predictions_inserted"], 0)

    def test_database_url_can_come_from_environment(self) -> None:
        stdout = StringIO()
        connection = FakeConnection()
        previous = os.environ.get("DATABASE_URL")
        os.environ["DATABASE_URL"] = "postgres://from-env"
        try:
            code = main(
                ["run-fixture", "--fixture-id", "42"],
                connect=lambda database_url: connection,
                runner=lambda fixture_id, **_: [],
                stdout=stdout,
            )
        finally:
            if previous is None:
                os.environ.pop("DATABASE_URL", None)
            else:
                os.environ["DATABASE_URL"] = previous

        self.assertEqual(code, 0)
        summary = json.loads(stdout.getvalue())
        self.assertEqual(summary["predictions_written"], 0)
        self.assertEqual(summary["fixtures_skipped"], 1)
        self.assertEqual(summary["skipped_fixture_ids"], [42])
        self.assertEqual(summary["fixtures_failed"], 0)

    def test_pending_backfill_is_reported_as_deferred_not_skipped_or_failed(self) -> None:
        stdout = StringIO()
        connection = FakeConnection(results=[FakeCursor(many=[{"id": 42}])])

        code = main(
            [
                "run-scope",
                "--scope",
                "upcoming",
                "--limit",
                "1",
                "--database-url",
                "postgres://example",
                "--generated-at",
                "2026-07-01T12:00:00+00:00",
            ],
            connect=lambda database_url: connection,
            runner=lambda fixture_id, **_: (_ for _ in ()).throw(BackfillDeferred(fixture_id)),
            stdout=stdout,
        )

        self.assertEqual(code, 0)
        summary = json.loads(stdout.getvalue())
        self.assertEqual(summary["fixtures_deferred"], 1)
        self.assertEqual(summary["deferred_fixture_ids"], [42])
        self.assertEqual(summary["fixtures_skipped"], 0)
        self.assertEqual(summary["fixtures_failed"], 0)

    def test_run_scope_continues_after_fixture_failure_and_reports_structured_summary(self) -> None:
        stdout = StringIO()
        connection = FakeConnection(results=[FakeCursor(many=[{"id": 42}, {"id": 43}, {"id": 44}])])
        calls: list[int] = []

        def runner(fixture_id: int, **_: object) -> list[object]:
            calls.append(fixture_id)
            if fixture_id == 43:
                raise ValueError("invalid normalized fixture")
            if fixture_id == 44:
                return []
            return [object(), object()]

        code = main(
            [
                "run-scope",
                "--scope",
                "upcoming",
                "--limit",
                "3",
                "--database-url",
                "postgres://example",
                "--generated-at",
                "2026-07-01T12:00:00+00:00",
            ],
            connect=lambda database_url: connection,
            runner=runner,
            stdout=stdout,
        )

        self.assertEqual(code, 0)
        self.assertEqual(calls, [42, 43, 44])
        self.assertTrue(connection.rolled_back)
        self.assertEqual(
            json.loads(stdout.getvalue()),
            {
                "failures": [
                    {
                        "error_type": "ValueError",
                        "fixture_id": 43,
                        "message": "invalid normalized fixture",
                    }
                ],
                "fixture_ids": [42, 43, 44],
                "fixtures_failed": 1,
                "fixtures_processed": 3,
                "fixtures_skipped": 1,
                "predictions_generated": 2,
                "predictions_inserted": 2,
                "predictions_stale": 0,
                "predictions_unchanged": 0,
                "predictions_versioned": 0,
                "predictions_written": 2,
                "scope": "upcoming",
                "skipped_fixture_ids": [44],
            },
        )

    def test_missing_database_url_returns_error(self) -> None:
        stdout = StringIO()
        previous = os.environ.pop("DATABASE_URL", None)
        try:
            code = main(["run-fixture", "--fixture-id", "42"], stdout=stdout)
        finally:
            if previous is not None:
                os.environ["DATABASE_URL"] = previous

        self.assertEqual(code, 2)
        self.assertIn("database URL is required", stdout.getvalue())

    def test_settle_fixture_invokes_settlement_runner_and_prints_summary(self) -> None:
        stdout = StringIO()
        connection = FakeConnection()
        calls: list[int] = []

        code = main(
            [
                "settle-fixture",
                "--fixture-id",
                "42",
                "--database-url",
                "postgres://example",
                "--generated-at",
                "2026-07-02T12:00:00+00:00",
            ],
            connect=lambda database_url: connection,
            settlement_runner=lambda fixture_id, **_: calls.append(fixture_id) or [object(), object(), object()],
            stdout=stdout,
        )

        self.assertEqual(code, 0)
        self.assertEqual(calls, [42])
        self.assertEqual(
            json.loads(stdout.getvalue()),
            {
                "fixture_id": 42,
                "results_written": 3,
                "scope": "settlement",
            },
        )

    def test_settle_scope_continues_past_a_failing_fixture(self) -> None:
        # One unsettleable fixture used to abort the whole batch: settle_fixture
        # rolls back and re-raises, and this loop had no per-fixture guard, so
        # every fixture queued behind the bad one went unsettled too. The
        # trigger in production was an in-play market with no settlement arm.
        stdout = StringIO()
        connection = FakeConnection(
            results=[FakeCursor(many=[{"fixture_id": 42}, {"fixture_id": 43}, {"fixture_id": 44}])]
        )
        calls: list[int] = []

        def runner(fixture_id: int, **_: object) -> list[object]:
            calls.append(fixture_id)
            if fixture_id == 43:
                raise ValueError("unsupported settlement market: match_result_in_play")
            return [object()]

        code = main(
            [
                "settle-scope",
                "--scope",
                "finished",
                "--limit",
                "3",
                "--database-url",
                "postgres://example",
                "--generated-at",
                "2026-07-02T12:00:00+00:00",
            ],
            connect=lambda database_url: connection,
            settlement_runner=runner,
            stdout=stdout,
        )

        # Every fixture is attempted, including the ones behind the failure.
        self.assertEqual(calls, [42, 43, 44])
        payload = json.loads(stdout.getvalue())
        self.assertEqual(payload["fixtures_processed"], 3)
        self.assertEqual(payload["fixtures_settled"], 2)
        self.assertEqual(payload["fixtures_failed"], 1)
        self.assertEqual(payload["results_written"], 2)
        self.assertEqual([failure["fixture_id"] for failure in payload["failures"]], [43])
        self.assertIn("match_result_in_play", payload["failures"][0]["error"])
        # Non-zero, because a batch that silently half-succeeds is how this
        # defect stayed invisible. Settlement is idempotent -- already-settled
        # rows drop out of the candidate query -- so a retry costs nothing.
        self.assertEqual(code, 1)

    def test_settle_scope_does_not_swallow_keyboard_interrupt(self) -> None:
        # The guard catches Exception, not BaseException: an operator stopping
        # the worker must not be recorded as a settlement failure and retried.
        stdout = StringIO()
        connection = FakeConnection(results=[FakeCursor(many=[{"fixture_id": 42}])])

        def runner(fixture_id: int, **_: object) -> list[object]:
            raise KeyboardInterrupt

        with self.assertRaises(KeyboardInterrupt):
            main(
                [
                    "settle-scope",
                    "--scope",
                    "finished",
                    "--limit",
                    "1",
                    "--database-url",
                    "postgres://example",
                    "--generated-at",
                    "2026-07-02T12:00:00+00:00",
                ],
                connect=lambda database_url: connection,
                settlement_runner=runner,
                stdout=stdout,
            )

    def test_settle_scope_lists_finished_fixtures_and_settles_each_one(self) -> None:
        stdout = StringIO()
        connection = FakeConnection(results=[FakeCursor(many=[{"fixture_id": 42}, {"fixture_id": 43}])])
        calls: list[int] = []

        code = main(
            [
                "settle-scope",
                "--scope",
                "finished",
                "--limit",
                "2",
                "--database-url",
                "postgres://example",
                "--generated-at",
                "2026-07-02T12:00:00+00:00",
            ],
            connect=lambda database_url: connection,
            settlement_runner=lambda fixture_id, **_: calls.append(fixture_id) or [object(), object()],
            stdout=stdout,
        )

        self.assertEqual(code, 0)
        self.assertEqual(calls, [42, 43])
        self.assertIn("FROM engine_predictions AS ep", connection.executed[0][0])
        self.assertEqual(
            json.loads(stdout.getvalue()),
            {
                "failures": [],
                "fixture_ids": [42, 43],
                "fixtures_failed": 0,
                "fixtures_processed": 2,
                "fixtures_settled": 2,
                "results_written": 4,
                "scope": "settlement",
                "settlement_scope": "finished",
            },
        )

    def test_void_expired_reports_what_it_terminated(self) -> None:
        # The sweep is the bound behind settlement: a prediction whose fixture
        # never produced a gradeable result gets a terminal state anyway.
        stdout = StringIO()
        connection = FakeConnection(
            results=[
                FakeCursor(
                    many=[
                        {
                            "id": "11111111-1111-1111-1111-111111111111",
                            "fixture_id": 3735781,
                            "market": "total_goals",
                            "prediction": "over_2.5",
                            "probability": 0.61,
                            "confidence": 0.58,
                            "confidence_tier": "medium",
                            "lifecycle_stage": "PRE_MATCH",
                            "model_version": "python-engine-v1",
                            "model_registry_id": "22222222-2222-2222-2222-222222222222",
                            "generated_at": datetime(2026, 9, 10, 12, 0, tzinfo=UTC),
                            "data_cutoff_at": None,
                            "feature_version": "features-v1",
                            "raw_probability": 0.61,
                            "calibrated_probability": 0.60,
                            "odds_snapshot_id": None,
                            "league_id": 10,
                            "void_reason": "result_never_arrived",
                        }
                    ]
                )
            ]
        )

        code = main(
            [
                "void-expired",
                "--limit",
                "100",
                "--max-pending-age-hours",
                "168",
                "--database-url",
                "postgres://example",
                "--generated-at",
                "2026-09-21T12:00:00+00:00",
            ],
            connect=lambda database_url: connection,
            stdout=stdout,
        )

        self.assertEqual(code, 0)
        self.assertEqual(
            json.loads(stdout.getvalue()),
            {
                "evaluation_facts_written": 1,
                "fixtures_processed": 1,
                "max_pending_age_hours": 168,
                "results_examined": 1,
                "results_voided": 1,
                "results_written": 1,
                "scope": "settlement_expiry",
                "unevaluable_prediction_ids": [],
                "void_reasons": {"result_never_arrived": 1},
            },
        )

    def test_void_expired_refuses_a_non_positive_bound(self) -> None:
        # A zero or negative bound is not "no bound"; it would void every
        # pending prediction the moment it kicked off.
        with self.assertRaises(SystemExit):
            main(
                ["void-expired", "--max-pending-age-hours", "0", "--database-url", "postgres://example"],
                connect=lambda database_url: FakeConnection(),
                stdout=StringIO(),
            )

    def test_snapshot_accuracy_invokes_accuracy_runner_and_prints_summary(self) -> None:
        stdout = StringIO()
        connection = FakeConnection()
        called_dates: list[str] = []

        code = main(
            [
                "snapshot-accuracy",
                "--database-url",
                "postgres://example",
                "--generated-at",
                "2026-07-02T12:00:00+00:00",
            ],
            connect=lambda database_url: connection,
            accuracy_runner=lambda *, snapshot_date, **_: called_dates.append(snapshot_date.isoformat()) or [object(), object()],
            stdout=stdout,
        )

        self.assertEqual(code, 0)
        self.assertEqual(called_dates, ["2026-07-02"])
        self.assertEqual(
            json.loads(stdout.getvalue()),
            {
                "snapshot_date": "2026-07-02",
                "snapshots_written": 2,
                "scope": "accuracy",
            },
        )

    def test_aggregate_prune_results_reports_durable_aggregate_and_prune_counts(self) -> None:
        stdout = StringIO()
        connection = FakeConnection(
            results=[FakeCursor(one={"results_examined": 12, "snapshots_written": 4, "results_pruned": 12})]
        )

        code = main(
            [
                "aggregate-prune-results",
                "--database-url",
                "postgres://example",
                "--generated-at",
                "2026-07-02T12:00:00+00:00",
                "--public-detail-hours",
                "24",
                "--raw-retention-days",
                "365",
            ],
            connect=lambda database_url: connection,
            stdout=stdout,
        )

        self.assertEqual(code, 0)
        self.assertEqual(
            json.loads(stdout.getvalue()),
            {
                "public_detail_hours": 24,
                "raw_cutoff": "2025-07-02T12:00:00+00:00",
                "raw_retention_days": 365,
                "results_examined": 12,
                "results_pruned": 12,
                "scope": "retention",
                "snapshots_written": 4,
            },
        )
        self.assertIn("INSERT INTO prediction_accuracy_daily", connection.executed[0][0])

    def test_resettle_retracted_reports_what_moved(self) -> None:
        # The reversal sweep: re-grade fixtures whose settled predictions rest on
        # evidence the provider has withdrawn, and say how much actually moved.
        stdout = StringIO()
        connection = FakeConnection(results=[FakeCursor(many=[{"fixture_id": 42}, {"fixture_id": 43}])])

        class Recorder:
            def __init__(self) -> None:
                self.calls: list[int] = []

            def list_retracted_settlement_fixture_ids(self, *, limit: int) -> list[int]:
                return [42, 43]

            def resettle_fixture(self, fixture_id: int, *, recorded_at: object) -> list[object]:
                self.calls.append(fixture_id)
                if fixture_id == 43:
                    raise RuntimeError("timeline unavailable")
                reversed_outcome = SimpleNamespace(
                    changed=True, result=SimpleNamespace(status="settlement_reversed")
                )
                unchanged = SimpleNamespace(changed=False, result=SimpleNamespace(status="settled"))
                return [reversed_outcome, unchanged]

        recorder = Recorder()
        code = main(
            [
                "resettle-retracted",
                "--limit",
                "10",
                "--database-url",
                "postgres://example",
                "--generated-at",
                "2026-09-30T12:00:00+00:00",
            ],
            connect=lambda database_url: connection,
            settlement_repository_override=recorder,
            stdout=stdout,
        )

        self.assertEqual(recorder.calls, [42, 43])
        payload = json.loads(stdout.getvalue())
        self.assertEqual(payload["predictions_examined"], 2)
        self.assertEqual(payload["predictions_changed"], 1)
        self.assertEqual(payload["predictions_reversed"], 1)
        self.assertEqual(payload["fixtures_failed"], 1)
        # Non-zero, and one bad fixture did not stop the other.
        self.assertEqual(code, 1)




class FakeConnection:
    def __init__(self, results: list["FakeCursor"] | None = None) -> None:
        self.results = results or []
        self.executed: list[tuple[str, dict[str, object]]] = []
        self.rolled_back = False
        self.committed = False

    def execute(self, sql: str, params: dict[str, object]) -> "FakeCursor":
        self.executed.append((sql, params))
        if self.results:
            return self.results.pop(0)
        return FakeCursor()

    def commit(self) -> None:
        self.committed = True

    def rollback(self) -> None:
        self.rolled_back = True


class FakeCursor:
    def __init__(self, one: dict[str, object] | None = None, many: list[dict[str, object]] | None = None) -> None:
        self.one = one
        self.many = many or []

    def fetchone(self) -> dict[str, object] | None:
        return self.one

    def fetchall(self) -> list[dict[str, object]]:
        return self.many


if __name__ == "__main__":
    unittest.main()