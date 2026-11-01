"""Executes the upcoming-candidate SQL against a real PostgreSQL database.

The bug this file exists for could not be caught by asserting on SQL text. A
fixture disappeared from the candidate list because of how one NOT EXISTS
subquery behaved against real rows, and the only way to prove behaviour is to
run the statement Postgres will run. `interval`, `COALESCE` and the correlated
subqueries rule out SQLite as a stand-in.

Set PREDICTION_ENGINE_TEST_DATABASE_URL (or DATABASE_URL) to a throwaway
database. Set PREDICTION_ENGINE_REQUIRE_DB_TESTS=1 -- as CI does -- to turn a
missing database into a failure instead of a skip, so this never quietly stops
running.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
import os
import unittest
import uuid

from prediction_engine.db_repository import PredictionInputRepository


AS_OF = datetime(2026, 9, 20, 6, 0, tzinfo=UTC)

RESOLVED = "resolved"
BLOCKS = "blocks"


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
CREATE TABLE fixtures (
  id BIGINT PRIMARY KEY,
  league_id BIGINT NOT NULL,
  season_id INT NOT NULL,
  home_team_id BIGINT NOT NULL,
  away_team_id BIGINT NOT NULL,
  kickoff_at TIMESTAMPTZ NOT NULL,
  status TEXT,
  home_goals_ft INT,
  away_goals_ft INT,
  updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE engine_predictions (
  id BIGSERIAL PRIMARY KEY,
  fixture_id BIGINT NOT NULL,
  generated_at TIMESTAMPTZ NOT NULL,
  superseded_at TIMESTAMPTZ
);
"""


