"""Model B, the Elo challenger: direction, point-in-time discipline, priors.

The gate in architecture v2 Phase 5 asks for two things before this model may
even be evaluated: a fixture's own result must never reach the rating used to
predict it, and an obvious mismatch must come out the right way round. The
rest pin the behaviours the module docstring promises, so a later tuning
change cannot quietly remove one.
"""

from __future__ import annotations

from dataclasses import replace
from datetime import UTC, datetime, timedelta
import random
import unittest

from prediction_engine.elo import (
    DEFAULT_ELO_CONFIG,
    MODEL_ID,
    EloRatingBook,
    run_elo,
)
from prediction_engine.features import FixtureRecord


LEAGUE = 39
OTHER_LEAGUE = 140
SECOND_TIER = 40
START = datetime(2024, 8, 1, 15, 0, tzinfo=UTC)

TOP = 1
BOTTOM = 2


def fixture(
    fixture_id: int,
    day: int,
    home: int,
    away: int,
    home_goals: int | None,
    away_goals: int | None,
    *,
    league_id: int = LEAGUE,
    league_name: str = "Premier League",
    season: int = 2024,
    status: str = "FT",
) -> FixtureRecord:
    return FixtureRecord(
        id=fixture_id,
        kickoff_at=START + timedelta(days=day),
        league_id=league_id,
        league_name=league_name,
        season=season,
        home_team_id=home,
        away_team_id=away,
        home_goals=home_goals,
        away_goals=away_goals,
        status=status,
    )


def upcoming(fixture_id: int, day: int, home: int, away: int, **kwargs: object) -> FixtureRecord:
    return fixture(fixture_id, day, home, away, None, None, status="NS", **kwargs)


def round_robin(
    teams: list[int],
    *,
    first_id: int,
    first_day: int,
    strength: dict[int, float],
    league_id: int = LEAGUE,
    season: int = 2024,
    seed: int = 7,
) -> list[FixtureRecord]:
    """A double round robin whose results follow the given team strengths."""
    generator = random.Random(seed)
    records: list[FixtureRecord] = []
    fixture_id, day = first_id, first_day
    for home in teams:
        for away in teams:
            if home == away:
                continue
            edge = strength[home] - strength[away] + 0.3
            home_goals = max(0, round(1.4 + edge + generator.uniform(-1.2, 1.2)))
            away_goals = max(0, round(1.1 - edge + generator.uniform(-1.2, 1.2)))
            records.append(
                fixture(fixture_id, day, home, away, home_goals, away_goals, league_id=league_id, season=season)
            )
            fixture_id += 1
            day += 1
    return records


def dominant_season() -> list[FixtureRecord]:
    """TOP beats everyone, BOTTOM loses to everyone, the rest are ordinary."""
    teams = [TOP, BOTTOM, 3, 4, 5, 6]
    strength = {TOP: 1.5, BOTTOM: -1.5, 3: 0.0, 4: 0.1, 5: -0.1, 6: 0.0}
    return round_robin(teams, first_id=1, first_day=0, strength=strength)


AS_OF = START + timedelta(days=200)


