"""Executes the in-play statistics read against a real PostgreSQL database.

The SQL is a CTE plus a GROUP BY with per-column `count()` guards, and every
one of those guards encodes a rule the engine cannot afford to get wrong:

  * both teams of ONE capture, never each team's newest row, because the writer
    stamps a batch with a single captured_at precisely so the two sides are
    comparable;
  * a statistic only when BOTH teams reported it, because half a total reads as
    a quiet match rather than an unreported one and would put the ladder's
    lines around a count that is systematically low.

None of that can be tested with a stub: the bug would be in the shape of the
statement. Set PREDICTION_ENGINE_TEST_DATABASE_URL (or DATABASE_URL) to a
throwaway database. PREDICTION_ENGINE_REQUIRE_DB_TESTS=1 -- as CI sets -- turns
a missing database into a failure instead of a skip.
"""

from __future__ import annotations

from datetime import UTC, datetime
import os
import unittest
import uuid

from prediction_engine.db_repository import _LATEST_INPLAY_STATISTICS_SQL
from prediction_engine.inplay_counts import CORNERS, FOULS, SHOTS


def _database_url() -> str:
    return (
        os.environ.get("PREDICTION_ENGINE_TEST_DATABASE_URL")
        or os.environ.get("DATABASE_URL")
        or ""
    )


def _requires_database() -> bool:
    return os.environ.get("PREDICTION_ENGINE_REQUIRE_DB_TESTS", "").strip().lower() in {
        "1",
        "true",
        "yes",
    }


_SCHEMA_SQL = """
CREATE TABLE fixture_statistics_snapshots (
  fixture_id BIGINT NOT NULL,
  team_id BIGINT NOT NULL,
  captured_at TIMESTAMPTZ NOT NULL,
  match_minute SMALLINT,
  shots_on_target INT,
  shots_total INT,
  corners INT,
  fouls INT,
  yellow_cards INT,
  red_cards INT,
  card_points INT,
  source TEXT NOT NULL DEFAULT 'api-football-live',
  PRIMARY KEY (fixture_id, team_id, captured_at)
);
"""

AS_OF = datetime(2026, 10, 1, 15, 5, tzinfo=UTC)
EARLY = datetime(2026, 10, 1, 15, 0, tzinfo=UTC)
LATE = datetime(2026, 10, 1, 15, 4, tzinfo=UTC)


class InPlayStatisticsSqlTest(unittest.TestCase):
    connection = None
    schema = ""

    @classmethod
    def setUpClass(cls) -> None:
        url = _database_url()
        if not url:
            message = (
                "set PREDICTION_ENGINE_TEST_DATABASE_URL to run the in-play "
                "statistics SQL tests against PostgreSQL"
            )
            if _requires_database():
                raise AssertionError(message)
            raise unittest.SkipTest(message)
        import psycopg

        cls.connection = psycopg.connect(url, autocommit=True)
        cls.schema = f"inplay_stats_sql_{uuid.uuid4().hex[:8]}"
        with cls.connection.cursor() as cursor:
            cursor.execute(f'CREATE SCHEMA "{cls.schema}"')
            cursor.execute(f'SET search_path TO "{cls.schema}"')
            cursor.execute(_SCHEMA_SQL)

    @classmethod
    def tearDownClass(cls) -> None:
        if cls.connection is None:
            return
        with cls.connection.cursor() as cursor:
            cursor.execute(f'DROP SCHEMA IF EXISTS "{cls.schema}" CASCADE')
        cls.connection.close()

    def setUp(self) -> None:
        with self.connection.cursor() as cursor:
            cursor.execute(f'SET search_path TO "{self.schema}"')
            cursor.execute("TRUNCATE fixture_statistics_snapshots")

    def insert(self, **row) -> None:
        columns = ", ".join(row)
        placeholders = ", ".join(f"%({name})s" for name in row)
        with self.connection.cursor() as cursor:
            cursor.execute(
                f"INSERT INTO fixture_statistics_snapshots ({columns}) VALUES ({placeholders})",
                row,
            )

    def read(self, fixture_id: int = 4411) -> dict | None:
        with self.connection.cursor(row_factory=_dict_row()) as cursor:
            cursor.execute(
                _LATEST_INPLAY_STATISTICS_SQL,
                {"fixture_id": fixture_id, "as_of_timestamp": AS_OF},
            )
            return cursor.fetchone()

    def test_it_reads_one_capture_and_sums_both_teams(self):
        self.insert(
            fixture_id=4411, team_id=10, captured_at=LATE, corners=6, fouls=9, shots_total=14
        )
        self.insert(
            fixture_id=4411, team_id=11, captured_at=LATE, corners=5, fouls=12, shots_total=11
        )
        row = self.read()
        self.assertEqual(row["team_rows"], 2)
        self.assertEqual(row["corners"], 11)
        self.assertEqual(row["fouls"], 21)
        self.assertEqual(row["shots_total"], 25)

    def test_an_earlier_capture_is_not_mixed_into_the_newest_one(self):
        """The failure this guards is subtle and would never raise.

        Taking each team's newest row independently would read home corners at
        the later capture and away corners at the earlier one, and present the
        sum as one moment. Here the away side is deliberately missing from the
        late capture.
        """
        self.insert(fixture_id=4411, team_id=10, captured_at=EARLY, corners=4)
        self.insert(fixture_id=4411, team_id=11, captured_at=EARLY, corners=3)
        self.insert(fixture_id=4411, team_id=10, captured_at=LATE, corners=6)
        row = self.read()
        self.assertEqual(row["captured_at"], LATE)
        # One team only in the newest capture: the reader declines the whole
        # capture rather than halving the total.
        self.assertEqual(row["team_rows"], 1)

    def test_a_statistic_only_one_team_reported_is_not_a_total(self):
        self.insert(fixture_id=4411, team_id=10, captured_at=LATE, corners=6, fouls=9)
        self.insert(fixture_id=4411, team_id=11, captured_at=LATE, corners=5, fouls=None)
        row = self.read()
        self.assertEqual(row["team_rows"], 2)
        self.assertEqual(row["corners_rows"], 2)
        self.assertEqual(row["fouls_rows"], 1)

    def test_a_capture_after_the_cutoff_is_invisible(self):
        """Replay has to see what was known then, not what is known now."""
        future = datetime(2026, 10, 1, 15, 30, tzinfo=UTC)
        self.insert(fixture_id=4411, team_id=10, captured_at=future, corners=9)
        self.insert(fixture_id=4411, team_id=11, captured_at=future, corners=8)
        self.assertIsNone(self.read())

    def test_another_fixture_is_not_read(self):
        self.insert(fixture_id=9999, team_id=10, captured_at=LATE, corners=6)
        self.insert(fixture_id=9999, team_id=11, captured_at=LATE, corners=5)
        self.assertIsNone(self.read())


