from __future__ import annotations

from contextlib import closing
from dataclasses import replace
from datetime import UTC, datetime
import sqlite3
import unittest

import prediction_engine.db_repository as db_repository_module
from prediction_engine.db_repository import (
    AccuracySnapshotRepository,
    EnginePredictionRepository,
    PredictionRetentionRepository,
    PredictionSettlementRepository,
    PredictionInputRepository,
    map_fixture_record,
    map_odds_snapshot,
    map_team_fixture_stats,
)
from prediction_engine.features import FixtureRecord, OddsSnapshot, TeamFixtureStats
from prediction_engine.persistence import ConfidenceTier, EnginePrediction, GateTier, PredictionLifecycleStage
from prediction_engine.pipeline import FixtureEngineInput
from prediction_engine.settlement import AccuracySnapshot


class DBRepositoryTests(unittest.TestCase):
    def test_accuracy_snapshot_read_excludes_future_same_day_computations(self) -> None:
        sql = db_repository_module._LATEST_ACCURACY_SNAPSHOTS_SQL

        self.assertIn("snapshot_date <= %(as_of_date)s", sql)
        self.assertGreaterEqual(sql.count("computed_at <= %(as_of_timestamp)s"), 2)

    def test_load_fixture_inputs_queries_target_history_statistics_odds_and_coverage(self) -> None:
        kickoff = datetime(2026, 7, 1, 15, 0, tzinfo=UTC)
        target_row = {
            "id": 42,
            "kickoff_at": kickoff,
            "league_id": 39,
            "league_name": "Premier League",
            "season_id": 2026,
            "home_team_id": 10,
            "away_team_id": 20,
            "home_goals_ft": None,
            "away_goals_ft": None,
            "status": "NS",
        }
        history_row = {
            "id": 41,
            "kickoff_at": datetime(2026, 6, 28, 15, 0, tzinfo=UTC),
            "league_id": 39,
            "league_name": "Premier League",
            "season_id": 2026,
            "home_team_id": 10,
            "away_team_id": 20,
            "home_goals_ft": 2,
            "away_goals_ft": 1,
            "status": "FT",
        }
        connection = FakeConnection(
            results=[
                FakeCursor(one=target_row),
                FakeCursor(many=[history_row]),
                FakeCursor(many=[{"fixture_id": 41, "team_id": 10, "shots_total": 12, "possession_pct": 55.0}]),
                FakeCursor(
                    many=[
                        {"outcome": "Home", "value": 1.90},
                        {"outcome": "Draw", "value": 3.40},
                        {"outcome": "Away", "value": 4.20},
                    ]
                ),
                FakeCursor(one={"has_statistics": True, "has_xg": False, "has_injuries": False, "has_odds": True, "has_lineups": False}),
                FakeCursor(
                    many=[
                        {"scope": "market", "scope_id": "1x2", "accuracy": 0.58},
                        {"scope": "market", "scope_id": "total_goals", "accuracy": 0.64},
                        {"scope": "league", "scope_id": "39", "accuracy": 0.61},
                    ]
                ),
            ]
        )

        inputs = PredictionInputRepository(connection).load_fixture_inputs(42, as_of_timestamp=kickoff)

        self.assertIsInstance(inputs, FixtureEngineInput)
        self.assertEqual(inputs.target_fixture.id, 42)
        self.assertEqual([record.id for record in inputs.history], [41])
        self.assertEqual(inputs.statistics_by_fixture[41][0].shots_total, 12)
        self.assertEqual(inputs.odds, OddsSnapshot(fixture_id=42, home_win=1.90, draw=3.40, away_win=4.20))
        self.assertEqual(inputs.coverage["has_statistics"], True)
        self.assertEqual(inputs.market_accuracy, {"1x2": 0.58, "total_goals": 0.64})
        self.assertEqual(inputs.league_accuracy, 0.61)
        self.assertEqual(inputs.as_of_timestamp, kickoff)
        self.assertEqual(len(connection.executed), 6)
        self.assertIn("FROM fixtures AS f", connection.executed[0][0])
        self.assertIn("f.id = %(fixture_id)s", connection.executed[0][0])
        self.assertIn("f.kickoff_at < %(target_kickoff_at)s", connection.executed[1][0])
        self.assertIn("f.kickoff_at < %(as_of_timestamp)s", connection.executed[1][0])
        self.assertIn("f.updated_at <= %(target_kickoff_at)s", connection.executed[1][0])
        self.assertIn("f.updated_at <= %(as_of_timestamp)s", connection.executed[1][0])
        self.assertNotIn("f.league_id = %(league_id)s", connection.executed[1][0])
        self.assertIn("f.season_id IN (%(season_id)s, %(season_id)s - 1)", connection.executed[1][0])
        self.assertEqual(connection.executed[1][1]["as_of_timestamp"], kickoff)
        self.assertIn("fixture_id = ANY(%(fixture_ids)s)", connection.executed[2][0])
        self.assertIn("updated_at <= %(as_of_timestamp)s", connection.executed[2][0])
        self.assertIn("fetched_at <= %(as_of_timestamp)s", connection.executed[3][0])
        self.assertIn("updated_at <= %(as_of_timestamp)s", connection.executed[4][0])
        self.assertIn("FROM accuracy_snapshots", connection.executed[5][0])
        self.assertIn("snapshot_date <= %(as_of_date)s", connection.executed[5][0])
        self.assertGreaterEqual(connection.executed[5][0].count("computed_at <= %(as_of_timestamp)s"), 2)
        self.assertEqual(connection.executed[5][1]["as_of_timestamp"], kickoff)

    def test_load_fixture_inputs_rejects_missing_target_fixture(self) -> None:
        connection = FakeConnection(results=[FakeCursor(one=None)])

        with self.assertRaisesRegex(LookupError, "fixture 42 not found"):
            PredictionInputRepository(connection).load_fixture_inputs(
                42,
                as_of_timestamp=datetime(2026, 7, 1, tzinfo=UTC),
            )

    def test_ensure_history_backfill_persists_provider_agnostic_requests_and_jobs(self) -> None:
        generated_at = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureRecord(
            id=42,
            kickoff_at=generated_at,
            league_id=39,
            league_name="Premier League",
            season=2026,
            home_team_id=10,
            away_team_id=20,
            home_goals=None,
            away_goals=None,
            status="NS",
        )
        inputs = FixtureEngineInput(
            as_of_timestamp=generated_at,
            target_fixture=target,
            history=[],
            statistics_by_fixture={},
            odds=None,
            generated_at=generated_at,
        )
        connection = FakeConnection(
            results=[FakeCursor(many=[
                {"feature_group": "results", "status": "pending"},
                {"feature_group": "results", "status": "in_progress"},
                {"feature_group": "rich_statistics", "status": "pending"},
            ])]
        )

        statuses = PredictionInputRepository(connection).ensure_history_backfill(
            inputs,
            matches_needed_by_feature_group={"results": 5, "rich_statistics": 10},
        )

        self.assertEqual(statuses, ("pending", "in_progress"))
        sql, params = connection.executed[0]
        self.assertIn("INSERT INTO data_backfill_requests", sql)
        self.assertIn("'backfill.team_history'", sql)
        self.assertIn("'data-backfill'", sql)
        self.assertIn('"team_id": 10', str(params["requests"]))
        self.assertIn('"team_id": 20', str(params["requests"]))
        self.assertIn('"feature_group": "rich_statistics"', str(params["requests"]))
        self.assertTrue(connection.committed)

    def test_load_fixture_inputs_falls_back_to_provisional_odds_when_latest_odds_incomplete(self) -> None:
        generated_at = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        connection = FakeConnection(
            results=[
                FakeCursor(
                    one={
                        "id": 42,
                        "kickoff_at": generated_at,
                        "league_id": 39,
                        "league_name": "Premier League",
                        "season_id": 2026,
                        "home_team_id": 10,
                        "away_team_id": 20,
                        "home_goals_ft": None,
                        "away_goals_ft": None,
                        "status": "NS",
                    }
                ),
                FakeCursor(many=[]),
                FakeCursor(many=[]),
                FakeCursor(
                    many=[
                        {"outcome": "Home", "value": 1.90},
                        {"outcome": "Draw", "value": 3.40},
                    ]
                ),
                FakeCursor(one=None),
                FakeCursor(many=[]),
            ]
        )

        inputs = PredictionInputRepository(connection).load_fixture_inputs(42, as_of_timestamp=generated_at)

        self.assertIsNotNone(inputs.odds)
        self.assertTrue(inputs.odds_provisional)

    def test_list_candidate_fixture_ids_supports_upcoming_and_historical_scopes(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        connection = FakeConnection(
            results=[
                FakeCursor(many=[{"id": 42}, {"id": 43}]),
                FakeCursor(many=[{"id": 10}]),
            ]
        )
        repository = PredictionInputRepository(connection)

        upcoming = repository.list_candidate_fixture_ids(scope="upcoming", limit=2, as_of_timestamp=now)
        historical = repository.list_candidate_fixture_ids(scope="historical", limit=1, as_of_timestamp=now)

        self.assertEqual(upcoming, [42, 43])
        self.assertEqual(historical, [10])
        self.assertIn("f.kickoff_at >= %(as_of_timestamp)s", connection.executed[0][0])
        self.assertIn("f.home_goals_ft IS NOT NULL", connection.executed[1][0])
        self.assertIn("COUNT(*)", connection.executed[1][0])
        self.assertNotIn("FROM odds", connection.executed[0][0])
        self.assertNotIn("FROM odds", connection.executed[1][0])

    def test_upcoming_candidate_query_includes_short_history_for_backfill_request(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        connection = FakeConnection(results=[FakeCursor(many=[{"id": 142}])])

        fixture_ids = PredictionInputRepository(connection).list_candidate_fixture_ids(
            scope="upcoming",
            limit=100,
            as_of_timestamp=now,
        )

        self.assertEqual(fixture_ids, [142])
        sql, params = connection.executed[0]
        self.assertNotIn("prior_home", sql)
        self.assertNotIn("prior_away", sql)
        self.assertEqual(sql.count("updated_at <= %(as_of_timestamp)s"), 1)
        self.assertNotIn("minimum_history", params)

    def test_upcoming_candidate_query_keeps_running_while_history_backfills(self) -> None:
        now = datetime(2026, 8, 21, 18, 0, tzinfo=UTC)
        connection = FakeConnection(results=[FakeCursor(many=[{"id": 501}])])

        PredictionInputRepository(connection).list_candidate_fixture_ids(
            scope="upcoming", limit=500, as_of_timestamp=now,
        )

        sql, _ = connection.executed[0]
        self.assertNotIn("FROM data_backfill_requests AS backfill", sql)
        self.assertNotIn("active_backfill_job", sql)

    def test_history_and_upcoming_queries_fall_back_to_prior_season(self) -> None:
        self.assertIn(
            "f.season_id IN (%(season_id)s, %(season_id)s - 1)",
            db_repository_module._HISTORY_FIXTURES_SQL,
        )

    def test_historical_replay_candidates_stay_same_season(self) -> None:
        self.assertIn("prior_home.season_id = f.season_id", db_repository_module._HISTORICAL_FIXTURE_IDS_SQL)
        self.assertIn("prior_away.season_id = f.season_id", db_repository_module._HISTORICAL_FIXTURE_IDS_SQL)

    def test_historical_candidates_require_results_available_by_target_and_as_of(self) -> None:
        as_of = datetime(2026, 7, 10, 12, 0, tzinfo=UTC)
        connection = FakeConnection(results=[FakeCursor(many=[])])

        PredictionInputRepository(connection).list_candidate_fixture_ids(
            scope="historical",
            limit=10,
            as_of_timestamp=as_of,
        )

        sql, params = connection.executed[0]
        self.assertIn("f.updated_at <= %(as_of_timestamp)s", sql)
        self.assertEqual(sql.count("updated_at <= f.kickoff_at"), 2)
        self.assertGreaterEqual(sql.count("updated_at <= %(as_of_timestamp)s"), 3)
        self.assertEqual(params["as_of_timestamp"], as_of)

    def test_list_candidate_fixture_ids_rejects_unknown_scope(self) -> None:
        with self.assertRaisesRegex(ValueError, "unsupported fixture scope"):
            PredictionInputRepository(FakeConnection()).list_candidate_fixture_ids(
                scope="live",
                limit=10,
                as_of_timestamp=datetime(2026, 7, 1, tzinfo=UTC),
            )

    def test_input_repository_rejects_implicit_or_naive_as_of_timestamps(self) -> None:
        repository = PredictionInputRepository(FakeConnection())

        with self.assertRaisesRegex(TypeError, "as_of_timestamp"):
            repository.load_fixture_inputs(42)  # type: ignore[call-arg]
        with self.assertRaisesRegex(ValueError, "timezone-aware"):
            repository.list_candidate_fixture_ids(
                scope="upcoming",
                limit=10,
                as_of_timestamp=datetime(2026, 7, 1),
            )

    def test_list_settlement_fixture_ids_finds_finished_current_unsettled_engine_rows(self) -> None:
        now = datetime(2026, 7, 2, 12, 0, tzinfo=UTC)
        connection = FakeConnection(results=[FakeCursor(many=[{"fixture_id": 42}, {"fixture_id": 43}])])
        repository = PredictionSettlementRepository(connection)

        fixture_ids = repository.list_settlement_fixture_ids(scope="finished", limit=2, now=now)

        self.assertEqual(fixture_ids, [42, 43])
        sql, params = connection.executed[0]
        self.assertIn("FROM engine_predictions AS ep", sql)
        self.assertIn("pr.prediction_id IS NULL", sql)
        self.assertIn("prediction_result_rollup_ledger", sql)
        self.assertIn("rollup.prediction_id IS NULL", sql)
        self.assertIn("ep.model_registry_id IS NOT NULL", sql)
        self.assertIn("f.home_goals_ft IS NOT NULL", sql)
        self.assertEqual(params["limit"], 2)

    def test_settlement_fixture_limit_skips_unattributable_predictions(self) -> None:
        # A lower-ID fixture with only in-play predictions used to occupy the
        # first slot on every run, starving a later fixture with valid rows.
        with closing(sqlite3.connect(":memory:")) as connection:
            connection.executescript(
                """
                CREATE TABLE fixtures (
                    id INTEGER PRIMARY KEY, status TEXT,
                    home_goals_ft INTEGER, away_goals_ft INTEGER
                );
                CREATE TABLE engine_predictions (
                    id INTEGER PRIMARY KEY, fixture_id INTEGER,
                    model_registry_id TEXT, superseded_at TEXT
                );
                CREATE TABLE prediction_results (prediction_id INTEGER);
                CREATE TABLE prediction_result_rollup_ledger (prediction_id INTEGER);
                INSERT INTO fixtures VALUES (41, 'finished', 2, 1);
                INSERT INTO fixtures VALUES (42, 'finished', 1, 0);
                INSERT INTO engine_predictions VALUES (1, 41, NULL, NULL);
                INSERT INTO engine_predictions VALUES (2, 42, NULL, NULL);
                INSERT INTO engine_predictions VALUES (3, 42, 'production-model', NULL);
                """
            )
            sql = db_repository_module._SETTLEMENT_FIXTURE_IDS_SQL.replace("%(limit)s", ":limit")
            selected = connection.execute(sql, {"limit": 1}).fetchall()

        self.assertEqual(selected, [(42,)])

    def test_list_settlement_fixture_ids_rejects_unknown_scope(self) -> None:
        with self.assertRaisesRegex(ValueError, "unsupported settlement scope"):
            PredictionSettlementRepository(FakeConnection()).list_settlement_fixture_ids(
                scope="upcoming",
                limit=10,
                now=datetime(2026, 7, 2, tzinfo=UTC),
            )

    def test_save_predictions_inserts_when_no_current_row_exists(self) -> None:
        connection = FakeConnection()
        repository = EnginePredictionRepository(connection)
        generated_at = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)

        stats = repository.save_predictions([prediction(generated_at)])

        self.assertEqual((stats.inserted, stats.versioned, stats.unchanged, stats.stale), (1, 0, 0, 0))
        self.assertEqual(len(connection.executed), 3)
        self.assertIn("pg_advisory_xact_lock", connection.executed[0][0])
        self.assertIn("FOR UPDATE", connection.executed[1][0])
        insert_sql, insert_params = connection.executed[2]
        self.assertIn("INSERT INTO engine_predictions", insert_sql)
        self.assertIn("engine_prediction_content_hash", insert_sql)
        self.assertEqual(insert_params["fixture_id"], 42)
        self.assertEqual(insert_params["market"], "1x2")
        self.assertEqual(insert_params["features_used"]["model_a_score_matrix"], [[0.6, 0.4]])
        self.assertTrue(connection.committed)

    def test_prediction_hash_ignores_only_the_evaluation_clock(self) -> None:
        for sql in (
            db_repository_module._CURRENT_PREDICTION_SQL,
            db_repository_module._INSERT_SQL,
        ):
            self.assertIn("%(features_used)s::JSONB - 'as_of_timestamp'", sql)
            self.assertIn("engine_prediction_content_hash", sql)

        self.assertNotIn(
            "content_hash = engine_prediction_content_hash",
            db_repository_module._CURRENT_PREDICTION_SQL,
        )
        self.assertGreaterEqual(
            db_repository_module._CURRENT_PREDICTION_SQL.count("engine_prediction_content_hash("),
            2,
        )
        self.assertIn(
            "content_hash = engine_prediction_content_hash(",
            db_repository_module._TOUCH_LAST_EVALUATED_SQL,
        )

        generated_at = datetime(2026, 7, 1, 12, 15, tzinfo=UTC)
        connection = FakeConnection(
            results=[
                FakeCursor(),
                FakeCursor(one={
                    "id": "current-id",
                    "generated_at": datetime(2026, 7, 1, 12, 0, tzinfo=UTC),
                    "content_matches": True,
                }),
            ]
        )
        row = prediction(generated_at)
        row.features_used["as_of_timestamp"] = generated_at.isoformat()

        stats = EnginePredictionRepository(connection).save_predictions([row])

        self.assertEqual((stats.inserted, stats.versioned, stats.unchanged), (0, 0, 1))
        self.assertEqual(connection.executed[1][1]["features_used"]["as_of_timestamp"], generated_at.isoformat())

    def test_save_predictions_versions_only_genuine_changes(self) -> None:
        generated_at = datetime(2026, 7, 1, 12, 15, tzinfo=UTC)
        connection = FakeConnection(
            results=[
                FakeCursor(),
                FakeCursor(
                    one={
                        "id": "current-id",
                        "generated_at": datetime(2026, 7, 1, 12, 0, tzinfo=UTC),
                        "content_matches": False,
                    }
                ),
            ]
        )

        stats = EnginePredictionRepository(connection).save_predictions([prediction(generated_at)])

        self.assertEqual((stats.inserted, stats.versioned, stats.unchanged, stats.stale), (0, 1, 0, 0))
        self.assertEqual(len(connection.executed), 4)
        update_sql, update_params = connection.executed[2]
        self.assertIn("superseded_at = %(superseded_at)s", update_sql)
        self.assertEqual(update_params, {"id": "current-id", "superseded_at": generated_at})
        self.assertIn("INSERT INTO engine_predictions", connection.executed[3][0])

    def test_save_predictions_persists_stability_from_immediately_preceding_current_version(self) -> None:
        generated_at = datetime(2026, 7, 1, 14, 0, tzinfo=UTC)
        connection = FakeConnection(
            results=[
                FakeCursor(),
                FakeCursor(one={
                    "id": "current-id",
                    "generated_at": datetime(2026, 7, 1, 12, 0, tzinfo=UTC),
                    "prediction": "away",
                    "probability": 0.60,
                    "calibrated_probability": 0.58,
                    "max_lifecycle_probability_shift": 0.03,
                    "content_matches": False,
                }),
            ]
        )

        EnginePredictionRepository(connection).save_predictions([prediction(generated_at)])

        _, insert_params = connection.executed[3]
        self.assertEqual(insert_params["previous_probability"], 0.58)
        self.assertAlmostEqual(insert_params["probability_delta"], 0.04)
        self.assertAlmostEqual(insert_params["max_lifecycle_probability_shift"], 0.04)
        self.assertTrue(insert_params["prediction_direction_changed"])
        self.assertAlmostEqual(insert_params["lifecycle_stability_score"], 0.4)

    def test_save_predictions_persists_neutral_stability_for_first_version(self) -> None:
        connection = FakeConnection()

        EnginePredictionRepository(connection, lifecycle_neutral_score=0.65).save_predictions([
            prediction(datetime(2026, 7, 1, 12, 0, tzinfo=UTC))
        ])

        _, insert_params = connection.executed[2]
        self.assertIsNone(insert_params["previous_probability"])
        self.assertIsNone(insert_params["probability_delta"])
        self.assertEqual(insert_params["max_lifecycle_probability_shift"], 0.0)
        self.assertFalse(insert_params["prediction_direction_changed"])
        self.assertEqual(insert_params["lifecycle_stability_score"], 0.65)

    def test_save_predictions_only_touches_last_evaluated_for_unchanged_content(self) -> None:
        generated_at = datetime(2026, 7, 1, 12, 15, tzinfo=UTC)
        connection = FakeConnection(
            results=[
                FakeCursor(),
                FakeCursor(
                    one={
                        "id": "current-id",
                        "generated_at": datetime(2026, 7, 1, 12, 0, tzinfo=UTC),
                        "content_matches": True,
                    }
                ),
            ]
        )

        stats = EnginePredictionRepository(connection).save_predictions([prediction(generated_at)])

        self.assertEqual((stats.inserted, stats.versioned, stats.unchanged, stats.stale), (0, 0, 1, 0))
        self.assertEqual(len(connection.executed), 3)
        touch_sql, touch_params = connection.executed[2]
        self.assertIn("last_evaluated_at = GREATEST", touch_sql)
        self.assertNotIn("generated_at =", touch_sql)
        self.assertNotIn("superseded_at =", touch_sql)
        self.assertEqual(touch_params, {"id": "current-id", "last_evaluated_at": generated_at})

    def test_save_predictions_ignores_stale_out_of_order_changes(self) -> None:
        generated_at = datetime(2026, 7, 1, 11, 45, tzinfo=UTC)
        connection = FakeConnection(
            results=[
                FakeCursor(),
                FakeCursor(
                    one={
                        "id": "current-id",
                        "generated_at": datetime(2026, 7, 1, 12, 0, tzinfo=UTC),
                        "content_matches": False,
                    }
                ),
            ]
        )

        stats = EnginePredictionRepository(connection).save_predictions([prediction(generated_at)])

        self.assertEqual((stats.inserted, stats.versioned, stats.unchanged, stats.stale), (0, 0, 0, 1))
        self.assertEqual(len(connection.executed), 2)
        self.assertTrue(connection.committed)

    def test_save_predictions_can_adapt_features_used_for_jsonb_drivers(self) -> None:
        connection = FakeConnection()
        repository = EnginePredictionRepository(connection, jsonb_adapter=lambda value: {"jsonb": value})

        repository.save_predictions([prediction(datetime(2026, 7, 1, 12, 0, tzinfo=UTC))])

        insert_sql = connection.executed[2][0]
        insert_params = connection.executed[2][1]
        self.assertIn("engine_prediction_market_family(%(market)s)", insert_sql)
        self.assertIn("model_registry_id", insert_sql)
        self.assertIn("as_of_timestamp", insert_sql)
        self.assertIn("feature_version", insert_sql)
        self.assertIn("raw_probability", insert_sql)
        self.assertEqual(insert_params["features_used"], {"jsonb": {"model_a_score_matrix": [[0.6, 0.4]], "kickoff_at": "2026-07-01T12:00:00+00:00", "league_id": 39}})

    def test_save_predictions_rolls_back_on_insert_failure(self) -> None:
        connection = FakeConnection(fail_on_call=3)
        repository = EnginePredictionRepository(connection)

        with self.assertRaises(RuntimeError):
            repository.save_predictions([prediction(datetime(2026, 7, 1, 12, 0, tzinfo=UTC))])

        self.assertTrue(connection.rolled_back)
        self.assertFalse(connection.committed)

    def test_in_play_publication_records_complete_visible_set_without_retiring_settleable_rows(self) -> None:
        generated_at = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        connection = FakeConnection()
        rows = [in_play_prediction(generated_at, market) for market in (
            "total_cards_4.5", "total_cards_5.5",
        )]

        stats = EnginePredictionRepository(connection).replace_in_play_predictions(
            42, rows, generated_at=generated_at
        )

        self.assertEqual(stats.inserted, 2)
        self.assertTrue(connection.committed)
        self.assertIn("inplay-fixture:", connection.executed[0][0])
        upsert_sql, upsert_params = connection.executed[-1]
        self.assertIn("inplay_publication_state", upsert_sql)
        self.assertEqual(upsert_params["current_markets"], ["total_cards_4.5", "total_cards_5.5"])
        self.assertFalse(any("UPDATE engine_predictions" in sql and "market <> ALL" in sql for sql, _ in connection.executed))

    def test_in_play_publication_can_be_empty_and_blocks_an_older_run(self) -> None:
        newest = datetime(2026, 7, 1, 12, 5, tzinfo=UTC)
        empty_connection = FakeConnection()
        repository = EnginePredictionRepository(empty_connection)
        repository.replace_in_play_predictions(42, [], generated_at=newest)
        self.assertEqual(empty_connection.executed[-1][1]["current_markets"], [])

        old = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        delayed_connection = FakeConnection(results=[
            FakeCursor(), FakeCursor(one={"generated_at": newest}),
        ])
        delayed = EnginePredictionRepository(delayed_connection).replace_in_play_predictions(
            42, [in_play_prediction(old, "total_cards_1.5")], generated_at=old
        )
        self.assertEqual(delayed.stale, 1)
        self.assertEqual(len(delayed_connection.executed), 2)
        self.assertTrue(delayed_connection.committed)

    def test_in_play_publication_rolls_back_rows_and_membership_on_failure(self) -> None:
        generated_at = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        connection = FakeConnection(fail_on_call=5)
        repository = EnginePredictionRepository(connection)

        with self.assertRaisesRegex(RuntimeError, "database write failed"):
            repository.replace_in_play_predictions(
                42, [in_play_prediction(generated_at, "total_cards_4.5")], generated_at=generated_at
            )

        self.assertTrue(connection.rolled_back)
        self.assertFalse(connection.committed)
        self.assertEqual(repository.total_write_stats.written, 0)

    def test_settle_fixture_loads_current_predictions_and_writes_results(self) -> None:
        settled_at = datetime(2026, 7, 2, 12, 0, tzinfo=UTC)
        connection = FakeConnection(
            results=[
                FakeCursor(
                    one={
                        "id": 42,
                        "league_id": 39,
                        "home_team_id": 10,
                        "away_team_id": 20,
                        "home_goals_ft": 2,
                        "away_goals_ft": 1,
                        "home_goals_ht": 1,
                        "away_goals_ht": 0,
                    }
                ),
                FakeCursor(many=[]),
                FakeCursor(
                    many=[
                        {
                            "id": "uuid-1",
                            "fixture_id": 42,
                            "market": "1x2",
                            "prediction": "home",
                            "probability": 0.62,
                            "confidence": 0.68,
                            "confidence_tier": "medium",
                            "lifecycle_stage": "PRE_MATCH",
                            "league_id": 39,
                            "model_registry_id": "model-1",
                            "generated_at": datetime(2026, 7, 1, 12, 0, tzinfo=UTC),
                            "data_cutoff_at": datetime(2026, 7, 1, 11, 55, tzinfo=UTC),
                            "feature_version": "v1-approximate",
                            "raw_probability": 0.62,
                            "calibrated_probability": None,
                            "odds_snapshot_id": None,
                        },
                        {
                            "id": "uuid-2",
                            "fixture_id": 42,
                            "market": "btts",
                            "prediction": "yes",
                            "probability": 0.55,
                            "confidence": 0.56,
                            "confidence_tier": "low",
                            "lifecycle_stage": "PRE_MATCH",
                            "league_id": 39,
                            "model_registry_id": "model-2",
                            "generated_at": datetime(2026, 7, 1, 12, 0, tzinfo=UTC),
                            "data_cutoff_at": datetime(2026, 7, 1, 11, 55, tzinfo=UTC),
                            "feature_version": "v1-approximate",
                            "raw_probability": 0.55,
                            "calibrated_probability": None,
                            "odds_snapshot_id": None,
                        },
                    ]
                ),
            ]
        )

        results = PredictionSettlementRepository(connection).settle_fixture(42, settled_at=settled_at)

        self.assertEqual([result.status for result in results], ["settled", "settled"])
        # Three writes per prediction now: the result upsert, the append-only
        # revision that gives a later reversal something to point back at, and
        # the evaluation fact.
        self.assertEqual(len(connection.executed), 9)
        self.assertIn("FROM fixtures AS f", connection.executed[0][0])
        self.assertIn("FROM fixture_statistics", connection.executed[1][0])
        self.assertIn("FROM engine_predictions AS ep", connection.executed[2][0])
        insert_sql, insert_params = connection.executed[3]
        self.assertIn("INSERT INTO prediction_results", insert_sql)
        self.assertIn("ON CONFLICT", insert_sql)
        revision_sql, revision_params = connection.executed[4]
        self.assertIn("INSERT INTO prediction_result_revisions", revision_sql)
        self.assertEqual(revision_params["revision_reason"], "initial")
        self.assertIsNone(revision_params["triggered_by_event_key"])
        self.assertEqual(insert_params["prediction_id"], "uuid-1")
        self.assertEqual(insert_params["actual"], "home")
        self.assertEqual(insert_params["is_correct"], True)
        self.assertEqual(insert_params["settlement_status"], "settled")
        fact_sql, fact_params = connection.executed[5]
        self.assertIn("INSERT INTO model_evaluation_facts", fact_sql)
        self.assertIn("ON CONFLICT (prediction_id) DO NOTHING", fact_sql)
        self.assertEqual(fact_params["model_registry_id"], "model-1")
        self.assertEqual(fact_params["settlement_classification"], "correct")
        self.assertEqual(fact_params["final_outcome"], "home")
        self.assertEqual(fact_params["evaluation_eligible"], True)
        self.assertIsNone(fact_params["exclusion_reason"])
        self.assertTrue(connection.committed)

    def test_settle_fixture_persists_unsettled_data_unavailable_rows(self) -> None:
        settled_at = datetime(2026, 7, 2, 12, 0, tzinfo=UTC)
        connection = FakeConnection(
            results=[
                FakeCursor(
                    one={
                        "id": 42,
                        "league_id": 39,
                        "home_team_id": 10,
                        "away_team_id": 20,
                        "home_goals_ft": 1,
                        "away_goals_ft": 0,
                        "home_goals_ht": None,
                        "away_goals_ht": None,
                    }
                ),
                FakeCursor(many=[]),
                FakeCursor(
                    many=[
                        {
                            "id": "uuid-1",
                            "fixture_id": 42,
                            "market": "team_to_score_first",
                            "prediction": "home",
                            "probability": 0.62,
                            "confidence": 0.68,
                            "confidence_tier": "medium",
                            "lifecycle_stage": "PRE_MATCH",
                            "league_id": 39,
                            "model_registry_id": "model-1",
                            "generated_at": datetime(2026, 7, 1, 12, 0, tzinfo=UTC),
                            "data_cutoff_at": datetime(2026, 7, 1, 11, 55, tzinfo=UTC),
                            "feature_version": "v1-approximate",
                            "raw_probability": 0.62,
                            "calibrated_probability": None,
                            "odds_snapshot_id": None,
                        }
                    ]
                ),
            ]
        )

        results = PredictionSettlementRepository(connection).settle_fixture(42, settled_at=settled_at)

        self.assertEqual(results[0].status, "unsettled_data_unavailable")
        insert_params = connection.executed[3][1]
        self.assertIsNone(insert_params["actual"])
        self.assertIsNone(insert_params["is_correct"])
        self.assertEqual(insert_params["settlement_status"], "unsettled_data_unavailable")
        self.assertIn("INSERT INTO prediction_result_revisions", connection.executed[4][0])
        fact_params = connection.executed[5][1]
        self.assertEqual(fact_params["settlement_classification"], "excluded")
        self.assertEqual(fact_params["final_outcome"], "data_unavailable")
        self.assertEqual(fact_params["evaluation_eligible"], False)
        self.assertEqual(fact_params["exclusion_reason"], "unsettled_data_unavailable")

    def test_settle_fixture_rolls_back_when_model_identity_is_missing(self) -> None:
        settled_at = datetime(2026, 7, 2, 12, 0, tzinfo=UTC)
        connection = FakeConnection(
            results=[
                FakeCursor(
                    one={
                        "id": 42,
                        "league_id": 39,
                        "home_team_id": 10,
                        "away_team_id": 20,
                        "home_goals_ft": 2,
                        "away_goals_ft": 1,
                        "home_goals_ht": 1,
                        "away_goals_ht": 0,
                    }
                ),
                FakeCursor(many=[]),
                FakeCursor(
                    many=[
                        {
                            "id": "uuid-1",
                            "fixture_id": 42,
                            "market": "1x2",
                            "prediction": "home",
                            "probability": 0.62,
                            "confidence": 0.68,
                            "confidence_tier": "medium",
                            "lifecycle_stage": "PRE_MATCH",
                            "league_id": 39,
                            "model_registry_id": None,
                            "generated_at": datetime(2026, 7, 1, 12, 0, tzinfo=UTC),
                            "data_cutoff_at": None,
                            "feature_version": None,
                            "raw_probability": None,
                            "calibrated_probability": None,
                            "odds_snapshot_id": None,
                        }
                    ]
                ),
            ]
        )

        # A prediction with no model_registry_id is still not settled: the
        # evaluation fact would have nothing to attribute the outcome to, and an
        # unattributable row corrupts the accuracy record.
        #
        # It no longer raises, because raising abandoned the whole fixture. Here
        # it is the only prediction, so nothing settles -- but the fixture is
        # not treated as a failure, and anything else on it would have settled.
        results = PredictionSettlementRepository(connection).settle_fixture(42, settled_at=settled_at)

        self.assertEqual(results, [])
        self.assertFalse(connection.rolled_back)
        self.assertTrue(connection.committed)
        # Reads only: the fixture, its stats, the predictions. No writes.
        self.assertEqual(len(connection.executed), 3)

    def test_settle_fixture_settles_the_rest_when_one_prediction_lacks_model_identity(self) -> None:
        """One unattributable prediction must not discard the whole fixture.

        This was the bug. The pre-check raised on the first prediction with no
        model_registry_id, abandoning every other prediction on the same match.
        In production 2,357 of 126,728 current predictions were missing the id
        -- under 2% -- and 1,070 finished fixtures from the previous seven days
        still had unsettled predictions behind them, so no result reached a
        reader and the accuracy record simply stopped.
        """
        settled_at = datetime(2026, 7, 2, 12, 0, tzinfo=UTC)
        connection = FakeConnection(
            results=[
                FakeCursor(
                    one={
                        "id": 42,
                        "league_id": 39,
                        "home_team_id": 10,
                        "away_team_id": 20,
                        "home_goals_ft": 2,
                        "away_goals_ft": 1,
                        "home_goals_ht": 1,
                        "away_goals_ht": 0,
                    }
                ),
                FakeCursor(many=[]),
                FakeCursor(
                    many=[
                        {
                            "id": "uuid-unattributable",
                            "fixture_id": 42,
                            "market": "1x2",
                            "prediction": "home",
                            "probability": 0.62,
                            "confidence": 0.68,
                            "confidence_tier": "medium",
                            "lifecycle_stage": "PRE_MATCH",
                            "league_id": 39,
                            "model_registry_id": None,
                            "generated_at": datetime(2026, 7, 1, 12, 0, tzinfo=UTC),
                            "data_cutoff_at": None,
                            "feature_version": None,
                            "raw_probability": None,
                            "calibrated_probability": None,
                            "odds_snapshot_id": None,
                        },
                        {
                            "id": "uuid-good",
                            "fixture_id": 42,
                            "market": "btts",
                            "prediction": "yes",
                            "probability": 0.55,
                            "confidence": 0.56,
                            "confidence_tier": "low",
                            "lifecycle_stage": "PRE_MATCH",
                            "league_id": 39,
                            "model_registry_id": "model-2",
                            "generated_at": datetime(2026, 7, 1, 12, 0, tzinfo=UTC),
                            "data_cutoff_at": datetime(2026, 7, 1, 11, 55, tzinfo=UTC),
                            "feature_version": "v1-approximate",
                            "raw_probability": 0.55,
                            "calibrated_probability": None,
                            "odds_snapshot_id": None,
                        },
                    ]
                ),
            ]
        )

        results = PredictionSettlementRepository(connection).settle_fixture(42, settled_at=settled_at)

        self.assertEqual(len(results), 1, "the attributable prediction should still settle")
        self.assertTrue(connection.committed)
        self.assertFalse(connection.rolled_back)
        written = [params.get("prediction_id") for _, params in connection.executed if isinstance(params, dict)]
        self.assertIn("uuid-good", written)
        self.assertNotIn(
            "uuid-unattributable",
            written,
            "a prediction with no model_registry_id must never be written as a result",
        )

    def test_settle_fixture_rolls_back_result_when_evaluation_fact_write_fails(self) -> None:
        settled_at = datetime(2026, 7, 2, 12, 0, tzinfo=UTC)
        connection = FakeConnection(
            fail_on_call=5,
            results=[
                FakeCursor(
                    one={
                        "id": 42,
                        "league_id": 39,
                        "home_team_id": 10,
                        "away_team_id": 20,
                        "home_goals_ft": 2,
                        "away_goals_ft": 1,
                        "home_goals_ht": 1,
                        "away_goals_ht": 0,
                    }
                ),
                FakeCursor(many=[]),
                FakeCursor(
                    many=[
                        {
                            "id": "uuid-1",
                            "fixture_id": 42,
                            "market": "1x2",
                            "prediction": "home",
                            "probability": 0.62,
                            "confidence": 0.68,
                            "confidence_tier": "medium",
                            "lifecycle_stage": "PRE_MATCH",
                            "league_id": 39,
                            "model_version": "python-engine-v1",
                            "model_registry_id": "model-1",
                            "generated_at": datetime(2026, 7, 1, 12, 0, tzinfo=UTC),
                            "data_cutoff_at": None,
                            "feature_version": "python-engine-v1",
                            "raw_probability": 0.62,
                            "calibrated_probability": None,
                            "odds_snapshot_id": None,
                        }
                    ]
                ),
            ],
        )

        with self.assertRaisesRegex(RuntimeError, "database write failed"):
            PredictionSettlementRepository(connection).settle_fixture(42, settled_at=settled_at)

        self.assertTrue(connection.rolled_back)
        self.assertFalse(connection.committed)
        self.assertIn("INSERT INTO prediction_results", connection.executed[3][0])

    def test_save_accuracy_snapshot_upserts_scheduled_snapshot_rows(self) -> None:
        connection = FakeConnection()
        snapshot = AccuracySnapshot(
            snapshot_date=datetime(2026, 7, 2, tzinfo=UTC).date(),
            scope="market",
            scope_id="1x2",
            total_predictions=12,
            correct=7,
            accuracy=7 / 12,
        )

        AccuracySnapshotRepository(connection).save_snapshot(snapshot)

        self.assertEqual(len(connection.executed), 1)
        sql, params = connection.executed[0]
        self.assertIn("INSERT INTO accuracy_snapshots", sql)
        self.assertIn("ON CONFLICT", sql)
        self.assertEqual(params["scope"], "market")
        self.assertEqual(params["scope_id"], "1x2")
        self.assertEqual(params["total_predictions"], 12)
        self.assertEqual(params["correct"], 7)
        self.assertAlmostEqual(params["accuracy"], 7 / 12)
        self.assertTrue(connection.committed)

    def test_compute_and_save_scheduled_accuracy_snapshots_groups_settled_results(self) -> None:
        snapshot_date = datetime(2026, 7, 2, tzinfo=UTC).date()
        connection = FakeConnection(
            results=[
                FakeCursor(
                    many=[
                        {"scope": "overall", "scope_id": None, "total_predictions": 3, "correct": 2},
                        {"scope": "league", "scope_id": "39", "total_predictions": 3, "correct": 2},
                        {"scope": "market", "scope_id": "1x2", "total_predictions": 2, "correct": 1},
                        {"scope": "market", "scope_id": "btts", "total_predictions": 1, "correct": 1},
                        {"scope": "market_tier", "scope_id": "1x2:medium", "total_predictions": 2, "correct": 1},
                        {"scope": "market_tier", "scope_id": "btts:low", "total_predictions": 1, "correct": 1},
                    ]
                )
            ]
        )

        snapshots = AccuracySnapshotRepository(connection).compute_and_save_snapshots(snapshot_date=snapshot_date)

        self.assertEqual(
            [(row.scope, row.scope_id, row.total_predictions, row.correct) for row in snapshots],
            [
                ("overall", None, 3, 2),
                ("league", "39", 3, 2),
                ("market", "1x2", 2, 1),
                ("market", "btts", 1, 1),
                ("market_tier", "1x2:medium", 2, 1),
                ("market_tier", "btts:low", 1, 1),
            ],
        )
        self.assertEqual(len(connection.executed), 7)
        self.assertIn("FROM prediction_results", connection.executed[0][0])
        self.assertIn("prediction_accuracy_daily", connection.executed[0][0])
        self.assertIn("PARTITION BY predictions.fixture_id, predictions.market", connection.executed[0][0])
        self.assertIn("FROM engine_predictions AS predictions", connection.executed[0][0])
        self.assertIn("predictions.generated_at <= fixtures.kickoff_at", connection.executed[0][0])
        self.assertTrue(connection.committed)

    def test_retention_aggregates_before_pruning_in_one_statement(self) -> None:
        raw_cutoff = datetime(2025, 7, 2, tzinfo=UTC)
        connection = FakeConnection(
            results=[
                FakeCursor(one={"results_examined": 12, "snapshots_written": 4, "results_pruned": 12})
            ]
        )

        stats = PredictionRetentionRepository(connection).aggregate_and_prune(
            raw_cutoff=raw_cutoff,
            batch_limit=1000,
        )

        self.assertEqual((stats.results_examined, stats.snapshots_written, stats.results_pruned), (12, 4, 12))
        sql, params = connection.executed[0]
        self.assertLess(sql.index("INSERT INTO prediction_accuracy_daily"), sql.index("DELETE FROM prediction_results"))
        self.assertIn("ON CONFLICT", sql)
        self.assertIn("FOR UPDATE OF results SKIP LOCKED", sql)
        self.assertLess(sql.index("WITH candidate_ids AS MATERIALIZED"), sql.index("LEFT JOIN LATERAL"))
        self.assertIn("eligible.generated_at <= fixtures.kickoff_at", sql)
        self.assertIn("ORDER BY eligible.generated_at DESC, eligible.public_id DESC", sql)
        self.assertIn("INSERT INTO prediction_result_rollup_ledger", sql)
        self.assertIn("COALESCE(accuracy_rank = 1, FALSE)", sql)
        self.assertIn("AND NOT already_rolled_up", sql)
        self.assertEqual(params, {"raw_cutoff": raw_cutoff, "batch_limit": 1000})
        self.assertTrue(connection.committed)

    def test_maps_db_fixture_columns_to_python_feature_record(self) -> None:
        kickoff = datetime(2026, 7, 1, 15, 0, tzinfo=UTC)

        record = map_fixture_record(
            {
                "id": 42,
                "kickoff_at": kickoff,
                "league_id": 39,
                "league_name": "Premier League",
                "season_id": 2026,
                "home_team_id": 10,
                "away_team_id": 20,
                "home_goals_ft": 2,
                "away_goals_ft": 1,
                "status": "FT",
            }
        )

        self.assertEqual(
            record,
            FixtureRecord(
                id=42,
                kickoff_at=kickoff,
                league_id=39,
                league_name="Premier League",
                season=2026,
                home_team_id=10,
                away_team_id=20,
                home_goals=2,
                away_goals=1,
                status="FT",
            ),
        )

    def test_maps_db_statistics_columns_to_python_stats_record(self) -> None:
        stats = map_team_fixture_stats(
            {
                "fixture_id": 42,
                "team_id": 10,
                "shots_total": 12,
                "shots_on_target": 5,
                "corners": 7,
                "fouls": 11,
                "card_points": 3,
                "possession_pct": 55.5,
                "xg": 1.25,
            }
        )

        self.assertEqual(
            stats,
            TeamFixtureStats(
                fixture_id=42,
                team_id=10,
                shots_total=12,
                shots_on_target=5,
                corners=7,
                fouls=11,
                card_points=3,
                possession=55.5,
                xg=1.25,
            ),
        )

    def test_maps_latest_one_x_two_odds_rows_to_odds_snapshot(self) -> None:
        snapshot = map_odds_snapshot(
            fixture_id=42,
            rows=[
                {"outcome": "Home", "value": 1.90},
                {"outcome": "Draw", "value": 3.40},
                {"outcome": "Away", "value": 4.20},
            ],
        )

        self.assertEqual(snapshot, OddsSnapshot(fixture_id=42, home_win=1.90, draw=3.40, away_win=4.20))

    def test_rejects_incomplete_one_x_two_odds_rows(self) -> None:
        with self.assertRaisesRegex(ValueError, "missing 1X2 odds outcome"):
            map_odds_snapshot(
                fixture_id=42,
                rows=[
                    {"outcome": "Home", "value": 1.90},
                    {"outcome": "Draw", "value": 3.40},
                ],
            )


class FakeConnection:
    def __init__(self, fail_on_call: int | None = None, results: list["FakeCursor"] | None = None) -> None:
        self.fail_on_call = fail_on_call
        self.results = results or []
        self.executed: list[tuple[str, dict[str, object]]] = []
        self.committed = False
        self.rolled_back = False

    def execute(self, sql: str, params: dict[str, object]) -> "FakeCursor":
        if self.fail_on_call == len(self.executed) + 1:
            raise RuntimeError("database write failed")
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


def prediction(generated_at: datetime) -> EnginePrediction:
    return EnginePrediction(
        fixture_id=42,
        market="1x2",
        prediction="home",
        probability=0.62,
        confidence=0.68,
        confidence_tier=ConfidenceTier.MEDIUM,
        is_best_bet=True,
        lifecycle_stage=PredictionLifecycleStage.PRE_MATCH,
        data_completeness=0.81,
        features_used={"model_a_score_matrix": [[0.6, 0.4]], "kickoff_at": generated_at.isoformat(), "league_id": 39},
        explanation="The data suggests the home side is favored.",
        generated_at=generated_at,
        gate_tier=GateTier.FULL,
        horizon_eligible=True,
        embargo_active=False,
        model_version="python-engine-v1",
    )


def in_play_prediction(generated_at: datetime, market: str) -> EnginePrediction:
    return replace(
        prediction(generated_at),
        market=market,
        prediction="over",
        lifecycle_stage=PredictionLifecycleStage.IN_PLAY_2H,
        is_best_bet=False,
        gate_tier=GateTier.BASIC,
        data_cutoff_at=generated_at,
        match_minute=60,
        period="SECOND_HALF",
    )


if __name__ == "__main__":
    unittest.main()
