from __future__ import annotations

from dataclasses import dataclass
import math
from typing import Any


Matrix = list[list[float]]


@dataclass(frozen=True)
class ModelAInputs:
    home_lambda: float
    away_lambda: float
    half_time_home_lambda: float | None = None
    half_time_away_lambda: float | None = None
    max_goals: int = 8
    rho: float = -0.08
    importance_widening: float = 0.0


@dataclass(frozen=True)
class ModelAResult:
    lambdas: dict[str, float]
    score_matrix: Matrix
    markets: dict[str, Any]
    features_used: dict[str, Any]


def poisson_score_matrix(home_lambda: float, away_lambda: float, max_goals: int = 8) -> Matrix:
    _validate_lambda(home_lambda, "home_lambda")
    _validate_lambda(away_lambda, "away_lambda")
    if max_goals < 0:
        raise ValueError("max_goals must be non-negative")

    home_dist = [_poisson_probability(home_lambda, goals) for goals in range(max_goals + 1)]
    away_dist = [_poisson_probability(away_lambda, goals) for goals in range(max_goals + 1)]
    return _normalize([[home_prob * away_prob for away_prob in away_dist] for home_prob in home_dist])


def apply_dixon_coles_correction(matrix: Matrix, home_lambda: float, away_lambda: float, rho: float) -> Matrix:
    corrected = _copy_matrix(matrix)
    if len(corrected) > 1 and len(corrected[0]) > 1 and len(corrected[1]) > 1:
        corrected[0][0] *= 1 - (home_lambda * away_lambda * rho)
        corrected[0][1] *= 1 + (home_lambda * rho)
        corrected[1][0] *= 1 + (away_lambda * rho)
        corrected[1][1] *= 1 - rho
    return _normalize(corrected)


def derive_tier_a_markets(
    score_matrix: Matrix,
    *,
    half_time_matrix: Matrix | None = None,
    second_half_matrix: Matrix | None = None,
    top_correct_scores: int = 5,
) -> dict[str, Any]:
    matrix = _normalize(score_matrix)
    ht_matrix = _normalize(half_time_matrix) if half_time_matrix is not None else matrix
    second_matrix = _normalize(second_half_matrix) if second_half_matrix is not None else ht_matrix

    match_result = _result_probabilities(matrix)
    half_time_result = _result_probabilities(ht_matrix)
    second_half_result = _result_probabilities(second_matrix)
    btts_yes = _sum_cells(matrix, lambda home, away: home > 0 and away > 0)

    total_goals = _total_goals_markets(matrix)
    team_totals = {
        "home": _team_total_goals_markets(_row_sums(matrix)),
        "away": _team_total_goals_markets(_column_sums(matrix)),
    }

    return {
        "match_result": match_result,
        "double_chance": {
            "home_or_draw": match_result["home"] + match_result["draw"],
            "draw_or_away": match_result["draw"] + match_result["away"],
            "home_or_away": match_result["home"] + match_result["away"],
        },
        "draw_no_bet": _draw_no_bet(match_result),
        "total_goals": total_goals,
        "btts": {"yes": btts_yes, "no": 1 - btts_yes},
        "btts_total_goals": _btts_total_goals(matrix),
        "btts_match_result": _btts_match_result(matrix),
        "correct_score": _top_correct_scores(matrix, top_correct_scores),
        "half_time_full_time": _half_time_full_time(half_time_result, match_result),
        "half_time_result": half_time_result,
        "highest_scoring_half": _highest_scoring_half(ht_matrix, second_matrix),
        "team_to_score_first": _team_to_score_first(matrix),
        "team_total_goals": team_totals,
        "win_to_nil": {
            "home": _sum_cells(matrix, lambda home, away: home > away and away == 0),
            "away": _sum_cells(matrix, lambda home, away: away > home and home == 0),
        },
        "win_either_half": {
            "home": _union_probability(half_time_result["home"], second_half_result["home"]),
            "away": _union_probability(half_time_result["away"], second_half_result["away"]),
        },
        "clean_sheet": {
            "home_yes": _sum_cells(matrix, lambda _home, away: away == 0),
            "home_no": 1 - _sum_cells(matrix, lambda _home, away: away == 0),
            "away_yes": _sum_cells(matrix, lambda home, _away: home == 0),
            "away_no": 1 - _sum_cells(matrix, lambda home, _away: home == 0),
        },
        "btts_both_halves": _btts_both_halves(ht_matrix, second_matrix),
        "odd_even_total_goals": {
            "odd": _sum_cells(matrix, lambda home, away: (home + away) % 2 == 1),
            "even": _sum_cells(matrix, lambda home, away: (home + away) % 2 == 0),
        },
    }