class UpcomingCandidateEligibilityTest(unittest.TestCase):
    """Every case is a fixture production actually lost, or must keep losing."""

    connection = None
    schema = ""

    @classmethod
    def setUpClass(cls) -> None:
        url = _database_url()
        if not url:
            message = (
                "set PREDICTION_ENGINE_TEST_DATABASE_URL to run the candidate "
                "eligibility tests against PostgreSQL"
            )
            if _requires_database():
                raise AssertionError(
                    message + " (PREDICTION_ENGINE_REQUIRE_DB_TESTS is set, so a skip is a failure)"
                )
            raise unittest.SkipTest(message)
        try:
            import psycopg
            from psycopg.rows import dict_row
        except ImportError as error:  # pragma: no cover - dependency guard
            if _requires_database():
                raise AssertionError(f"psycopg is required for these tests: {error}") from error
            raise unittest.SkipTest(f"psycopg unavailable: {error}") from error

        cls.connection = psycopg.connect(url, row_factory=dict_row)
        cls.schema = "prediction_candidate_test_" + uuid.uuid4().hex[:12]
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
        self.connection.execute("TRUNCATE fixtures, engine_predictions")

    def _insert(
        self,
        fixture_id: int,
        *,
        home: int,
        away: int,
        kickoff: datetime,
        status: str = "upcoming",
        home_goals: int | None = None,
        away_goals: int | None = None,
    ) -> None:
        self.connection.execute(
            """
            INSERT INTO fixtures (
              id, league_id, season_id, home_team_id, away_team_id,
              kickoff_at, status, home_goals_ft, away_goals_ft, updated_at
            ) VALUES (
              %(id)s, 39, 2026, %(home)s, %(away)s,
              %(kickoff)s, %(status)s, %(home_goals)s, %(away_goals)s, %(updated_at)s
            )
            """,
            {
                "id": fixture_id,
                "home": home,
                "away": away,
                "kickoff": kickoff,
                "status": status,
                "home_goals": home_goals,
                "away_goals": away_goals,
                "updated_at": AS_OF - timedelta(days=1),
            },
        )

    def _candidates(self) -> set[int]:
        return set(
            PredictionInputRepository(self.connection).list_candidate_fixture_ids(
                scope="upcoming",
                limit=500,
                as_of_timestamp=AS_OF,
            )
        )

    def test_a_played_match_that_never_reported_a_result_stops_blocking_its_teams(self) -> None:
        """Manchester City vs Sunderland, 2026-09-20.

        Sunderland vs AZ Alkmaar kicked off four days earlier and its status
        was still `unknown`. Under the old clause any non-terminal status in
        the preceding fortnight suppressed both teams, so City vs Sunderland
        was never even offered to the engine -- it did not appear in
        `skipped_fixture_ids` either, because nothing ever looked at it. On
        production this single mechanism hid 1,931 of the 2,098 suppressed
        fixtures in the seven-day horizon.
        """
        self._insert(1, home=2, away=3, kickoff=AS_OF - timedelta(days=4), status="unknown")
        self._insert(100, home=1, away=2, kickoff=AS_OF + timedelta(hours=7), status="unknown")

        self.assertIn(100, self._candidates())

    def test_a_stale_upcoming_status_on_a_played_match_stops_blocking(self) -> None:
        """Brighton vs Arsenal, 2026-09-19.

        Same mechanism, different stale value: Manchester United vs Brighton
        had been played but its status never advanced past `upcoming`. Status
        `upcoming` in the past was the single largest blocker in production,
        accounting for 1,319 suppressed fixtures.
        """
        self._insert(2, home=6, away=4, kickoff=AS_OF - timedelta(days=4), status="upcoming")
        self._insert(200, home=4, away=5, kickoff=AS_OF + timedelta(days=2))

        self.assertIn(200, self._candidates())

    def test_a_fixture_that_has_not_kicked_off_cannot_block_a_later_one(self) -> None:
        """A team's second match in the horizon must still be predicted.

        A future fixture is not "awaiting its result"; it simply has not
        happened. The old clause matched it anyway, so for any team playing
        twice inside the seven-day horizon only the first match was ever
        predicted -- 522 fixtures in production.
        """
        self._insert(3, home=7, away=18, kickoff=AS_OF + timedelta(days=2))
        self._insert(300, home=7, away=8, kickoff=AS_OF + timedelta(days=5))

        self.assertIn(300, self._candidates())

    def test_a_match_in_progress_still_blocks_its_teams(self) -> None:
        """The guard the clause exists for must survive the fix.

        A match that kicked off two hours ago and has no result yet is
        genuinely in flight: its outcome is about to change both teams' form,
        so predicting their next fixture now would publish a number that is
        already stale.
        """
        self._insert(4, home=9, away=11, kickoff=AS_OF - timedelta(hours=2), status="live")
        self._insert(400, home=9, away=10, kickoff=AS_OF + timedelta(hours=3))

        self.assertNotIn(400, self._candidates())

    def test_awarded_and_walkover_count_as_resolved(self) -> None:
        """Decided without being played is still decided.

        matchstate lists both as terminal; the clause did not, so a technical
        loss suppressed the teams for a fortnight.
        """
        self._insert(5, home=12, away=14, kickoff=AS_OF - timedelta(hours=3), status="awarded")
        self._insert(500, home=12, away=13, kickoff=AS_OF + timedelta(hours=3))
        self._insert(6, home=15, away=17, kickoff=AS_OF - timedelta(hours=3), status="walkover")
        self._insert(600, home=15, away=16, kickoff=AS_OF + timedelta(hours=3))

        candidates = self._candidates()
        self.assertIn(500, candidates)
        self.assertIn(600, candidates)

    def test_a_recorded_score_resolves_a_fixture_whatever_its_status_says(self) -> None:
        """A row with a full-time score is resolved even if it still claims to
        be in the second half. The status text is the unreliable field; the
        score is not."""
        self._insert(
            7,
            home=20,
            away=22,
            kickoff=AS_OF - timedelta(hours=2),
            status="second_half",
            home_goals=2,
            away_goals=1,
        )
        self._insert(700, home=20, away=21, kickoff=AS_OF + timedelta(hours=3))

        self.assertIn(700, self._candidates())

    def test_an_unrecognised_provider_status_is_still_predictable(self) -> None:
        """`unknown` means two different things and the clause must not
        conflate them.

        matchstate.Normalize falls back to `unknown` for any provider label it
        does not recognise, so an ordinary fixture that has not kicked off can
        carry it -- Manchester City vs Sunderland did. It is terminal for a
        match that has already been played, never a reason to refuse to
        predict one that has not.
        """
        self._insert(800, home=30, away=31, kickoff=AS_OF + timedelta(days=1), status="unknown")

        self.assertIn(800, self._candidates())


if __name__ == "__main__":
    unittest.main()
