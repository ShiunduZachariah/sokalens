"""The shadow challenger path against the real, migrated schema.

Forecast, settle and report run end to end on rows written to the tables
migration 000090 and the governance migrations create, so every SQL statement
in `prediction_engine.challengers` is executed against the constraints and
triggers production has. Everything runs in one transaction that is rolled
back, and the dates sit years ahead so other tests' rows cannot be targets.

Set PREDICTION_ENGINE_TEST_DATABASE_URL (or DATABASE_URL) to a migrated
throwaway database. PREDICTION_ENGINE_REQUIRE_DB_TESTS=1 -- as CI sets --
turns a missing database into a failure instead of a skip.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
import os
import unittest

from prediction_engine import challengers
from prediction_engine.db_repository import PredictionInputRepository
from prediction_engine.model_a import ModelAInputs, run_model_a
from prediction_engine.model_c import load_model
from prediction_engine.pipeline import champion_match_result


AS_OF = datetime(2031, 3, 1, 12, 0, tzinfo=UTC)
BASE_ID = 9_310_000_000
TEAMS = 6


def _database_url() -> str:
    return os.environ.get("PREDICTION_ENGINE_TEST_DATABASE_URL") or os.environ.get("DATABASE_URL") or ""


def _requires_database() -> bool:
    return os.environ.get("PREDICTION_ENGINE_REQUIRE_DB_TESTS", "").strip().lower() in {"1", "true", "yes"}


class _Uncommitted:
    """The test's connection, with commit and rollback held back to the end."""

    def __init__(self, connection: object) -> None:
        self._connection = connection

    def execute(self, sql, params=None):
        return self._connection.execute(sql, params)

    def cursor(self, *args, **kwargs):
        return self._connection.cursor(*args, **kwargs)

    def commit(self) -> None:
        pass

    def rollback(self) -> None:
        pass


