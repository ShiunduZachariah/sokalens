"""Derive per-league scoring strength from observed inter-league results.

The engine's goal rates are built from a team's own domestic record. Left
unscaled, that treats a goal in a weak league as identical evidence to a goal
in a strong one, so a side that farms goals against weak domestic opposition
outranks an elite side from a deep league. That is exactly how a continental
tie between a dominant small-league team and a European heavyweight ends up
predicted the wrong way round.

Rather than hardcode a coefficient table that rots every season, the strength
of each league is measured from results already in the fixture history: when
teams whose home leagues differ meet in a continental or cup competition, the
scoreline is direct evidence about the two leagues' relative level.

The rating is an adjusted-margin iteration (a reduced Massey rating). A
league's rating is its mean goal margin in inter-league play, corrected each
pass by the rating of the leagues it actually faced, so a league that only
ever plays weak opponents does not inherit their inflated margins. Ratings are
then shrunk toward neutral by sample size and converted into multiplicative
attack/defence scales on the log-goal scale used by the Poisson model.
"""

from __future__ import annotations

from collections import defaultdict
from dataclasses import dataclass
import math
from typing import Iterable

from prediction_engine.features import (
    DEFAULT_HOME_ADVANTAGE,
    FixtureRecord,
    _is_finished,
    credible_leagues,
    is_continental_competition,
    team_primary_leagues,
)


# Inter-league matches needed before a league's measured margin is trusted at
# full weight. Below this the rating is pulled toward neutral in proportion,
# so a league seen twice cannot swing a prediction on a two-match sample.
SHRINKAGE_MATCHES = 12

# Refinement passes for the adjusted-margin iteration. The rating converges
# quickly; four passes is well past the point of practical movement.
ITERATIONS = 6

# Weight given to a faced league's own rating when correcting a margin. Below
# 1.0 the iteration contracts instead of amplifying across a sparse graph.
OPPONENT_CORRECTION_DAMPING = 0.5

# Goals-per-match of adjusted margin converted into log-goal units. A league a
# full goal per match stronger than neutral has its teams' scoring valued
# e**0.40 (about 1.5x) higher.
MARGIN_TO_LOG_GOALS = 0.40

# Hard bound on the log-scale rating, so a thin or lopsided sample cannot
# produce an extreme coefficient. +/-0.55 caps the scale factor at roughly
# 0.58x to 1.73x.
MAX_LOG_STRENGTH = 0.55

# Finished matches a competition needs before its own home/away split is used
# in place of the default home edge.
MIN_HOME_ADVANTAGE_SAMPLE = 20

# Ceiling on the measured home edge, so a small or freak sample cannot turn
# home advantage into the dominant term in the model.
MAX_HOME_ADVANTAGE = 1.35


@dataclass(frozen=True)
class LeagueStrength:
    """Multiplicative scales putting one league's goal rates on a neutral scale."""

    league_id: int
    log_strength: float
    sample_size: int

    @property
    def attack_scale(self) -> float:
        """Goals scored in this league are worth this much on a neutral scale."""
        return math.exp(self.log_strength)

    @property
    def defence_scale(self) -> float:
        """Goals conceded in this league, restated on a neutral scale.

        Inverse of the attack scale: a clean sheet in a strong league is
        stronger evidence of a good defence than one in a weak league, so a
        strong league's goals-against are scaled down.
        """
        return math.exp(-self.log_strength)


NEUTRAL_STRENGTH = LeagueStrength(league_id=0, log_strength=0.0, sample_size=0)


@dataclass(frozen=True)
class LeagueStrengthTable:
    ratings: dict[int, LeagueStrength]
    team_primary_league: dict[int, int]

    def for_league(self, league_id: int | None) -> LeagueStrength:
        if league_id is None:
            return NEUTRAL_STRENGTH
        return self.ratings.get(league_id, NEUTRAL_STRENGTH)

    def for_team(self, team_id: int) -> LeagueStrength:
        return self.for_league(self.team_primary_league.get(team_id))


def compute_league_strength(history: Iterable[FixtureRecord]) -> LeagueStrengthTable:
    records = [record for record in history if _is_finished(record)]
    primary = team_primary_leagues(records)
    # A league is rated only from teams whose own league is credible. Without
    # this a corrupt competition drags every league it has ever met toward its
    # own impossible scoreline.
    eligible = credible_leagues(records)
    primary = {
        team_id: league_id
        for team_id, league_id in primary.items()
        if league_id in eligible
    }
    observations = _inter_league_observations(records, primary)
    if not observations:
        return LeagueStrengthTable(ratings={}, team_primary_league=primary)

    ratings = _adjusted_margins(observations)
    sample_sizes = {league_id: len(entries) for league_id, entries in observations.items()}
    return LeagueStrengthTable(
        ratings={
            league_id: LeagueStrength(
                league_id=league_id,
                log_strength=_to_log_strength(margin, sample_sizes[league_id]),
                sample_size=sample_sizes[league_id],
            )
            for league_id, margin in ratings.items()
        },
        team_primary_league=primary,
    )


