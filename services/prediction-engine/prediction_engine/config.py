from __future__ import annotations

from copy import deepcopy
from dataclasses import dataclass, replace
import math
from typing import Any


class ConfigValidationError(ValueError):
    """Raised when prediction-engine configuration is invalid."""


@dataclass(frozen=True)
class ConfidenceThresholds:
    strong_minimum: float
    medium_minimum: float
    low_minimum: float


@dataclass(frozen=True)
class MatchGateConfig:
    minimum_completeness_to_attempt: float
    minimum_completeness_for_full: float
    minimum_historical_matches_per_team: int
    dual_injury_impact_skip_threshold: float


@dataclass(frozen=True)
class BackfillConfig:
    matches_needed_by_feature_group: dict[str, int]


@dataclass(frozen=True)
class BlendWeights:
    statistical_model: float
    form_adjustment: float


@dataclass(frozen=True)
class FormConfig:
    decay_rate: float
    lookback_windows: tuple[int, ...]


@dataclass(frozen=True)
class ProbabilityClamp:
    minimum: float
    maximum: float


@dataclass(frozen=True)
class CorrectScoreConfig:
    top_n: int


V2_MARKET_FAMILIES = (
    "1x2",
    "double_chance",
    "btts",
    "total_goals",
    "team_goals",
    "ht_ft",
    "correct_score",
    "clean_sheet",
    "win_to_nil",
    "corners",
    "cards",
    "fouls",
    "offsides",
    "red_card",
    "shots",
    "possession",
    "pass_accuracy",
    "goalkeeper_saves",
    "combination",
)

V2_ENSEMBLE_MARKET_FAMILIES = (
    "1x2",
    "double_chance",
    "btts",
    "total_goals",
    "team_goals",
    "ht_ft",
    "correct_score",
    "clean_sheet",
    "win_to_nil",
)


@dataclass(frozen=True)
class MarketTierStrictness:
    tier_b_completeness_addition: float
    tier_c_completeness_addition: float


@dataclass(frozen=True)
class EnsembleWeights:
    model_a: float
    model_b: float
    model_c: float


@dataclass(frozen=True)
class SeasonRecentWeights:
    season_long: float
    recent_form: float


@dataclass(frozen=True)
class RankingWeightsConfig:
    probability: float
    confidence: float
    data_completeness: float
    market_accuracy: float
    league_accuracy: float
    edge_vs_odds: float


@dataclass(frozen=True)
class FeatureDecayRates:
    match_result: float
    expected_goals: float
    discipline: float
    corners: float
    fouls: float
    lineup_availability: float


@dataclass(frozen=True)
class CalibrationPolicy:
    primary_method_by_market: dict[str, str]
    minimum_sample_by_market: dict[str, int]
    low_sample_fallback_method: str


@dataclass(frozen=True)
class OODThresholds:
    confidence_cap: float
    force_basic: float


@dataclass(frozen=True)
class DriftWindows:
    accuracy_predictions: int
    calibration_days: int
    accuracy_alert_drop: float
    accuracy_retraining_drop: float
    calibration_alert_error: float
    calibration_retraining_error: float


@dataclass(frozen=True)
class RetrainingPolicy:
    minimum_new_settled_samples: int


@dataclass(frozen=True)
class PromotionPolicy:
    minimum_shadow_settled_samples: int
    significance_confidence_level: float


@dataclass(frozen=True)
class LifecycleStabilityPolicy:
    tolerance: float
    direction_change_penalty: float
    neutral_score: float


@dataclass(frozen=True)
class V2FoundationConfig:
    market_tier_strictness: MarketTierStrictness
    ensemble_weights: dict[str, EnsembleWeights]
    season_recent_weights: SeasonRecentWeights
    ranking_weights: RankingWeightsConfig
    feature_decay_rates: FeatureDecayRates
    recommendation_tier_by_market: dict[str, str]
    calibration: CalibrationPolicy
    ood_thresholds: OODThresholds
    drift_windows: DriftWindows
    retraining: RetrainingPolicy
    promotion: PromotionPolicy
    lifecycle_stability: LifecycleStabilityPolicy


