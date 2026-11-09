"""Opponent-adjusted attack and defence ratings for individual teams.

League strength explains why a goal in one competition is worth more than a
goal in another. It says nothing about the fixtures *inside* a league, where a
team's raw scoring average is still treated as if every opponent were the same.
Two mid-table sides with identical goals-per-game are not equally good when one
has played the bottom five and the other the top five, and a side that has kept
clean sheets against relegation candidates is not the defence its record claims.

This module fits a simplified Dixon-Coles attack/defence model per league. A
team's attack is the goals it scored measured against the defences it actually
faced, and its defence is the goals it conceded measured against the attacks it
actually faced; both are solved together by iteration because each depends on
the other. Ratings are multipliers around 1.0 relative to the team's own league,
so they compose with the between-league scales rather than duplicating them.

Matches are not weighted equally either: recent form carries more weight than
old form through an exponential decay, and a rating built on few matches is
shrunk toward the league average so a two-game sample cannot masquerade as an
established level.
"""

from __future__ import annotations

from collections import defaultdict
from dataclasses import dataclass
from typing import Iterable

from prediction_engine.features import (
    DEFAULT_HOME_ADVANTAGE,
    FixtureRecord,
    _is_finished,
    credible_leagues,
    team_primary_leagues,
)


# Passes of the alternating attack/defence solve. The fit converges quickly;
# beyond about five passes the ratings move by less than a rounding error.
ITERATIONS = 6

# Decay-weighted matches at which a rating sits halfway to its league average.
# Chosen with DECAY on the walk-forward tuning window, 2019-07 to 2022-07.
SHRINKAGE_MATCHES = 12

# Per-match recency decay. A match n matches back carries DECAY**n weight. At
# 0.90 a rating rested on about ten matches, too few to tell teams apart.
DECAY = 0.98

# Ratings are bounded so one freak scoreline cannot produce an absurd
# multiplier: the best attacks are roughly twice the league average, not ten
# times it.
MIN_RATING = 0.35
MAX_RATING = 2.60


@dataclass(frozen=True)
class TeamStrength:
    team_id: int
    league_id: int
    attack: float
    defence: float
    sample_size: int
    league_goal_rate: float


NEUTRAL_TEAM_STRENGTH = TeamStrength(
    team_id=0, league_id=0, attack=1.0, defence=1.0, sample_size=0, league_goal_rate=0.0
)


@dataclass(frozen=True)
class TeamStrengthTable:
    ratings: dict[int, TeamStrength]

    def for_team(self, team_id: int) -> TeamStrength:
        return self.ratings.get(team_id, NEUTRAL_TEAM_STRENGTH)

    def has(self, team_id: int) -> bool:
        return team_id in self.ratings


@dataclass(frozen=True)
class _Observation:
    team_id: int
    opponent_id: int
    goals_for: float
    goals_against: float
    at_home: bool
    weight: float


def compute_team_strength(
    history: Iterable[FixtureRecord],
    *,
    home_advantage: float = DEFAULT_HOME_ADVANTAGE,
) -> TeamStrengthTable:
    """Fit attack/defence multipliers for every team, within its own league."""
    records = [record for record in history if _is_finished(record)]
    if not records:
        return TeamStrengthTable(ratings={})

    primary = team_primary_leagues(records)
    eligible = credible_leagues(records)
    by_league: dict[int, list[FixtureRecord]] = defaultdict(list)
    for record in records:
        # Rate a team on its own league's matches. Continental results are
        # evidence about leagues, not about a team's level within its league,
        # and mixing them here would double-count the between-league scale.
        # A competition that is not a credible league supplies no baseline: its
        # goal rate would become the team's expected-goals anchor.
        if record.league_id not in eligible:
            continue
        if primary.get(record.home_team_id) == primary.get(record.away_team_id):
            by_league[record.league_id].append(record)

    ratings: dict[int, TeamStrength] = {}
    for league_id, league_records in by_league.items():
        ratings.update(_fit_league(league_id, league_records, home_advantage))
    return TeamStrengthTable(ratings=ratings)


