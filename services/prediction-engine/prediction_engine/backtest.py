from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass

from prediction_engine.persistence import ConfidenceTier


@dataclass(frozen=True)
class BacktestPrediction:
    fixture_id: str
    market: str
    prediction: str
    confidence_tier: ConfidenceTier


@dataclass(frozen=True)
class BacktestResult:
    total_predictions: int
    correct_predictions: int
    overall_accuracy: float
    accuracy_by_tier: dict[ConfidenceTier, float]
    confidence_ordering_holds: bool


def run_backtest(
    predictions: list[BacktestPrediction],
    *,
    actual_outcome: Callable[[BacktestPrediction], str],
) -> BacktestResult:
    correct_by_tier = {tier: 0 for tier in ConfidenceTier}
    total_by_tier = {tier: 0 for tier in ConfidenceTier}
    total_correct = 0
    for prediction in predictions:
        is_correct = prediction.prediction == actual_outcome(prediction)
        total_by_tier[prediction.confidence_tier] += 1
        if is_correct:
            total_correct += 1
            correct_by_tier[prediction.confidence_tier] += 1

    accuracy_by_tier = {
        tier: (correct_by_tier[tier] / total_by_tier[tier]) if total_by_tier[tier] else 0.0
        for tier in ConfidenceTier
    }
    return BacktestResult(
        total_predictions=len(predictions),
        correct_predictions=total_correct,
        overall_accuracy=(total_correct / len(predictions)) if predictions else 0.0,
        accuracy_by_tier=accuracy_by_tier,
        confidence_ordering_holds=_confidence_ordering_holds(accuracy_by_tier, total_by_tier),
    )


def _confidence_ordering_holds(
    accuracy_by_tier: dict[ConfidenceTier, float],
    total_by_tier: dict[ConfidenceTier, int],
) -> bool:
    ordered = [ConfidenceTier.STRONG, ConfidenceTier.MEDIUM, ConfidenceTier.LOW]
    populated = [tier for tier in ordered if total_by_tier[tier] > 0]
    return all(
        accuracy_by_tier[left] >= accuracy_by_tier[right]
        for left, right in zip(populated, populated[1:], strict=False)
    )
