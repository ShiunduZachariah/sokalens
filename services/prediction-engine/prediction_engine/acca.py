from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from typing import Iterable

from prediction_engine.persistence import ConfidenceTier, EnginePrediction
from prediction_engine.ranking import _kickoff_at, _stored_ranking_score


@dataclass(frozen=True)
class AccaRequest:
    start_datetime: datetime
    end_datetime: datetime
    legs_requested: int = 6
    target_odds: float | None = None
    markets_filter: tuple[str, ...] = ()
    per_fixture_cap: int = 2
    minimum_confidence_tier: ConfidenceTier = ConfidenceTier.LOW


@dataclass(frozen=True)
class AccaSelection:
    requested_at: datetime
    legs_requested: int
    target_odds: float | None
    markets_filter: tuple[str, ...]
    start_datetime: datetime
    end_datetime: datetime
    legs: list[EnginePrediction]
    combined_fair_probability: float
    combined_fair_odds: float
    leg_count: int
    any_leg_embargoed: bool
    target_met: bool
    explanation: str


def select_acca(predictions: Iterable[EnginePrediction], request: AccaRequest) -> AccaSelection:
    if request.legs_requested <= 0:
        raise ValueError("legs_requested must be positive")
    if request.per_fixture_cap <= 0:
        raise ValueError("per_fixture_cap must be positive")
    candidates = [
        prediction
        for prediction in predictions
        if _eligible_leg(prediction, request)
    ]
    candidates.sort(key=lambda prediction: (_stored_ranking_score(prediction), prediction.confidence, prediction.probability), reverse=True)

    selected: list[EnginePrediction] = []
    fixture_counts: dict[int, int] = {}
    for candidate in candidates:
        count = fixture_counts.get(candidate.fixture_id, 0)
        # ENG-009 is deliberately stricter than the product-configurable cap:
        # until the typed correlation support matrix exists, no second leg from
        # one fixture may enter an acca. Standalone pre-priced combination
        # markets remain eligible as one leg.
        if count >= min(request.per_fixture_cap, 1):
            continue
        selected.append(candidate)
        fixture_counts[candidate.fixture_id] = count + 1
        if len(selected) >= request.legs_requested:
            break

    combined_probability = 1.0
    for leg in selected:
        combined_probability *= leg.probability
    combined_odds = (1 / combined_probability) if combined_probability > 0 else 0.0
    target_met = request.target_odds is None or combined_odds >= request.target_odds
    explanation = _explain_selection(selected, request, combined_odds, target_met)
    return AccaSelection(
        requested_at=request.start_datetime,
        legs_requested=request.legs_requested,
        target_odds=request.target_odds,
        markets_filter=request.markets_filter,
        start_datetime=request.start_datetime,
        end_datetime=request.end_datetime,
        legs=selected,
        combined_fair_probability=combined_probability,
        combined_fair_odds=combined_odds,
        leg_count=len(selected),
        any_leg_embargoed=any(leg.embargo_active for leg in selected),
        target_met=target_met,
        explanation=explanation,
    )


def _eligible_leg(prediction: EnginePrediction, request: AccaRequest) -> bool:
    if not prediction.is_best_bet:
        return False
    if prediction.embargo_active or prediction.superseded_at is not None:
        return False
    if _tier_rank(prediction.confidence_tier) < _tier_rank(request.minimum_confidence_tier):
        return False
    if request.markets_filter and prediction.market not in request.markets_filter:
        return False
    kickoff_at = _kickoff_at(prediction)
    return request.start_datetime <= kickoff_at < request.end_datetime


def _tier_rank(tier: ConfidenceTier) -> int:
    return {
        ConfidenceTier.NONE: 0,
        ConfidenceTier.LOW: 1,
        ConfidenceTier.MEDIUM: 2,
        ConfidenceTier.STRONG: 3,
    }[tier]


def _explain_selection(
    selected: list[EnginePrediction],
    request: AccaRequest,
    combined_odds: float,
    target_met: bool,
) -> str:
    if not selected:
        return "No eligible acca legs were available for the requested window."
    profile = ", ".join(f"{leg.market} {leg.prediction}" for leg in selected)
    explanation = f"Selected {len(selected)} legs: {profile}. Combined fair odds are {combined_odds:.2f}."
    if request.target_odds is not None and not target_met:
        explanation += " The requested target could not be met with eligible confidence-filtered legs."
    return explanation