def run_model_a(inputs: ModelAInputs) -> ModelAResult:
    home_lambda = inputs.home_lambda
    away_lambda = inputs.away_lambda
    half_time_home_lambda = inputs.half_time_home_lambda if inputs.half_time_home_lambda is not None else home_lambda / 2
    half_time_away_lambda = inputs.half_time_away_lambda if inputs.half_time_away_lambda is not None else away_lambda / 2

    score_matrix = poisson_score_matrix(home_lambda, away_lambda, inputs.max_goals)
    if inputs.importance_widening:
        score_matrix = _widen_matrix(score_matrix, inputs.importance_widening)
    score_matrix = apply_dixon_coles_correction(score_matrix, home_lambda, away_lambda, inputs.rho)

    half_time_matrix = poisson_score_matrix(half_time_home_lambda, half_time_away_lambda, inputs.max_goals)
    second_half_matrix = poisson_score_matrix(
        max(home_lambda - half_time_home_lambda, 0.0),
        max(away_lambda - half_time_away_lambda, 0.0),
        inputs.max_goals,
    )
    markets = derive_tier_a_markets(
        score_matrix,
        half_time_matrix=half_time_matrix,
        second_half_matrix=second_half_matrix,
    )
    lambdas = {
        "home": home_lambda,
        "away": away_lambda,
        "half_time_home": half_time_home_lambda,
        "half_time_away": half_time_away_lambda,
    }
    return ModelAResult(
        lambdas=lambdas,
        score_matrix=score_matrix,
        markets=markets,
        features_used={"model_a_score_matrix": score_matrix},
    )


def _poisson_probability(lambda_value: float, goals: int) -> float:
    return math.exp(-lambda_value) * (lambda_value**goals) / math.factorial(goals)


def _validate_lambda(lambda_value: float, name: str) -> None:
    if lambda_value < 0:
        raise ValueError(f"{name} must be non-negative")


def _copy_matrix(matrix: Matrix) -> Matrix:
    return [[float(value) for value in row] for row in matrix]


def _normalize(matrix: Matrix) -> Matrix:
    copied = _copy_matrix(matrix)
    total = sum(sum(row) for row in copied)
    if total <= 0:
        raise ValueError("matrix total must be positive")
    return [[value / total for value in row] for row in copied]


def _widen_matrix(matrix: Matrix, widening: float) -> Matrix:
    bounded = min(max(widening, 0.0), 1.0)
    cell_count = sum(len(row) for row in matrix)
    uniform_probability = 1 / cell_count
    return _normalize(
        [
            [((1 - bounded) * value) + (bounded * uniform_probability) for value in row]
            for row in matrix
        ]
    )


def _sum_cells(matrix: Matrix, predicate: Any) -> float:
    return sum(probability for home, row in enumerate(matrix) for away, probability in enumerate(row) if predicate(home, away))


def _row_sums(matrix: Matrix) -> list[float]:
    return [sum(row) for row in matrix]


def _column_sums(matrix: Matrix) -> list[float]:
    return [sum(row[column] for row in matrix) for column in range(len(matrix[0]))]


def _result_probabilities(matrix: Matrix) -> dict[str, float]:
    return {
        "home": _sum_cells(matrix, lambda home, away: home > away),
        "draw": _sum_cells(matrix, lambda home, away: home == away),
        "away": _sum_cells(matrix, lambda home, away: home < away),
    }


def _draw_no_bet(match_result: dict[str, float]) -> dict[str, float]:
    decisive_probability = match_result["home"] + match_result["away"]
    return {
        "home": match_result["home"] / decisive_probability,
        "away": match_result["away"] / decisive_probability,
    }


def _total_goals_markets(matrix: Matrix) -> dict[str, float]:
    markets: dict[str, float] = {}
    for line in range(6):
        under = _sum_cells(matrix, lambda home, away, line=line: home + away <= line)
        markets[f"under_{line}.5"] = under
        markets[f"over_{line}.5"] = 1 - under
    return markets


def _team_total_goals_markets(distribution: list[float]) -> dict[str, float]:
    markets: dict[str, float] = {}
    for line in range(4):
        under = sum(probability for goals, probability in enumerate(distribution) if goals <= line)
        markets[f"under_{line}.5"] = under
        markets[f"over_{line}.5"] = 1 - under
    return markets