@dataclass(frozen=True)
class PredictionEngineConfig:
    confidence_thresholds: ConfidenceThresholds
    match_gate: MatchGateConfig
    backfill: BackfillConfig
    model_weights: dict[str, BlendWeights]
    form: FormConfig
    probability_clamp: ProbabilityClamp
    correct_score: CorrectScoreConfig
    v2: V2FoundationConfig

    def validate(self) -> None:
        problems: list[str] = []
        thresholds = self.confidence_thresholds
        if not (
            _valid_ratio(thresholds.strong_minimum)
            and _valid_ratio(thresholds.medium_minimum)
            and _valid_ratio(thresholds.low_minimum)
            and thresholds.strong_minimum > thresholds.medium_minimum > thresholds.low_minimum > 0
        ):
            problems.append("confidence thresholds must be strictly descending: strong > medium > low > 0")

        match_gate = self.match_gate
        if not _valid_ratio(match_gate.minimum_completeness_to_attempt):
            problems.append("match gate minimum completeness to attempt must be between 0 and 1")
        if not _valid_ratio(match_gate.minimum_completeness_for_full):
            problems.append("match gate minimum completeness for full must be between 0 and 1")
        if (
            _valid_ratio(match_gate.minimum_completeness_for_full)
            and _valid_ratio(match_gate.minimum_completeness_to_attempt)
            and match_gate.minimum_completeness_for_full < match_gate.minimum_completeness_to_attempt
        ):
            problems.append("match gate full completeness must be greater than or equal to attempt completeness")
        if not _positive_int(match_gate.minimum_historical_matches_per_team):
            problems.append("minimum historical matches per team must be positive")
        if not (
            _is_number(match_gate.dual_injury_impact_skip_threshold)
            and 0 < match_gate.dual_injury_impact_skip_threshold <= 1
        ):
            problems.append("dual injury impact skip threshold must be greater than 0 and at most 1")

        expected_backfill_groups = {"results", "rich_statistics"}
        if set(self.backfill.matches_needed_by_feature_group) != expected_backfill_groups:
            problems.append("backfill matches-needed config must cover results and rich_statistics")
        elif any(not _positive_int(value) for value in self.backfill.matches_needed_by_feature_group.values()):
            problems.append("backfill matches-needed values must be positive integers")

        if not self.model_weights:
            problems.append("at least one model blend ratio is required")
        for market, weights in sorted(self.model_weights.items()):
            if not isinstance(market, str) or not market.strip():
                problems.append("model blend ratio market key must not be empty")
                continue
            if any(
                not _is_number(weight) or weight < 0
                for weight in (weights.statistical_model, weights.form_adjustment)
            ):
                problems.append(f'model blend ratio for "{market}" contains negative weights')
                continue
            if not math.isclose(weights.statistical_model + weights.form_adjustment, 1.0, abs_tol=1e-9):
                problems.append(f'model blend ratio for "{market}" must sum to 1.0')

        if not (_is_number(self.form.decay_rate) and 0 < self.form.decay_rate < 1):
            problems.append("form decay rate must be strictly between 0 and 1")
        if not self.form.lookback_windows or any(not _positive_int(window) for window in self.form.lookback_windows):
            problems.append("form lookback windows must be positive")

        clamp = self.probability_clamp
        if not (
            _is_number(clamp.minimum)
            and _is_number(clamp.maximum)
            and 0 < clamp.minimum < clamp.maximum < 1
        ):
            problems.append("probability clamp must satisfy 0 < minimum < maximum < 1")
        if not _positive_int(self.correct_score.top_n):
            problems.append("correct score top-N must be positive")

        v2 = self.v2
        tier_strictness = v2.market_tier_strictness
        if not _valid_ratio(tier_strictness.tier_b_completeness_addition):
            problems.append("Tier B completeness addition must be between 0 and 1")
        if not _valid_ratio(tier_strictness.tier_c_completeness_addition):
            problems.append("Tier C completeness addition must be between 0 and 1")
        if (
            _valid_ratio(tier_strictness.tier_c_completeness_addition)
            and _valid_ratio(tier_strictness.tier_b_completeness_addition)
            and tier_strictness.tier_c_completeness_addition < tier_strictness.tier_b_completeness_addition
        ):
            problems.append("Tier C completeness addition must be greater than or equal to Tier B")
        if (
            _is_number(match_gate.minimum_completeness_for_full)
            and _is_number(tier_strictness.tier_c_completeness_addition)
            and match_gate.minimum_completeness_for_full + tier_strictness.tier_c_completeness_addition > 1
        ):
            problems.append("Tier C completeness floor must not exceed 1")

        _validate_weight_map(v2.ensemble_weights, problems)
        season_recent = v2.season_recent_weights
        _validate_sum_to_one(
            "season-long/recent-form weights",
            (season_recent.season_long, season_recent.recent_form),
            problems,
        )
        ranking = v2.ranking_weights
        _validate_sum_to_one(
            "v2 ranking weights",
            (
                ranking.probability,
                ranking.confidence,
                ranking.data_completeness,
                ranking.market_accuracy,
                ranking.league_accuracy,
                ranking.edge_vs_odds,
            ),
            problems,
        )

        for name, decay_rate in vars(v2.feature_decay_rates).items():
            if not (_is_number(decay_rate) and 0 < decay_rate < 1):
                problems.append(f'feature decay rate "{name}" must be strictly between 0 and 1')

        _validate_market_keys("recommendation tiers", v2.recommendation_tier_by_market, problems)
        allowed_tiers = {"low", "medium", "strong"}
        for market, tier in sorted(v2.recommendation_tier_by_market.items()):
            if not isinstance(tier, str) or tier.strip().lower() not in allowed_tiers:
                problems.append(f'recommendation tier for "{market}" must be low, medium, or strong')

        calibration = v2.calibration
        _validate_market_keys("calibration methods", calibration.primary_method_by_market, problems)
        _validate_market_keys("calibration minimum samples", calibration.minimum_sample_by_market, problems)
        allowed_calibration_methods = {"isotonic", "platt", "beta"}
        for market, method in sorted(calibration.primary_method_by_market.items()):
            if not isinstance(method, str) or method.strip().lower() not in allowed_calibration_methods:
                problems.append(f'calibration method for "{market}" must be isotonic, platt, or beta')
        if (
            not isinstance(calibration.low_sample_fallback_method, str)
            or calibration.low_sample_fallback_method.strip().lower() not in allowed_calibration_methods
        ):
            problems.append("low-sample calibration fallback must be isotonic, platt, or beta")
        for market, sample in sorted(calibration.minimum_sample_by_market.items()):
            if not _positive_int(sample):
                problems.append(f'calibration minimum sample for "{market}" must be a positive integer')

        ood = v2.ood_thresholds
        if not (
            _valid_ratio(ood.confidence_cap)
            and _valid_ratio(ood.force_basic)
            and 0 < ood.confidence_cap < ood.force_basic < 1
        ):
            problems.append("OOD thresholds must satisfy 0 < confidence cap < force BASIC < 1")

        if not _positive_int(v2.drift_windows.accuracy_predictions):
            problems.append("accuracy drift window must be a positive integer")
        if not _positive_int(v2.drift_windows.calibration_days):
            problems.append("calibration drift window must be a positive integer")
        _validate_ordered_drift_thresholds(
            "accuracy drift",
            v2.drift_windows.accuracy_alert_drop,
            v2.drift_windows.accuracy_retraining_drop,
            problems,
        )
        _validate_ordered_drift_thresholds(
            "calibration drift",
            v2.drift_windows.calibration_alert_error,
            v2.drift_windows.calibration_retraining_error,
            problems,
        )
        if not _positive_int(v2.retraining.minimum_new_settled_samples):
            problems.append("retraining minimum new settled sample must be a positive integer")
        if not _positive_int(v2.promotion.minimum_shadow_settled_samples):
            problems.append("promotion minimum shadow settled sample must be a positive integer")
        significance = v2.promotion.significance_confidence_level
        if not (_is_number(significance) and 0.5 < significance < 1):
            problems.append("promotion significance confidence level must be strictly between 0.5 and 1")
        stability = v2.lifecycle_stability
        if not (_is_number(stability.tolerance) and 0 < stability.tolerance <= 1):
            problems.append("lifecycle stability tolerance must be greater than 0 and at most 1")
        if not _valid_ratio(stability.direction_change_penalty):
            problems.append("lifecycle direction-change penalty must be between 0 and 1")
        if not _valid_ratio(stability.neutral_score):
            problems.append("lifecycle neutral score must be between 0 and 1")

        if problems:
            raise ConfigValidationError("invalid prediction engine config: " + "; ".join(problems))


