"""Same-league separation: teams and matches must not be weighted alike.

League strength explains cross-border fixtures. Inside one league every team was
still judged on its raw goals-per-game, so a side that had played the bottom of
the table looked identical to one that had played the top. These tests pin the
opponent adjustment, the recency weighting and the small-sample shrinkage.
"""

from __future__ import annotations

from dataclasses import replace
from datetime import UTC, datetime, timedelta
import math
import random
from statistics import pstdev
import unittest

from prediction_engine.features import FixtureRecord
from prediction_engine.pipeline import FixtureEngineInput, _team_strength_table
from prediction_engine.team_strength import (
    DECAY,
    SHRINKAGE_MATCHES,
    _shrink,
    compute_team_strength,
)


LEAGUE = 39
NOW = datetime(2026, 9, 10, 12, 0, tzinfo=UTC)

STRONG = 1  # genuinely good side
WEAK = 2  # genuinely poor side
FARMER = 3  # ordinary side that only ever plays the poor ones
TESTED = 4  # ordinary side that only ever plays the good ones


def match(
    fixture_id: int,
    days_ago: int,
    home_team_id: int,
    away_team_id: int,
    home_goals: int,
    away_goals: int,
    league_id: int = LEAGUE,
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
        status="FT",
    )


def schedule_of_convenience() -> list[FixtureRecord]:
    """Two sides with identical raw records but opposite strength of schedule."""
    records: list[FixtureRecord] = []
    fixture_id = 1

    # Establish that STRONG is strong and WEAK is weak, against each other and
    # against a pool of ordinary filler clubs.
    for index in range(8):
        records.append(match(fixture_id, 200 - index * 6, STRONG, 100 + index, 4, 0))
        fixture_id += 1
        records.append(match(fixture_id, 199 - index * 6, WEAK, 100 + index, 0, 3))
        fixture_id += 1
        records.append(match(fixture_id, 198 - index * 6, 100 + index, 110 + index, 1, 1))
        fixture_id += 1

    # FARMER and TESTED post the SAME scoreline every week -- two goals for,
    # one against -- but FARMER does it against WEAK and TESTED against STRONG.
    for index in range(8):
        records.append(match(fixture_id, 60 - index * 6, FARMER, WEAK, 2, 1))
        fixture_id += 1
        records.append(match(fixture_id, 59 - index * 6, TESTED, STRONG, 2, 1))
        fixture_id += 1

    return records


class OpponentAdjustmentTests(unittest.TestCase):
    def test_identical_records_are_separated_by_strength_of_schedule(self) -> None:
        table = compute_team_strength(schedule_of_convenience())

        farmer = table.for_team(FARMER)
        tested = table.for_team(TESTED)

        # Identical goals for and against, so the old raw-average model rated
        # these two exactly the same.
        self.assertEqual(farmer.sample_size, tested.sample_size)
        self.assertGreater(tested.attack, farmer.attack)
        # Conceding once a week to the best attack in the league is a better
        # defence than conceding once a week to the worst.
        self.assertLess(tested.defence, farmer.defence)

    def test_the_strong_side_outrates_the_weak_one(self) -> None:
        table = compute_team_strength(schedule_of_convenience())

        self.assertGreater(table.for_team(STRONG).attack, table.for_team(WEAK).attack)
        self.assertLess(table.for_team(STRONG).defence, table.for_team(WEAK).defence)

    def test_ratings_are_relative_to_the_league_average(self) -> None:
        table = compute_team_strength(schedule_of_convenience())

        attacks = [rating.attack for rating in table.ratings.values()]
        # Centred on 1.0, so the ratings describe relative level and compose
        # with the between-league scales instead of restating them.
        self.assertAlmostEqual(sum(attacks) / len(attacks), 1.0, delta=0.20)


class WeightingTests(unittest.TestCase):
    def test_recent_form_outweighs_old_form(self) -> None:
        def history(improving: bool) -> list[FixtureRecord]:
            early, late = (0, 4) if improving else (4, 0)
            records: list[FixtureRecord] = []
            fixture_id = 1
            for index in range(6):
                records.append(match(fixture_id, 200 - index * 5, STRONG, 100 + index, early, 1))
                fixture_id += 1
            for index in range(6):
                records.append(match(fixture_id, 60 - index * 5, STRONG, 110 + index, late, 1))
                fixture_id += 1
            for index in range(8):
                records.append(match(fixture_id, 150 - index * 5, 100 + index, 110 + index, 1, 1))
                fixture_id += 1
            return records

        improving = compute_team_strength(history(improving=True)).for_team(STRONG)
        declining = compute_team_strength(history(improving=False)).for_team(STRONG)

        # Same twelve matches and the same total goals in both; only the order
        # differs. The side scoring four goals lately must rate higher.
        self.assertGreater(improving.attack, declining.attack)

    def test_a_thin_sample_is_shrunk_toward_the_league_average(self) -> None:
        base = schedule_of_convenience()
        newcomer = 500
        thin = [*base, match(9001, 5, newcomer, WEAK, 6, 0)]

        rating = compute_team_strength(thin).for_team(newcomer)

        self.assertEqual(rating.sample_size, 1)
        # One 6-0 must not mint the best attack in the league.
        self.assertLess(rating.attack, table_best_attack(compute_team_strength(base)))

    def test_shrinkage_strengthens_with_sample_size(self) -> None:
        base = schedule_of_convenience()
        newcomer = 501

        def rating_after(appearances: int) -> float:
            records = list(base)
            for index in range(appearances):
                records.append(match(9100 + index, 40 - index, newcomer, WEAK, 4, 0))
            return compute_team_strength(records).for_team(newcomer).attack

        few = rating_after(2)
        many = rating_after(SHRINKAGE_MATCHES * 3)

        # The same per-match evidence, believed more as it accumulates.
        self.assertGreater(many, few)