def _inter_league_observations(
    records: list[FixtureRecord],
    primary: dict[int, int],
) -> dict[int, list[tuple[int, float]]]:
    """Collect (opponent league, goal margin) pairs per league.

    Only fixtures whose two sides come from different home leagues carry any
    information about relative league level, so domestic matches are skipped.
    """
    observations: dict[int, list[tuple[int, float]]] = defaultdict(list)
    for record in records:
        # Only continental competition compares two domestic leagues.
        #
        # Without this the evidence is dominated by matches that say nothing
        # about it: pre-season friendlies supplied 72 of the Bundesliga's 119
        # observations, and domestic cup ties against lower divisions supplied
        # most of the rest. Both measure "beats its own country's weaker
        # clubs", which every top division does by a similar margin -- so the
        # Bundesliga and the Eliteserien came out within 5% of each other, and
        # a Champions League tie between them was decided by the domestic
        # ratings alone. Restricting to continental play leaves less evidence
        # but makes it evidence of the right thing.
        if not is_continental_competition(record.league_name):
            continue
        home_league = primary.get(record.home_team_id)
        away_league = primary.get(record.away_team_id)
        if home_league is None or away_league is None or home_league == away_league:
            continue
        margin = float(record.home_goals or 0) - float(record.away_goals or 0)
        observations[home_league].append((away_league, margin))
        observations[away_league].append((home_league, -margin))
    return dict(observations)


def _adjusted_margins(observations: dict[int, list[tuple[int, float]]]) -> dict[int, float]:
    """Iterate mean goal margin, corrected for the strength of leagues faced.

    The opponent correction is damped. Feeding a neighbour's full rating back in
    each pass is a diffusion that amplifies on a sparse graph: across production's
    ~1,000 competitions, most of which meet only a handful of others, the
    undamped form diverged until nearly every league sat on the clamp and the
    ratings stopped discriminating -- the Bundesliga and the Eliteserien came out
    within 1% of each other. Damping makes the iteration a contraction, so it
    settles on a spread instead of a wall.
    """
    margins: dict[int, float] = {league_id: 0.0 for league_id in observations}
    for _ in range(ITERATIONS):
        updated = {
            league_id: sum(
                margin + (OPPONENT_CORRECTION_DAMPING * margins[opponent])
                for opponent, margin in entries
            )
            / len(entries)
            for league_id, entries in observations.items()
        }
        # Recentre on zero so the ratings describe relative level rather than
        # drifting together; the absolute level is already carried by the
        # target competition's own goal baseline.
        mean = sum(updated.values()) / len(updated)
        margins = {league_id: value - mean for league_id, value in updated.items()}
    return margins


def _to_log_strength(margin: float, sample_size: int) -> float:
    shrunk = margin * (sample_size / (sample_size + SHRINKAGE_MATCHES))
    return _clamp(shrunk * MARGIN_TO_LOG_GOALS, -MAX_LOG_STRENGTH, MAX_LOG_STRENGTH)


def _clamp(value: float, minimum: float, maximum: float) -> float:
    return min(max(value, minimum), maximum)


def measure_home_advantage(history: Iterable[FixtureRecord], league_id: int) -> float:
    """Measure a competition's home edge as a goal multiplier.

    Home advantage is real and varies by competition, and the model previously
    applied none at all. It is returned as gamma: one side's expected goals is
    multiplied by it and the other divided, so the ratio it is built from is
    square-rooted to split the effect evenly across the two.
    """
    fixtures = [
        record
        for record in history
        if record.league_id == league_id and _is_finished(record)
    ]
    if len(fixtures) < MIN_HOME_ADVANTAGE_SAMPLE:
        return DEFAULT_HOME_ADVANTAGE
    home_goals = sum(record.home_goals or 0 for record in fixtures) / len(fixtures)
    away_goals = sum(record.away_goals or 0 for record in fixtures) / len(fixtures)
    if home_goals <= 0 or away_goals <= 0:
        return DEFAULT_HOME_ADVANTAGE
    return _clamp(math.sqrt(home_goals / away_goals), 1.0, MAX_HOME_ADVANTAGE)