def _dict_row():
    from psycopg.rows import dict_row

    return dict_row


class RepositoryMappingTest(unittest.TestCase):
    """The loader's own rules, over a stubbed row rather than a database.

    Separate from the SQL cases so these still run without PostgreSQL: which
    statistics become observations, and that cards never do.
    """

    def observations(self, row: dict):
        from prediction_engine.db_repository import PredictionInputRepository

        class _Cursor:
            def __init__(self, result):
                self._result = result

            def fetchone(self):
                return self._result

        class _Connection:
            def __init__(self, result):
                self._result = result

            def execute(self, *_args, **_kwargs):
                return _Cursor(self._result)

        repository = PredictionInputRepository.__new__(PredictionInputRepository)
        repository._connection = _Connection(row)
        return repository.load_in_play_count_observations(4411, as_of_timestamp=AS_OF)

    def test_only_statistics_both_teams_reported_become_observations(self):
        row = {
            "captured_at": LATE,
            "team_rows": 2,
            "corners": 11,
            "corners_rows": 2,
            "fouls": 9,
            "fouls_rows": 1,
            "shots_total": 25,
            "shots_total_rows": 2,
            "shots_on_target": None,
            "shots_on_target_rows": 0,
            "card_points": 7,
            "card_points_rows": 2,
            "card_rows": 4,
        }
        statistics = {observation.statistic for observation in self.observations(row)}
        self.assertEqual(statistics, {CORNERS, SHOTS})
        self.assertNotIn(FOULS, statistics)

    def test_cards_are_never_returned_here(self):
        """One market, one source.

        The table carries cards, but the live event list carries them a minute
        sooner and the model reads them off the features. Returning them here
        as well would give one market two sources with no rule about which wins.
        """
        row = {
            "captured_at": LATE,
            "team_rows": 2,
            "corners": None,
            "corners_rows": 0,
            "fouls": None,
            "fouls_rows": 0,
            "shots_total": None,
            "shots_total_rows": 0,
            "shots_on_target": None,
            "shots_on_target_rows": 0,
            "card_points": 7,
            "card_points_rows": 2,
            "card_rows": 4,
        }
        self.assertEqual(self.observations(row), ())

    def test_a_one_sided_capture_is_declined_entirely(self):
        row = {
            "captured_at": LATE,
            "team_rows": 1,
            "corners": 6,
            "corners_rows": 1,
            "fouls": None,
            "fouls_rows": 0,
            "shots_total": None,
            "shots_total_rows": 0,
            "shots_on_target": None,
            "shots_on_target_rows": 0,
            "card_points": None,
            "card_points_rows": 0,
            "card_rows": 0,
        }
        self.assertEqual(self.observations(row), ())

    def test_no_capture_at_all_is_empty_not_an_error(self):
        self.assertEqual(self.observations(None), ())


if __name__ == "__main__":
    unittest.main()