def default_config() -> PredictionEngineConfig:
    return PredictionEngineConfig(
        confidence_thresholds=ConfidenceThresholds(
            strong_minimum=0.75,
            medium_minimum=0.65,
            low_minimum=0.55,
        ),
        match_gate=MatchGateConfig(
            minimum_completeness_to_attempt=0.35,
            minimum_completeness_for_full=0.55,
            minimum_historical_matches_per_team=3,
            dual_injury_impact_skip_threshold=0.50,
        ),
        backfill=BackfillConfig(
            matches_needed_by_feature_group={"results": 3, "rich_statistics": 10},
        ),
        model_weights={
            "1x2": BlendWeights(statistical_model=0.55, form_adjustment=0.45),
            "double_chance": BlendWeights(statistical_model=0.55, form_adjustment=0.45),
            "btts": BlendWeights(statistical_model=0.65, form_adjustment=0.35),
            "total_goals": BlendWeights(statistical_model=0.70, form_adjustment=0.30),
            "team_goals": BlendWeights(statistical_model=0.65, form_adjustment=0.35),
            "ht_ft": BlendWeights(statistical_model=0.60, form_adjustment=0.40),
            "correct_score": BlendWeights(statistical_model=0.55, form_adjustment=0.45),
            "clean_sheet": BlendWeights(statistical_model=0.65, form_adjustment=0.35),
            "win_to_nil": BlendWeights(statistical_model=0.65, form_adjustment=0.35),
        },
        form=FormConfig(decay_rate=0.85, lookback_windows=(5, 10)),
        probability_clamp=ProbabilityClamp(minimum=0.01, maximum=0.99),
        correct_score=CorrectScoreConfig(top_n=8),
        v2=V2FoundationConfig(
            market_tier_strictness=MarketTierStrictness(
                tier_b_completeness_addition=0.10,
                tier_c_completeness_addition=0.15,
            ),
            # These are registration-time defaults only. Model A remains the entire
            # blend until independently trained Models B/C earn challenger status.
            ensemble_weights={
                market: EnsembleWeights(model_a=1.0, model_b=0.0, model_c=0.0)
                for market in V2_ENSEMBLE_MARKET_FAMILIES
            },
            season_recent_weights=SeasonRecentWeights(season_long=0.70, recent_form=0.30),
            ranking_weights=RankingWeightsConfig(
                probability=0.30,
                confidence=0.25,
                data_completeness=0.20,
                market_accuracy=0.10,
                league_accuracy=0.10,
                edge_vs_odds=0.05,
            ),
            feature_decay_rates=FeatureDecayRates(
                match_result=0.90,
                expected_goals=0.88,
                discipline=0.80,
                corners=0.85,
                fouls=0.80,
                lineup_availability=0.25,
            ),
            recommendation_tier_by_market={market: "medium" for market in V2_MARKET_FAMILIES},
            calibration=CalibrationPolicy(
                primary_method_by_market={
                    market: "beta" if market in {"possession", "pass_accuracy"} else "isotonic"
                    for market in V2_MARKET_FAMILIES
                },
                minimum_sample_by_market={market: 500 for market in V2_MARKET_FAMILIES},
                low_sample_fallback_method="platt",
            ),
            ood_thresholds=OODThresholds(confidence_cap=0.60, force_basic=0.80),
            drift_windows=DriftWindows(
                accuracy_predictions=200,
                calibration_days=30,
                accuracy_alert_drop=0.05,
                accuracy_retraining_drop=0.10,
                calibration_alert_error=0.05,
                calibration_retraining_error=0.10,
            ),
            retraining=RetrainingPolicy(minimum_new_settled_samples=500),
            promotion=PromotionPolicy(
                minimum_shadow_settled_samples=500,
                significance_confidence_level=0.95,
            ),
            lifecycle_stability=LifecycleStabilityPolicy(
                tolerance=0.20,
                direction_change_penalty=0.50,
                neutral_score=0.50,
            ),
        ),
    )


