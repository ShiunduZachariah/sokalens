from __future__ import annotations

from dataclasses import dataclass, replace
from datetime import datetime
from enum import StrEnum
import json
import math
from typing import Any, Iterable
from uuid import UUID, uuid4


class ConfidenceTier(StrEnum):
    STRONG = "strong"
    MEDIUM = "medium"
    LOW = "low"
    NONE = "none"


class GateTier(StrEnum):
    SKIP = "SKIP"
    BASIC = "BASIC"
    FULL = "FULL"


class PredictionLifecycleStage(StrEnum):
    """The `engine_predictions.lifecycle_stage` vocabulary.

    Migration 000071 widened the database CHECK constraint to the architecture
    v2 7.4 list; this enum was left at the three pre-match values, which is why
    no in-play row could be constructed even though the column would have
    accepted one. The pre-match three are unchanged and keep their meaning --
    7.5 is "extend, don't delete", and that applies to the vocabulary as much
    as to the SQL guards.
    """

    EARLY = "EARLY"
    PRE_MATCH = "PRE_MATCH"
    LINEUP_CONFIRMED = "LINEUP_CONFIRMED"
    IN_PLAY_1H = "IN_PLAY_1H"
    HALF_TIME = "HALF_TIME"
    IN_PLAY_2H = "IN_PLAY_2H"
    EXTRA_TIME = "EXTRA_TIME"
    FINAL = "FINAL"


# The stages produced by the in-play path and by nothing else. A pre-match
# surface that filters on lifecycle stage can exclude exactly these.
IN_PLAY_LIFECYCLE_STAGES = frozenset(
    {
        PredictionLifecycleStage.IN_PLAY_1H,
        PredictionLifecycleStage.HALF_TIME,
        PredictionLifecycleStage.IN_PLAY_2H,
        PredictionLifecycleStage.EXTRA_TIME,
    }
)


