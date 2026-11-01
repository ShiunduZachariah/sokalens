"""Regressions for the cross-league favourite inversion.

A dominant small-league side used to be predicted over a European heavyweight
because each side's expected goals was its own domestic goals-scored average
and nothing else: no opponent defence, no league scale, no home edge. These
tests pin the shape of the fixture that exposed it (Bayern v Bodo/Glimt, PSG v
Slovan Bratislava) plus each mechanism that fixes it.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
import unittest

from prediction_engine.features import FeatureAssembler, FixtureRecord, OddsSnapshot
from prediction_engine.league_strength import compute_league_strength, measure_home_advantage
from prediction_engine.pipeline import (
    FixtureEngineInput,
    _expected_goals,
    _league_strength_scales,
    run_fixture_engine,
)


STRONG_LEAGUE = 78  # deep domestic league, e.g. Bundesliga
WEAK_LEAGUE = 103  # shallow domestic league, e.g. Eliteserien
CONTINENTAL = 2  # the competition the two actually meet in

HEAVYWEIGHT = 10  # strong-league side: good, not domestically rampant
MINNOW = 20  # weak-league side: rampant at home, untested abroad

NOW = datetime(2026, 9, 10, 12, 0, tzinfo=UTC)


def _league_name(league_id: int) -> str:
    """Name the competitions the way a provider does.

    League strength is measured only from continental play, so the synthetic
    competition the two sides meet in has to be a real confederation name for
    the fixture to exercise the rating at all.
    """
    if league_id == CONTINENTAL:
        return "UEFA Champions League"
    return f"league-{league_id}"


def match(
    fixture_id: int,
    days_ago: int,
    league_id: int,
    home_team_id: int,
    away_team_id: int,
    home_goals: int,
    away_goals: int,
    season: int = 2026,
) -> FixtureRecord:
    return FixtureRecord(
        id=fixture_id,
        kickoff_at=NOW - timedelta(days=days_ago),
        league_id=league_id,
        league_name=_league_name(league_id),
        season=season,
        home_team_id=home_team_id,
        away_team_id=away_team_id,
        home_goals=home_goals,
        away_goals=away_goals,
        status="FT",
    )


def cross_league_history() -> list[FixtureRecord]:
    """History where the weak league out-scores itself but loses across leagues."""
    records: list[FixtureRecord] = []
    next_id = 1

    # The minnow sweeps its own league: three goals a game, barely conceding.
    for index in range(10):
        records.append(match(next_id, 120 - index * 7, WEAK_LEAGUE, MINNOW, 200 + index, 3, 0))
        next_id += 1
    # Its league-mates play each other to ordinary, low scorelines.
    for index in range(10):
        records.append(match(next_id, 118 - index * 7, WEAK_LEAGUE, 200 + index, 210 + index, 1, 1))
        next_id += 1

    # The heavyweight wins its own league without the same goal glut, because
    # the opposition is materially better.
    for index in range(10):
        records.append(match(next_id, 119 - index * 7, STRONG_LEAGUE, HEAVYWEIGHT, 300 + index, 2, 1))
        next_id += 1
    for index in range(10):
        records.append(match(next_id, 117 - index * 7, STRONG_LEAGUE, 300 + index, 310 + index, 2, 1))
        next_id += 1

    # Inter-league evidence: when the two leagues meet, the strong one wins
    # comfortably. This is what the strength rating is measured from.
    for index in range(14):
        records.append(match(next_id, 200 - index * 5, CONTINENTAL, 300 + index, 200 + index, 3, 0, season=2025))
        next_id += 1

    return records


def target_fixture() -> FixtureRecord:
    return FixtureRecord(
        id=9001,
        kickoff_at=NOW + timedelta(hours=10),
        league_id=CONTINENTAL,
        league_name="UEFA Champions League",
        season=2026,
        home_team_id=HEAVYWEIGHT,
        away_team_id=MINNOW,
        home_goals=None,
        away_goals=None,
        status="scheduled",
    )


def assemble(history: list[FixtureRecord], target: FixtureRecord, odds: OddsSnapshot | None = None):
    table = compute_league_strength(history)
    return FeatureAssembler(form_windows=(5, 10), decay_rate=0.85).assemble(
        target,
        history,
        {},
        as_of_timestamp=NOW,
        odds=odds,
        league_strength=_league_strength_scales(table, target),
        home_advantage=measure_home_advantage(history, target.league_id),
    )


class LeagueStrengthTests(unittest.TestCase):
    def test_inter_league_results_rate_the_stronger_league_higher(self) -> None:
        table = compute_league_strength(cross_league_history())

        strong = table.for_league(STRONG_LEAGUE)
        weak = table.for_league(WEAK_LEAGUE)

        self.assertGreater(strong.log_strength, weak.log_strength)
        self.assertGreater(strong.attack_scale, 1.0)
        self.assertLess(weak.attack_scale, 1.0)
        # Defence is the mirror of attack: conceding little in a weak league is
        # weaker evidence of a good defence than doing so in a strong one.
        self.assertLess(strong.defence_scale, 1.0)
        self.assertGreater(weak.defence_scale, 1.0)

    def test_teams_are_rated_against_their_own_league_not_the_competition(self) -> None:
        table = compute_league_strength(cross_league_history())

        self.assertEqual(table.team_primary_league[HEAVYWEIGHT], STRONG_LEAGUE)
        self.assertEqual(table.team_primary_league[MINNOW], WEAK_LEAGUE)

    def test_a_league_with_no_inter_league_evidence_stays_neutral(self) -> None:
        domestic_only = [
            match(index, 100 - index, STRONG_LEAGUE, HEAVYWEIGHT, 300 + index, 2, 1)
            for index in range(1, 8)
        ]

        table = compute_league_strength(domestic_only)

        self.assertEqual(table.for_league(STRONG_LEAGUE).attack_scale, 1.0)
        self.assertEqual(table.for_league(STRONG_LEAGUE).defence_scale, 1.0)

    def test_a_thin_inter_league_sample_is_shrunk_toward_neutral(self) -> None:
        history = cross_league_history()
        thin = [record for record in history if record.league_id != CONTINENTAL]
        thin.append(match(9999, 150, CONTINENTAL, 300, 200, 5, 0, season=2025))

        thin_scale = compute_league_strength(thin).for_league(STRONG_LEAGUE).attack_scale
        full_scale = compute_league_strength(history).for_league(STRONG_LEAGUE).attack_scale

        # One lopsided result must not move a league as far as fourteen do.
        self.assertLess(thin_scale, full_scale)


class ExpectedGoalsTests(unittest.TestCase):
    def test_heavyweight_outranks_a_dominant_small_league_side(self) -> None:
        features = assemble(cross_league_history(), target_fixture())

        home_lambda, away_lambda = _expected_goals(features)

        # The minnow scores more per game domestically than the heavyweight
        # does; the pick must still come out the other way.
        self.assertGreater(
            features.away_recent["goals_for_per_match_5"],
            features.home_recent["goals_for_per_match_5"],
        )
        self.assertGreater(home_lambda, away_lambda)

    def test_the_published_pick_is_a_home_win(self) -> None:
        rows = run_fixture_engine(
            FixtureEngineInput(
                as_of_timestamp=NOW,
                target_fixture=target_fixture(),
                history=cross_league_history(),
                statistics_by_fixture={},
                odds=None,
                generated_at=NOW,
            )
        )

        one_x_two = next((row for row in rows if row.market == "1x2"), None)
        self.assertIsNotNone(one_x_two)
        self.assertEqual(one_x_two.prediction, "home")

    def test_opponent_defence_moves_expected_goals(self) -> None:
        history = cross_league_history()
        target = target_fixture()
        leaky = [
            record
            for record in history
            if not (record.league_id == WEAK_LEAGUE and record.home_team_id == MINNOW)
        ]
        # Same attack, far leakier defence: the opponent's expected goals must rise.
        leaky.extend(
            match(5000 + index, 120 - index * 7, WEAK_LEAGUE, MINNOW, 200 + index, 3, 4)
            for index in range(10)
        )

        tight_home, _ = _expected_goals(assemble(history, target))
        leaky_home, _ = _expected_goals(assemble(leaky, target))

        self.assertGreater(leaky_home, tight_home)

    def test_home_advantage_favours_the_home_side_of_an_even_fixture(self) -> None:
        even_history: list[FixtureRecord] = []
        for index in range(10):
            even_history.append(match(600 + index, 100 - index * 5, STRONG_LEAGUE, HEAVYWEIGHT, 300 + index, 2, 1))
            even_history.append(match(700 + index, 99 - index * 5, STRONG_LEAGUE, MINNOW, 300 + index, 2, 1))
        target = FixtureRecord(
            id=9002,
            kickoff_at=NOW + timedelta(hours=10),
            league_id=STRONG_LEAGUE,
            league_name="league-78",
            season=2026,
            home_team_id=HEAVYWEIGHT,
            away_team_id=MINNOW,
            home_goals=None,
            away_goals=None,
            status="scheduled",
        )

        home_lambda, away_lambda = _expected_goals(assemble(even_history, target))

        self.assertGreater(home_lambda, away_lambda)

    def test_home_advantage_falls_back_without_enough_competition_history(self) -> None:
        self.assertEqual(measure_home_advantage([], CONTINENTAL), 1.15)


class MarketAnchoringTests(unittest.TestCase):
    def test_a_settled_market_moves_the_published_probability(self) -> None:
        history = cross_league_history()
        target = target_fixture()
        base = FixtureEngineInput(
            as_of_timestamp=NOW,
            target_fixture=target,
            history=history,
            statistics_by_fixture={},
            odds=None,
            generated_at=NOW,
        )
        # A market that rates the home side far shorter than the model does.
        with_odds = FixtureEngineInput(
            as_of_timestamp=NOW,
            target_fixture=target,
            history=history,
            statistics_by_fixture={},
            odds=OddsSnapshot(fixture_id=target.id, home_win=1.10, draw=9.00, away_win=21.00),
            generated_at=NOW,
        )

        without = next(row for row in run_fixture_engine(base) if row.market == "1x2")
        anchored = next(row for row in run_fixture_engine(with_odds) if row.market == "1x2")

        self.assertEqual(anchored.prediction, "home")
        self.assertGreater(anchored.probability, without.probability)

    def test_provisional_odds_do_not_anchor_the_probability(self) -> None:
        history = cross_league_history()
        target = target_fixture()
        odds = OddsSnapshot(fixture_id=target.id, home_win=1.10, draw=9.00, away_win=21.00)

        def run(provisional: bool):
            return next(
                row
                for row in run_fixture_engine(
                    FixtureEngineInput(
                        as_of_timestamp=NOW,
                        target_fixture=target,
                        history=history,
                        statistics_by_fixture={},
                        odds=odds,
                        generated_at=NOW,
                        odds_provisional=provisional,
                    )
                )
                if row.market == "1x2"
            )

        # Synthesized odds are display-only; anchoring to them would feed the
        # model its own output back as independent evidence.
        self.assertLess(run(provisional=True).probability, run(provisional=False).probability)


class BttsTotalGoalsCoverageTests(unittest.TestCase):
    def test_the_market_offers_both_btts_sides(self) -> None:
        from prediction_engine.model_a import ModelAInputs, run_model_a

        markets = run_model_a(ModelAInputs(home_lambda=2.4, away_lambda=0.3)).markets
        btts_total_goals = markets["btts_total_goals"]

        # Settlement has always graded "no" outcomes; generation must be able
        # to predict them, or the market is dropped for every BTTS-no fixture.
        self.assertIn("no_over_2.5", btts_total_goals)
        self.assertIn("no_under_2.5", btts_total_goals)
        self.assertAlmostEqual(
            sum(
                probability
                for selection, probability in btts_total_goals.items()
                if selection.endswith("_2.5")
            ),
            1.0,
            places=6,
        )


if __name__ == "__main__":
    unittest.main()