def load_config(overrides: dict[str, Any] | None = None) -> PredictionEngineConfig:
    config = default_config()
    try:
        if overrides:
            config = _apply_overrides(config, overrides)
        config.validate()
    except ConfigValidationError:
        raise
    except (AttributeError, TypeError) as exc:
        raise ConfigValidationError(f"invalid prediction engine config shape: {exc}") from exc
    return config


def _apply_overrides(config: PredictionEngineConfig, overrides: dict[str, Any]) -> PredictionEngineConfig:
    updated = config
    for key, value in overrides.items():
        if key == "confidence_thresholds":
            updated = replace(updated, confidence_thresholds=replace(updated.confidence_thresholds, **value))
        elif key == "match_gate":
            updated = replace(updated, match_gate=replace(updated.match_gate, **value))
        elif key == "backfill":
            groups = deepcopy(updated.backfill.matches_needed_by_feature_group)
            groups.update(value.get("matches_needed_by_feature_group", {}))
            updated = replace(updated, backfill=replace(updated.backfill, matches_needed_by_feature_group=groups))
        elif key == "form":
            form_values = dict(value)
            if "lookback_windows" in form_values:
                form_values["lookback_windows"] = tuple(form_values["lookback_windows"])
            updated = replace(updated, form=replace(updated.form, **form_values))
        elif key == "model_weights":
            weights = deepcopy(updated.model_weights)
            for market, raw_weights in value.items():
                current = weights.get(market, BlendWeights(statistical_model=0, form_adjustment=0))
                weights[market] = replace(current, **raw_weights)
            updated = replace(updated, model_weights=weights)
        elif key == "probability_clamp":
            updated = replace(updated, probability_clamp=replace(updated.probability_clamp, **value))
        elif key == "correct_score":
            updated = replace(updated, correct_score=replace(updated.correct_score, **value))
        elif key == "v2":
            updated = replace(updated, v2=_apply_v2_overrides(updated.v2, value))
        else:
            raise ConfigValidationError(f"unknown prediction engine config section: {key}")
    return updated


