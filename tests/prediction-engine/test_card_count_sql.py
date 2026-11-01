"""Executes the engine's card-count SQL against a real PostgreSQL database.

fixture_statistics.card_points was written in two scales: the football-data
importer stored yellow + 2*red, the live statistics job yellow*10 + red*25.
The engine forecast and settled total_cards lines of 2.5-5.5 on that column,
so one yellow on a live-ingested fixture settled "over 5.5" as won. The engine
now derives the card count from yellow_cards and red_cards in SQL; these tests
run that SQL on rows written in both old scales and prove the count ignores
the stored column.

Set PREDICTION_ENGINE_TEST_DATABASE_URL (or DATABASE_URL) to a throwaway
database. PREDICTION_ENGINE_REQUIRE_DB_TESTS=1 -- as CI sets -- turns a missing
database into a failure instead of a skip.
"""

from __future__ import annotations

from datetime import UTC, datetime
import os
import unittest
import uuid

from prediction_engine.db_repository import _FIXTURE_STATISTICS_SQL, _SETTLEMENT_STATISTICS_SQL
from prediction_engine.settlement import ActualFixtureResult, settle_prediction


def _database_url() -> str:
    return (
        os.environ.get("PREDICTION_ENGINE_TEST_DATABASE_URL")
        or os.environ.get("DATABASE_URL")
        or ""
    )


def _requires_database() -> bool:
    return os.environ.get("PREDICTION_ENGINE_REQUIRE_DB_TESTS", "").strip().lower() in {"1", "true", "yes"}


_SCHEMA_SQL = """
CREATE TABLE fixture_statistics (
  fixture_id BIGINT NOT NULL,
  team_id BIGINT NOT NULL,
  shots_on_target INT,
  shots_total INT,
  corners INT,
  fouls INT,
  yellow_cards INT,
  red_cards INT,
  card_points INT,
  possession_pct NUMERIC(5,2),
  xg NUMERIC(6,3),
  updated_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (fixture_id, team_id)
);
"""

AS_OF = datetime(2026, 9, 24, 12, 0, tzinfo=UTC)
WRITTEN = datetime(2026, 9, 20, 12, 0, tzinfo=UTC)


class CardCountSqlTest(unittest.TestCase):
    connection = None
    schema = ""

    @classmethod
    def setUpClass(cls) -> None:
        url = _database_url()
        if not url:
            message = "set PREDICTION_ENGINE_TEST_DATABASE_URL to run the card-count SQL tests against PostgreSQL"
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
        cls.schema = "prediction_card_count_test_" + uuid.uuid4().hex[:12]
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
        self.connection.execute(f'SET search_path TO "{self.schema}"')
        self.connection.execute("TRUNCATE fixture_statistics")

    def _row(self, fixture_id: int, team_id: int, *, yellow, red, card_points, fouls=10, shots=12, corners=5, updated_at=WRITTEN) -> None:
        self.connection.execute(
            """
            INSERT INTO fixture_statistics (fixture_id, team_id, yellow_cards, red_cards, card_points, fouls, shots_total, corners, updated_at)
            VALUES (%(fixture_id)s, %(team_id)s, %(yellow)s, %(red)s, %(card_points)s, %(fouls)s, %(shots)s, %(corners)s, %(updated_at)s)
            """,
            {
                "fixture_id": fixture_id, "team_id": team_id, "yellow": yellow, "red": red,
                "card_points": card_points, "fouls": fouls, "shots": shots, "corners": corners,
                "updated_at": updated_at,
            },
        )

    def _history_counts(self, fixture_ids: list[int]) -> dict[tuple[int, int], int | None]:
        rows = self.connection.execute(
            _FIXTURE_STATISTICS_SQL,
            {"fixture_ids": fixture_ids, "as_of_timestamp": AS_OF},
        ).fetchall()
        return {(int(row["fixture_id"]), int(row["team_id"])): row["card_points"] for row in rows}

    def test_both_old_scales_read_as_the_same_card_count(self) -> None:
        # Two yellows and a red for team 1 in each fixture: the importer
        # stored 4, the live job 45. Both are three cards.
        self._row(1, 1, yellow=2, red=1, card_points=4)
        self._row(2, 1, yellow=2, red=1, card_points=45)
        counts = self._history_counts([1, 2])
        self.assertEqual(counts[(1, 1)], 3)
        self.assertEqual(counts[(2, 1)], 3)

    def test_nulls_on_a_captured_row_are_a_clean_match(self) -> None:
        self._row(3, 1, yellow=None, red=None, card_points=None, fouls=9)
        self.assertEqual(self._history_counts([3])[(3, 1)], 0)

    def test_a_row_that_captured_nothing_stays_unknown(self) -> None:
        self._row(4, 1, yellow=None, red=None, card_points=None, fouls=None, shots=None, corners=None)
        self.assertIsNone(self._history_counts([4])[(4, 1)])

    def test_a_null_red_alongside_yellows_counts_as_zero_reds(self) -> None:
        self._row(5, 1, yellow=3, red=None, card_points=30)
        self.assertEqual(self._history_counts([5])[(5, 1)], 3)

    def test_the_temporal_guard_still_applies(self) -> None:
        self._row(6, 1, yellow=1, red=0, card_points=10, updated_at=datetime(2026, 9, 25, tzinfo=UTC))
        self.assertEqual(self._history_counts([6]), {})

    def test_one_yellow_no_longer_settles_over_five_and_a_half(self) -> None:
        # The live job wrote 10 points for a single yellow; the settlement
        # read that as ten cards and graded "over 5.5" a win.
        self._row(7, 1, yellow=1, red=0, card_points=10)
        self._row(7, 2, yellow=None, red=None, card_points=None, fouls=8)
        rows = self.connection.execute(
            _SETTLEMENT_STATISTICS_SQL,
            {"fixture_id": 7, "home_team_id": 1, "away_team_id": 2},
        ).fetchall()
        cards = {int(row["team_id"]): row["card_points"] for row in rows}
        self.assertEqual(cards, {1: 1, 2: 0})

        actual = ActualFixtureResult(home_goals=1, away_goals=0, home_card_points=cards[1], away_card_points=cards[2])
        self.assertFalse(settle_prediction("total_cards", "over_5.5", actual).is_correct)
        self.assertTrue(settle_prediction("total_cards", "under_2.5", actual).is_correct)


if __name__ == "__main__":
    unittest.main()
