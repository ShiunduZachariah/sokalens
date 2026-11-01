from __future__ import annotations

from dataclasses import dataclass
import math


@dataclass(frozen=True)
class OverUnderLine:
    line: float
    over: float
    under: float


@dataclass(frozen=True)
class RateMarketResult:
    market_key: str
    lambda_value: float
    lines: dict[float, OverUnderLine]
    method: str
    referee_modifier: float = 1.0


@dataclass(frozen=True)
class TeamRateMarketResult:
    market_key: str
    team_key: str
    lambda_value: float
    lines: dict[float, OverUnderLine]
    method: str


@dataclass(frozen=True)
class RareEventMarketResult:
    market_key: str
    probability_yes: float
    probability_no: float
    max_confidence_tier: str
    method: str


def poisson_probability(lambda_value: float, count: int) -> float:
    _validate_lambda(lambda_value, "lambda_value")
    if count < 0:
        raise ValueError("count must be non-negative")
    return math.exp(-lambda_value) * (lambda_value**count) / math.factorial(count)


def combined_total_market(
    *,
    market_key: str,
    expected_total_lambda: float,
    lines: tuple[float, ...],
    referee_stat_average: float | None = None,
    league_stat_average: float | None = None,
) -> RateMarketResult:
    modifier = _referee_modifier(referee_stat_average, league_stat_average)
    adjusted_lambda = expected_total_lambda * modifier
    return RateMarketResult(
        market_key=market_key,
        lambda_value=adjusted_lambda,
        lines=_derive_over_under_lines(adjusted_lambda, lines),
        method="combined_total_poisson",
        referee_modifier=modifier,
    )


def team_total_market(
    *,
    market_key: str,
    team_key: str,
    team_lambda: float,
    lines: tuple[float, ...],
) -> TeamRateMarketResult:
    return TeamRateMarketResult(
        market_key=market_key,
        team_key=team_key,
        lambda_value=team_lambda,
        lines=_derive_over_under_lines(team_lambda, lines),
        method="team_total_poisson",
    )


def rare_event_market(
    *,
    market_key: str,
    probability_yes: float,
    max_confidence_tier: str,
) -> RareEventMarketResult:
    _validate_probability(probability_yes, "probability_yes")
    if not max_confidence_tier.strip():
        raise ValueError("max_confidence_tier must not be empty")
    return RareEventMarketResult(
        market_key=market_key,
        probability_yes=probability_yes,
        probability_no=1 - probability_yes,
        max_confidence_tier=max_confidence_tier,
        method="direct_probability",
    )


def _derive_over_under_lines(lambda_value: float, lines: tuple[float, ...]) -> dict[float, OverUnderLine]:
    _validate_lambda(lambda_value, "lambda_value")
    derived: dict[float, OverUnderLine] = {}
    for line in lines:
        _validate_line(line)
        under = _poisson_cumulative_probability(lambda_value, math.floor(line))
        derived[line] = OverUnderLine(line=line, over=1 - under, under=under)
    return derived


def _poisson_cumulative_probability(lambda_value: float, max_count: int) -> float:
    if max_count < 0:
        return 0.0
    return sum(poisson_probability(lambda_value, count) for count in range(max_count + 1))


def _validate_lambda(lambda_value: float, name: str) -> None:
    if not math.isfinite(lambda_value) or lambda_value < 0:
        raise ValueError(f"{name} must be non-negative")


def _referee_modifier(referee_stat_average: float | None, league_stat_average: float | None) -> float:
    if referee_stat_average is None:
        return 1.0
    _validate_lambda(referee_stat_average, "referee_stat_average")
    if league_stat_average is None or not math.isfinite(league_stat_average) or league_stat_average <= 0:
        raise ValueError("league_stat_average must be positive when referee_stat_average is provided")
    return referee_stat_average / league_stat_average


def _validate_line(line: float) -> None:
    if not math.isfinite(line) or line < 0:
        raise ValueError("line must be non-negative")


def _validate_probability(value: float, name: str) -> None:
    if not math.isfinite(value) or value < 0 or value > 1:
        raise ValueError(f"{name} must be between 0 and 1")