def _btts_total_goals(matrix: Matrix) -> dict[str, float]:
    """BTTS combined with a total-goals line, across both BTTS sides.

    The "no" half used to be missing, so the market could only ever be
    published for a fixture the model expected both teams to score in. Any
    fixture predicted BTTS-no had no satisfiable selection here and the market
    was dropped by the score-consistency filter. Settlement has always graded
    "no_over"/"no_under" outcomes, so the gap was in generation alone.
    """
    markets: dict[str, float] = {}
    for line in range(6):
        both_scored = lambda home, away: home > 0 and away > 0  # noqa: E731
        markets[f"yes_under_{line}.5"] = _sum_cells(
            matrix, lambda home, away, line=line: both_scored(home, away) and home + away <= line
        )
        markets[f"yes_over_{line}.5"] = _sum_cells(
            matrix, lambda home, away, line=line: both_scored(home, away) and home + away > line
        )
        markets[f"no_under_{line}.5"] = _sum_cells(
            matrix, lambda home, away, line=line: not both_scored(home, away) and home + away <= line
        )
        markets[f"no_over_{line}.5"] = _sum_cells(
            matrix, lambda home, away, line=line: not both_scored(home, away) and home + away > line
        )
    return markets


def _btts_match_result(matrix: Matrix) -> dict[str, float]:
    return {
        "home_yes": _sum_cells(matrix, lambda home, away: home > away and home > 0 and away > 0),
        "draw_yes": _sum_cells(matrix, lambda home, away: home == away and home > 0 and away > 0),
        "away_yes": _sum_cells(matrix, lambda home, away: away > home and home > 0 and away > 0),
        "home_no": _sum_cells(matrix, lambda home, away: home > away and (home == 0 or away == 0)),
        "draw_no": _sum_cells(matrix, lambda home, away: home == away and (home == 0 or away == 0)),
        "away_no": _sum_cells(matrix, lambda home, away: away > home and (home == 0 or away == 0)),
    }


def _top_correct_scores(matrix: Matrix, limit: int) -> list[dict[str, float | str]]:
    scorelines = [
        {"score": f"{home}-{away}", "probability": probability}
        for home, row in enumerate(matrix)
        for away, probability in enumerate(row)
    ]
    return sorted(scorelines, key=lambda scoreline: (-float(scoreline["probability"]), str(scoreline["score"])))[:limit]


def _half_time_full_time(half_time_result: dict[str, float], full_time_result: dict[str, float]) -> dict[str, float]:
    return {
        f"{half_label}_{full_label}": half_probability * full_probability
        for half_label, half_probability in half_time_result.items()
        for full_label, full_probability in full_time_result.items()
    }


def _total_distribution(matrix: Matrix) -> dict[int, float]:
    distribution: dict[int, float] = {}
    for home, row in enumerate(matrix):
        for away, probability in enumerate(row):
            distribution[home + away] = distribution.get(home + away, 0.0) + probability
    return distribution


def _highest_scoring_half(first_half_matrix: Matrix, second_half_matrix: Matrix) -> dict[str, float]:
    first = _total_distribution(first_half_matrix)
    second = _total_distribution(second_half_matrix)
    first_higher = sum(first_probability * second_probability for first_goals, first_probability in first.items() for second_goals, second_probability in second.items() if first_goals > second_goals)
    second_higher = sum(first_probability * second_probability for first_goals, first_probability in first.items() for second_goals, second_probability in second.items() if second_goals > first_goals)
    equal = sum(first_probability * second_probability for first_goals, first_probability in first.items() for second_goals, second_probability in second.items() if first_goals == second_goals)
    return {"first_half": first_higher, "second_half": second_higher, "equal": equal}


def _team_to_score_first(matrix: Matrix) -> dict[str, float]:
    no_goals = matrix[0][0]
    home_expected_goals = sum(home * probability for home, row in enumerate(matrix) for probability in row)
    away_expected_goals = sum(away * probability for row in matrix for away, probability in enumerate(row))
    expected_total = home_expected_goals + away_expected_goals
    if expected_total == 0:
        return {"home": 0.0, "away": 0.0, "no_goals": 1.0}
    scoring_probability = 1 - no_goals
    return {
        "home": scoring_probability * (home_expected_goals / expected_total),
        "away": scoring_probability * (away_expected_goals / expected_total),
        "no_goals": no_goals,
    }


def _union_probability(left: float, right: float) -> float:
    return left + right - (left * right)


def _btts_both_halves(first_half_matrix: Matrix, second_half_matrix: Matrix) -> dict[str, float]:
    first_yes = _sum_cells(first_half_matrix, lambda home, away: home > 0 and away > 0)
    second_yes = _sum_cells(second_half_matrix, lambda home, away: home > 0 and away > 0)
    yes = first_yes * second_yes
    return {"yes": yes, "no": 1 - yes}
