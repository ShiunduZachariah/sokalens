from __future__ import annotations

from datetime import UTC, datetime, timedelta
import unittest

from prediction_engine.features import FixtureRecord, OddsSnapshot, TeamFixtureStats
from prediction_engine.persistence import ConfidenceTier, GateTier
from prediction_engine.config import default_config
from prediction_engine.pipeline import BackfillDeferred, FixtureEngineInput, _published_result_markets, _score_consistent_candidates, _selection_matches_score, _tier_a_outcome, _top_total_goals_2_5_selection, run_and_save_fixture, run_fixture, run_fixture_engine


class PipelineTests(unittest.TestCase):
    def test_score_consistency_gate_rejects_every_contradictory_public_market(self) -> None:
        candidates = [
            ("1x2", "away", 0.54),
            ("btts", "no", 0.60),
            ("total_goals", "under_2.5", 0.58),
            ("btts_total_goals", "yes_over_0.5", 0.51),
            ("btts_match_result", "away_yes", 0.42),
            ("result_total_goals", "home_over_2.5", 0.41),
            ("correct_score", "1-1", 0.20),
            ("total_cards", "over_3.5", 0.65),
        ]

        filtered = _score_consistent_candidates(candidates)

        self.assertEqual(
            {(market, selection) for market, selection, _ in filtered},
            {("1x2", "away"), ("btts", "no"), ("total_goals", "under_2.5"), ("total_cards", "over_3.5")},
        )

    def test_no_goals_is_only_true_of_a_goalless_score(self) -> None:
        # The check compared against "no_goal", while the market and settlement
        # both say "no_goals", so the selection read as "some goal was scored".
        self.assertTrue(_selection_matches_score("team_to_score_first", "no_goals", 0, 0))
        self.assertFalse(_selection_matches_score("team_to_score_first", "no_goals", 1, 0))
        self.assertFalse(_selection_matches_score("team_to_score_first", "no_goals", 0, 2))

    def test_a_first_scorer_has_to_score(self) -> None:
        self.assertTrue(_selection_matches_score("team_to_score_first", "home", 1, 1))
        self.assertFalse(_selection_matches_score("team_to_score_first", "home", 0, 1))
        self.assertTrue(_selection_matches_score("team_to_score_first", "away", 1, 1))
        self.assertFalse(_selection_matches_score("team_to_score_first", "away", 1, 0))
        self.assertFalse(_selection_matches_score("team_to_score_first", "home", 0, 0))

    def test_a_goalless_story_keeps_its_first_scorer_market(self) -> None:
        # A draw with no BTTS can only be 0-0. The mismatch rejected every
        # selection here, so the market vanished from the fixture.
        filtered = _score_consistent_candidates(
            [("1x2", "draw", 0.40), ("btts", "no", 0.70), ("team_to_score_first", "no_goals", 0.30)]
        )

        self.assertIn(("team_to_score_first", "no_goals", 0.30), filtered)

    def test_no_goals_does_not_sit_beside_a_winner(self) -> None:
        # The mismatch let "no goals" stand next to "home wins".
        filtered = _score_consistent_candidates(
            [("1x2", "home", 0.45), ("team_to_score_first", "no_goals", 0.40)],
            alternatives={
                "team_to_score_first": [
                    ("team_to_score_first", "no_goals", 0.40),
                    ("team_to_score_first", "home", 0.35),
                    ("team_to_score_first", "away", 0.25),
                ]
            },
        )

        self.assertIn(("team_to_score_first", "home", 0.35), filtered)
        self.assertNotIn(("team_to_score_first", "no_goals", 0.40), filtered)

    def test_an_exact_score_cannot_contradict_the_first_scorer(self) -> None:
        filtered = _score_consistent_candidates(
            [("team_to_score_first", "home", 0.55), ("correct_score", "0-1", 0.12)],
            alternatives={
                "correct_score": [
                    ("correct_score", "0-1", 0.12),
                    ("correct_score", "1-1", 0.11),
                ]
            },
        )

        self.assertIn(("correct_score", "1-1", 0.11), filtered)
        self.assertNotIn(("correct_score", "0-1", 0.12), filtered)

    def test_total_goals_2_5_uses_the_stronger_model_side(self) -> None:
        self.assertEqual(
            _top_total_goals_2_5_selection({"over_2.5": 0.42, "under_2.5": 0.58}),
            ("total_goals", "under_2.5", 0.58),
        )
        self.assertEqual(
            _top_total_goals_2_5_selection({"over_2.5": 0.63, "under_2.5": 0.37}),
            ("total_goals", "over_2.5", 0.63),
        )

    def test_draw_no_bet_cannot_favour_the_side_the_1x2_has_against_it_without_odds(self) -> None:
        # The form adjustment moved the 1X2 to the away side; the raw markets beside
        # it still favour the home side. The score consistency gate would keep the
        # raw number on the away pick, publishing draw no bet "away" at 44%.
        markets = {
            "match_result": {"home": 0.34, "draw": 0.28, "away": 0.38},
            "double_chance": {"home_or_draw": 0.70, "draw_or_away": 0.58, "home_or_away": 0.72},
            "draw_no_bet": {"home": 0.56, "away": 0.44},
        }

        published = _published_result_markets(markets, {}, default_config().probability_clamp)

        self.assertEqual(published["match_result"], markets["match_result"])
        self.assertEqual(max(published["draw_no_bet"], key=published["draw_no_bet"].get), "away")
        self.assertAlmostEqual(published["draw_no_bet"]["away"], 0.38 / 0.72)
        self.assertAlmostEqual(published["double_chance"]["home_or_draw"], 0.62)
        self.assertAlmostEqual(published["double_chance"]["draw_or_away"], 0.66)
        self.assertAlmostEqual(published["double_chance"]["home_or_away"], 0.72)

    def test_dependent_markets_follow_the_final_1x2_on_every_odds_path(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureRecord(
            id=42,
            kickoff_at=now + timedelta(hours=4),
            league_id=39,
            league_name="Premier League",
            season=2026,
            home_team_id=10,
            away_team_id=20,
            home_goals=None,
            away_goals=None,
            status="scheduled",
        )
        history = full_history(now)
        odds = OddsSnapshot(fixture_id=42, home_win=1.85, draw=3.60, away_win=4.10)

        for name, provisional in (("real odds", False), ("provisional odds", True)):
            with self.subTest(name):
                inputs = FixtureEngineInput(
                    as_of_timestamp=now,
                    target_fixture=target,
                    history=history,
                    statistics_by_fixture=statistics_for(history),
                    odds=odds,
                    odds_provisional=provisional,
                    generated_at=now,
                    coverage={"has_statistics": True},
                )
                markets = _tier_a_outcome(inputs, default_config()).tier_a_markets
                result = markets["match_result"]

                self.assertAlmostEqual(markets["double_chance"]["home_or_draw"], 1 - result["away"])
                self.assertAlmostEqual(markets["double_chance"]["draw_or_away"], 1 - result["home"])
                self.assertAlmostEqual(markets["double_chance"]["home_or_away"], 1 - result["draw"])
                self.assertAlmostEqual(markets["draw_no_bet"]["home"], result["home"] / (result["home"] + result["away"]))

    def test_pipeline_entry_point_requires_as_of_timestamp(self) -> None:
        with self.assertRaisesRegex(TypeError, "as_of_timestamp"):
            run_fixture(42, input_repository=object())  # type: ignore[call-arg]

    def test_run_and_save_fixture_loads_inputs_runs_engine_and_persists_rows(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureRecord(
            id=42,
            kickoff_at=now + timedelta(hours=4),
            league_id=39,
            league_name="Premier League",
            season=2026,
            home_team_id=10,
            away_team_id=20,
            home_goals=None,
            away_goals=None,
            status="scheduled",
        )
        history = full_history(now)
        input_repository = FakeInputRepository(
            FixtureEngineInput(
                as_of_timestamp=now,
                target_fixture=target,
                history=history,
                statistics_by_fixture=statistics_for(history),
                odds=OddsSnapshot(fixture_id=42, home_win=1.85, draw=3.60, away_win=4.10),
                generated_at=now,
            )
        )
        output_repository = FakeOutputRepository()

        rows = run_and_save_fixture(
            42,
            as_of_timestamp=now,
            input_repository=input_repository,
            output_repository=output_repository,
        )

        self.assertEqual(input_repository.loaded_fixture_id, 42)
        self.assertEqual(input_repository.loaded_as_of_timestamp, now)
        self.assertEqual(rows, output_repository.saved)
        self.assertGreater(len(output_repository.saved), 1)
        self.assertEqual(output_repository.saved[0].fixture_id, 42)

    def test_published_rows_use_the_stored_forecasts_and_say_so(self) -> None:
        from prediction_engine.challenger_markets import count_document, goal_document
        from prediction_engine.model_a import ModelAInputs, run_model_a
        from prediction_engine.promoted_markets import ModelForecasts

        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureRecord(
            id=42,
            kickoff_at=now + timedelta(hours=4),
            league_id=39,
            league_name="Premier League",
            season=2026,
            home_team_id=10,
            away_team_id=20,
            home_goals=None,
            away_goals=None,
            status="scheduled",
        )
        history = full_history(now)
        statistics = statistics_for(history)
        odds = OddsSnapshot(fixture_id=42, home_win=1.85, draw=3.60, away_win=4.10)

        def engine(forecasts: ModelForecasts | None) -> dict[str, object]:
            rows = run_fixture_engine(
                FixtureEngineInput(
                    now,
                    target,
                    history,
                    statistics,
                    odds,
                    now,
                    coverage={"has_statistics": True},
                    model_forecasts=forecasts,
                )
            )
            return {row.market: row for row in rows}

        def document(home: float, away: float) -> dict[str, dict[str, float]]:
            result = run_model_a(ModelAInputs(home_lambda=home, away_lambda=away))
            return goal_document(result.markets, result.score_matrix)

        champion = engine(None)
        self.assertEqual({row.features_used["probability_source"] for row in champion.values()}, {"champion"})
        self.assertEqual(next(iter(champion.values())).model_version, "python-engine-v3")

        counts: dict[str, dict[str, float]] = {}
        for market, mean in (
            ("total_corners", 10.0),
            ("team_corners_home", 5.2),
            ("team_corners_away", 4.8),
            ("total_cards", 4.2),
            ("total_fouls", 22.0),
            ("total_shots", 24.0),
            ("shots_on_target", 8.2),
        ):
            counts.update(count_document(market, mean, 0.02))
        model_b, model_c = document(0.7, 2.4), {**document(0.9, 2.1), **counts}
        promoted = engine(
            ModelForecasts(model_b=model_b, model_c=model_c, provenance={"model-c-x": now.isoformat()})
        )

        # Coverage does not move: the same markets publish, from different numbers.
        self.assertEqual(set(promoted), set(champion))
        for market in ("1x2", "double_chance", "draw_no_bet"):
            self.assertEqual(promoted[market].probability, champion[market].probability, market)
            self.assertEqual(promoted[market].features_used["probability_source"], "champion")
        self.assertEqual(promoted["total_goals"].features_used["probability_source"], "champion_b_c_mix")
        self.assertEqual(promoted["btts"].features_used["probability_source"], "champion_b_c_mix")
        self.assertEqual(promoted["half_time_result"].features_used["probability_source"], "model_b")
        self.assertEqual(promoted["half_time_result"].probability, max(model_b["half_time_result"].values()))
        if "total_corners" in promoted:
            self.assertEqual(promoted["total_corners"].features_used["probability_source"], "model_c")
        self.assertEqual(promoted["btts"].features_used["model_forecasts"], {"model-c-x": now.isoformat()})
        self.assertNotEqual(promoted["total_goals"].probability, champion["total_goals"].probability)

    def test_run_and_save_fixture_defers_when_history_backfill_is_pending_and_gate_has_no_signal(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureRecord(
            id=42,
            kickoff_at=now + timedelta(days=4),
            league_id=39,
            league_name="Premier League",
            season=2026,
            home_team_id=10,
            away_team_id=20,
            home_goals=None,
            away_goals=None,
            status="scheduled",
        )
        input_repository = FakeInputRepository(
            FixtureEngineInput(
                as_of_timestamp=now,
                target_fixture=target,
                history=[],
                statistics_by_fixture={},
                odds=None,
                generated_at=now,
            ),
            backfill_statuses=("pending",),
        )
        output_repository = FakeOutputRepository()

        with self.assertRaisesRegex(BackfillDeferred, "fixture 42"):
            run_and_save_fixture(
                42,
                as_of_timestamp=now,
                input_repository=input_repository,
                output_repository=output_repository,
            )

        self.assertEqual(input_repository.requested_matches_needed, 3)
        self.assertEqual(output_repository.saved, [])

    def test_future_fixture_uses_available_history_while_backfill_continues(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureRecord(42, now + timedelta(days=6), 39, "Premier League", 2026, 10, 20, None, None, "scheduled")
        history = full_history(now)
        repository = FakeInputRepository(
            FixtureEngineInput(now, target, history, statistics_for(history), None, now),
            backfill_statuses=("pending",),
        )
        output = FakeOutputRepository()

        rows = run_and_save_fixture(
            42,
            as_of_timestamp=now,
            input_repository=repository,
            output_repository=output,
        )

        self.assertTrue(rows)
        self.assertEqual(rows, output.saved)

    def test_imminent_fixture_does_not_wait_for_pending_history_backfill(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureRecord(42, now + timedelta(hours=4), 39, "Premier League", 2026, 10, 20, None, None, "scheduled")
        history = full_history(now)
        repository = FakeInputRepository(
            FixtureEngineInput(now, target, history, statistics_for(history), None, now),
            backfill_statuses=("pending",),
        )
        output = FakeOutputRepository()

        rows = run_and_save_fixture(
            42,
            as_of_timestamp=now,
            input_repository=repository,
            output_repository=output,
        )

        self.assertTrue(rows)
        self.assertEqual(rows, output.saved)

    def test_league_baseline_produces_basic_markets_when_both_teams_are_new(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureRecord(42, now + timedelta(hours=4), 39, "Premier League", 2026, 10, 20, None, None, "scheduled")
        history = [
            FixtureRecord(index, now - timedelta(days=index), 39, "Premier League", 2026, 100 + index, 200 + index, 2, 1, "FT")
            for index in range(1, 11)
        ]

        rows = run_fixture_engine(FixtureEngineInput(now, target, history, {}, None, now))

        markets = {row.market for row in rows}
        self.assertIn("1x2", markets)
        self.assertIn("total_goals", markets)
        self.assertTrue(all(row.gate_tier is GateTier.BASIC for row in rows))
        flags = rows[0].features_used["feature_vector"]["fallback_flags"]
        self.assertIn("home_league_baseline_fallback_used", flags)
        self.assertIn("away_league_baseline_fallback_used", flags)

    def test_run_fixture_engine_produces_current_engine_prediction_for_fixture(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureRecord(
            id=42,
            kickoff_at=now + timedelta(hours=4),
            league_id=39,
            league_name="Premier League",
            season=2026,
            home_team_id=10,
            away_team_id=20,
            home_goals=None,
            away_goals=None,
            status="scheduled",
        )

        history = full_history(now)

        rows = run_fixture_engine(
            FixtureEngineInput(
                as_of_timestamp=now,
                target_fixture=target,
                history=history,
                statistics_by_fixture=statistics_for(history),
                odds=OddsSnapshot(fixture_id=42, home_win=1.85, draw=3.60, away_win=4.10),
                generated_at=now,
                coverage={"has_statistics": True},
                market_accuracy={"1x2": 0.42, "total_goals": 0.76},
                league_accuracy=0.63,
            )
        )

        self.assertGreaterEqual(len(rows), 16)
        markets = {row.market for row in rows}
        self.assertIn("1x2", markets)
        self.assertIn("double_chance", markets)
        self.assertIn("btts", markets)
        self.assertIn("total_goals", markets)
        self.assertIn("correct_score", markets)
        self.assertIn("total_corners", markets)
        self.assertIn("team_corners_home", markets)
        self.assertIn("total_cards", markets)
        self.assertIn("total_fouls", markets)
        self.assertIn("total_shots", markets)
        self.assertIn("shots_on_target", markets)
        self.assertIn("possession_winner", markets)
        self.assertIn("btts_total_goals", markets)
        self.assertIn("btts_match_result", markets)
        self.assertIn("result_total_goals", markets)
        self.assertEqual(len({(row.market, row.prediction) for row in rows}), len(rows))

        row = next(item for item in rows if item.market == "1x2")
        self.assertEqual(row.fixture_id, 42)
        self.assertEqual(row.market, "1x2")
        self.assertIn(row.prediction, {"home", "draw", "away"})
        for prediction in rows:
            self.assertGreaterEqual(prediction.probability, 0.01)
            self.assertLessEqual(prediction.probability, 0.99)
            self.assertEqual(prediction.gate_tier, GateTier.FULL)
            self.assertEqual(prediction.features_used["kickoff_at"], target.kickoff_at.isoformat())
            self.assertEqual(prediction.features_used["league_id"], 39)
            self.assertEqual(prediction.features_used["as_of_timestamp"], now.isoformat())
        self.assertEqual(row.confidence_tier, ConfidenceTier.LOW)
        self.assertGreaterEqual(row.confidence, 0.0)
        self.assertLessEqual(row.confidence, 1.0)
        self.assertEqual(row.features_used["market_accuracy"], 0.42)
        self.assertEqual(row.features_used["league_accuracy"], 0.63)
        self.assertIn("model_a_score_matrix", row.features_used)

        total_goals = next(item for item in rows if item.market == "total_goals")
        # Under, not over: the home side averages 1.9 goals and the away side
        # 0.9, but naively adding those to 2.8 ignores who they were scored
        # against. Once each side is rated against the defences it actually
        # faced, the away side's better-than-average defence pulls the expected
        # total to roughly 2.1 in a league averaging 2.56 goals per match.
        self.assertEqual(total_goals.prediction, "under_2.5")
        self.assertGreater(total_goals.confidence, row.confidence)
        self.assertEqual(total_goals.features_used["market_accuracy"], 0.76)

    def test_direct_rich_statistics_override_stale_league_coverage_flag(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureRecord(42, now + timedelta(hours=4), 39, "Premier League", 2026, 10, 20, None, None, "scheduled")
        history = full_history(now)

        rows = run_fixture_engine(
            FixtureEngineInput(
                now,
                target,
                history,
                statistics_for(history),
                None,
                now,
                coverage={"has_statistics": False},
            )
        )

        markets = {row.market for row in rows}
        self.assertIn("total_corners", markets)
        self.assertIn("total_cards", markets)

    def test_market_specific_samples_are_not_suppressed_by_unrelated_rich_gaps(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureRecord(42, now + timedelta(hours=4), 39, "Premier League", 2026, 10, 20, None, None, "scheduled")
        history = full_history(now)
        sparse_ids = {1, 2, 3, 4, 5, 11, 12, 13}
        statistics = {
            fixture_id: rows
            for fixture_id, rows in statistics_for(history).items()
            if fixture_id in sparse_ids
        }

        rows = run_fixture_engine(FixtureEngineInput(now, target, history, statistics, None, now))

        markets = {row.market for row in rows}
        self.assertIn("total_corners", markets)
        self.assertIn("total_cards", markets)

    def test_provisional_odds_are_display_only_not_completeness_or_cross_validation(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureRecord(
            id=42,
            kickoff_at=now + timedelta(hours=4),
            league_id=39,
            league_name="Premier League",
            season=2026,
            home_team_id=10,
            away_team_id=20,
            home_goals=None,
            away_goals=None,
            status="scheduled",
        )
        history = full_history(now)

        rows = run_fixture_engine(
            FixtureEngineInput(
                as_of_timestamp=now,
                target_fixture=target,
                history=history,
                statistics_by_fixture=statistics_for(history),
                odds=OddsSnapshot(fixture_id=42, home_win=1.85, draw=3.60, away_win=4.10),
                odds_provisional=True,
                generated_at=now,
                coverage={"has_statistics": True},
            )
        )

        self.assertTrue(rows)
        feature_vector = rows[0].features_used["feature_vector"]
        self.assertEqual(feature_vector["group_completeness"]["odds"], 0.0)
        self.assertIn("provisional_odds_display_only", feature_vector["fallback_flags"])

    def test_run_fixture_engine_skips_possession_market_when_provider_omits_possession(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureRecord(
            id=42,
            kickoff_at=now + timedelta(hours=4),
            league_id=39,
            league_name="Premier League",
            season=2026,
            home_team_id=10,
            away_team_id=20,
            home_goals=None,
            away_goals=None,
            status="scheduled",
        )
        history = full_history(now)

        rows = run_fixture_engine(
            FixtureEngineInput(
                as_of_timestamp=now,
                target_fixture=target,
                history=history,
                statistics_by_fixture=statistics_for(history, include_possession=False),
                odds=OddsSnapshot(fixture_id=42, home_win=1.85, draw=3.60, away_win=4.10),
                generated_at=now,
                coverage={"has_statistics": True},
            )
        )

        markets = {row.market for row in rows}
        self.assertIn("total_shots", markets)
        self.assertIn("shots_on_target", markets)
        self.assertNotIn("possession_winner", markets)

    def test_partial_statistics_emit_only_markets_with_enough_metric_samples(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureRecord(
            id=42,
            kickoff_at=now + timedelta(hours=4),
            league_id=39,
            league_name="Premier League",
            season=2026,
            home_team_id=10,
            away_team_id=20,
            home_goals=None,
            away_goals=None,
            status="scheduled",
        )
        history = full_history(now)
        statistics = statistics_for(history, include_corners=False)

        rows = run_fixture_engine(
            FixtureEngineInput(
                as_of_timestamp=now,
                target_fixture=target,
                history=history,
                statistics_by_fixture=statistics,
                odds=OddsSnapshot(fixture_id=42, home_win=1.85, draw=3.60, away_win=4.10),
                generated_at=now,
                coverage={"has_statistics": True},
            )
        )

        markets = {row.market for row in rows}
        self.assertNotIn("total_corners", markets)
        self.assertNotIn("team_corners_home", markets)
        self.assertNotIn("team_corners_away", markets)
        self.assertIn("total_cards", markets)
        self.assertIn("total_fouls", markets)
        self.assertIn("total_shots", markets)
        self.assertIn("shots_on_target", markets)
        self.assertIn("possession_winner", markets)

    def test_possession_only_statistics_do_not_emit_unobserved_rate_markets(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureRecord(
            id=42,
            kickoff_at=now + timedelta(hours=4),
            league_id=39,
            league_name="Premier League",
            season=2026,
            home_team_id=10,
            away_team_id=20,
            home_goals=None,
            away_goals=None,
            status="scheduled",
        )
        history = full_history(now)

        rows = run_fixture_engine(
            FixtureEngineInput(
                as_of_timestamp=now,
                target_fixture=target,
                history=history,
                statistics_by_fixture=statistics_for(history, possession_only=True),
                odds=OddsSnapshot(fixture_id=42, home_win=1.85, draw=3.60, away_win=4.10),
                generated_at=now,
                coverage={"has_statistics": True},
            )
        )

        markets = {row.market for row in rows}
        self.assertTrue(markets)
        self.assertTrue(
            markets.isdisjoint(
                {
                    "total_corners",
                    "team_corners_home",
                    "team_corners_away",
                    "total_cards",
                    "total_fouls",
                    "total_shots",
                    "shots_on_target",
                }
            )
        )

    def test_basic_gate_emits_only_tier_a_markets(self) -> None:
        now = datetime(2026, 7, 1, 12, 0, tzinfo=UTC)
        target = FixtureRecord(
            id=42,
            kickoff_at=now + timedelta(hours=4),
            league_id=39,
            league_name="Premier League",
            season=2026,
            home_team_id=10,
            away_team_id=20,
            home_goals=None,
            away_goals=None,
            status="scheduled",
        )
        history = [
            fixture(100 + index, now - timedelta(days=20 - index), 30 + index, 10, 1, 0)
            for index in range(10)
        ] + [
            fixture(200 + index, now - timedelta(days=20 - index), 20, 40 + index, 1, 0)
            for index in range(10)
        ]

        rows = run_fixture_engine(
            FixtureEngineInput(
                as_of_timestamp=now,
                target_fixture=target,
                history=history,
                statistics_by_fixture={},
                odds=None,
                generated_at=now,
            )
        )

        self.assertEqual(
            {row.market for row in rows},
            {
                "1x2",
                "double_chance",
                "draw_no_bet",
                "btts",
                "total_goals",
                "half_time_result",
                "highest_scoring_half",
                "team_to_score_first",
                "odd_even_total_goals",
                "correct_score",
            },
        )
        self.assertTrue(rows)
        self.assertTrue(all(row.gate_tier is GateTier.BASIC for row in rows))


def fixture(
    fixture_id: int,
    kickoff_at: datetime,
    home_team_id: int,
    away_team_id: int,
    home_goals: int,
    away_goals: int,
) -> FixtureRecord:
    return FixtureRecord(
        id=fixture_id,
        kickoff_at=kickoff_at,
        league_id=39,
        league_name="Premier League",
        season=2026,
        home_team_id=home_team_id,
        away_team_id=away_team_id,
        home_goals=home_goals,
        away_goals=away_goals,
        status="FT",
    )


def full_history(now: datetime) -> list[FixtureRecord]:
    return [
        fixture(1, now - timedelta(days=20), 10, 20, 2, 0),
        fixture(2, now - timedelta(days=19), 20, 10, 1, 2),
        fixture(3, now - timedelta(days=18), 10, 30, 2, 0),
        fixture(4, now - timedelta(days=17), 31, 10, 1, 1),
        fixture(5, now - timedelta(days=16), 10, 32, 3, 1),
        fixture(6, now - timedelta(days=15), 33, 10, 0, 2),
        fixture(7, now - timedelta(days=14), 10, 34, 1, 0),
        fixture(8, now - timedelta(days=13), 35, 10, 1, 3),
        fixture(9, now - timedelta(days=12), 10, 36, 2, 2),
        fixture(10, now - timedelta(days=11), 37, 10, 0, 1),
        fixture(11, now - timedelta(days=18), 20, 38, 0, 2),
        fixture(12, now - timedelta(days=17), 39, 20, 1, 1),
        fixture(13, now - timedelta(days=16), 20, 40, 2, 1),
        fixture(14, now - timedelta(days=15), 41, 20, 3, 0),
        fixture(15, now - timedelta(days=14), 20, 42, 1, 2),
        fixture(16, now - timedelta(days=13), 43, 20, 2, 2),
        fixture(17, now - timedelta(days=12), 20, 44, 1, 0),
        fixture(18, now - timedelta(days=11), 45, 20, 2, 1),
    ]


def statistics_for(
    history: list[FixtureRecord],
    *,
    include_possession: bool = True,
    include_corners: bool = True,
    possession_only: bool = False,
) -> dict[int, list[TeamFixtureStats]]:
    return {
        record.id: [
            TeamFixtureStats(
                fixture_id=record.id,
                team_id=record.home_team_id,
                shots_total=None if possession_only else 12,
                shots_on_target=None if possession_only else 5,
                corners=5 if include_corners and not possession_only else None,
                fouls=None if possession_only else 10,
                card_points=None if possession_only else 2,
                possession=54.0 if include_possession else None,
                xg=None if possession_only else 1.5,
            ),
            TeamFixtureStats(
                fixture_id=record.id,
                team_id=record.away_team_id,
                shots_total=None if possession_only else 9,
                shots_on_target=None if possession_only else 3,
                corners=4 if include_corners and not possession_only else None,
                fouls=None if possession_only else 11,
                card_points=None if possession_only else 3,
                possession=46.0 if include_possession else None,
                xg=None if possession_only else 1.0,
            ),
        ]
        for record in history
    }


class FakeInputRepository:
    def __init__(self, inputs: FixtureEngineInput, backfill_statuses: tuple[str, ...] = ()) -> None:
        self.inputs = inputs
        self.backfill_statuses = backfill_statuses
        self.loaded_fixture_id: int | None = None
        self.requested_matches_needed: int | None = None

    def load_fixture_inputs(self, fixture_id: int, *, as_of_timestamp: datetime) -> FixtureEngineInput:
        self.loaded_fixture_id = fixture_id
        self.loaded_as_of_timestamp = as_of_timestamp
        return self.inputs

    def ensure_history_backfill(
        self,
        inputs: FixtureEngineInput,
        *,
        matches_needed_by_feature_group: dict[str, int],
    ) -> tuple[str, ...]:
        self.requested_matches_needed = matches_needed_by_feature_group["results"]
        return self.backfill_statuses


class FakeOutputRepository:
    def __init__(self) -> None:
        self.saved = []
        self.provisional_odds = []

    def save_predictions(self, predictions) -> None:  # type: ignore[no-untyped-def]
        self.saved = predictions

    def write_provisional_odds(self, snapshot) -> None:  # type: ignore[no-untyped-def]
        self.provisional_odds.append(snapshot)


if __name__ == "__main__":
    unittest.main()
