from __future__ import annotations

from dataclasses import dataclass
from dataclasses import replace
from datetime import UTC, date, datetime, time
from typing import Iterable

from prediction_engine.persistence import ConfidenceTier, EnginePrediction, qualifies_for_best_bet_pool


@dataclass(frozen=True)
class RankingInput:
    probability: float
    confidence: float
    data_completeness: float
    market_accuracy: float
    league_accuracy: float
    edge_vs_odds: float = 0.0


@dataclass(frozen=True)
class RankingWeights:
    probability: float = 0.30
    confidence: float = 0.25
    data_completeness: float = 0.20
    market_accuracy: float = 0.10
    league_accuracy: float = 0.10
    edge_vs_odds: float = 0.05


@dataclass(frozen=True)
class PredictionSlice:
    start_date: date | None = None
    end_date: date | None = None
    league_id: int | None = None
    market: str | None = None
    confidence_tier: ConfidenceTier | None = None


@dataclass(frozen=True)
class ExplanationInput:
    market: str
    prediction: str
    probability: float
    drivers: dict[str, float]
    fallback_flags: tuple[str, ...] = ()
    max_confidence_tier: ConfidenceTier | None = None
    embargo_recently_lifted: bool = False


def rank_score(inputs: RankingInput, weights: RankingWeights | None = None) -> float:
    active_weights = weights or RankingWeights()
    return (
        inputs.probability * active_weights.probability
        + inputs.confidence * active_weights.confidence
        + inputs.data_completeness * active_weights.data_completeness
        + inputs.market_accuracy * active_weights.market_accuracy
        + inputs.league_accuracy * active_weights.league_accuracy
        + inputs.edge_vs_odds * active_weights.edge_vs_odds
    )


def best_bet_pool(
    predictions: Iterable[EnginePrediction],
    *,
    now: datetime,
    minimum_probability: float = 0.55,
    minimum_tier: ConfidenceTier = ConfidenceTier.MEDIUM,
) -> list[EnginePrediction]:
    pool = [
        prediction
        for prediction in predictions
        if qualifies_for_best_bet_pool(
            prediction,
            kickoff_at=_kickoff_at(prediction),
            now=now,
            minimum_probability=minimum_probability,
            minimum_tier=minimum_tier,
        )
    ]
    return sorted(pool, key=lambda prediction: (_stored_ranking_score(prediction), prediction.confidence, prediction.probability), reverse=True)


def assign_best_bet_flags(
    predictions: Iterable[EnginePrediction],
    *,
    now: datetime,
    minimum_probability: float = 0.55,
    minimum_tier: ConfidenceTier = ConfidenceTier.MEDIUM,
) -> list[EnginePrediction]:
    rows = list(predictions)
    return [
        replace(
            prediction,
            is_best_bet=qualifies_for_best_bet_pool(
                prediction,
                kickoff_at=_kickoff_at(prediction),
                now=now,
                minimum_probability=minimum_probability,
                minimum_tier=minimum_tier,
            ),
        )
        for prediction in rows
    ]


def slice_predictions(
    predictions: Iterable[EnginePrediction],
    prediction_slice: PredictionSlice,
) -> list[EnginePrediction]:
    start = (
        datetime.combine(prediction_slice.start_date, time.min, tzinfo=UTC)
        if prediction_slice.start_date is not None
        else None
    )
    end = (
        datetime.combine(prediction_slice.end_date, time.min, tzinfo=UTC)
        if prediction_slice.end_date is not None
        else None
    )
    filtered: list[EnginePrediction] = []
    for prediction in predictions:
        kickoff_at = _kickoff_at(prediction)
        if start is not None and kickoff_at < start:
            continue
        if end is not None and kickoff_at >= end:
            continue
        if prediction_slice.league_id is not None and _league_id(prediction) != prediction_slice.league_id:
            continue
        if prediction_slice.market is not None and prediction.market != prediction_slice.market:
            continue
        if prediction_slice.confidence_tier is not None and prediction.confidence_tier is not prediction_slice.confidence_tier:
            continue
        filtered.append(prediction)
    return sorted(filtered, key=lambda prediction: (_stored_ranking_score(prediction), prediction.generated_at), reverse=True)


def explain_prediction(inputs: ExplanationInput) -> str:
    driver_text = ", ".join(f"{name} {value:g}" for name, value in list(inputs.drivers.items())[:3])
    parts = [
        f"The data suggests {inputs.prediction} in {inputs.market}, with model probability {inputs.probability:.0%}.",
    ]
    if driver_text:
        parts.append(f"Main drivers: {driver_text}.")
    if inputs.fallback_flags:
        parts.append(f"Caveats: fallback flags used: {', '.join(inputs.fallback_flags)}.")
    if inputs.max_confidence_tier is not None:
        parts.append(f"This market is harder to call precisely and is capped at {inputs.max_confidence_tier.value} confidence.")
    if inputs.embargo_recently_lifted:
        parts.append("A recently lifted embargo means intervening fixture information is included.")
    return " ".join(parts)


def _kickoff_at(prediction: EnginePrediction) -> datetime:
    raw = prediction.features_used.get("kickoff_at")
    if not isinstance(raw, str):
        raise ValueError("prediction features_used must include kickoff_at ISO timestamp")
    return datetime.fromisoformat(raw)


def _league_id(prediction: EnginePrediction) -> int:
    raw = prediction.features_used.get("league_id")
    if not isinstance(raw, int):
        raise ValueError("prediction features_used must include integer league_id")
    return raw


def _stored_ranking_score(prediction: EnginePrediction) -> float:
    raw = prediction.features_used.get("ranking_score", 0.0)
    return float(raw) if isinstance(raw, (int, float)) else 0.0
