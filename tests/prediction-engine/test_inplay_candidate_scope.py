"""The §7.5 guard pair: the in-play branch is parallel, not a replacement.

The failure this guards against is silent. If the pre-match guard
`f.kickoff_at >= %(as_of_timestamp)s` were relaxed to let in-play fixtures
through the same query, post-kickoff predictions would appear on pre-match
surfaces and every row involved would still look valid. So the assertions here
are as much about what the upcoming query still says as about what the new one
says.
"""

from __future__ import annotations

from datetime import UTC, datetime
import unittest

from prediction_engine import db_repository as db_repository_module
from prediction_engine.db_repository import IN_PLAY_SCOPES, PredictionInputRepository
from prediction_engine.inplay_features import STALE_AFTER_SECONDS


AS_OF = datetime(2026, 9, 21, 7, 45, 33, tzinfo=UTC)


class FakeCursor:
    def __init__(self, many: list[dict[str, object]]) -> None:
        self._many = many

    def fetchall(self) -> list[dict[str, object]]:
        return self._many

    def fetchone(self) -> dict[str, object] | None:
        return self._many[0] if self._many else None


class FakeConnection:
    def __init__(self, results: list[FakeCursor] | None = None) -> None:
        self._results = results or []
        self.executed: list[tuple[str, dict[str, object]]] = []

    def execute(self, sql: str, params: dict[str, object]) -> FakeCursor:
        self.executed.append((sql, params))
        return self._results.pop(0) if self._results else FakeCursor([])

    def commit(self) -> None:  # pragma: no cover - not exercised here
        return None

    def rollback(self) -> None:  # pragma: no cover - not exercised here
        return None


class InPlayCandidateScopeTests(unittest.TestCase):
    def test_the_pre_match_guard_is_still_in_the_upcoming_query(self) -> None:
        self.assertIn(
            "f.kickoff_at >= %(as_of_timestamp)s",
            db_repository_module._UPCOMING_FIXTURE_IDS_SQL,
        )

    def test_the_in_play_branch_mirrors_the_guard_instead_of_dropping_it(self) -> None:
        sql = db_repository_module._IN_PLAY_FIXTURE_IDS_SQL

        self.assertIn("f.kickoff_at < %(as_of_timestamp)s", sql)
        self.assertNotIn("f.kickoff_at >= %(as_of_timestamp)s\n", sql)

    def test_in_play_scope_selects_the_in_play_query(self) -> None:
        connection = FakeConnection(results=[FakeCursor([{"id": 3981689}])])

        fixture_ids = PredictionInputRepository(connection).list_candidate_fixture_ids(
            scope="in-play", limit=50, as_of_timestamp=AS_OF
        )

        sql, params = connection.executed[0]
        self.assertEqual(fixture_ids, [3981689])
        self.assertIn("fixture_live_snapshots", sql)
        self.assertEqual(params["as_of_timestamp"], AS_OF)

    def test_every_in_play_alias_reaches_the_same_query(self) -> None:
        for alias in ("in_play", "inplay", "IN-PLAY", " In_Play "):
            connection = FakeConnection(results=[FakeCursor([])])
            PredictionInputRepository(connection).list_candidate_fixture_ids(
                scope=alias, limit=1, as_of_timestamp=AS_OF
            )
            self.assertEqual(
                connection.executed[0][0],
                db_repository_module._IN_PLAY_FIXTURE_IDS_SQL,
                alias,
            )

    def test_live_is_still_an_unsupported_scope(self) -> None:
        # "live" deliberately stays unrecognised: it is the ambiguous word the
        # rest of the codebase uses for ingestion polling, not for prediction
        # candidacy, and an existing test relies on it being rejected.
        self.assertNotIn("live", IN_PLAY_SCOPES)
        with self.assertRaisesRegex(ValueError, "unsupported fixture scope"):
            PredictionInputRepository(FakeConnection()).list_candidate_fixture_ids(
                scope="live", limit=10, as_of_timestamp=AS_OF
            )

    def test_the_in_play_query_requires_a_recent_snapshot(self) -> None:
        sql = db_repository_module._IN_PLAY_FIXTURE_IDS_SQL

        self.assertIn("snapshot.captured_at", sql)
        self.assertIn(
            f"interval '{db_repository_module.IN_PLAY_SNAPSHOT_FRESHNESS_SECONDS} seconds'",
            sql,
        )

    def test_the_freshness_window_matches_the_staleness_guard(self) -> None:
        self.assertEqual(
            float(db_repository_module.IN_PLAY_SNAPSHOT_FRESHNESS_SECONDS),
            STALE_AFTER_SECONDS,
        )

    def test_the_in_play_query_is_bounded_by_the_reaper_hard_bound(self) -> None:
        self.assertEqual(db_repository_module.IN_PLAY_HARD_BOUND_MINUTES, 210)
        self.assertIn(
            "interval '210 minutes'",
            db_repository_module._IN_PLAY_FIXTURE_IDS_SQL,
        )

    def test_terminal_fixtures_are_excluded(self) -> None:
        self.assertIn(
            db_repository_module.NON_PREDICTABLE_FIXTURE_STATUSES,
            db_repository_module._IN_PLAY_FIXTURE_IDS_SQL,
        )


if __name__ == "__main__":
    unittest.main()
