"""Edge and negative cases for the strength ratings and the lambda derivation.

The ratings are fitted by iteration and divide by quantities derived from the
data, so the failure modes worth pinning are the degenerate inputs: nobody
scored, one team appears once, a whole league is goalless, a fixture has no
history at all. None of these may raise, produce a NaN, or emit a probability
outside a usable range -- a crash here takes down a prediction run, and a NaN
propagates silently into published output.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
import math
import unittest

from prediction_engine.features import FeatureAssembler, FixtureRecord, OddsSnapshot
from prediction_engine.league_strength import (
    MAX_LOG_STRENGTH,
    compute_league_strength,
    measure_home_advantage,
)
from prediction_engine.pipeline import (
    FixtureEngineInput,
    _expected_goals,
    _league_strength_scales,
    _team_strength_ratings,
    run_fixture_engine,
)
from prediction_engine.team_strength import (
    MAX_RATING,
    MIN_RATING,
    compute_team_strength,
)

LEAGUE = 39
NOW = datetime(2026, 9, 10, 12, 0, tzinfo=UTC)


def match(
    fixture_id: int,
    days_ago: int,
    home_team_id: int,
    away_team_id: int,
    home_goals: int | None,
    away_goals: int | None,
    league_id: int = LEAGUE,
    status: str = "FT",
) -> FixtureRecord:
    return FixtureRecord(
        id=fixture_id,
        kickoff_at=NOW - timedelta(days=days_ago),
        league_id=league_id,
        league_name="league",
        season=2026,
        home_team_id=home_team_id,
        away_team_id=away_team_id,
        home_goals=home_goals,
        away_goals=away_goals,
        status=status,
    )


def target(home_team_id: int = 1, away_team_id: int = 2) -> FixtureRecord:
    return FixtureRecord(
        id=99999,
        kickoff_at=NOW + timedelta(hours=6),
        league_id=LEAGUE,
        league_name="league",
        season=2026,
        home_team_id=home_team_id,
        away_team_id=away_team_id,
        home_goals=None,
        away_goals=None,
        status="scheduled",
    )


def assemble(history: list[FixtureRecord], fixture: FixtureRecord):
    league_table = compute_league_strength(history)
    home_advantage = measure_home_advantage(history, fixture.league_id)
    team_table = compute_team_strength(history, home_advantage=home_advantage)
    return FeatureAssembler(form_windows=(5, 10), decay_rate=0.85).assemble(
        fixture,
        history,
        {},
        as_of_timestamp=NOW,
        league_strength=_league_strength_scales(league_table, fixture),
        home_advantage=home_advantage,
        team_strength=_team_strength_ratings(team_table, fixture),
    )


def assert_usable(case: unittest.TestCase, home: float, away: float) -> None:
    for label, value in (("home", home), ("away", away)):
        case.assertTrue(math.isfinite(value), f"{label} lambda must be finite, got {value}")
        case.assertGreater(value, 0.0, f"{label} lambda must be positive")
        case.assertLess(value, 6.0, f"{label} lambda must stay in a football range")


class GoallessAndDegenerateTests(unittest.TestCase):
    def test_a_league_where_nobody_scores_does_not_divide_by_zero(self) -> None:
        history = [match(i, 100 - i, 1 + (i % 4), 5 + (i % 4), 0, 0) for i in range(1, 21)]

        ratings = compute_team_strength(history)
        home, away = _expected_goals(assemble(history, target()))

        # Every rating is the league average because every team is identical.
        self.assertAlmostEqual(ratings.for_team(1).attack, 1.0, delta=0.01)
        assert_usable(self, home, away)

    def test_a_team_that_never_scores_is_floored_not_zeroed(self) -> None:
        history = [match(i, 100 - i, 1, 5 + (i % 3), 0, 3) for i in range(1, 13)]
        history += [match(100 + i, 90 - i, 5 + (i % 3), 6 + (i % 3), 1, 1) for i in range(1, 13)]

        rating = compute_team_strength(history).for_team(1)
        home, away = _expected_goals(assemble(history, target()))

        self.assertGreaterEqual(rating.attack, MIN_RATING)
        # A zero rate would collapse the whole scoreline matrix.
        assert_usable(self, home, away)

    def test_a_single_lopsided_result_cannot_exceed_the_rating_ceiling(self) -> None:
        history = [match(i, 100 - i, 5 + (i % 3), 6 + (i % 3), 1, 1) for i in range(1, 21)]
        history.append(match(500, 2, 1, 5, 30, 0))

        rating = compute_team_strength(history).for_team(1)

        self.assertLessEqual(rating.attack, MAX_RATING)
        self.assertTrue(math.isfinite(rating.attack))

    def test_a_team_seen_once_still_produces_a_usable_fixture(self) -> None:
        history = [match(i, 100 - i, 5 + (i % 3), 6 + (i % 3), 2, 1) for i in range(1, 21)]
        history.append(match(600, 3, 1, 5, 1, 0))
        history.append(match(601, 3, 2, 6, 0, 1))

        home, away = _expected_goals(assemble(history, target()))

        assert_usable(self, home, away)

    def test_history_with_no_finished_matches_is_ignored(self) -> None:
        scheduled = [
            match(i, -i, 1, 2, None, None, status="scheduled") for i in range(1, 6)
        ]

        self.assertEqual(compute_team_strength(scheduled).ratings, {})
        self.assertEqual(compute_league_strength(scheduled).ratings, {})

    def test_a_fixture_with_no_history_at_all_does_not_raise(self) -> None:
        rows = run_fixture_engine(
            FixtureEngineInput(
                as_of_timestamp=NOW,
                target_fixture=target(),
                history=[],
                statistics_by_fixture={},
                odds=None,
                generated_at=NOW,
            )
        )

        # The quality gate is expected to withhold; the run must not crash.
        self.assertIsInstance(rows, list)


class BoundaryTests(unittest.TestCase):
    def test_league_strength_is_capped_against_an_absurd_margin(self) -> None:
        history: list[FixtureRecord] = []
        for index in range(30):
            history.append(match(index, 200 - index, 1, 2, 20, 0, league_id=1))
        for index in range(30):
            history.append(match(100 + index, 199 - index, 1, 3, 1, 1, league_id=1))
            history.append(match(200 + index, 198 - index, 2, 4, 1, 1, league_id=2))

        for rating in compute_league_strength(history).ratings.values():
            self.assertLessEqual(abs(rating.log_strength), MAX_LOG_STRENGTH + 1e-9)

    def test_home_advantage_never_favours_the_away_side(self) -> None:
        # A league where away teams happen to outscore home teams must not
        # invert the multiplier into a home penalty.
        away_dominant = [match(i, 100 - i, 1 + (i % 4), 5 + (i % 4), 0, 3) for i in range(1, 41)]

        self.assertGreaterEqual(measure_home_advantage(away_dominant, LEAGUE), 1.0)

    def test_home_advantage_is_bounded_above(self) -> None:
        home_dominant = [match(i, 100 - i, 1 + (i % 4), 5 + (i % 4), 9, 0) for i in range(1, 41)]

        self.assertLessEqual(measure_home_advantage(home_dominant, LEAGUE), 1.35)

    def test_published_probabilities_stay_inside_the_clamp(self) -> None:
        history: list[FixtureRecord] = []
        for index in range(1, 25):
            history.append(match(index, 120 - index, 1, 5 + (index % 4), 7, 0))
            history.append(match(1000 + index, 119 - index, 2, 5 + (index % 4), 0, 7))
            history.append(match(2000 + index, 118 - index, 5 + (index % 4), 6 + (index % 4), 1, 1))

        rows = run_fixture_engine(
            FixtureEngineInput(
                as_of_timestamp=NOW,
                target_fixture=target(),
                history=history,
                statistics_by_fixture={},
                odds=None,
                generated_at=NOW,
            )
        )

        self.assertTrue(rows, "a lopsided but well-sampled fixture should publish")
        for row in rows:
            self.assertGreaterEqual(row.probability, 0.01)
            self.assertLessEqual(row.probability, 0.99)
            self.assertTrue(math.isfinite(row.probability))


class MalformedOddsTests(unittest.TestCase):
    def _history(self) -> list[FixtureRecord]:
        history: list[FixtureRecord] = []
        for index in range(1, 21):
            history.append(match(index, 120 - index, 1, 5 + (index % 4), 2, 1))
            history.append(match(1000 + index, 119 - index, 2, 5 + (index % 4), 1, 2))
            history.append(match(2000 + index, 118 - index, 5 + (index % 4), 6 + (index % 4), 1, 1))
        return history

    def test_placeholder_odds_do_not_anchor_the_probability(self) -> None:
        history = self._history()

        def probability(odds: OddsSnapshot | None) -> float:
            rows = run_fixture_engine(
                FixtureEngineInput(
                    as_of_timestamp=NOW,
                    target_fixture=target(),
                    history=history,
                    statistics_by_fixture={},
                    odds=odds,
                    generated_at=NOW,
                )
            )
            return next(row.probability for row in rows if row.market == "1x2")

        # A provider publishing 1.0/0-style placeholders while a market forms
        # must be ignored rather than treated as a near-certain outcome.
        self.assertAlmostEqual(
            probability(OddsSnapshot(fixture_id=99999, home_win=1.0, draw=1.0, away_win=1.0)),
            probability(None),
            places=9,
        )

    def test_a_real_market_does_move_the_probability(self) -> None:
        history = self._history()

        def probability(odds: OddsSnapshot | None) -> float:
            rows = run_fixture_engine(
                FixtureEngineInput(
                    as_of_timestamp=NOW,
                    target_fixture=target(),
                    history=history,
                    statistics_by_fixture={},
                    odds=odds,
                    generated_at=NOW,
                )
            )
            return next(row.probability for row in rows if row.market == "1x2")

        # Guards the negative case above: it must pass because placeholders are
        # rejected, not because anchoring is broken.
        self.assertNotAlmostEqual(
            probability(OddsSnapshot(fixture_id=99999, home_win=1.05, draw=15.0, away_win=40.0)),
            probability(None),
            places=6,
        )


if __name__ == "__main__":
    unittest.main()
