from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
import math


class ConfidenceTier(StrEnum):
    STRONG = "strong"
    MEDIUM = "medium"
    LOW = "low"
    NONE = "none"


@dataclass(frozen=True)
class ConfidenceWeights:
    data_completeness: int = 25
    sample_size: int = 15
    model_agreement: int = 20
    market_cross_validation: int = 15
    lineup_confirmation: int = 10
    market_volatility: int = 5
    historical_accuracy: int = 10


@dataclass(frozen=True)
class ConfidenceInput:
    data_completeness: float
    home_sample_size: int
    away_sample_size: int
    model_a_probability: float
    blended_probability: float
    external_probabilities: tuple[float, ...]
    lineups_confirmed: bool
    market_volatility: float
    historical_accuracy: float
    max_confidence_tier: ConfidenceTier | None = None


@dataclass(frozen=True)
class ConfidenceResult:
    raw_score: float
    calibrated_score: float
    factor_points: dict[str, float]
    uncapped_tier: ConfidenceTier
    tier: ConfidenceTier


@dataclass(frozen=True)
class CalibrationBin:
    lower: float
    upper: float
    total: int
    correct: int

    @property
    def observed_accuracy(self) -> float:
        return (self.correct / self.total) if self.total else 0.0


@dataclass(frozen=True)
class CalibrationProfile:
    bins: tuple[CalibrationBin, ...]
    minimum_bin_size: int = 20

    def calibrate(self, score: float) -> float:
        if not math.isfinite(score):
            raise ValueError("score must be finite")
        for calibration_bin in self.bins:
            _validate_calibration_bin(calibration_bin)
            if calibration_bin.total < self.minimum_bin_size:
                continue
            upper_match = score <= calibration_bin.upper if calibration_bin.upper >= 1.0 else score < calibration_bin.upper
            if calibration_bin.lower <= score and upper_match:
                return min(max(calibration_bin.observed_accuracy, 0.0), 1.0)
        return min(max(score, 0.0), 1.0)


class ConfidenceScorer:
    def score(
        self,
        confidence_input: ConfidenceInput,
        weights: ConfidenceWeights | None = None,
        calibration_profile: CalibrationProfile | None = None,
    ) -> ConfidenceResult:
        active_weights = weights or ConfidenceWeights()
        _validate_input(confidence_input)
        factor_scores = {
            "data_completeness": confidence_input.data_completeness,
            "sample_size": min(min(confidence_input.home_sample_size, confidence_input.away_sample_size) / 15, 1.0),
            "model_agreement": max(
                1 - (abs(confidence_input.model_a_probability - confidence_input.blended_probability) / 0.25),
                0.0,
            ),
            "market_cross_validation": _cross_validation_score(
                confidence_input.blended_probability,
                confidence_input.external_probabilities,
            ),
            "lineup_confirmation": 1.0 if confidence_input.lineups_confirmed else 0.0,
            "market_volatility": 1 - confidence_input.market_volatility,
            "historical_accuracy": confidence_input.historical_accuracy,
        }
        factor_points = {
            factor: score * float(getattr(active_weights, factor))
            for factor, score in factor_scores.items()
        }
        total_weight = sum(float(value) for value in active_weights.__dict__.values())
        raw_score = sum(factor_points.values()) / total_weight if total_weight > 0 else 0.0
        raw_score = min(max(raw_score, 0.0), 1.0)
        calibrated_score = calibration_profile.calibrate(raw_score) if calibration_profile else raw_score
        uncapped_tier = tier_for_score(calibrated_score)
        tier = _apply_cap(uncapped_tier, confidence_input.max_confidence_tier)
        return ConfidenceResult(
            raw_score=raw_score,
            calibrated_score=calibrated_score,
            factor_points=factor_points,
            uncapped_tier=uncapped_tier,
            tier=tier,
        )


def tier_for_score(score: float) -> ConfidenceTier:
    if not math.isfinite(score):
        raise ValueError("score must be finite")
    if score >= 0.75:
        return ConfidenceTier.STRONG
    if score >= 0.65:
        return ConfidenceTier.MEDIUM
    if score >= 0.55:
        return ConfidenceTier.LOW
    return ConfidenceTier.NONE


def _cross_validation_score(blended_probability: float, external_probabilities: tuple[float, ...]) -> float:
    if not external_probabilities:
        return 0.0
    worst_difference = max(abs(blended_probability - reference) for reference in external_probabilities)
    return max(1 - (worst_difference / 0.25), 0.0)


def _apply_cap(tier: ConfidenceTier, cap: ConfidenceTier | None) -> ConfidenceTier:
    if cap is None:
        return tier
    return tier if _tier_rank(tier) <= _tier_rank(cap) else cap


def _tier_rank(tier: ConfidenceTier) -> int:
    return {
        ConfidenceTier.NONE: 0,
        ConfidenceTier.LOW: 1,
        ConfidenceTier.MEDIUM: 2,
        ConfidenceTier.STRONG: 3,
    }[tier]


def _validate_input(confidence_input: ConfidenceInput) -> None:
    ratio_fields = (
        "data_completeness",
        "model_a_probability",
        "blended_probability",
        "market_volatility",
        "historical_accuracy",
    )
    for field in ratio_fields:
        value = getattr(confidence_input, field)
        if not (math.isfinite(value) and 0 <= value <= 1):
            raise ValueError(f"{field} must be between 0 and 1")
    for reference in confidence_input.external_probabilities:
        if not (math.isfinite(reference) and 0 <= reference <= 1):
            raise ValueError("external probabilities must be between 0 and 1")
    if confidence_input.home_sample_size < 0 or confidence_input.away_sample_size < 0:
        raise ValueError("sample sizes must be non-negative")


def _validate_calibration_bin(calibration_bin: CalibrationBin) -> None:
    if not (math.isfinite(calibration_bin.lower) and math.isfinite(calibration_bin.upper)):
        raise ValueError("calibration bin bounds must be finite")
    if not 0 <= calibration_bin.lower < calibration_bin.upper <= 1:
        raise ValueError("calibration bin bounds must satisfy 0 <= lower < upper <= 1")
    if calibration_bin.total < 0 or calibration_bin.correct < 0:
        raise ValueError("calibration bin counts must be non-negative")
    if calibration_bin.correct > calibration_bin.total:
        raise ValueError("calibration bin correct count must not exceed total")
