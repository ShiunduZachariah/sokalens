from __future__ import annotations

import math

from prediction_engine.features import DEFAULT_HOME_ADVANTAGE, FixtureRecord, OddsSnapshot
from prediction_engine.league_strength import compute_league_strength

# Marker written into the odds table so provisional odds can be told apart from
# real bookmaker odds and replaced once real odds arrive.
PROVISIONAL_BOOKMAKER = "sokalens-provisional"

_MAX_GOALS = 10
# Small overround so provisional prices stay plausible instead of "fair" 1/p.
_MARGIN = 1.06
_MIN_LAMBDA = 0.3
_MAX_LAMBDA = 4.5
_MIN_DECIMAL_ODDS = 1.01


def synthesize_provisional_1x2(
    history: list[FixtureRecord],
    home_team_id: int,
    away_team_id: int,
    fixture_id: int,
    *,
    home_advantage: float = DEFAULT_HOME_ADVANTAGE,
) -> OddsSnapshot:
    """Build a provisional 1x2 price from recent goals via an independent
    Poisson model. This is a fallback so the match-quality gate can still run
    for fixtures that will never receive bookmaker odds; real odds always win
    later because they are stored separately under their own bookmaker.

    Goal rates are restated on a neutral-league scale first. These prices are
    displayed when no bookmaker market exists, so leaving them league-blind
    would show a small-league side as favourite against a heavyweight on the
    very fixtures where no market is there to contradict it.
    """
    strength = compute_league_strength(history)
    home_strength = strength.for_team(home_team_id)
    away_strength = strength.for_team(away_team_id)

    home_attack, home_defense = _attack_defense(history, home_team_id)
    away_attack, away_defense = _attack_defense(history, away_team_id)

    home_attack *= home_strength.attack_scale
    home_defense *= home_strength.defence_scale
    away_attack *= away_strength.attack_scale
    away_defense *= away_strength.defence_scale

    home_lambda = _clamp(((home_attack + away_defense) / 2.0) * home_advantage)
    away_lambda = _clamp(((away_attack + home_defense) / 2.0) / home_advantage)

    p_home = 0.0
    p_draw = 0.0
    p_away = 0.0
    for home_goals in range(_MAX_GOALS + 1):
        for away_goals in range(_MAX_GOALS + 1):
            probability = _poisson_pmf(home_goals, home_lambda) * _poisson_pmf(away_goals, away_lambda)
            if home_goals > away_goals:
                p_home += probability
            elif home_goals == away_goals:
                p_draw += probability
            else:
                p_away += probability

    total = p_home + p_draw + p_away
    if total <= 0:
        return OddsSnapshot(fixture_id=fixture_id, home_win=2.5, draw=3.2, away_win=2.9)

    return OddsSnapshot(
        fixture_id=fixture_id,
        home_win=_decimal_odds(p_home / total),
        draw=_decimal_odds(p_draw / total),
        away_win=_decimal_odds(p_away / total),
    )


def _attack_defense(history: list[FixtureRecord], team_id: int) -> tuple[float, float]:
    scored = [r for r in history if (r.home_team_id == team_id or r.away_team_id == team_id)]
    attack_values = [_goals_for(r, team_id) for r in scored]
    defense_values = [_goals_against(r, team_id) for r in scored]
    attack = _average(attack_values)
    defense = _average(defense_values)
    # Neutral 1.2/1.2 keeps the model stable when a side has no usable sample.
    return attack or 1.2, defense or 1.2


def _goals_for(record: FixtureRecord, team_id: int) -> int | None:
    return record.home_goals if record.home_team_id == team_id else record.away_goals


def _goals_against(record: FixtureRecord, team_id: int) -> int | None:
    return record.away_goals if record.home_team_id == team_id else record.home_goals


def _average(values: list[int | None]) -> float | None:
    present = [float(value) for value in values if value is not None]
    if not present:
        return None
    return sum(present) / len(present)


def _clamp(value: float) -> float:
    return max(_MIN_LAMBDA, min(_MAX_LAMBDA, value))


def _poisson_pmf(goals: int, lam: float) -> float:
    return math.exp(-lam) * (lam**goals) / math.factorial(goals)


def _decimal_odds(probability: float) -> float:
    # PostgreSQL requires implied_prob < 1. Extremely one-sided Poisson
    # outputs can otherwise round the displayed price to 1.00 and abort the
    # fixture's entire prediction transaction.
    return max(_MIN_DECIMAL_ODDS, round((1.0 / max(probability, 0.001)) / _MARGIN, 2))
