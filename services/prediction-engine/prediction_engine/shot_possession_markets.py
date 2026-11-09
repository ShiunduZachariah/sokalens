from __future__ import annotations

from dataclasses import dataclass
import math

from prediction_engine.rate_markets import OverUnderLine, poisson_probability


# Precision of the Beta on the home share. An actual share landed a standard
# deviation of 9.5 points from the expected one, on 700 fixtures in 55 leagues:
# m(1-m)/sd^2 - 1 at m=0.5. See docs/prediction-engine/possession-market.md.
_POSSESSION_SHARE_PRECISION = 26.0

# Expected shares in the evidence ran from 33.5 to 64.5. Past that there is
# nothing to say the curve still holds, so the mean stops at the edge rather than
# publishing an extrapolated near-certainty.
_POSSESSION_EVIDENCE_RANGE = (33.0, 67.0)

_BETA_MASS_STEPS = 400


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


def expected_home_possession_share(home_average: float, away_average: float) -> float:
    """The home side's expected share, from what both teams usually hold.

    Possession is one number split between two sides, so a 60% side meeting a
    60% side is an even game. Reading the home average alone correlated with the
    actual share at 0.22 on held-out fixtures; reading both, at 0.49.
    """
    return min(max((home_average + (100.0 - away_average)) / 2.0, 0.0), 100.0)


def possession_market(
    *,
    expected_home_possession_pct: float,
    bands: tuple[tuple[int, int], ...] = ((0, 49), (50, 50), (51, 100)),
) -> PossessionMarketResult:
    """A probability for each possession outcome, from a Beta on the home share.

    Architecture v2 8.4.6: possession is a bounded proportion and is never a
    count, so this is a Beta rather than the Poisson it replaced. Its mean is the
    expected share and its precision is measured; see
    docs/prediction-engine/possession-market.md.

    The provider reports whole percentages, so "level" is the mass that rounds
    to 50 and a band covers the half-point either side of its limits.
    """
    _validate_non_negative(expected_home_possession_pct, "expected_home_possession_pct")
    low, high = _POSSESSION_EVIDENCE_RANGE
    mean = min(max(expected_home_possession_pct, low), high) / 100.0
    alpha, beta = mean * _POSSESSION_SHARE_PRECISION, (1.0 - mean) * _POSSESSION_SHARE_PRECISION

    band_probabilities: dict[str, float] = {}
    for lower, upper in bands:
        if lower < 0 or upper < lower:
            raise ValueError("possession bands must be non-negative ordered ranges")
        band_probabilities[f"{lower}-{upper}"] = _beta_mass(
            alpha,
            beta,
            max(lower - 0.5, 0.0) / 100.0,
            min(upper + 0.5, 100.0) / 100.0,
        )

    away_more = _beta_mass(alpha, beta, 0.0, 0.495)
    level = _beta_mass(alpha, beta, 0.495, 0.505)
    return PossessionMarketResult(
        expected_home_possession_pct=expected_home_possession_pct,
        band_probabilities=band_probabilities,
        side_probabilities={"home_more": 1.0 - away_more - level, "level": level, "away_more": away_more},
        method="possession_share_beta",
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


def _beta_mass(alpha: float, beta: float, lower: float, upper: float) -> float:
    """The Beta(alpha, beta) probability of [lower, upper], by midpoint rule.

    Both shapes are well above 1 inside the evidence range, so the density is
    smooth and vanishes at the ends, and this runs once per fixture.
    """
    if upper <= lower:
        return 0.0
    log_norm = math.lgamma(alpha + beta) - math.lgamma(alpha) - math.lgamma(beta)
    width = (upper - lower) / _BETA_MASS_STEPS
    total = 0.0
    for step in range(_BETA_MASS_STEPS):
        point = lower + (step + 0.5) * width
        total += math.exp(log_norm + (alpha - 1.0) * math.log(point) + (beta - 1.0) * math.log1p(-point))
    return total * width


def _validate_non_negative(value: float, name: str) -> None:
    if not math.isfinite(value) or value < 0:
        raise ValueError(f"{name} must be non-negative")