@dataclass(frozen=True)
class EnginePrediction:
    fixture_id: int
    market: str
    prediction: str
    probability: float
    confidence: float
    confidence_tier: ConfidenceTier
    is_best_bet: bool
    lifecycle_stage: PredictionLifecycleStage
    data_completeness: float
    features_used: dict[str, Any]
    explanation: str
    generated_at: datetime
    gate_tier: GateTier
    horizon_eligible: bool
    embargo_active: bool
    model_version: str
    id: UUID | None = None
    superseded_at: datetime | None = None
    previous_probability: float | None = None
    probability_delta: float | None = None
    max_lifecycle_probability_shift: float | None = None
    prediction_direction_changed: bool | None = None
    lifecycle_stability_score: float | None = None

    # --- architecture v2 7.4 audit envelope ---------------------------------
    #
    # Nullable for every pre-match row, which is why they are optional here:
    # a pre-match prediction has no snapshot to anchor to and no match minute
    # to record. On an in-play row they are what makes the prediction
    # falsifiable -- `fixture_live_snapshot_id` names the observation it was
    # derived from, so "what did SokaLens know at minute 63?" has an answer
    # that does not depend on trusting the prediction itself.
    fixture_live_snapshot_id: int | None = None
    data_cutoff_at: datetime | None = None
    match_minute: int | None = None
    period: str | None = None
    # Defaults to the model version, which is what the insert used to write
    # unconditionally. The in-play path overrides it with the feature version,
    # because features and model are versioned separately there.
    feature_version: str | None = None

    def with_id(self) -> EnginePrediction:
        return self if self.id is not None else replace(self, id=uuid4())

    def validate(self) -> None:
        problems: list[str] = []
        if self.fixture_id <= 0:
            problems.append("fixture_id must be positive")
        if not self.market.strip():
            problems.append("market is required")
        if not self.prediction.strip():
            problems.append("prediction is required")
        # The 0.01-0.99 band is a pre-match invariant: a pre-match model claiming
        # 99.5% is far more likely broken than right, so the guard is worth
        # keeping. In play it is wrong, and wrong in the expensive direction --
        # a 1X2 on a three-goal lead at 85 minutes genuinely exceeds 0.99, is not
        # a determined market, and is exactly the clearest read of the match. The
        # flat band silently rejected those rows, suppressing the calls the model
        # is most confident about.
        #
        # Exclusive of 0 and 1 in play, because `publishable_markets` drops
        # markets whose selections have collapsed onto certainty, so a published
        # in-play probability is never exactly either.
        if self.lifecycle_stage in IN_PLAY_LIFECYCLE_STAGES:
            if not 0 < self.probability < 1:
                problems.append("in-play probability must be between 0 and 1 exclusive")
        elif not _within(self.probability, 0.01, 0.99):
            problems.append("probability must be between 0.01 and 0.99")
        if not _within(self.confidence, 0.0, 1.0):
            problems.append("confidence must be between 0 and 1")
        if not _within(self.data_completeness, 0.0, 1.0):
            problems.append("data_completeness must be between 0 and 1")
        if self.previous_probability is not None and not _within(self.previous_probability, 0.0, 1.0):
            problems.append("previous_probability must be between 0 and 1")
        if self.probability_delta is not None and not _within(self.probability_delta, -1.0, 1.0):
            problems.append("probability_delta must be between -1 and 1")
        if self.max_lifecycle_probability_shift is not None and not _within(
            self.max_lifecycle_probability_shift, 0.0, 1.0
        ):
            problems.append("max_lifecycle_probability_shift must be between 0 and 1")
        if self.lifecycle_stability_score is not None and not _within(self.lifecycle_stability_score, 0.0, 1.0):
            problems.append("lifecycle_stability_score must be between 0 and 1")
        if (self.previous_probability is None) != (self.probability_delta is None):
            problems.append("previous_probability and probability_delta must both be set or both be null")
        lifecycle_metadata = (
            self.max_lifecycle_probability_shift,
            self.prediction_direction_changed,
            self.lifecycle_stability_score,
        )
        if any(value is not None for value in lifecycle_metadata) and not all(
            value is not None for value in lifecycle_metadata
        ):
            problems.append("lifecycle stability metadata must be entirely set or entirely null")
        if self.previous_probability is not None and not all(value is not None for value in lifecycle_metadata):
            problems.append("previous lifecycle evidence requires complete stability metadata")
        if (
            self.previous_probability is None
            and all(value is not None for value in lifecycle_metadata)
            and (self.max_lifecycle_probability_shift != 0 or self.prediction_direction_changed is not False)
        ):
            problems.append("first lifecycle version must have zero shift and unchanged direction")
        if (
            self.probability_delta is not None
            and self.max_lifecycle_probability_shift is not None
            and self.max_lifecycle_probability_shift < abs(self.probability_delta)
        ):
            problems.append("max_lifecycle_probability_shift must cover probability_delta")
        if not self.model_version.strip():
            problems.append("model_version is required")
        if self.match_minute is not None and not 0 <= self.match_minute <= 240:
            problems.append("match_minute must be between 0 and 240")
        if self.fixture_live_snapshot_id is not None and self.fixture_live_snapshot_id <= 0:
            problems.append("fixture_live_snapshot_id must be positive")
        if self.data_cutoff_at is not None and (
            self.data_cutoff_at.tzinfo is None or self.data_cutoff_at.utcoffset() is None
        ):
            problems.append("data_cutoff_at must be timezone-aware")
        if self.lifecycle_stage in IN_PLAY_LIFECYCLE_STAGES:
            if self.data_cutoff_at is None:
                problems.append("in-play predictions require data_cutoff_at")
            if self.match_minute is None:
                problems.append("in-play predictions require match_minute")
            if not (self.period or "").strip():
                problems.append("in-play predictions require period")
        try:
            json.dumps(self.features_used, sort_keys=True)
        except (TypeError, ValueError):
            problems.append("features_used must be JSON serializable")
        if self.superseded_at is not None and self.superseded_at < self.generated_at:
            problems.append("superseded_at must not be before generated_at")
        if problems:
            raise ValueError("invalid engine prediction: " + "; ".join(problems))

    def to_db_row(self) -> dict[str, Any]:
        prediction = self.with_id()
        prediction.validate()
        return {
            "id": prediction.id,
            "fixture_id": prediction.fixture_id,
            "market": prediction.market,
            "prediction": prediction.prediction,
            "probability": prediction.probability,
            "confidence": prediction.confidence,
            "confidence_tier": prediction.confidence_tier.value,
            "is_best_bet": prediction.is_best_bet,
            "lifecycle_stage": prediction.lifecycle_stage.value,
            "data_completeness": prediction.data_completeness,
            "features_used": json.dumps(prediction.features_used, sort_keys=True),
            "explanation": prediction.explanation,
            "generated_at": prediction.generated_at,
            "superseded_at": prediction.superseded_at,
            "gate_tier": prediction.gate_tier.value,
            "horizon_eligible": prediction.horizon_eligible,
            "embargo_active": prediction.embargo_active,
            "model_version": prediction.model_version,
            "previous_probability": prediction.previous_probability,
            "probability_delta": prediction.probability_delta,
            "max_lifecycle_probability_shift": prediction.max_lifecycle_probability_shift,
            "prediction_direction_changed": prediction.prediction_direction_changed,
            "lifecycle_stability_score": prediction.lifecycle_stability_score,
            "fixture_live_snapshot_id": prediction.fixture_live_snapshot_id,
            "data_cutoff_at": prediction.data_cutoff_at,
            "match_minute": prediction.match_minute,
            "period": prediction.period,
            "feature_version": prediction.feature_version or prediction.model_version,
        }