class ScopeTests(unittest.TestCase):
    def test_a_continental_result_does_not_move_a_domestic_rating(self) -> None:
        # Cross-league results are evidence about leagues, not about a team's
        # standing inside its own league; counting them here would double-count
        # the between-league scale applied elsewhere.
        domestic = schedule_of_convenience()
        # A foreign side with a domestic league of its own, so the tie between
        # them is genuinely cross-league rather than two clubs of one league.
        foreign = 900
        for index in range(6):
            domestic.append(match(8000 + index, 90 - index * 5, foreign, 910 + index, 2, 1, league_id=61))
        with_continental = [
            *domestic,
            match(8100, 3, STRONG, foreign, 5, 0, league_id=2),
        ]

        before = compute_team_strength(domestic).for_team(STRONG)
        after = compute_team_strength(with_continental).for_team(STRONG)

        self.assertEqual(after.sample_size, before.sample_size)
        self.assertAlmostEqual(after.attack, before.attack, places=9)

    def test_unknown_team_falls_back_to_neutral(self) -> None:
        table = compute_team_strength(schedule_of_convenience())

        unknown = table.for_team(999999)

        self.assertEqual(unknown.attack, 1.0)
        self.assertEqual(unknown.defence, 1.0)
        self.assertEqual(unknown.league_goal_rate, 0.0)

    def test_empty_history_yields_no_ratings(self) -> None:
        self.assertEqual(compute_team_strength([]).ratings, {})


class FitSampleTests(unittest.TestCase):
    """Twenty identical teams: any spread in their fitted ratings is an artefact."""

    TRIALS = 30

    def _season(self, generator: random.Random) -> list[FixtureRecord]:
        records: list[FixtureRecord] = []
        teams = list(range(1, 21))
        for week in range(76):
            generator.shuffle(teams)
            for home, away in zip(teams[::2], teams[1::2]):
                records.append(
                    match(
                        len(records) + 1,
                        7 * (week + 1),
                        home,
                        away,
                        _poisson(generator, 1.5),
                        _poisson(generator, 1.2),
                    )
                )
        return records

    def test_fitting_on_the_two_teams_alone_invents_a_spread_the_whole_league_does_not(self) -> None:
        generator = random.Random(20261104)
        two_team: list[float] = []
        whole_league: list[float] = []
        for _ in range(self.TRIALS):
            records = self._season(generator)
            sample = sorted(
                (record for record in records if {record.home_team_id, record.away_team_id} & {1, 2}),
                key=lambda record: record.kickoff_at,
                reverse=True,
            )[:80]
            two_team.append(compute_team_strength(sample).for_team(1).attack)
            whole_league.append(compute_team_strength(records).for_team(1).attack)

        self.assertLess(pstdev(whole_league), pstdev(two_team) * 0.8)

    def test_a_rating_shrinks_by_what_its_matches_are_worth(self) -> None:
        records = schedule_of_convenience()
        fitted = compute_team_strength(records).for_team(STRONG)
        effective = sum(DECAY**index for index in range(fitted.sample_size))

        self.assertLess(effective, fitted.sample_size)
        self.assertAlmostEqual(_shrink(2.0, effective), 1 + effective / (effective + SHRINKAGE_MATCHES))

    def test_the_pipeline_fits_on_the_league_when_it_has_one(self) -> None:
        league = schedule_of_convenience()
        two_team = league[:4]

        self.assertEqual(
            _team_strength_table(replace(_inputs(two_team), league_history=league), 1.1),
            compute_team_strength(league, home_advantage=1.1),
        )
        self.assertEqual(
            _team_strength_table(_inputs(two_team), 1.1),
            compute_team_strength(two_team, home_advantage=1.1),
        )


def _inputs(history: list[FixtureRecord]) -> FixtureEngineInput:
    target = match(99, -7, STRONG, WEAK, 0, 0)
    return FixtureEngineInput(
        as_of_timestamp=NOW,
        target_fixture=target,
        history=history,
        statistics_by_fixture={},
        odds=None,
        generated_at=NOW,
    )


def _poisson(generator: random.Random, rate: float) -> int:
    threshold = math.exp(-rate)
    goals = 0
    product = generator.random()
    while product > threshold:
        goals += 1
        product *= generator.random()
    return goals


def table_best_attack(table) -> float:
    return max(rating.attack for rating in table.ratings.values())


if __name__ == "__main__":
    unittest.main()