class DirectionTests(unittest.TestCase):
    def test_obvious_mismatch_favours_the_strong_side_in_a_sensible_range(self) -> None:
        history = dominant_season()
        home_strong = run_elo(upcoming(999, 100, TOP, BOTTOM), history, as_of_timestamp=AS_OF)
        away_strong = run_elo(upcoming(998, 100, BOTTOM, TOP), history, as_of_timestamp=AS_OF)

        self.assertEqual(home_strong.model_id, MODEL_ID)
        self.assertGreater(home_strong.match_result["home"], 0.60)
        self.assertLess(home_strong.match_result["home"], 0.97)
        self.assertGreater(away_strong.match_result["away"], away_strong.match_result["home"])
        # The home edge helps the weaker side but does not flip the pick.
        self.assertGreater(away_strong.match_result["home"], 1 - home_strong.match_result["home"] - home_strong.match_result["draw"])

    def test_equal_unrated_teams_get_the_home_edge_and_the_prior_draw_rate(self) -> None:
        prediction = run_elo(upcoming(1, 0, 10, 11), [], as_of_timestamp=AS_OF)

        self.assertGreater(prediction.match_result["home"], prediction.match_result["away"])
        self.assertAlmostEqual(prediction.draw_rate, DEFAULT_ELO_CONFIG.draw_rate_prior)
        self.assertIn("home_unrated", prediction.fallback_flags)
        self.assertIn("away_unrated", prediction.fallback_flags)

    def test_probabilities_are_valid_for_any_history(self) -> None:
        generator = random.Random(11)
        for trial in range(25):
            teams = list(range(1, 9))
            strength = {team: generator.uniform(-2, 2) for team in teams}
            history = round_robin(teams, first_id=1, first_day=0, strength=strength, seed=trial)
            home, away = generator.sample(teams, 2)
            prediction = run_elo(upcoming(5000, 120, home, away), history, as_of_timestamp=AS_OF)
            for market in prediction.markets.values():
                for probability in market.values():
                    self.assertGreater(probability, 0.0)
                    self.assertLess(probability, 1.0)
            self.assertAlmostEqual(sum(prediction.match_result.values()), 1.0, places=12)
            self.assertAlmostEqual(sum(prediction.draw_no_bet.values()), 1.0, places=12)

    def test_a_rout_moves_ratings_further_than_a_narrow_win(self) -> None:
        narrow = run_elo(upcoming(9, 2, 3, 4), [fixture(1, 0, 3, 4, 1, 0)], as_of_timestamp=AS_OF)
        rout = run_elo(upcoming(9, 2, 3, 4), [fixture(1, 0, 3, 4, 4, 0)], as_of_timestamp=AS_OF)

        self.assertGreater(rout.home_rating - rout.away_rating, narrow.home_rating - narrow.away_rating)

    def test_a_corrupt_scoreline_counts_no_more_than_the_margin_cap(self) -> None:
        capped = run_elo(upcoming(9, 2, 3, 4), [fixture(1, 0, 3, 4, 5, 0)], as_of_timestamp=AS_OF)
        corrupt = run_elo(upcoming(9, 2, 3, 4), [fixture(1, 0, 3, 4, 57, 0)], as_of_timestamp=AS_OF)

        self.assertAlmostEqual(capped.home_rating, corrupt.home_rating)

    def test_winning_more_never_lowers_a_rating(self) -> None:
        base = [fixture(index, index, 3, 4, 1, 1) for index in range(1, 6)]
        better = [fixture(index, index, 3, 4, 2, 1) for index in range(1, 6)]
        draws = run_elo(upcoming(99, 30, 3, 4), base, as_of_timestamp=AS_OF)
        wins = run_elo(upcoming(99, 30, 3, 4), better, as_of_timestamp=AS_OF)

        self.assertGreater(wins.home_rating, draws.home_rating)
        self.assertGreater(wins.match_result["home"], draws.match_result["home"])


class PointInTimeTests(unittest.TestCase):
    def test_future_results_and_the_fixtures_own_result_change_nothing(self) -> None:
        history = dominant_season()
        target = upcoming(999, 100, 3, 4)
        before = run_elo(target, history, as_of_timestamp=AS_OF)

        future = [
            # The target's own result, as it would read after full time.
            fixture(999, 100, 3, 4, 6, 0),
            fixture(1001, 101, 3, 5, 5, 0),
            fixture(1002, 250, 4, 3, 0, 4),
        ]
        after = run_elo(target, history + future, as_of_timestamp=AS_OF)

        self.assertEqual(before, after)

    def test_results_after_the_cutoff_are_invisible_even_before_kickoff(self) -> None:
        history = dominant_season()
        target = upcoming(999, 300, 3, 4)
        cutoff = START + timedelta(days=60)
        visible = [record for record in history if record.kickoff_at < cutoff]

        self.assertEqual(
            run_elo(target, history, as_of_timestamp=cutoff),
            run_elo(target, visible, as_of_timestamp=cutoff),
        )

    def test_a_simultaneous_kickoff_does_not_inform_the_target(self) -> None:
        history = dominant_season()
        target = upcoming(999, 100, 3, 4)
        same_slot = fixture(1000, 100, 3, 5, 7, 0)

        self.assertEqual(
            run_elo(target, history, as_of_timestamp=AS_OF),
            run_elo(target, history + [same_slot], as_of_timestamp=AS_OF),
        )

    def test_timezone_naive_cutoff_is_refused(self) -> None:
        with self.assertRaises(ValueError):
            run_elo(upcoming(1, 0, 3, 4), [], as_of_timestamp=datetime(2025, 1, 1))

    def test_an_incremental_book_matches_a_full_replay(self) -> None:
        history = dominant_season()
        book = EloRatingBook()
        for record in history:
            target = replace(record, home_goals=None, away_goals=None, status="NS")
            self.assertEqual(
                book.predict(target),
                run_elo(target, history, as_of_timestamp=AS_OF),
            )
            book.record(record)