def _fit_league(
    league_id: int,
    records: list[FixtureRecord],
    home_advantage: float,
) -> dict[int, TeamStrength]:
    league_goal_rate = _league_goal_rate(records)
    if league_goal_rate <= 0:
        return {}

    observations = _weighted_observations(records)
    if not observations:
        return {}

    teams = sorted(observations.keys())
    attack = {team_id: 1.0 for team_id in teams}
    defence = {team_id: 1.0 for team_id in teams}

    for _ in range(ITERATIONS):
        attack = {
            team_id: _solve_rate(
                numerator=sum(o.goals_for * o.weight for o in entries),
                denominator=sum(
                    league_goal_rate
                    * defence.get(o.opponent_id, 1.0)
                    * _venue_factor(o.at_home, home_advantage)
                    * o.weight
                    for o in entries
                ),
            )
            for team_id, entries in observations.items()
        }
        defence = {
            team_id: _solve_rate(
                numerator=sum(o.goals_against * o.weight for o in entries),
                denominator=sum(
                    league_goal_rate
                    * attack.get(o.opponent_id, 1.0)
                    # The opponent's scoring is boosted when *they* are at home,
                    # which is the inverse of this team's venue.
                    * _venue_factor(not o.at_home, home_advantage)
                    * o.weight
                    for o in entries
                ),
            )
            for team_id, entries in observations.items()
        }
        attack = _recentre(attack)
        defence = _recentre(defence)

    fitted: dict[int, TeamStrength] = {}
    for team_id, entries in observations.items():
        effective_matches = sum(o.weight for o in entries)
        fitted[team_id] = TeamStrength(
            team_id=team_id,
            league_id=league_id,
            attack=_shrink(attack[team_id], effective_matches),
            defence=_shrink(defence[team_id], effective_matches),
            sample_size=len(entries),
            league_goal_rate=league_goal_rate,
        )
    return fitted


def _weighted_observations(records: list[FixtureRecord]) -> dict[int, list[_Observation]]:
    """One weighted observation per team per match, newest weighted highest."""
    ordered = sorted(records, key=lambda record: record.kickoff_at, reverse=True)
    per_team_seen: dict[int, int] = defaultdict(int)
    observations: dict[int, list[_Observation]] = defaultdict(list)
    for record in ordered:
        home_goals = float(record.home_goals or 0)
        away_goals = float(record.away_goals or 0)
        for team_id, opponent_id, goals_for, goals_against, at_home in (
            (record.home_team_id, record.away_team_id, home_goals, away_goals, True),
            (record.away_team_id, record.home_team_id, away_goals, home_goals, False),
        ):
            weight = DECAY ** per_team_seen[team_id]
            per_team_seen[team_id] += 1
            observations[team_id].append(
                _Observation(
                    team_id=team_id,
                    opponent_id=opponent_id,
                    goals_for=goals_for,
                    goals_against=goals_against,
                    at_home=at_home,
                    weight=weight,
                )
            )
    return dict(observations)


def _league_goal_rate(records: list[FixtureRecord]) -> float:
    """Goals per team per match across the league."""
    total = sum(float(record.home_goals or 0) + float(record.away_goals or 0) for record in records)
    return total / (len(records) * 2) if records else 0.0


def _venue_factor(at_home: bool, home_advantage: float) -> float:
    gamma = home_advantage if home_advantage > 0 else DEFAULT_HOME_ADVANTAGE
    return gamma if at_home else 1 / gamma


def _solve_rate(*, numerator: float, denominator: float) -> float:
    if denominator <= 0:
        return 1.0
    return _clamp(numerator / denominator, MIN_RATING, MAX_RATING)


def _recentre(ratings: dict[int, float]) -> dict[int, float]:
    """Hold the league average at 1.0 so ratings stay relative, not drifting."""
    if not ratings:
        return ratings
    mean = sum(ratings.values()) / len(ratings)
    if mean <= 0:
        return ratings
    return {team_id: value / mean for team_id, value in ratings.items()}


def _shrink(rating: float, effective_matches: float) -> float:
    weight = effective_matches / (effective_matches + SHRINKAGE_MATCHES)
    return _clamp(1.0 + ((rating - 1.0) * weight), MIN_RATING, MAX_RATING)


def _clamp(value: float, minimum: float, maximum: float) -> float:
    return min(max(value, minimum), maximum)
