from __future__ import annotations

from dataclasses import dataclass
import math

from prediction_engine.rate_markets import OverUnderLine, poisson_probability


@dataclass(frozen=True)
class ShotMarketResult:
    market_key: str
    lambda_value: float
    lines: dict[float, OverUnderLine]
    method: str


@dataclass(frozen=True)
class PossessionMarketResult:
    expected_home_possession_pct: float
    band_probabilities: dict[str, float]
    side_probabilities: dict[str, float]
    method: str


@dataclass(frozen=True)
class TierEligibilityDecision:
    eligible: bool
    market_tier: str
    required_completeness: float
    exclusion_reason: str | None = None


def shots_on_target_market(
    *,
    expected_total_lambda: float,
    lines: tuple[float, ...],
) -> ShotMarketResult:
    _validate_non_negative(expected_total_lambda, "expected_total_lambda")
    return ShotMarketResult(
        market_key="shots_on_target",
        lambda_value=expected_total_lambda,
        lines=_derive_over_under_lines(expected_total_lambda, lines),
        method="shots_on_target_poisson",
    )


def possession_market(
    *,
    expected_home_possession_pct: float,
    bands: tuple[tuple[int, int], ...] = ((0, 49), (50, 50), (51, 100)),
) -> PossessionMarketResult:
    _validate_non_negative(expected_home_possession_pct, "expected_home_possession_pct")
    band_probabilities: dict[str, float] = {}
    for lower, upper in bands:
        if lower < 0 or upper < lower:
            raise ValueError("possession bands must be non-negative ordered ranges")
        band_probabilities[f"{lower}-{upper}"] = _poisson_range_probability(
            expected_home_possession_pct,
            lower,
            upper,
        )

    if expected_home_possession_pct > 50:
        side_probabilities = {"home_more": 1.0, "level": 0.0, "away_more": 0.0}
    elif expected_home_possession_pct < 50:
        side_probabilities = {"home_more": 0.0, "level": 0.0, "away_more": 1.0}
    else:
        side_probabilities = {"home_more": 0.0, "level": 1.0, "away_more": 0.0}

    return PossessionMarketResult(
        expected_home_possession_pct=expected_home_possession_pct,
        band_probabilities=band_probabilities,
        side_probabilities=side_probabilities,
        method="possession_pct_poisson",
    )


def tier_market_eligibility(
    *,
    market_tier: str,
    has_statistics: bool,
    group_completeness: float,
    statistic_sample_size: int,
    minimum_sample_size: int = 5,
) -> TierEligibilityDecision:
    normalized_tier = market_tier.upper()
    required = {"B": 0.65, "C": 0.70}.get(normalized_tier)
    if required is None:
        raise ValueError('market_tier must be "B" or "C"')
    if not has_statistics:
        return TierEligibilityDecision(False, normalized_tier, required, "statistics_not_supported")
    if statistic_sample_size < minimum_sample_size:
        return TierEligibilityDecision(False, normalized_tier, required, "insufficient_sample")
    if group_completeness < required:
        return TierEligibilityDecision(False, normalized_tier, required, "insufficient_tier_completeness")
    return TierEligibilityDecision(True, normalized_tier, required)


def _derive_over_under_lines(lambda_value: float, lines: tuple[float, ...]) -> dict[float, OverUnderLine]:
    derived: dict[float, OverUnderLine] = {}
    for line in lines:
        _validate_non_negative(line, "line")
        under = sum(poisson_probability(lambda_value, count) for count in range(math.floor(line) + 1))
        derived[line] = OverUnderLine(line=line, over=1 - under, under=under)
    return derived


def _poisson_range_probability(lambda_value: float, lower: int, upper: int) -> float:
    if upper >= 100:
        return 1 - sum(poisson_probability(lambda_value, count) for count in range(lower))
    return sum(poisson_probability(lambda_value, count) for count in range(lower, upper + 1))


def _validate_non_negative(value: float, name: str) -> None:
    if not math.isfinite(value) or value < 0:
        raise ValueError(f"{name} must be non-negative")