class DeterminismTests(unittest.TestCase):
    def test_history_order_and_repeats_do_not_change_the_answer(self) -> None:
        history = dominant_season()
        shuffled = list(history)
        random.Random(3).shuffle(shuffled)
        target = upcoming(999, 100, TOP, 4)

        first = run_elo(target, history, as_of_timestamp=AS_OF)
        self.assertEqual(first, run_elo(target, shuffled, as_of_timestamp=AS_OF))
        self.assertEqual(first, run_elo(target, history, as_of_timestamp=AS_OF))

    def test_an_unrelated_league_does_not_move_this_leagues_prediction(self) -> None:
        history = dominant_season()
        elsewhere = round_robin(
            [21, 22, 23, 24],
            first_id=500,
            first_day=0,
            strength={21: 2.0, 22: -2.0, 23: 0.0, 24: 0.5},
            league_id=OTHER_LEAGUE,
        )
        target = upcoming(999, 100, TOP, BOTTOM)

        self.assertEqual(
            run_elo(target, history, as_of_timestamp=AS_OF).match_result,
            run_elo(target, history + elsewhere, as_of_timestamp=AS_OF).match_result,
        )


class PriorAndSeasonTests(unittest.TestCase):
    def test_a_newcomer_to_an_established_league_starts_below_its_mean(self) -> None:
        history = dominant_season()
        # Team 30 arrives the next season, never having played in this league.
        target = upcoming(999, 400, 30, 3, season=2025)
        prediction = run_elo(target, history, as_of_timestamp=START + timedelta(days=500))

        self.assertIn("home_unrated", prediction.fallback_flags)
        self.assertLess(prediction.home_rating, DEFAULT_ELO_CONFIG.initial_rating)

    def test_a_promoted_side_drops_its_second_tier_rating(self) -> None:
        second_tier = round_robin(
            [30, 31, 32, 33],
            first_id=500,
            first_day=0,
            strength={30: 2.0, 31: -0.5, 32: -0.5, 33: -1.0},
            league_id=SECOND_TIER,
        )
        history = dominant_season() + second_tier
        cutoff = START + timedelta(days=500)
        dominant_in_tier_two = run_elo(upcoming(998, 390, 30, 31, league_id=SECOND_TIER), history, as_of_timestamp=cutoff)
        # 30 is promoted: its first top-flight match is against an established side.
        promoted = run_elo(upcoming(999, 400, 30, 3, season=2025), history, as_of_timestamp=cutoff)

        self.assertGreater(dominant_in_tier_two.home_rating, DEFAULT_ELO_CONFIG.initial_rating + 50)
        self.assertIn("home_newcomer_prior", promoted.fallback_flags)
        self.assertLess(promoted.home_rating, DEFAULT_ELO_CONFIG.initial_rating)

    def test_measured_newcomers_move_the_prior(self) -> None:
        config = replace(DEFAULT_ELO_CONFIG, newcomer_settle_matches=4, newcomer_shrinkage=1)
        first = dominant_season()
        # Season two: newcomer 40 replaces BOTTOM and wins everything.
        teams = [TOP, 3, 4, 5, 6, 40]
        strength = {TOP: 1.5, 3: 0.0, 4: 0.1, 5: -0.1, 6: 0.0, 40: 3.0}
        second = round_robin(teams, first_id=200, first_day=400, strength=strength, season=2025)
        cutoff = START + timedelta(days=900)
        before = run_elo(upcoming(999, 800, 41, 3, season=2026), first, as_of_timestamp=cutoff, config=config)
        after = run_elo(upcoming(999, 800, 41, 3, season=2026), first + second, as_of_timestamp=cutoff, config=config)

        self.assertIn("home_unrated", after.fallback_flags)
        relative_before = before.home_rating - before.away_rating
        relative_after = after.home_rating - after.away_rating
        self.assertGreater(relative_after, relative_before)

    def test_a_new_season_pulls_ratings_back_toward_the_league_mean(self) -> None:
        history = dominant_season()
        cutoff = START + timedelta(days=500)
        same_season = run_elo(upcoming(998, 100, TOP, 3), history, as_of_timestamp=cutoff)
        next_season = run_elo(upcoming(999, 400, TOP, 3, season=2025), history, as_of_timestamp=cutoff)

        gap_now = same_season.home_rating - same_season.away_rating
        gap_later = next_season.home_rating - next_season.away_rating
        self.assertAlmostEqual(gap_later, gap_now * (1 - DEFAULT_ELO_CONFIG.season_regression), places=6)