def _apply_v2_overrides(config: V2FoundationConfig, overrides: dict[str, Any]) -> V2FoundationConfig:
    if not isinstance(overrides, dict):
        raise ConfigValidationError("prediction engine v2 config must be an object")
    updated = config
    for key, value in overrides.items():
        if key == "market_tier_strictness":
            updated = replace(updated, market_tier_strictness=replace(updated.market_tier_strictness, **value))
        elif key == "ensemble_weights":
            weights = deepcopy(updated.ensemble_weights)
            for market, raw_weights in value.items():
                current = weights.get(market, EnsembleWeights(model_a=0, model_b=0, model_c=0))
                weights[market] = replace(current, **raw_weights)
            updated = replace(updated, ensemble_weights=weights)
        elif key == "season_recent_weights":
            updated = replace(updated, season_recent_weights=replace(updated.season_recent_weights, **value))
        elif key == "ranking_weights":
            updated = replace(updated, ranking_weights=replace(updated.ranking_weights, **value))
        elif key == "feature_decay_rates":
            updated = replace(updated, feature_decay_rates=replace(updated.feature_decay_rates, **value))
        elif key == "recommendation_tier_by_market":
            tiers = deepcopy(updated.recommendation_tier_by_market)
            tiers.update(value)
            updated = replace(updated, recommendation_tier_by_market=tiers)
        elif key == "calibration":
            updated = replace(updated, calibration=_apply_calibration_overrides(updated.calibration, value))
        elif key == "ood_thresholds":
            updated = replace(updated, ood_thresholds=replace(updated.ood_thresholds, **value))
        elif key == "drift_windows":
            updated = replace(updated, drift_windows=replace(updated.drift_windows, **value))
        elif key == "retraining":
            updated = replace(updated, retraining=replace(updated.retraining, **value))
        elif key == "promotion":
            updated = replace(updated, promotion=replace(updated.promotion, **value))
        elif key == "lifecycle_stability":
            updated = replace(updated, lifecycle_stability=replace(updated.lifecycle_stability, **value))
        else:
            raise ConfigValidationError(f"unknown prediction engine v2 config section: {key}")
    return updated


