"""The goal timeline query against real PostgreSQL.

The fake connection cannot tell whether a retracted goal or a missed penalty
reaches first-goal grading. Only the statement itself can. Runs against a
throwaway schema when PREDICTION_ENGINE_TEST_DATABASE_URL (or DATABASE_URL) is
set, and skips otherwise.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
import os
import unittest
import uuid

from prediction_engine.db_repository import PredictionSettlementRepository, _SETTLEMENT_GOAL_TIMELINE_SQL
from prediction_engine.settlement import ActualFixtureResult

KICKOFF = datetime(2026, 11, 4, 15, 0, tzinfo=UTC)

_SCHEMA_SQL = """
CREATE TABLE fixture_events (
  id BIGSERIAL PRIMARY KEY,
  fixture_id BIGINT NOT NULL,
  observed_at TIMESTAMPTZ NOT NULL,
  elapsed_minute SMALLINT NOT NULL,
  elapsed_extra SMALLINT,
  event_type TEXT NOT NULL,
  event_detail TEXT NOT NULL,
  team_id BIGINT,
  identity_key TEXT NOT NULL,
  is_retracted BOOLEAN NOT NULL DEFAULT false
);
"""


def _database_url() -> str:
    return os.environ.get("PREDICTION_ENGINE_TEST_DATABASE_URL") or os.environ.get("DATABASE_URL") or ""


def _requires_database() -> bool:
    return os.environ.get("PREDICTION_ENGINE_REQUIRE_DB_TESTS", "").strip().lower() in {"1", "true", "yes"}


class FirstGoalSqlTest(unittest.TestCase):
    connection = None
    schema = ""

    @classmethod
    def setUpClass(cls) -> None:
        url = _database_url()
        if not url:
            message = "set PREDICTION_ENGINE_TEST_DATABASE_URL to run the first-goal SQL tests against PostgreSQL"
            if _requires_database():
                raise AssertionError(message + " (PREDICTION_ENGINE_REQUIRE_DB_TESTS is set, so a skip is a failure)")
            raise unittest.SkipTest(message)
        try:
            import psycopg
            from psycopg.rows import dict_row
        except ImportError as error:  # pragma: no cover - dependency guard
            if _requires_database():
                raise AssertionError(f"psycopg is required for these tests: {error}") from error
            raise unittest.SkipTest(f"psycopg unavailable: {error}") from error
        cls.connection = psycopg.connect(url, row_factory=dict_row)
        cls.schema = "prediction_first_goal_test_" + uuid.uuid4().hex[:12]
        cls.connection.execute(f'CREATE SCHEMA "{cls.schema}"')
        cls.connection.execute(f'SET search_path TO "{cls.schema}"')
        cls.connection.execute(_SCHEMA_SQL)
        cls.connection.commit()

    @classmethod
    def tearDownClass(cls) -> None:
        if cls.connection is None:
            return
        cls.connection.rollback()
        cls.connection.execute(f'DROP SCHEMA IF EXISTS "{cls.schema}" CASCADE')
        cls.connection.commit()
        cls.connection.close()

    def setUp(self) -> None:
        self.connection.execute("TRUNCATE fixture_events")

    def _event(self, key: str, team: int | None, minute: int, *, detail: str = "Normal Goal", event_type: str = "Goal", retracted: bool = False) -> None:
        self.connection.execute(
            """
            INSERT INTO fixture_events
              (fixture_id, observed_at, elapsed_minute, event_type, event_detail, team_id, identity_key, is_retracted)
            VALUES (42, %(observed_at)s, %(minute)s, %(event_type)s, %(detail)s, %(team)s, %(key)s, %(retracted)s)
            """,
            {
                "observed_at": KICKOFF + timedelta(minutes=minute),
                "minute": minute,
                "event_type": event_type,
                "detail": detail,
                "team": team,
                "key": key,
                "retracted": retracted,
            },
        )

    def _first_goal_side(self, home_goals: int, away_goals: int) -> str | None:
        repository = PredictionSettlementRepository(self.connection)
        rows = [{"market": "team_to_score_first", "lifecycle_stage": "PRE_MATCH"}]
        evidence = repository._load_goal_evidence(
            42,
            {"home_team_id": 10, "away_team_id": 20},
            ActualFixtureResult(home_goals=home_goals, away_goals=away_goals),
            rows,
        )
        return evidence.actual.first_goal_team

    def test_the_query_returns_the_clock_columns_it_grades_on(self) -> None:
        self._event("h", 10, 30)

        row = self.connection.execute(_SETTLEMENT_GOAL_TIMELINE_SQL, {"fixture_id": 42}).fetchone()

        self.assertEqual(row["elapsed_minute"], 30)
        self.assertIsNone(row["elapsed_extra"])

    def test_a_retracted_opener_is_not_the_first_goal(self) -> None:
        self._event("disallowed", 20, 5, retracted=True)
        self._event("h", 10, 30)

        self.assertEqual(self._first_goal_side(1, 0), "home")

    def test_a_missed_penalty_is_not_the_first_goal(self) -> None:
        self._event("miss", 20, 5, detail="Missed Penalty")
        self._event("h", 10, 30)

        self.assertEqual(self._first_goal_side(1, 0), "home")

    def test_a_card_is_not_the_first_goal(self) -> None:
        self._event("card", 20, 5, detail="Yellow Card", event_type="Card")
        self._event("h", 10, 30)

        self.assertEqual(self._first_goal_side(1, 0), "home")

    def test_an_own_goal_counts_for_the_side_the_event_credits(self) -> None:
        # Same attribution ReconcileScoringEvents applies: the per-team count
        # has to equal the official score, so a side credited wrongly fails to
        # reconcile instead of naming a first scorer.
        self._event("og", 20, 5, detail="Own Goal")
        self._event("h", 10, 30)

        self.assertEqual(self._first_goal_side(1, 1), "away")
        self.assertIsNone(self._first_goal_side(2, 0))


if __name__ == "__main__":
    unittest.main()