class CrossLeagueTests(unittest.TestCase):
    def test_a_continental_result_moves_the_league_offset_not_the_team(self) -> None:
        home_league = dominant_season()
        away_league = round_robin(
            [21, 22, 23, 24],
            first_id=500,
            first_day=0,
            strength={21: 0.5, 22: 0.0, 23: 0.0, 24: -0.5},
            league_id=OTHER_LEAGUE,
        )
        tie = fixture(900, 120, 3, 22, 4, 0, league_id=2, league_name="UEFA Champions League")
        history = home_league + away_league
        domestic = upcoming(999, 130, 3, 4)
        continental = upcoming(998, 130, 4, 23, league_id=2, league_name="UEFA Champions League")

        without_tie = run_elo(continental, history, as_of_timestamp=AS_OF)
        with_tie = run_elo(continental, history + [tie], as_of_timestamp=AS_OF)

        # Team 4 never played the tie, yet its league is now rated higher.
        self.assertIn("cross_league", with_tie.fallback_flags)
        self.assertGreater(with_tie.home_rating, without_tie.home_rating)
        # Inside the league nothing changed: offsets cancel between two of its own.
        self.assertEqual(
            run_elo(domestic, history, as_of_timestamp=AS_OF).match_result,
            run_elo(domestic, history + [tie], as_of_timestamp=AS_OF).match_result,
        )

    def test_a_continental_tie_with_an_unrated_side_is_ignored(self) -> None:
        history = dominant_season() + round_robin(
            [21, 22, 23, 24],
            first_id=500,
            first_day=0,
            strength={21: 0.5, 22: 0.0, 23: 0.0, 24: -0.5},
            league_id=OTHER_LEAGUE,
        )
        # 77 has no domestic level, so a rout of it says nothing about leagues.
        tie = fixture(900, 120, 3, 77, 5, 0, league_id=2, league_name="UEFA Europa League")
        target = upcoming(999, 130, 4, 23, league_id=2, league_name="UEFA Champions League")

        self.assertEqual(
            run_elo(target, history, as_of_timestamp=AS_OF),
            run_elo(target, history + [tie], as_of_timestamp=AS_OF),
        )

    def test_a_domestic_cup_tie_does_not_move_a_team_into_the_cup(self) -> None:
        # The cup tie opens the new season, before any league match can
        # establish where the two sides belong this year.
        history = dominant_season() + [
            fixture(900, 400, 3, 4, 2, 1, league_id=45, league_name="FA Cup", season=2025),
        ]
        prediction = run_elo(
            upcoming(999, 410, 3, 5, season=2025),
            history,
            as_of_timestamp=START + timedelta(days=500),
        )

        self.assertNotIn("cross_league", prediction.fallback_flags)
        self.assertNotIn("home_newcomer_prior", prediction.fallback_flags)

    def test_friendlies_are_ignored(self) -> None:
        history = dominant_season()
        friendly = fixture(900, 120, BOTTOM, TOP, 6, 0, league_id=667, league_name="Friendlies Clubs")
        target = upcoming(999, 130, TOP, BOTTOM)

        self.assertEqual(
            run_elo(target, history, as_of_timestamp=AS_OF),
            run_elo(target, history + [friendly], as_of_timestamp=AS_OF),
        )


if __name__ == "__main__":
    unittest.main()
