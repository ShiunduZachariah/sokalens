from __future__ import annotations

from dataclasses import dataclass
from typing import Callable


Matrix = list[list[float]]
ScorePredicate = Callable[[int, int], bool]


@dataclass(frozen=True)
class MarketLeg:
    market: str
    selection: str
    predicate: ScorePredicate

    @classmethod
    def match_result(cls, selection: str) -> MarketLeg:
        if selection == "home":
            return cls("match_result", selection, lambda home, away: home > away)
        if selection == "draw":
            return cls("match_result", selection, lambda home, away: home == away)
        if selection == "away":
            return cls("match_result", selection, lambda home, away: away > home)
        raise ValueError('match_result selection must be "home", "draw", or "away"')

    @classmethod
    def total_goals(cls, side: str, line: float) -> MarketLeg:
        if side == "under":
            return cls("total_goals", f"under_{line}", lambda home, away: home + away < line)
        if side == "over":
            return cls("total_goals", f"over_{line}", lambda home, away: home + away > line)
        raise ValueError('total_goals side must be "under" or "over"')

    @classmethod
    def btts(cls, selection: str) -> MarketLeg:
        if selection == "yes":
            return cls("btts", selection, lambda home, away: home > 0 and away > 0)
        if selection == "no":
            return cls("btts", selection, lambda home, away: home == 0 or away == 0)
        raise ValueError('btts selection must be "yes" or "no"')


@dataclass(frozen=True)
class ComponentEligibility:
    eligible: bool
    tier: str
    reason: str | None = None


def joint_probability(score_matrix: Matrix, left: MarketLeg, right: MarketLeg) -> float:
    matrix = _normalize(score_matrix)
    return sum(
        probability
        for home, row in enumerate(matrix)
        for away, probability in enumerate(row)
        if left.predicate(home, away) and right.predicate(home, away)
    )


def combination_eligible(left: ComponentEligibility, right: ComponentEligibility) -> ComponentEligibility:
    inherited_tier = _stricter_tier(left.tier, right.tier)
    if left.eligible and right.eligible:
        return ComponentEligibility(eligible=True, tier=inherited_tier)

    reasons = [component.reason for component in (left, right) if not component.eligible and component.reason]
    reason = "component market ineligible"
    if reasons:
        reason = f"{reason}: {'; '.join(reasons)}"
    return ComponentEligibility(eligible=False, tier=inherited_tier, reason=reason)


def _normalize(matrix: Matrix) -> Matrix:
    _validate_matrix(matrix)
    total = sum(sum(row) for row in matrix)
    if total <= 0:
        raise ValueError("score_matrix total must be positive")
    return [[float(value) / total for value in row] for row in matrix]


def _validate_matrix(matrix: Matrix) -> None:
    if not matrix or not matrix[0]:
        raise ValueError("score_matrix must not be empty")
    width = len(matrix[0])
    for row in matrix:
        if len(row) != width:
            raise ValueError("score_matrix rows must all have the same length")
        if any(value < 0 for value in row):
            raise ValueError("score_matrix probabilities must be non-negative")


def _stricter_tier(left: str, right: str) -> str:
    order = {"SKIP": 0, "BASIC": 1, "FULL": 2}
    left_rank = order.get(left, 1)
    right_rank = order.get(right, 1)
    return left if left_rank <= right_rank else right