def supersede_current_predictions(
    current_predictions: Iterable[EnginePrediction],
    new_predictions: Iterable[EnginePrediction],
) -> list[EnginePrediction]:
    new_rows = [prediction.with_id() for prediction in new_predictions]
    for prediction in new_rows:
        prediction.validate()
    if not new_rows:
        return list(current_predictions)

    superseded_at_by_key: dict[tuple[int, str], datetime] = {}
    for prediction in new_rows:
        key = (prediction.fixture_id, prediction.market)
        superseded_at_by_key[key] = max(superseded_at_by_key.get(key, prediction.generated_at), prediction.generated_at)

    superseded = [
        replace(prediction, superseded_at=superseded_at_by_key[(prediction.fixture_id, prediction.market)])
        if prediction.superseded_at is None and (prediction.fixture_id, prediction.market) in superseded_at_by_key
        else prediction
        for prediction in current_predictions
    ]
    return [*superseded, *new_rows]


def qualifies_for_best_bet_pool(
    prediction: EnginePrediction,
    *,
    kickoff_at: datetime,
    now: datetime,
    minimum_probability: float = 0.55,
    minimum_tier: ConfidenceTier = ConfidenceTier.MEDIUM,
    excluded_markets: Iterable[str] = ("correct_score",),
) -> bool:
    prediction.validate()
    if prediction.superseded_at is not None:
        return False
    if prediction.gate_tier is not GateTier.FULL:
        return False
    if not prediction.horizon_eligible or prediction.embargo_active:
        return False
    if kickoff_at <= now:
        return False
    if prediction.probability < minimum_probability:
        return False
    if prediction.market in set(excluded_markets):
        return False
    return _tier_rank(prediction.confidence_tier) >= _tier_rank(minimum_tier)


def _tier_rank(tier: ConfidenceTier) -> int:
    ranks = {
        ConfidenceTier.NONE: 0,
        ConfidenceTier.LOW: 1,
        ConfidenceTier.MEDIUM: 2,
        ConfidenceTier.STRONG: 3,
    }
    return ranks[tier]


def _within(value: float, minimum: float, maximum: float) -> bool:
    return math.isfinite(value) and minimum <= value <= maximum