class ChallengerSqlTest(unittest.TestCase):
    connection = None

    @classmethod
    def setUpClass(cls) -> None:
        url = _database_url()
        if not url:
            message = "set PREDICTION_ENGINE_TEST_DATABASE_URL to run the challenger SQL tests against PostgreSQL"
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
        if cls.connection.execute("SELECT to_regclass('challenger_predictions') AS name").fetchone()["name"] is None:
            cls.connection.close()
            message = "the database is not migrated through 000090_challenger_predictions"
            if _requires_database():
                raise AssertionError(message)
            raise unittest.SkipTest(message)

    @classmethod
    def tearDownClass(cls) -> None:
        if cls.connection is not None and not cls.connection.closed:
            cls.connection.close()

    def setUp(self) -> None:
        self.connection.rollback()
        self.db = _Uncommitted(self.connection)
        self.repository = challengers.ChallengerRepository(self.db)
        self.model = load_model()
        self._seed()

    def tearDown(self) -> None:
        self.connection.rollback()

    def _execute(self, sql: str, params: dict | None = None):
        return self.connection.execute(sql, params or {})

    def _seed(self) -> None:
        sport = self._execute(
            "INSERT INTO sports (slug, name) VALUES ('challenger-test', 'Challenger test') RETURNING id"
        ).fetchone()["id"]
        self.league = self._execute(
            "INSERT INTO leagues (sport_id, slug, name) VALUES (%(sport)s, 'challenger-test-league', 'Testland Premier') RETURNING id",
            {"sport": sport},
        ).fetchone()["id"]
        self.teams = [
            self._execute(
                "INSERT INTO teams (slug, name) VALUES (%(slug)s, %(name)s) RETURNING id",
                {"slug": f"challenger-test-team-{index}", "name": f"Challenger Team {index}"},
            ).fetchone()["id"]
            for index in range(TEAMS)
        ]
        # Two seasons of double round robins: lower index always wins.
        self.next_id = BASE_ID
        kickoff = AS_OF - timedelta(days=400)
        for season in (2029, 2030):
            for home in range(TEAMS):
                for away in range(TEAMS):
                    if home == away:
                        continue
                    goals = (2, 0) if home < away else (0, 1)
                    self._fixture(home, away, kickoff, season=season, goals=goals)
                    kickoff += timedelta(days=3)
        self.upcoming = self._fixture(0, TEAMS - 1, AS_OF + timedelta(days=2), season=2030, goals=None)
        self._execute(
            """
            INSERT INTO engine_predictions (
              fixture_id, market, prediction, probability, confidence, confidence_tier,
              lifecycle_stage, data_completeness, gate_tier, model_version, content_hash, generated_at
            )
            VALUES (%(fixture)s, '1x2', 'home', 0.6, 0.5, 'medium', 'EARLY', 0.8, 'FULL',
                    'python-engine-v1', 'challenger-test', %(as_of)s)
            """,
            {"fixture": self.upcoming, "as_of": AS_OF - timedelta(hours=1)},
        )

    def _fixture(self, home: int, away: int, kickoff: datetime, *, season: int, goals) -> int:
        fixture_id = self.next_id
        self.next_id += 1
        params = {
            "id": fixture_id, "league": self.league, "home": self.teams[home], "away": self.teams[away],
            "kickoff": kickoff, "season": season,
            "home_goals": goals[0] if goals else None, "away_goals": goals[1] if goals else None,
            "status": "FT" if goals else "NS",
        }
        self._execute(
            """
            INSERT INTO matches (id, league_id, home_team_id, away_team_id, kickoff_at)
            VALUES (%(id)s, %(league)s, %(home)s, %(away)s, %(kickoff)s)
            """,
            params,
        )
        self._execute(
            """
            INSERT INTO fixtures (id, league_id, season_id, home_team_id, away_team_id, kickoff_at, status,
                                  home_goals_ft, away_goals_ft)
            VALUES (%(id)s, %(league)s, %(season)s, %(home)s, %(away)s, %(kickoff)s, %(status)s,
                    %(home_goals)s, %(away_goals)s)
            """,
            params,
        )
        return fixture_id

    def _forecast(self, origin: str, champion=None, as_of: datetime = AS_OF) -> dict:
        return challengers.run_forecasts(
            self.repository,
            as_of=as_of,
            origin=origin,
            limit=5,
            history_days=900,
            min_interval=timedelta(minutes=30),
            champion=champion or (lambda fixture, at: {"home": 0.5, "draw": 0.3, "away": 0.2}),
            model_c=self.model,
        )

    def test_live_forecast_settle_and_report(self) -> None:
        summary = self._forecast("live")
        self.assertEqual(summary["targets"], 1)
        self.assertEqual(summary["challenger_forecasts"], 2)
        registry = self._execute(
            "SELECT model_key, status, market_family FROM model_registry WHERE id = ANY(%(ids)s::uuid[])",
            {"ids": list(summary["model_registry_ids"].values())},
        ).fetchall()
        self.assertEqual({row["model_key"] for row in registry}, {"elo", "model-c-gbdt"})
        self.assertEqual({row["status"] for row in registry}, {"shadow"})

        rows = self._execute(
            "SELECT * FROM challenger_predictions WHERE fixture_id = %(id)s", {"id": self.upcoming}
        ).fetchall()
        self.assertEqual(len(rows), 2)
        for row in rows:
            self.assertEqual(row["lifecycle_stage"], "EARLY")
            self.assertEqual(float(row["champion_home_probability"]), 0.5)
            self.assertGreater(row["home_probability"], row["away_probability"])

        # Throttled straight after, and nothing new once it may run again.
        self.assertEqual(self._forecast("live")["skipped"], "min_interval")
        self.assertEqual(self._forecast("live", as_of=AS_OF + timedelta(hours=1))["targets"], 0)

        self._execute(
            "UPDATE fixtures SET status = 'FT', home_goals_ft = 3, away_goals_ft = 0 WHERE id = %(id)s",
            {"id": self.upcoming},
        )
        settled_at = AS_OF + timedelta(days=3)
        self.assertEqual(challengers.run_settlement(self.repository, settled_at=settled_at, limit=50)["challenger_settled"], 2)
        self.assertEqual(challengers.run_settlement(self.repository, settled_at=settled_at, limit=50)["challenger_settled"], 0)
        settlements = self._execute(
            "SELECT * FROM shadow_settlements WHERE fixture_id = %(id)s", {"id": self.upcoming}
        ).fetchall()
        self.assertEqual({row["final_outcome"] for row in settlements}, {"home"})
        self.assertTrue(all(row["evaluation_eligible"] for row in settlements))
        self.assertEqual({row["settlement_classification"] for row in settlements}, {"correct"})

        report = challengers.run_report(self.repository, now=settled_at, origin="live", minimum_samples=500, write=True)
        self.assertEqual(report["evaluations_written"], 2)
        self.assertEqual({entry["decision"] for entry in report["evaluations"]}, {"extend"})
        evaluations = self._execute(
            "SELECT decision, sample_count FROM champion_challenger_evaluations WHERE window_end = %(end)s",
            {"end": settled_at},
        ).fetchall()
        self.assertEqual([(row["decision"], row["sample_count"]) for row in evaluations], [("extend", 1)] * 2)

    def test_live_evidence_counts_only_from_the_champions_promotion(self) -> None:
        self._forecast("live")
        self._execute(
            "UPDATE fixtures SET status = 'FT', home_goals_ft = 3, away_goals_ft = 0 WHERE id = %(id)s",
            {"id": self.upcoming},
        )
        settled_at = AS_OF + timedelta(days=3)
        challengers.run_settlement(self.repository, settled_at=settled_at, limit=50)
        self.assertEqual(
            challengers.run_report(self.repository, now=settled_at, origin="live", minimum_samples=500, write=False)["settled_rows"],
            2,
        )

        # A new champion took over after these forecasts stored the old one's 1X2.
        self._execute(
            "UPDATE model_registry SET promoted_at = %(later)s WHERE market_family = '1x2' AND status = 'production'",
            {"later": AS_OF + timedelta(days=1)},
        )
        report = challengers.run_report(self.repository, now=settled_at, origin="live", minimum_samples=500, write=False)
        self.assertEqual(report["settled_rows"], 0)

    def _market_forecast(self, origin: str, *, as_of: datetime = AS_OF) -> dict[str, object]:
        from prediction_engine import challenger_markets as markets

        result = run_model_a(ModelAInputs(home_lambda=1.4, away_lambda=1.1))

        def champion(fixture: object, at: datetime) -> dict[str, dict[str, float]]:
            return {
                **markets.goal_document(result.markets, result.score_matrix),
                **markets.count_document("total_corners", 10.0, 0.0),
            }

        return markets.run_market_forecasts(
            markets.MarketRepository(self.db),
            as_of=as_of,
            origin=origin,
            limit=5,
            history_days=900,
            min_interval=timedelta(minutes=30),
            champion=champion,
            models=markets.load_market_models(),
            code_commit="test",
        )

    def test_all_market_forecast_settle_and_report(self) -> None:
        from prediction_engine import challenger_markets as markets

        summary = self._market_forecast("live")
        self.assertEqual(summary["targets"], 1)
        self.assertEqual(summary["market_forecasts"], 2)
        rows = self._execute(
            """
            SELECT mr.model_key, mr.status, mr.market_family, cp.markets
            FROM challenger_market_predictions AS cp
            JOIN model_registry AS mr ON mr.id = cp.model_registry_id
            WHERE cp.fixture_id = %(id)s
            """,
            {"id": self.upcoming},
        ).fetchall()
        by_key = {row["model_key"]: row for row in rows}
        self.assertEqual(set(by_key), {"model-c-markets", "elo-markets", "python-engine-v3"})
        self.assertEqual(by_key["model-c-markets"]["status"], "shadow")
        self.assertEqual(by_key["elo-markets"]["status"], "shadow")
        self.assertEqual(by_key["python-engine-v3"]["status"], "production")
        self.assertIn("total_corners:9.5", by_key["model-c-markets"]["markets"])
        self.assertNotIn("total_corners:9.5", by_key["elo-markets"]["markets"])

        # Throttled straight after, and nothing new once it may run again.
        self.assertEqual(self._market_forecast("live")["skipped"], "min_interval")
        self.assertEqual(self._market_forecast("live", as_of=AS_OF + timedelta(hours=1))["targets"], 0)

        self._execute(
            "UPDATE fixtures SET status = 'FT', home_goals_ft = 3, away_goals_ft = 0, home_goals_ht = 2, away_goals_ht = 0 WHERE id = %(id)s",
            {"id": self.upcoming},
        )
        self._execute(
            """
            INSERT INTO fixture_statistics (fixture_id, team_id, corners) VALUES
              (%(id)s, %(home)s, 7), (%(id)s, %(away)s, 4)
            ON CONFLICT (fixture_id, team_id) DO UPDATE SET corners = EXCLUDED.corners
            """,
            {"id": self.upcoming, "home": self.teams[0], "away": self.teams[TEAMS - 1]},
        )
        repository = markets.MarketRepository(self.db)
        settled_at = AS_OF + timedelta(days=3)
        self.assertEqual(markets.run_market_settlement(repository, settled_at=settled_at, limit=50)["market_settled"], 3)
        self.assertEqual(markets.run_market_settlement(repository, settled_at=settled_at, limit=50)["market_settled"], 0)
        evidence = self._execute(
            "SELECT scored, evaluation_eligible FROM challenger_market_settlements WHERE fixture_id = %(id)s",
            {"id": self.upcoming},
        ).fetchall()
        self.assertTrue(all(row["evaluation_eligible"] for row in evidence))
        self.assertEqual({row["scored"]["1x2"][0] for row in evidence}, {"home"})
        self.assertEqual({row["scored"]["total_goals:2.5"][0] for row in evidence}, {"over_2.5"})
        with self.assertRaises(Exception):
            self.connection.execute("SAVEPOINT immutable")
            self._execute("DELETE FROM challenger_market_settlements WHERE fixture_id = %(id)s", {"id": self.upcoming})
        self.connection.execute("ROLLBACK TO SAVEPOINT immutable")

        report = markets.run_market_report(repository, now=settled_at, origin="live", minimum_samples=500, write=True)
        by_market = {(row["model_key"], row["market"]): row for row in report["market_evaluations"]}
        self.assertEqual(by_market[("model-c-markets", "total_goals:2.5")]["paired_with_champion"], 1)
        self.assertEqual(by_market[("model-c-markets", "total_corners:9.5")]["paired_with_champion"], 1)
        self.assertNotIn(("elo-markets", "total_corners:9.5"), by_market)
        self.assertEqual({row["decision"] for row in report["market_evaluations"]}, {"extend"})
        self.assertGreater(report["market_evaluations_written"], 0)
        family = self._execute(
            """
            SELECT cce.market_family, champion.model_key
            FROM champion_challenger_evaluations AS cce
            JOIN model_registry AS champion ON champion.id = cce.champion_model_id
            WHERE cce.evaluation_contract_version = 'shadow-markets-v1:total_goals:2.5'
            LIMIT 1
            """
        ).fetchone()
        self.assertEqual(family["market_family"], "total_goals")
        self.assertEqual(family["model_key"], "python-engine-v3")

    def test_publishing_reads_the_stored_forecasts_up_to_its_as_of_time(self) -> None:
        self.assertEqual(self._market_forecast("live")["targets"], 1)
        repository = PredictionInputRepository(self.connection)

        before = repository.load_fixture_inputs(self.upcoming, as_of_timestamp=AS_OF - timedelta(minutes=1))
        self.assertIsNone(before.model_forecasts)

        inputs = repository.load_fixture_inputs(self.upcoming, as_of_timestamp=AS_OF)
        self.assertIn("total_goals:2.5", inputs.model_forecasts.model_b)
        self.assertIn("total_corners:9.5", inputs.model_forecasts.model_c)
        self.assertNotIn("total_corners:9.5", inputs.model_forecasts.model_b)
        self.assertEqual(len(inputs.model_forecasts.provenance), 2)

        previous = os.environ.get("PREDICTION_ENGINE_PROMOTED_MARKETS_ENABLED")
        os.environ["PREDICTION_ENGINE_PROMOTED_MARKETS_ENABLED"] = "false"
        try:
            switched_off = repository.load_fixture_inputs(self.upcoming, as_of_timestamp=AS_OF)
        finally:
            if previous is None:
                del os.environ["PREDICTION_ENGINE_PROMOTED_MARKETS_ENABLED"]
            else:
                os.environ["PREDICTION_ENGINE_PROMOTED_MARKETS_ENABLED"] = previous
        self.assertIsNone(switched_off.model_forecasts)

    def test_a_new_forecast_makes_a_predicted_fixture_run_once_more(self) -> None:
        repository = PredictionInputRepository(self.connection)
        later = AS_OF + timedelta(minutes=15)
        self._execute(
            "UPDATE engine_predictions SET last_evaluated_at = %(at)s WHERE fixture_id = %(id)s",
            {"at": AS_OF - timedelta(minutes=5), "id": self.upcoming},
        )
        self.assertNotIn(self.upcoming, repository.list_candidate_fixture_ids(scope="upcoming", limit=50, as_of_timestamp=later))

        self.assertEqual(self._market_forecast("live")["targets"], 1)
        self.assertIn(self.upcoming, repository.list_candidate_fixture_ids(scope="upcoming", limit=50, as_of_timestamp=later))
        # Point in time: a run before the forecast existed does not see it.
        self.assertNotIn(
            self.upcoming,
            repository.list_candidate_fixture_ids(scope="upcoming", limit=50, as_of_timestamp=AS_OF - timedelta(minutes=1)),
        )

        # Evaluated again, whether or not the rows changed, so it does not keep re-running.
        self._execute(
            "UPDATE engine_predictions SET last_evaluated_at = %(at)s WHERE fixture_id = %(id)s",
            {"at": later, "id": self.upcoming},
        )
        self.assertNotIn(self.upcoming, repository.list_candidate_fixture_ids(scope="upcoming", limit=50, as_of_timestamp=later))

    def test_a_replay_forecast_is_never_published(self) -> None:
        self.assertGreater(self._market_forecast("replay")["market_forecasts"], 0)
        repository = PredictionInputRepository(self.connection)
        for fixture_id in (self.upcoming,):
            inputs = repository.load_fixture_inputs(fixture_id, as_of_timestamp=AS_OF)
            self.assertIsNone(inputs.model_forecasts)

    def test_all_market_replay_is_never_evaluation_evidence(self) -> None:
        from prediction_engine import challenger_markets as markets

        summary = self._market_forecast("replay")
        self.assertEqual(summary["targets"], 5)
        repository = markets.MarketRepository(self.db)
        markets.run_market_settlement(repository, settled_at=AS_OF, limit=50)
        evidence = self._execute(
            "SELECT evaluation_eligible, exclusion_reason FROM challenger_market_settlements WHERE origin = 'replay'"
        ).fetchall()
        self.assertEqual(len(evidence), 15)
        self.assertEqual({(row["evaluation_eligible"], row["exclusion_reason"]) for row in evidence}, {(False, "replay_backtest")})
        report = markets.run_market_report(repository, now=AS_OF, origin="replay", minimum_samples=500, write=True)
        self.assertEqual(report["settled_rows"], 15)
        self.assertEqual(report["market_evaluations_written"], 0)

    def test_replay_rows_are_settled_but_never_evaluated(self) -> None:
        summary = self._forecast("replay", champion=lambda fixture, at: None)
        self.assertEqual(summary["targets"], 5)
        self.assertEqual(summary["champion_paired"], 0)
        rows = self._execute(
            "SELECT as_of_timestamp, kickoff_at FROM challenger_predictions WHERE origin = 'replay'"
        ).fetchall()
        self.assertEqual(len(rows), 10)
        self.assertTrue(all(row["kickoff_at"] - row["as_of_timestamp"] == timedelta(hours=1) for row in rows))

        challengers.run_settlement(self.repository, settled_at=AS_OF, limit=50)
        settlements = self._execute(
            "SELECT evaluation_eligible, exclusion_reason FROM shadow_settlements AS ss "
            "JOIN challenger_predictions AS cp ON cp.id = ss.prediction_id WHERE cp.origin = 'replay'"
        ).fetchall()
        self.assertEqual(len(settlements), 10)
        self.assertEqual({(row["evaluation_eligible"], row["exclusion_reason"]) for row in settlements}, {(False, "replay_backtest")})

        report = challengers.run_report(self.repository, now=AS_OF, origin="replay", minimum_samples=500, write=True)
        self.assertEqual(report["settled_rows"], 10)
        self.assertEqual(report["evaluations_written"], 0)

    def test_the_team_strength_fit_reads_the_whole_league_up_to_the_cutoff(self) -> None:
        inputs = PredictionInputRepository(self.connection).load_fixture_inputs(self.upcoming, as_of_timestamp=AS_OF)
        target_teams = {self.teams[0], self.teams[TEAMS - 1]}

        self.assertGreater(len(inputs.league_history), len(inputs.history))
        self.assertTrue(
            any(not {record.home_team_id, record.away_team_id} & target_teams for record in inputs.league_history),
            "the fit must see matches between other teams",
        )
        self.assertTrue(all(record.league_id == self.league for record in inputs.league_history))
        self.assertTrue(all(record.kickoff_at < AS_OF for record in inputs.league_history))

    def test_the_champion_read_works_on_this_schema(self) -> None:
        inputs = PredictionInputRepository(self.connection).load_fixture_inputs(self.upcoming, as_of_timestamp=AS_OF)
        result = champion_match_result(inputs)
        # This fixture is an extreme mismatch with no market odds, the case
        # that used to publish a 1X2 summing to 1.037.
        self.assertIsNotNone(result)
        self.assertEqual(set(result), {"home", "draw", "away"})
        self.assertTrue(all(0.01 <= value <= 0.99 for value in result.values()))
        self.assertAlmostEqual(sum(result.values()), 1.0, places=9)


if __name__ == "__main__":
    unittest.main()
