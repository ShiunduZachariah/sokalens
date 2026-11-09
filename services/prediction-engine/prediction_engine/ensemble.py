from __future__ import annotations

from copy import deepcopy
import math
from typing import Any

from prediction_engine.config import BlendWeights, PredictionEngineConfig, ProbabilityClamp, default_config


_MARKET_GROUPS = {
    "match_result": "1x2",
    "double_chance": "double_chance",
    "draw_no_bet": "1x2",
    "total_goals": "total_goals",
    "btts": "btts",
    "btts_total_goals": "btts",
    "btts_match_result": "btts",
    "correct_score": "correct_score",
    "half_time_full_time": "ht_ft",
    "half_time_result": "1x2",
    "highest_scoring_half": "total_goals",
    "team_to_score_first": "team_goals",
    "team_total_goals": "team_goals",
    "win_to_nil": "win_to_nil",
    "win_either_half": "1x2",
    "clean_sheet": "clean_sheet",
    "btts_both_halves": "btts",
    "odd_even_total_goals": "total_goals",
}


def blend_probability(
    *,
    raw_probability: float,
    adjustment_delta: float,
    weights: BlendWeights,
    clamp: ProbabilityClamp,
) -> float:
    _validate_probability_input(raw_probability, "raw_probability")
    if not math.isfinite(adjustment_delta):
        raise ValueError("adjustment_delta must be finite")

    blended = _blend_unclamped(raw_probability, adjustment_delta, weights)
    return _clamp_probability(blended, clamp)


def blend_tier_a_markets(
    model_a_markets: dict[str, Any],
    model_b_deltas: dict[str, Any],
    config: PredictionEngineConfig | None = None,
) -> dict[str, Any]:
    active_config = config or default_config()
    active_config.validate()

    blended: dict[str, Any] = {}
    for market_name, raw_value in model_a_markets.items():
        weights = _weights_for_market(market_name, active_config)
        market_delta = model_b_deltas.get(market_name, {})
        if market_name == "match_result" and isinstance(raw_value, dict):
            blended[market_name] = _blend_and_renormalize_one_x_two(
                raw_value,
                market_delta if isinstance(market_delta, dict) else {},
                weights,
                active_config.probability_clamp,
            )
        else:
            blended[market_name] = _blend_value(
                raw_value,
                market_delta,
                weights,
                active_config.probability_clamp,
            )
    return blended


def _blend_and_renormalize_one_x_two(
    raw_probabilities: dict[str, Any],
    deltas: dict[str, Any],
    weights: BlendWeights,
    clamp: ProbabilityClamp,
) -> dict[str, float]:
    blended = {
        outcome: _blend_unclamped(float(raw_probability), float(deltas.get(outcome, 0.0)), weights)
        for outcome, raw_probability in raw_probabilities.items()
        if _is_number(raw_probability)
    }
    if sum(blended.values()) <= 0:
        raise ValueError("1X2 blended probabilities must have a positive total before renormalization")
    return bounded_distribution(blended, clamp)


def bounded_distribution(probabilities: dict[str, float], clamp: ProbabilityClamp) -> dict[str, float]:
    """Scale outcomes to sum to one with every outcome inside the clamp.

    Clamping after scaling breaks the total: a form delta can push a small
    outcome below zero, and lifting it to the minimum adds mass nothing takes
    back (0.983 / 0.044 / 0.010 sums to 1.037). So an outcome outside the clamp
    is pinned to the bound and the others share what remains in proportion. A
    distribution already inside the clamp is only scaled.
    """
    size = len(probabilities)
    if not size * clamp.minimum <= 1 <= size * clamp.maximum:
        raise ValueError(f"{size} outcomes cannot sum to one inside the probability clamp")
    free = {outcome: max(probability, 0.0) for outcome, probability in probabilities.items()}
    if sum(free.values()) <= 0:
        raise ValueError("probabilities must have a positive total")

    pinned: dict[str, float] = {}
    while True:
        remaining = 1 - sum(pinned.values())
        free_total = sum(free.values())
        scaled = {
            outcome: remaining * probability / free_total if free_total > 0 else remaining / len(free)
            for outcome, probability in free.items()
        }
        below = [outcome for outcome, probability in scaled.items() if probability < clamp.minimum]
        above = [outcome for outcome, probability in scaled.items() if probability > clamp.maximum]
        if not below and not above:
            bounded = {**pinned, **scaled}
            return {outcome: bounded[outcome] for outcome in probabilities}
        # Pin only the side that clamping would overshoot: pinning both at once
        # can leave a total the outcomes still free cannot make up.
        clamped_total = sum(_clamp_probability(probability, clamp) for probability in scaled.values())
        outcomes, bound = (below, clamp.minimum) if clamped_total >= remaining else (above, clamp.maximum)
        for outcome in outcomes:
            pinned[outcome] = bound
            del free[outcome]


def _blend_value(raw_value: Any, delta_value: Any, weights: BlendWeights, clamp: ProbabilityClamp) -> Any:
    if _is_number(raw_value):
        adjustment_delta = float(delta_value) if _is_number(delta_value) else 0.0
        return blend_probability(
            raw_probability=float(raw_value),
            adjustment_delta=adjustment_delta,
            weights=weights,
            clamp=clamp,
        )
    if isinstance(raw_value, dict):
        delta_mapping = delta_value if isinstance(delta_value, dict) else {}
        return {
            key: _blend_value(value, delta_mapping.get(key), weights, clamp)
            for key, value in raw_value.items()
        }
    if isinstance(raw_value, list):
        delta_list = delta_value if isinstance(delta_value, list) else []
        return [
            _blend_value(value, delta_list[index] if index < len(delta_list) else None, weights, clamp)
            for index, value in enumerate(raw_value)
        ]
    return deepcopy(raw_value)


def _blend_unclamped(raw_probability: float, adjustment_delta: float, weights: BlendWeights) -> float:
    return (weights.statistical_model * raw_probability) + (
        weights.form_adjustment * (raw_probability + adjustment_delta)
    )


def _weights_for_market(market_name: str, config: PredictionEngineConfig) -> BlendWeights:
    market_group = _MARKET_GROUPS.get(market_name, market_name)
    return config.model_weights.get(market_group, BlendWeights(statistical_model=1.0, form_adjustment=0.0))


def _validate_probability_input(value: float, name: str) -> None:
    if not math.isfinite(value):
        raise ValueError(f"{name} must be finite")


def _is_number(value: Any) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool)


def _clamp_probability(value: float, clamp: ProbabilityClamp) -> float:
    return min(max(value, clamp.minimum), clamp.maximum)
