from __future__ import annotations

from datetime import UTC, datetime
import unittest

from prediction_engine.features import FeatureAssembler, FixtureRecord, OddsSnapshot, TeamFixtureStats


class FeatureAssemblerTests(unittest.TestCase):
    def test_assembles_recent_form_home_away_h2h_odds_and_completeness(self) -> None:
        assembler = FeatureAssembler(form_windows=(3, 5), decay_rate=0.8)
        target = fixture(99, "2026-07-01", 10, 20, 1, "Premier League")
        history = [
            fixture(1, "2026-06-01", 10, 30, 1, "Premier League", 2, 0),
            fixture(2, "2026-06-05", 31, 10, 1, "Premier League", 1, 1),
            fixture(3, "2026-06-10", 10, 20, 1, "Premier League", 3, 1),
            fixture(4, "2026-06-15", 32, 10, 1, "Premier League", 0, 2),
            fixture(5, "2026-06-20", 10, 33, 1, "Premier League", 1, 0),
            fixture(6, "2026-06-02", 20, 34, 1, "Premier League", 1, 2),
            fixture(7, "2026-06-07", 35, 20, 1, "Premier League", 2, 2),
            fixture(8, "2026-06-12", 20, 10, 1, "Premier League", 0, 1),
            fixture(9, "2026-06-17", 36, 20, 1, "Premier League", 3, 0),
            fixture(10, "2026-06-22", 20, 37, 1, "Premier League", 2, 1),
        ]
        stats = {
            record.id: [
                TeamFixtureStats(
                    fixture_id=record.id,
                    team_id=record.home_team_id,
                    shots_total=10,
                    shots_on_target=5,
                    corners=4,
                    fouls=10,
                    card_points=2,
                    possession=55.0,
                    xg=1.4,
                ),
                TeamFixtureStats(
                    fixture_id=record.id,
                    team_id=record.away_team_id,
                    shots_total=9,
                    shots_on_target=4,
                    corners=3,
                    fouls=9,
                    card_points=3,
                    possession=45.0,
                    xg=1.1,
                ),
            ]
            for record in history
        }
        odds = OddsSnapshot(fixture_id=99, home_win=1.90, draw=3.40, away_win=4.20)

        features = assembler.assemble(target, history, stats, as_of_timestamp=target.kickoff_at, odds=odds)

        self.assertEqual(features.fixture_id, 99)
        self.assertEqual(features.home_recent["sample_size_5"], 5)
        self.assertEqual(features.away_recent["sample_size_5"], 5)
        self.assertGreater(features.home_recent["points_per_match_5"], features.away_recent["points_per_match_5"])
        self.assertEqual(features.h2h["sample_size"], 2)
        self.assertEqual(features.statistics["home_shots_on_target"], 4.5)
        self.assertEqual(features.statistics["away_shots_on_target"], 4.5)
        self.assertEqual(features.statistics["home_card_points"], 2.5)
        self.assertEqual(features.statistics["away_card_points"], 2.5)
        self.assertEqual(features.statistics["home_possession"], 50.0)
        self.assertEqual(features.statistics["away_possession"], 50.0)
        self.assertAlmostEqual(sum(features.odds_implied_1x2.values()), 1.0)
        self.assertEqual(features.fallback_flags, ())
        self.assertGreaterEqual(features.data_completeness, 0.90)

    def test_invalid_provider_odds_placeholder_is_treated_as_missing(self) -> None:
        assembler = FeatureAssembler(form_windows=(1,), decay_rate=0.85)
        target = fixture(99, "2026-07-01", 10, 20, 1, "Premier League")
        odds = OddsSnapshot(fixture_id=99, home_win=1.0, draw=3.40, away_win=4.20)

        features = assembler.assemble(
            target,
            history=[],
            statistics_by_fixture={},
            as_of_timestamp=target.kickoff_at,
            odds=odds,
        )

        self.assertEqual(features.odds_implied_1x2, {})
        self.assertIn("odds_missing", features.fallback_flags)

    def test_sparse_data_sets_fallback_flags_and_lower_completeness(self) -> None:
        assembler = FeatureAssembler(form_windows=(5, 10), decay_rate=0.85)
        target = fixture(99, "2026-07-01", 10, 20, 1, "League Two")
        history = [
            fixture(1, "2026-06-01", 10, 30, 1, "League Two", 1, 1),
            fixture(2, "2026-06-02", 20, 31, 1, "League Two", 0, 0),
        ]

        features = assembler.assemble(
            target,
            history,
            statistics_by_fixture={},
            as_of_timestamp=target.kickoff_at,
            odds=None,
        )

        self.assertIn("home_recent_sample_insufficient", features.fallback_flags)
        self.assertIn("away_recent_sample_insufficient", features.fallback_flags)
        self.assertIn("h2h_insufficient", features.fallback_flags)
        self.assertIn("odds_missing", features.fallback_flags)
        self.assertIn("statistics_missing", features.fallback_flags)
        self.assertLess(features.data_completeness, 0.70)

    def test_all_null_fixture_statistics_count_as_missing(self) -> None:
        assembler = FeatureAssembler(form_windows=(1,), decay_rate=0.85)
        target = fixture(99, "2026-07-01", 10, 20, 1, "League")
        history = [fixture(1, "2026-06-01", 10, 20, 1, "League", 1, 0)]
        statistics = {
            1: [
                TeamFixtureStats(fixture_id=1, team_id=10),
                TeamFixtureStats(fixture_id=1, team_id=20),
            ]
        }

        features = assembler.assemble(
            target,
            history,
            statistics_by_fixture=statistics,
            as_of_timestamp=target.kickoff_at,
        )

        self.assertEqual(features.statistics, {})
        self.assertEqual(features.group_completeness["statistics"], 0.0)
        self.assertIn("statistics_missing", features.fallback_flags)

    def test_partial_fixture_statistics_preserve_missing_metrics_and_report_fractional_completeness(self) -> None:
        assembler = FeatureAssembler(form_windows=(1,), decay_rate=0.85)
        target = fixture(99, "2026-07-01", 10, 20, 1, "League")
        history = [fixture(1, "2026-06-01", 10, 20, 1, "League", 1, 0)]
        statistics = {
            1: [
                TeamFixtureStats(fixture_id=1, team_id=10, possession=55.0),
                TeamFixtureStats(fixture_id=1, team_id=20, possession=45.0),
            ]
        }

        features = assembler.assemble(
            target,
            history,
            statistics_by_fixture=statistics,
            as_of_timestamp=target.kickoff_at,
        )

        self.assertIsNone(features.statistics["home_corners_for"])
        self.assertIsNone(features.statistics["away_shots_total"])
        self.assertEqual(features.statistics["home_possession"], 55.0)
        self.assertEqual(features.statistics["away_possession"], 45.0)
        self.assertAlmostEqual(features.group_completeness["statistics"], 1 / 7)
        self.assertEqual(features.statistic_sample_sizes["home_possession"], 1)
        self.assertEqual(features.statistic_sample_sizes["home_corners_for"], 0)

    def test_league_baseline_is_cached_per_run_and_reused(self) -> None:
        assembler = FeatureAssembler(form_windows=(2,), decay_rate=0.85)
        history = [
            fixture(1, "2026-06-01", 10, 30, 7, "Championship", 2, 1),
            fixture(2, "2026-06-02", 20, 31, 7, "Championship", 0, 2),
        ]

        as_of = datetime(2026, 7, 1, tzinfo=UTC)
        first = assembler.league_baseline(7, 2026, history, as_of_timestamp=as_of)
        second = assembler.league_baseline(7, 2026, history, as_of_timestamp=as_of)

        self.assertIs(first, second)
        self.assertAlmostEqual(first.home_goals_per_match, 1.0)
        self.assertAlmostEqual(first.away_goals_per_match, 1.5)

    def test_early_season_uses_prior_season_history_and_prior_league_baseline(self) -> None:
        assembler = FeatureAssembler(form_windows=(3, 5), decay_rate=0.85)
        target = fixture(99, "2026-08-01", 10, 20, 7, "Championship")
        current = fixture(1, "2026-07-20", 10, 30, 7, "Championship", 1, 0)
        prior = FixtureRecord(
            id=2,
            kickoff_at=datetime(2025, 5, 1, tzinfo=UTC),
            league_id=7,
            league_name="Championship",
            season=2025,
            home_team_id=20,
            away_team_id=31,
            home_goals=2,
            away_goals=1,
            status="FT",
        )

        features = assembler.assemble(
            target,
            [current, prior],
            statistics_by_fixture={},
            as_of_timestamp=target.kickoff_at,
        )

        self.assertEqual(features.home_recent["current_season_sample_size"], 1)
        self.assertEqual(features.away_recent["prior_season_sample_size"], 1)
        self.assertGreater(features.league_baseline.total_goals_per_match, 0)
        self.assertIn("early_season", features.fallback_flags)
        self.assertIn("prior_season_fallback_used", features.fallback_flags)

    def test_cross_competition_prior_season_form_is_tier_adjusted(self) -> None:
        assembler = FeatureAssembler(form_windows=(1,), decay_rate=0.85)
        target = fixture(99, "2026-08-01", 10, 20, 39, "Premier League")
        promoted_team_win = FixtureRecord(
            id=2,
            kickoff_at=datetime(2025, 5, 1, tzinfo=UTC),
            league_id=40,
            league_name="Championship",
            season=2025,
            home_team_id=10,
            away_team_id=31,
            home_goals=4,
            away_goals=0,
            status="FT",
        )

        features = assembler.assemble(target, [promoted_team_win], {}, as_of_timestamp=target.kickoff_at)

        self.assertEqual(features.home_recent["prior_season_sample_size"], 1)
        self.assertLess(features.home_recent["points_per_match_1"], 3.0)
        self.assertIn("tier_adjusted_prior_season_fallback_used", features.fallback_flags)

    def test_structurally_missing_h2h_is_redistributed_without_copying_statistics(self) -> None:
        assembler = FeatureAssembler(form_windows=(1,), decay_rate=0.85)
        target = fixture(99, "2026-07-01", 10, 20, 1, "League")
        history = [
            fixture(1, "2026-06-01", 10, 30, 1, "League", 1, 0),
            fixture(2, "2026-06-02", 20, 31, 1, "League", 2, 0),
        ]
        statistics = {
            1: [TeamFixtureStats(fixture_id=1, team_id=10, shots_total=10)],
            2: [TeamFixtureStats(fixture_id=2, team_id=20, shots_total=8)],
        }

        features = assembler.assemble(
            target,
            history,
            statistics_by_fixture=statistics,
            as_of_timestamp=target.kickoff_at,
        )

        self.assertEqual(features.group_completeness["h2h"], 0.0)
        self.assertNotEqual(
            features.group_completeness["h2h"],
            features.group_completeness["statistics"],
        )
        self.assertIn("h2h_structurally_unavailable", features.fallback_flags)

    def test_finished_provider_status_counts_as_completed_history(self) -> None:
        assembler = FeatureAssembler(form_windows=(1,), decay_rate=0.85)
        target = fixture(99, "2026-07-01", 10, 20, 1, "League")
        history = [
            FixtureRecord(
                id=1,
                kickoff_at=datetime.fromisoformat("2026-06-01").replace(tzinfo=UTC),
                league_id=1,
                league_name="League",
                season=2026,
                home_team_id=10,
                away_team_id=20,
                home_goals=2,
                away_goals=1,
                status="finished",
            )
        ]

        features = assembler.assemble(
            target,
            history,
            statistics_by_fixture={},
            as_of_timestamp=target.kickoff_at,
        )

        self.assertEqual(features.home_recent["sample_size_1"], 1)
        self.assertEqual(features.away_recent["sample_size_1"], 1)

    def test_as_of_timestamp_is_mandatory_and_must_be_timezone_aware(self) -> None:
        assembler = FeatureAssembler(form_windows=(1,), decay_rate=0.85)
        target = fixture(99, "2026-07-01", 10, 20, 1, "League")

        with self.assertRaisesRegex(TypeError, "as_of_timestamp"):
            assembler.assemble(target, [], statistics_by_fixture={})  # type: ignore[call-arg]
        with self.assertRaisesRegex(ValueError, "timezone-aware"):
            assembler.assemble(
                target,
                [],
                statistics_by_fixture={},
                as_of_timestamp=datetime(2026, 7, 1),
            )

    def test_future_results_and_statistics_are_excluded_at_an_earlier_as_of(self) -> None:
        assembler = FeatureAssembler(form_windows=(1,), decay_rate=0.85)
        target = fixture(99, "2026-07-10", 10, 20, 1, "League")
        visible = fixture(1, "2026-07-01", 10, 20, 1, "League", 1, 0)
        future = fixture(2, "2026-07-05", 10, 20, 1, "League", 9, 9)
        statistics = {
            visible.id: [TeamFixtureStats(visible.id, 10, shots_total=10)],
            future.id: [TeamFixtureStats(future.id, 10, shots_total=100)],
        }

        features = assembler.assemble(
            target,
            [visible, future],
            statistics,
            as_of_timestamp=datetime(2026, 7, 3, tzinfo=UTC),
        )

        self.assertEqual(features.as_of_timestamp, datetime(2026, 7, 3, tzinfo=UTC))
        self.assertEqual(features.home_recent["sample_size_1"], 1)
        self.assertEqual(features.home_recent["goals_for_per_match_1"], 1)
        self.assertEqual(features.statistics["home_shots_total"], 10)


def fixture(
    fixture_id: int,
    kickoff_date: str,
    home_team_id: int,
    away_team_id: int,
    league_id: int,
    league_name: str,
    home_goals: int | None = None,
    away_goals: int | None = None,
) -> FixtureRecord:
    return FixtureRecord(
        id=fixture_id,
        kickoff_at=datetime.fromisoformat(kickoff_date).replace(tzinfo=UTC),
        league_id=league_id,
        league_name=league_name,
        season=2026,
        home_team_id=home_team_id,
        away_team_id=away_team_id,
        home_goals=home_goals,
        away_goals=away_goals,
        status="FT" if home_goals is not None and away_goals is not None else "NS",
    )


if __name__ == "__main__":
    unittest.main()