def _apply_calibration_overrides(config: CalibrationPolicy, overrides: dict[str, Any]) -> CalibrationPolicy:
    values = dict(overrides)
    if "primary_method_by_market" in values:
        methods = deepcopy(config.primary_method_by_market)
        methods.update(values["primary_method_by_market"])
        values["primary_method_by_market"] = methods
    if "minimum_sample_by_market" in values:
        samples = deepcopy(config.minimum_sample_by_market)
        samples.update(values["minimum_sample_by_market"])
        values["minimum_sample_by_market"] = samples
    return replace(config, **values)


def _validate_weight_map(weights_by_market: dict[str, EnsembleWeights], problems: list[str]) -> None:
    _validate_exact_keys("v2 ensemble weights", V2_ENSEMBLE_MARKET_FAMILIES, weights_by_market, problems)
    for market, weights in sorted(weights_by_market.items()):
        _validate_sum_to_one(
            f'v2 ensemble weights for "{market}"',
            (weights.model_a, weights.model_b, weights.model_c),
            problems,
        )


def _validate_sum_to_one(name: str, values: tuple[float, ...], problems: list[str]) -> None:
    if any(not _is_number(value) or value < 0 for value in values):
        problems.append(f"{name} must contain only finite non-negative weights")
        return
    if not math.isclose(sum(values), 1.0, abs_tol=1e-9):
        problems.append(f"{name} must sum to 1.0")


def _validate_market_keys(name: str, values: dict[str, Any], problems: list[str]) -> None:
    _validate_exact_keys(name, V2_MARKET_FAMILIES, values, problems)


def _validate_exact_keys(
    name: str,
    expected_keys: tuple[str, ...],
    values: dict[str, Any],
    problems: list[str],
) -> None:
    expected = set(expected_keys)
    actual = set(values)
    if actual != expected:
        missing = ", ".join(sorted(expected - actual)) or "none"
        extra = ", ".join(sorted(actual - expected)) or "none"
        problems.append(f"{name} must cover exactly the v2 market families (missing: {missing}; extra: {extra})")


def _validate_ordered_drift_thresholds(
    name: str,
    alert_threshold: float,
    retraining_threshold: float,
    problems: list[str],
) -> None:
    if not (
        _valid_ratio(alert_threshold)
        and _valid_ratio(retraining_threshold)
        and 0 < alert_threshold < retraining_threshold < 1
    ):
        problems.append(f"{name} thresholds must satisfy 0 < alert < retraining < 1")


def _is_number(value: Any) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)


def _valid_ratio(value: Any) -> bool:
    return _is_number(value) and 0 <= value <= 1


def _positive_int(value: Any) -> bool:
    return isinstance(value, int) and not isinstance(value, bool) and value > 0
