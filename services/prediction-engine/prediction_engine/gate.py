from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum

from prediction_engine.config import PredictionEngineConfig


class GateOutcome(Enum):
    SKIP = "SKIP"
    BASIC = "BASIC"
    FULL = "FULL"


class GateReason(Enum):
    FIXTURE_STATUS = "fixture_status"
    INSUFFICIENT_SAMPLE = "insufficient_sample"
    INSUFFICIENT_COMPLETENESS = "insufficient_completeness"
    DUAL_INJURY_IMPACT = "dual_injury_impact"
    BASIC_DATA = "basic_data"
    FULL_DATA = "full_data"


class GateSoftFlag(Enum):
    FIXTURE_CONGESTION = "fixture_congestion"
    EARLY_SEASON = "early_season"
    MISSING_REFEREE_DATA = "missing_referee_data"
    MISSING_ODDS_DATA = "missing_odds_data"


class MarketTier(Enum):
    A = "A"
    B = "B"
    C = "C"
    D = "D"


@dataclass(frozen=True)
class MatchQualityInput:
    fixture_status: str
    home_recent_sample: int
    away_recent_sample: int
    completeness_score: float
    group_completeness: dict[str, float] = field(default_factory=dict)
    home_injury_impact_score: float = 0
    away_injury_impact_score: float = 0
    home_fixture_congestion: float = 0
    away_fixture_congestion: float = 0
    early_season: bool = False
    referee_fallback_used: bool = False
    one_x_two_odds_present: bool = False


@dataclass(frozen=True)
class GateDecision:
    outcome: GateOutcome
    reason: GateReason
    data_completeness_score: float
    soft_flags: tuple[GateSoftFlag, ...] = ()
    allowed_market_tiers: tuple[MarketTier, ...] = ()
    best_bet_eligible: bool = False
    confidence_scoring_enabled: bool = False


@dataclass(frozen=True)
class Phase3PrecheckInput:
    fixture_id: int


@dataclass(frozen=True)
class Phase3PrecheckDecision:
    passed: bool


class MatchQualityGate:
    def __init__(self, config: PredictionEngineConfig) -> None:
        config.validate()
        self._config = config.match_gate

    def evaluate(self, vector: MatchQualityInput) -> GateDecision:
        if _fixture_status_skips(vector.fixture_status):
            return self._skip(vector, GateReason.FIXTURE_STATUS)
        if (
            vector.home_recent_sample < self._config.minimum_historical_matches_per_team
            or vector.away_recent_sample < self._config.minimum_historical_matches_per_team
        ):
            return self._skip(vector, GateReason.INSUFFICIENT_SAMPLE)
        if vector.completeness_score < self._config.minimum_completeness_to_attempt:
            return self._skip(vector, GateReason.INSUFFICIENT_COMPLETENESS)
        if (
            vector.home_injury_impact_score > self._config.dual_injury_impact_skip_threshold
            and vector.away_injury_impact_score > self._config.dual_injury_impact_skip_threshold
        ):
            return self._skip(vector, GateReason.DUAL_INJURY_IMPACT)

        soft_flags = _soft_flags(vector)
        if vector.completeness_score < self._config.minimum_completeness_for_full:
            return GateDecision(
                outcome=GateOutcome.BASIC,
                reason=GateReason.BASIC_DATA,
                data_completeness_score=vector.completeness_score,
                soft_flags=soft_flags,
                allowed_market_tiers=(MarketTier.A,),
            )

        return GateDecision(
            outcome=GateOutcome.FULL,
            reason=GateReason.FULL_DATA,
            data_completeness_score=vector.completeness_score,
            soft_flags=soft_flags,
            allowed_market_tiers=(MarketTier.A, MarketTier.B, MarketTier.C, MarketTier.D),
            best_bet_eligible=True,
            confidence_scoring_enabled=True,
        )

    def _skip(self, vector: MatchQualityInput, reason: GateReason) -> GateDecision:
        return GateDecision(
            outcome=GateOutcome.SKIP,
            reason=reason,
            data_completeness_score=vector.completeness_score,
        )


def evaluate_phase3_horizon_embargo_precheck(_: Phase3PrecheckInput) -> Phase3PrecheckDecision:
    return Phase3PrecheckDecision(passed=True)


def _fixture_status_skips(status: str) -> bool:
    normalized = status.strip().lower().replace("_", " ").replace("-", " ")
    return normalized in {
        "cancelled",
        "canceled",
        "cancelled match",
        "canceled match",
        "postponed",
        "abandoned",
        "canc",
        "pst",
        "abd",
    }


def _soft_flags(vector: MatchQualityInput) -> tuple[GateSoftFlag, ...]:
    flags: list[GateSoftFlag] = []
    if vector.home_fixture_congestion > 0.50 or vector.away_fixture_congestion > 0.50:
        flags.append(GateSoftFlag.FIXTURE_CONGESTION)
    if vector.early_season:
        flags.append(GateSoftFlag.EARLY_SEASON)
    if vector.referee_fallback_used or vector.group_completeness.get("referee") == 0:
        flags.append(GateSoftFlag.MISSING_REFEREE_DATA)
    if not vector.one_x_two_odds_present or vector.group_completeness.get("market_cross_reference") == 0:
        flags.append(GateSoftFlag.MISSING_ODDS_DATA)
    return tuple(flags)
