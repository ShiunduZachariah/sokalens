from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass, replace
from datetime import datetime, timedelta
from enum import StrEnum
import math
from typing import Callable, Generic, Iterable, TypeVar


T = TypeVar("T")


class LifecycleStage(StrEnum):
    EARLY = "EARLY"
    PRE_MATCH = "PRE_MATCH"
    LINEUP_CONFIRMED = "LINEUP_CONFIRMED"
    LOCKED = "LOCKED"


@dataclass(frozen=True)
class LifecycleConfig:
    pre_match_starts_before_kickoff: timedelta = timedelta(hours=6)
    lineup_confirmed_starts_before_kickoff: timedelta = timedelta(minutes=90)
    stage_priority: dict[LifecycleStage, int] | None = None

    def priority_for(self, stage: LifecycleStage) -> int:
        priorities = self.stage_priority or {
            LifecycleStage.LINEUP_CONFIRMED: 0,
            LifecycleStage.PRE_MATCH: 1,
            LifecycleStage.EARLY: 2,
            LifecycleStage.LOCKED: 3,
        }
        return priorities[stage]


@dataclass(frozen=True)
class LifecycleStability:
    previous_probability: float | None
    probability_delta: float | None
    max_lifecycle_probability_shift: float
    prediction_direction_changed: bool
    lifecycle_stability_score: float


def calculate_lifecycle_stability(
    *,
    probability: float,
    prediction: str,
    previous_probability: float | None,
    previous_prediction: str | None,
    previous_max_shift: float | None = None,
    tolerance: float = 0.20,
    direction_change_penalty: float = 0.50,
    neutral_score: float = 0.50,
) -> LifecycleStability:
    """Calculate version-to-version stability without affecting model output.

    The caller must supply the immediately preceding current version for the
    same fixture and market. A missing predecessor is deliberately neutral.
    """
    for name, value in (("probability", probability), ("neutral_score", neutral_score)):
        if not 0.0 <= value <= 1.0:
            raise ValueError(f"{name} must be between 0 and 1")
    if not math.isfinite(tolerance) or tolerance <= 0.0 or tolerance > 1.0:
        raise ValueError("tolerance must be greater than 0 and at most 1")
    if not 0.0 <= direction_change_penalty <= 1.0:
        raise ValueError("direction_change_penalty must be between 0 and 1")
    if previous_probability is None:
        return LifecycleStability(None, None, 0.0, False, neutral_score)
    if not 0.0 <= previous_probability <= 1.0:
        raise ValueError("previous_probability must be between 0 and 1")

    delta = probability - previous_probability
    prior_max = 0.0 if previous_max_shift is None else previous_max_shift
    if not 0.0 <= prior_max <= 1.0:
        raise ValueError("previous_max_shift must be between 0 and 1")
    max_shift = max(prior_max, abs(delta))
    direction_changed = previous_prediction is not None and prediction != previous_prediction
    score = 1.0 - min(max_shift / tolerance, 1.0)
    if direction_changed:
        score *= direction_change_penalty
    return LifecycleStability(
        previous_probability=previous_probability,
        probability_delta=delta,
        max_lifecycle_probability_shift=max_shift,
        prediction_direction_changed=direction_changed,
        lifecycle_stability_score=score,
    )


@dataclass(frozen=True)
class PredictionVersion:
    fixture_id: int
    market: str
    lifecycle_stage: LifecycleStage
    generated_at: datetime
    superseded_at: datetime | None = None


@dataclass(frozen=True)
class PredictionWorkItem:
    fixture_id: int
    kickoff_at: datetime
    stage: LifecycleStage


@dataclass(frozen=True)
class FixtureProcessResult(Generic[T]):
    fixture_id: int
    stage: LifecycleStage
    value: T | None = None
    error: str | None = None


def assign_lifecycle_stage(
    now: datetime,
    kickoff_at: datetime,
    lineups_confirmed: bool,
    config: LifecycleConfig,
) -> LifecycleStage:
    if now >= kickoff_at:
        return LifecycleStage.LOCKED
    if lineups_confirmed:
        return LifecycleStage.LINEUP_CONFIRMED
    time_to_kickoff = kickoff_at - now
    if time_to_kickoff <= config.lineup_confirmed_starts_before_kickoff:
        return LifecycleStage.PRE_MATCH
    if time_to_kickoff <= config.pre_match_starts_before_kickoff:
        return LifecycleStage.PRE_MATCH
    return LifecycleStage.EARLY


def supersede_previous_versions(
    current_versions: Iterable[PredictionVersion],
    new_versions: Iterable[PredictionVersion],
) -> list[PredictionVersion]:
    new_rows = list(new_versions)
    if not new_rows:
        return list(current_versions)
    superseded_at = max(row.generated_at for row in new_rows)
    superseded = [
        replace(row, superseded_at=superseded_at) if row.superseded_at is None else row
        for row in current_versions
    ]
    return [*superseded, *new_rows]


def process_fixture_batch(
    items: Iterable[PredictionWorkItem],
    max_workers: int,
    processor: Callable[[PredictionWorkItem], T],
    config: LifecycleConfig | None = None,
) -> list[FixtureProcessResult[T]]:
    if max_workers <= 0:
        raise ValueError("max_workers must be positive")
    config = config or LifecycleConfig()
    ordered_items = sorted(items, key=lambda item: (config.priority_for(item.stage), item.kickoff_at, item.fixture_id))
    if max_workers == 1:
        return [_process_one(item, processor) for item in ordered_items]

    results: list[FixtureProcessResult[T]] = []
    with ThreadPoolExecutor(max_workers=max_workers) as executor:
        future_to_item = {executor.submit(_process_one, item, processor): item for item in ordered_items}
        for future in as_completed(future_to_item):
            results.append(future.result())
    result_order = {item.fixture_id: index for index, item in enumerate(ordered_items)}
    results.sort(key=lambda result: result_order[result.fixture_id])
    return results


def _process_one(
    item: PredictionWorkItem,
    processor: Callable[[PredictionWorkItem], T],
) -> FixtureProcessResult[T]:
    try:
        return FixtureProcessResult(fixture_id=item.fixture_id, stage=item.stage, value=processor(item))
    except Exception as exc:  # noqa: BLE001 - fixture failures must be isolated and reported.
        return FixtureProcessResult(fixture_id=item.fixture_id, stage=item.stage, error=str(exc))
