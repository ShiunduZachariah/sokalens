"""The in-play scope on the CLI, and the §7.5 wall around it.

The in-play path is reached by naming `--scope in_play` and by nothing else.
That is the whole of §7.5 at this layer, and it is asserted in both
directions, because the failure it prevents is silent: a post-kickoff row on a
pre-match surface looks exactly like a pre-match row.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
from io import StringIO
import json
import unittest

from prediction_engine.cli import IN_PLAY_SCOPE_CHOICES, main
from prediction_engine.inplay_features import MatchPeriod
from prediction_engine.inplay_model import SuppressionReason
from prediction_engine.inplay_runner import CADENCE_UNCHANGED, InPlayRunOutcome


AS_OF = "2026-09-21T09:52:30+00:00"
NOW = datetime(2026, 9, 21, 9, 52, 30, tzinfo=UTC)


class FakeCursor:
    def __init__(self, one: dict[str, object] | None = None, many: list[dict[str, object]] | None = None) -> None:
        self.one = one
        self.many = many or []

    def fetchone(self) -> dict[str, object] | None:
        return self.one

    def fetchall(self) -> list[dict[str, object]]:
        return self.many


class FakeConnection:
    def __init__(self, results: list[FakeCursor] | None = None) -> None:
        self.results = results or []
        self.executed: list[tuple[str, dict[str, object]]] = []
        self.rolled_back = False

    def execute(self, sql: str, params: dict[str, object]) -> FakeCursor:
        self.executed.append((sql, params))
        return self.results.pop(0) if self.results else FakeCursor()

    def commit(self) -> None:
        return None

    def rollback(self) -> None:
        self.rolled_back = True


def candidates(*fixture_ids: int) -> FakeConnection:
    return FakeConnection([FakeCursor(many=[{"id": fixture_id} for fixture_id in fixture_ids])])


def published(fixture_id: int, markets: int = 4) -> InPlayRunOutcome:
    return InPlayRunOutcome(
        fixture_id=fixture_id,
        published=tuple(object() for _ in range(markets)),  # type: ignore[arg-type]
        reason=SuppressionReason.NONE.value,
        regeneration_reason="goal",
        match_minute=63,
        period=MatchPeriod.SECOND_HALF.value,
        fixture_live_snapshot_id=4821,
        snapshot_age_seconds=12.0,
    )


def suppressed(fixture_id: int, reason: str) -> InPlayRunOutcome:
    return InPlayRunOutcome(
        fixture_id=fixture_id,
        reason=reason,
        match_minute=70,
        period=MatchPeriod.SECOND_HALF.value,
        fixture_live_snapshot_id=99,
        snapshot_age_seconds=140.0,
    )


class InPlayScopeCLITests(unittest.TestCase):
    def test_the_scope_is_exposed_under_both_spellings(self) -> None:
        # db_repository.IN_PLAY_SCOPES already accepted both; the CLI refused
        # both, which is why the path was unreachable.
        self.assertEqual(IN_PLAY_SCOPE_CHOICES, ("in_play", "inplay"))

    def test_the_in_play_scope_runs_the_in_play_runner(self) -> None:
        stdout = StringIO()
        seen: list[int] = []

        code = main(
            ["run-scope", "--scope", "in_play", "--database-url", "postgres://x", "--generated-at", AS_OF],
            connect=lambda _url: candidates(3956996, 3956998),
            in_play_runner=lambda fixture_id, **_: seen.append(fixture_id) or published(fixture_id),
            stdout=stdout,
        )

        self.assertEqual(code, 0)
        self.assertEqual(seen, [3956996, 3956998])
        summary = json.loads(stdout.getvalue())
        self.assertEqual(summary["scope"], "in_play")
        self.assertEqual(summary["fixtures_published"], 2)
        self.assertEqual(summary["predictions_generated"], 8)
        self.assertEqual(summary["suppressed_by_reason"], {})

    def test_the_in_play_scope_never_reaches_the_pre_match_runner(self) -> None:
        # §7.5: the pre-match runner carries the guards that keep post-kickoff
        # rows off pre-match surfaces. If the in-play scope called it, those
        # guards would be doing the wrong job on the wrong data.
        pre_match_calls: list[int] = []

        main(
            ["run-scope", "--scope", "in_play", "--database-url", "postgres://x", "--generated-at", AS_OF],
            connect=lambda _url: candidates(3956996),
            runner=lambda fixture_id, **_: pre_match_calls.append(fixture_id) or [],
            in_play_runner=lambda fixture_id, **_: published(fixture_id),
            stdout=StringIO(),
        )

        self.assertEqual(pre_match_calls, [])

    def test_the_upcoming_scope_never_reaches_the_in_play_runner(self) -> None:
        in_play_calls: list[int] = []

        main(
            ["run-scope", "--scope", "upcoming", "--database-url", "postgres://x", "--generated-at", AS_OF],
            connect=lambda _url: candidates(3956996),
            runner=lambda fixture_id, **_: [],
            in_play_runner=lambda fixture_id, **_: in_play_calls.append(fixture_id) or published(fixture_id),
            stdout=StringIO(),
        )

        self.assertEqual(in_play_calls, [])

    def test_the_upcoming_summary_shape_is_unchanged_by_the_new_branch(self) -> None:
        stdout = StringIO()

        main(
            ["run-scope", "--scope", "upcoming", "--database-url", "postgres://x", "--generated-at", AS_OF],
            connect=lambda _url: candidates(11),
            runner=lambda fixture_id, **_: [],
            stdout=stdout,
        )

        summary = json.loads(stdout.getvalue())
        self.assertEqual(summary["scope"], "upcoming")
        # The in-play-only keys must not have leaked into the pre-match report.
        self.assertNotIn("suppressed_by_reason", summary)
        self.assertNotIn("fixtures_published", summary)

    def test_suppressions_are_reported_by_reason_rather_than_as_silence(self) -> None:
        # §14. "Nothing published" and "nothing ran" produce the same
        # prediction count; only the reason breakdown separates them.
        stdout = StringIO()
        outcomes = {
            1: suppressed(1, SuppressionReason.STALE_DATA.value),
            2: suppressed(2, SuppressionReason.STALE_DATA.value),
            3: suppressed(3, CADENCE_UNCHANGED),
            4: published(4, markets=3),
        }

        main(
            ["run-scope", "--scope", "in_play", "--database-url", "postgres://x", "--generated-at", AS_OF],
            connect=lambda _url: candidates(1, 2, 3, 4),
            in_play_runner=lambda fixture_id, **_: outcomes[fixture_id],
            stdout=stdout,
        )

        summary = json.loads(stdout.getvalue())
        self.assertEqual(summary["fixtures_processed"], 4)
        self.assertEqual(summary["fixtures_published"], 1)
        self.assertEqual(summary["fixtures_suppressed"], 3)
        self.assertEqual(
            summary["suppressed_by_reason"],
            {"STALE_DATA": 2, CADENCE_UNCHANGED: 1},
        )
        self.assertEqual(summary["freshness_limit_seconds"], 90.0)

    def test_every_fixture_gets_an_auditable_outcome_line(self) -> None:
        stdout = StringIO()

        main(
            ["run-scope", "--scope", "in_play", "--database-url", "postgres://x", "--generated-at", AS_OF],
            connect=lambda _url: candidates(3956996),
            in_play_runner=lambda fixture_id, **_: published(fixture_id),
            stdout=stdout,
        )

        outcome = json.loads(stdout.getvalue())["outcomes"][0]
        self.assertEqual(outcome["fixture_id"], 3956996)
        self.assertEqual(outcome["fixture_live_snapshot_id"], 4821)
        self.assertEqual(outcome["match_minute"], 63)
        self.assertEqual(outcome["period"], "SECOND_HALF")
        self.assertEqual(outcome["regeneration_reason"], "goal")
        self.assertEqual(outcome["markets_published"], 4)

    def test_one_failing_fixture_does_not_end_the_scope(self) -> None:
        stdout = StringIO()
        connection = candidates(1, 2)

        def runner(fixture_id: int, **_: object) -> InPlayRunOutcome:
            if fixture_id == 1:
                raise RuntimeError("snapshot partition missing")
            return published(fixture_id)

        code = main(
            ["run-scope", "--scope", "in_play", "--database-url", "postgres://x", "--generated-at", AS_OF],
            connect=lambda _url: connection,
            in_play_runner=runner,
            stdout=stdout,
        )

        summary = json.loads(stdout.getvalue())
        self.assertEqual(code, 0)
        self.assertEqual(summary["fixtures_failed"], 1)
        self.assertEqual(summary["fixtures_published"], 1)
        self.assertTrue(connection.rolled_back)

    def test_the_tier_flag_selects_the_7_6_backstop_interval(self) -> None:
        seen: list[object] = []

        main(
            [
                "run-scope",
                "--scope",
                "in_play",
                "--tier",
                "B",
                "--database-url",
                "postgres://x",
                "--generated-at",
                AS_OF,
            ],
            connect=lambda _url: candidates(1),
            in_play_runner=lambda fixture_id, **kwargs: seen.append(kwargs["tier"]) or published(fixture_id),
            stdout=StringIO(),
        )

        self.assertEqual([tier.value for tier in seen], ["B"])

    def test_the_candidate_query_is_asked_for_the_in_play_scope(self) -> None:
        connection = candidates(1)

        main(
            ["run-scope", "--scope", "inplay", "--database-url", "postgres://x", "--generated-at", AS_OF],
            connect=lambda _url: connection,
            in_play_runner=lambda fixture_id, **_: published(fixture_id),
            stdout=StringIO(),
        )

        sql, params = connection.executed[0]
        self.assertIn("fixture_live_snapshots", sql)
        # The mirrored §7.5 guard, not the pre-match one.
        self.assertIn("f.kickoff_at < %(as_of_timestamp)s", sql)
        self.assertEqual(params["as_of_timestamp"], NOW)

    def test_an_as_of_in_the_past_does_not_read_the_future(self) -> None:
        # A replay must see what was known then. Without the upper bound on
        # captured_at the engine would quietly read later snapshots.
        connection = candidates(1)
        earlier = (NOW - timedelta(hours=2)).isoformat()

        main(
            ["run-scope", "--scope", "in_play", "--database-url", "postgres://x", "--generated-at", earlier],
            connect=lambda _url: connection,
            in_play_runner=lambda fixture_id, **_: published(fixture_id),
            stdout=StringIO(),
        )

        _sql, params = connection.executed[0]
        self.assertEqual(params["as_of_timestamp"], NOW - timedelta(hours=2))


if __name__ == "__main__":
    unittest.main()
