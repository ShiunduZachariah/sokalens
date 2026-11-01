from __future__ import annotations

from dataclasses import dataclass
import math


@dataclass(frozen=True)
class ModelBConfig:
    one_x_two_shift_cap: float = 0.16
    btts_shift_cap: float = 0.08
    total_goals_shift_cap: float = 0.08
    max_points_per_game: float = 3.0


@dataclass(frozen=True)
class ModelBInputs:
    home_recent_ppg: float
    away_recent_ppg: float
    home_btts_rate: float
    away_btts_rate: float
    model_a_btts_yes: float
    home_over_2_5_rate: float
    away_over_2_5_rate: float
    model_a_over_2_5: float


@dataclass(frozen=True)
class ModelBResult:
    current_strength: dict[str, float]
    strength_differential: float
    market_deltas: dict[str, dict[str, float]]


def run_model_b(inputs: ModelBInputs, config: ModelBConfig | None = None) -> ModelBResult:
    active_config = config or ModelBConfig()
    _validate_config(active_config)
    _validate_inputs(inputs, active_config)

    current_strength = {
        "home": _normalize_points_per_game(inputs.home_recent_ppg, active_config.max_points_per_game),
        "away": _normalize_points_per_game(inputs.away_recent_ppg, active_config.max_points_per_game),
    }
    strength_differential = current_strength["home"] - current_strength["away"]

    btts_delta = _rate_delta(
        inputs.home_btts_rate,
        inputs.away_btts_rate,
        inputs.model_a_btts_yes,
        active_config.btts_shift_cap,
    )
    total_goals_delta = _rate_delta(
        inputs.home_over_2_5_rate,
        inputs.away_over_2_5_rate,
        inputs.model_a_over_2_5,
        active_config.total_goals_shift_cap,
    )

    return ModelBResult(
        current_strength=current_strength,
        strength_differential=strength_differential,
        market_deltas={
            "match_result": map_strength_differential_to_one_x_two_deltas(
                strength_differential,
                active_config.one_x_two_shift_cap,
            ),
            "btts": {"yes": btts_delta, "no": -btts_delta},
            "total_goals": {"over_2.5": total_goals_delta, "under_2.5": -total_goals_delta},
        },
    )


def map_strength_differential_to_one_x_two_deltas(strength_differential: float, shift_cap: float = 0.16) -> dict[str, float]:
    if not math.isfinite(strength_differential):
        raise ValueError("strength_differential must be finite")
    if not _valid_cap(shift_cap):
        raise ValueError("shift_cap must be a finite ratio between 0 and 1")

    bounded_differential = _clamp(strength_differential, -1.0, 1.0)
    shift = min(abs(bounded_differential) * shift_cap, shift_cap)
    counter_shift = -(shift / 2)
    if bounded_differential >= 0:
        return {"home": shift, "draw": counter_shift, "away": counter_shift}
    return {"home": counter_shift, "draw": counter_shift, "away": shift}


def _rate_delta(home_rate: float, away_rate: float, model_a_baseline: float, shift_cap: float) -> float:
    form_rate = (home_rate + away_rate) / 2
    return _clamp(form_rate - model_a_baseline, -shift_cap, shift_cap)


def _normalize_points_per_game(points_per_game: float, maximum: float) -> float:
    return _clamp(points_per_game / maximum, 0.0, 1.0)


def _validate_config(config: ModelBConfig) -> None:
    for field_name in ("one_x_two_shift_cap", "btts_shift_cap", "total_goals_shift_cap"):
        if not _valid_cap(getattr(config, field_name)):
            raise ValueError(f"{field_name} must be a finite ratio between 0 and 1")
    if not (math.isfinite(config.max_points_per_game) and config.max_points_per_game > 0):
        raise ValueError("max_points_per_game must be finite and positive")


def _validate_inputs(inputs: ModelBInputs, config: ModelBConfig) -> None:
    for field_name in ("home_recent_ppg", "away_recent_ppg"):
        value = getattr(inputs, field_name)
        if not math.isfinite(value):
            raise ValueError(f"{field_name} must be finite")
        if value < 0:
            raise ValueError(f"{field_name} must be non-negative")
        if value > config.max_points_per_game:
            raise ValueError(f"{field_name} must not exceed max_points_per_game")

    for field_name in (
        "home_btts_rate",
        "away_btts_rate",
        "model_a_btts_yes",
        "home_over_2_5_rate",
        "away_over_2_5_rate",
        "model_a_over_2_5",
    ):
        value = getattr(inputs, field_name)
        if not (math.isfinite(value) and 0 <= value <= 1):
            raise ValueError(f"{field_name} must be a finite ratio between 0 and 1")


def _valid_cap(value: float) -> bool:
    return math.isfinite(value) and 0 <= value <= 1


def _clamp(value: float, minimum: float, maximum: float) -> float:
    return min(max(value, minimum), maximum)
