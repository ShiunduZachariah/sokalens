"""`possession_winner` publishes a measured probability, not a certainty.

The market used to return 1.0 for whichever side's expected share was above
50%, clamped to the 0.99 maximum. A 51% expectation published as 99%. On the
copy of production data those rows were right 67% of the time.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
import unittest

from prediction_engine.features import FixtureRecord, OddsSnapshot, TeamFixtureStats
from prediction_engine.pipeline import FixtureEngineInput, _top_possession_market_selection, run_fixture_engine
from prediction_engine import shot_possession_markets
from prediction_engine.shot_possession_markets import possession_market


class PossessionMarketTests(unittest.TestCase):
    def test_a_51_percent_expectation_is_close_to_a_coin_flip(self) -> None:
        result = possession_market(expected_home_possession_pct=51.0)

        self.assertLess(result.side_probabilities["home_more"], 0.60)
        self.assertGreater(result.side_probabilities["home_more"], result.side_probabilities["away_more"])

    def test_the_favourite_is_not_certain_at_any_ordinary_expectation(self) -> None:
        for expected in (51.0, 55.0, 60.0, 65.0):
            with self.subTest(expected=expected):
                result = possession_market(expected_home_possession_pct=expected)
                self.assertLess(result.side_probabilities["home_more"], 0.95)
                self.assertGreater(result.side_probabilities["away_more"], 0.0)

    def test_the_three_outcomes_are_a_distribution(self) -> None:
        for expected in (20.0, 33.0, 47.5, 50.0, 58.0, 80.0):
            with self.subTest(expected=expected):
                sides = possession_market(expected_home_possession_pct=expected).side_probabilities
                self.assertAlmostEqual(sum(sides.values()), 1.0, places=6)
                self.assertTrue(all(0.0 < value < 1.0 for value in sides.values()))

    def test_the_probability_rises_with_the_expected_share(self) -> None:
        values = [
            possession_market(expected_home_possession_pct=expected).side_probabilities["home_more"]
            for expected in (40.0, 45.0, 50.0, 55.0, 60.0)
        ]

        self.assertEqual(values, sorted(values))

    def test_the_two_sides_are_mirror_images(self) -> None:
        home_favoured = possession_market(expected_home_possession_pct=58.0).side_probabilities
        away_favoured = possession_market(expected_home_possession_pct=42.0).side_probabilities

        self.assertAlmostEqual(home_favoured["home_more"], away_favoured["away_more"], places=5)
        self.assertAlmostEqual(home_favoured["level"], away_favoured["level"], places=5)

    def test_an_even_expectation_splits_the_sides_and_leaves_room_for_level(self) -> None:
        sides = possession_market(expected_home_possession_pct=50.0).side_probabilities

        self.assertAlmostEqual(sides["home_more"], sides["away_more"], places=5)
        self.assertGreater(sides["level"], 0.0)
        self.assertLess(sides["level"], 0.10)

    def test_the_measured_curve_is_pinned(self) -> None:
        # From docs/prediction-engine/possession-market.md. A change here means
        # the parameter moved, and the evidence has to move with it.
        for expected, probability in ((52.0, 0.56), (55.0, 0.68), (60.0, 0.84)):
            with self.subTest(expected=expected):
                sides = possession_market(expected_home_possession_pct=expected).side_probabilities
                self.assertAlmostEqual(sides["home_more"], probability, delta=0.01)

    def test_an_expectation_beyond_the_evidence_does_not_extrapolate(self) -> None:
        at_edge = possession_market(expected_home_possession_pct=67.0).side_probabilities["home_more"]
        beyond = possession_market(expected_home_possession_pct=95.0).side_probabilities["home_more"]

        self.assertEqual(beyond, at_edge)
        self.assertLess(beyond, 0.97)

    def test_bands_are_a_real_distribution_rather_than_a_poisson_of_a_percentage(self) -> None:
        bands = possession_market(expected_home_possession_pct=55.0).band_probabilities

        self.assertAlmostEqual(sum(bands.values()), 1.0, places=6)
        self.assertLess(bands["51-100"], 0.8)
        self.assertEqual(possession_market(expected_home_possession_pct=55.0).method, "possession_share_beta")

    def test_invalid_expectations_are_refused(self) -> None:
        for value in (-1.0, float("nan"), float("inf")):
            with self.subTest(value=value), self.assertRaises(ValueError):
                possession_market(expected_home_possession_pct=value)

    def test_the_expected_share_reads_both_teams(self) -> None:
        # Possession is one number split between two sides. A 60% side meeting a
        # 60% side is an even game, not a 60% home expectation.
        self.assertEqual(shot_possession_markets.expected_home_possession_share(60.0, 60.0), 50.0)
        self.assertEqual(shot_possession_markets.expected_home_possession_share(55.0, 45.0), 55.0)
        self.assertEqual(shot_possession_markets.expected_home_possession_share(40.0, 60.0), 40.0)

    def test_a_corrupt_average_cannot_push_the_share_out_of_range(self) -> None:
        self.assertEqual(shot_possession_markets.expected_home_possession_share(400.0, 0.0), 100.0)
        self.assertEqual(shot_possession_markets.expected_home_possession_share(0.0, 400.0), 0.0)

    def test_the_published_selection_keeps_the_real_probability(self) -> None:
        market, selection, probability = _top_possession_market_selection(
            possession_market(expected_home_possession_pct=56.0)
        )

        self.assertEqual((market, selection), ("possession_winner", "home_more"))
        self.assertLess(probability, 0.80)


class PossessionPipelineTests(unittest.TestCase):
    def published(self, home_share: float, away_share: float):
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
        history: list[FixtureRecord] = []
        statistics: dict[int, list[TeamFixtureStats]] = {}
        for index in range(1, 21):
            home_row = index % 2 == 0
            record = FixtureRecord(
                id=index,
                kickoff_at=now - timedelta(days=index),
                league_id=39,
                league_name="Premier League",
                season=2026,
                home_team_id=10 if home_row else 20,
                away_team_id=100 + index,
                home_goals=2,
                away_goals=1,
                status="FT",
            )
            history.append(record)
            team = 10 if home_row else 20
            share = home_share if home_row else away_share
            statistics[index] = [
                TeamFixtureStats(fixture_id=index, team_id=team, corners=5, shots_total=12, shots_on_target=4, fouls=11, card_points=2, possession=share, xg=1.2),
                TeamFixtureStats(fixture_id=index, team_id=100 + index, corners=4, shots_total=9, shots_on_target=3, fouls=12, card_points=2, possession=100 - share, xg=0.9),
            ]
        rows = run_fixture_engine(
            FixtureEngineInput(
                as_of_timestamp=now,
                target_fixture=target,
                history=history,
                statistics_by_fixture=statistics,
                odds=OddsSnapshot(fixture_id=42, home_win=2.1, draw=3.3, away_win=3.4),
                generated_at=now,
                coverage={"has_statistics": True},
                market_accuracy={},
                league_accuracy=0.5,
            )
        )
        return next(row for row in rows if row.market == "possession_winner")

    def test_a_51_percent_expectation_no_longer_publishes_at_the_clamp(self) -> None:
        row = self.published(51.0, 49.0)

        self.assertEqual(row.prediction, "home_more")
        self.assertLess(row.probability, 0.60)

    def test_the_published_selection_uses_the_opponent_too(self) -> None:
        # Home averages 55%, but away averages 65%: away is the favourite. The
        # old input read only the home side's average and picked home.
        row = self.published(55.0, 65.0)

        self.assertEqual(row.prediction, "away_more")
        self.assertLess(row.probability, 0.80)


if __name__ == "__main__":
    unittest.main()
