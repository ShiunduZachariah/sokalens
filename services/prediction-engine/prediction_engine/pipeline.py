from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timedelta
from typing import Protocol

from prediction_engine.combination_markets import MarketLeg, joint_probability
from prediction_engine.confidence import ConfidenceInput, ConfidenceScorer
from prediction_engine.ensemble import blend_tier_a_markets
from prediction_engine.features import (
    DEFAULT_HOME_ADVANTAGE,
    FeatureAssembler,
    FeatureVector,
    FixtureRecord,
    OddsSnapshot,
    TeamFixtureStats,
)
from prediction_engine.gate import GateOutcome, MarketTier, MatchQualityGate, MatchQualityInput
from prediction_engine.league_strength import (
    LeagueStrengthTable,
    compute_league_strength,
    measure_home_advantage,
)
from prediction_engine.model_a import ModelAInputs, run_model_a
from prediction_engine.model_b import ModelBInputs, run_model_b
from prediction_engine.team_strength import TeamStrengthTable, compute_team_strength
from prediction_engine.persistence import (
    EnginePrediction,
    GateTier,
    PredictionLifecycleStage,
)
from prediction_engine.rate_markets import RateMarketResult, TeamRateMarketResult, combined_total_market, team_total_market
from prediction_engine.ranking import ExplanationInput, RankingInput, assign_best_bet_flags, explain_prediction, rank_score
from prediction_engine.config import default_config
from prediction_engine.shot_possession_markets import PossessionMarketResult, ShotMarketResult, possession_market, shots_on_target_market, tier_market_eligibility


@dataclass(frozen=True)
class FixtureEngineInput:
    as_of_timestamp: datetime
    target_fixture: FixtureRecord
    history: list[FixtureRecord]
    statistics_by_fixture: dict[int, list[TeamFixtureStats]]
    odds: OddsSnapshot | None
    generated_at: datetime
    # True when the odds above were synthesized (no real bookmaker odds yet).
    odds_provisional: bool = False
    coverage: dict[str, bool] | None = None
    market_accuracy: dict[str, float] = field(default_factory=dict)
    league_accuracy: float | None = None
    lifecycle_stage: PredictionLifecycleStage = PredictionLifecycleStage.PRE_MATCH
    model_version: str = "python-engine-v1"


class FixtureInputRepository(Protocol):
    def load_fixture_inputs(self, fixture_id: int, *, as_of_timestamp: datetime) -> FixtureEngineInput: ...


class PredictionOutputRepository(Protocol):
    def save_predictions(self, predictions: list[EnginePrediction]) -> None: ...
    def write_provisional_odds(self, snapshot: OddsSnapshot) -> None: ...


class BackfillDeferred(RuntimeError):
    def __init__(self, fixture_id: int) -> None:
        self.fixture_id = fixture_id
        super().__init__(f"fixture {fixture_id} deferred while history backfill is pending")


def run_and_save_fixture(
    fixture_id: int,
    *,
    as_of_timestamp: datetime,
    input_repository: FixtureInputRepository,
    output_repository: PredictionOutputRepository,
) -> list[EnginePrediction]:
    inputs = input_repository.load_fixture_inputs(fixture_id, as_of_timestamp=as_of_timestamp)
    ensure_history_backfill = getattr(input_repository, "ensure_history_backfill", None)
    if callable(ensure_history_backfill):
        statuses = ensure_history_backfill(
            inputs,
            matches_needed_by_feature_group=default_config().backfill.matches_needed_by_feature_group,
        )
    predictions = assign_best_bet_flags(run_fixture_engine(inputs), now=inputs.generated_at)
    # A backfill request is work running in parallel, not a blanket embargo on
    # the full seven-day prediction horizon. Publish whenever the quality gate
    # can already produce an honest BASIC/FULL result from N/N-1 history or the
    # league baseline; defer only when the gate still has no usable signal.
    if not predictions and any(status in {"pending", "in_progress"} for status in statuses):
        raise BackfillDeferred(fixture_id)
    if predictions and inputs.odds_provisional and inputs.odds is not None:
        output_repository.write_provisional_odds(inputs.odds)
    output_repository.save_predictions(predictions)
    return predictions


def run_fixture(
    fixture_id: int,
    *,
    as_of_timestamp: datetime,
    input_repository: FixtureInputRepository,
) -> list[EnginePrediction]:
    inputs = input_repository.load_fixture_inputs(fixture_id, as_of_timestamp=as_of_timestamp)
    return assign_best_bet_flags(run_fixture_engine(inputs), now=inputs.generated_at)


@dataclass(frozen=True)
class PreMatchRates:
    """The pre-match Poisson rates, and how complete the data behind them is."""

    home_lambda: float
    away_lambda: float
    data_completeness: float
    fallback_flags: tuple[str, ...]

    # Expected full-match totals for the counting statistics, keyed by the
    # in-play market name: total_cards, total_corners, total_shots,
    # shots_on_target, total_fouls. A statistic whose rolling averages are
    # missing is absent rather than zero -- zero is a prediction, absence is
    # not -- and the in-play model then waits until the match itself has
    # produced enough play to infer a rate.
    #
    # Taken from the SAME `features.statistics` entries the pre-match rate
    # markets use, for the reason fixture_expected_goals exists at all: a
    # second derivation of the same expectation is a second definition that
    # can drift from this one silently, and then the pre-match corner market
    # and the in-play corner ladder would disagree about the same match.
    count_means: dict[str, float] = field(default_factory=dict)


def fixture_expected_goals(inputs: FixtureEngineInput) -> PreMatchRates:
    """lambda_home / lambda_away for a fixture, by the pre-match derivation.

    Exposed because the in-play model (architecture v2 7.2) is Model A's
    machinery *conditioned on match state*: it needs the same pre-match rates
    the pre-match pipeline computes, and a second derivation would be a second
    definition of the same number that could drift from this one silently.

    This runs the feature assembly and stops before the quality gate on
    purpose. The gate answers "is this fixture worth publishing pre-match
    markets for", which is a different question from "what were the rates" --
    a fixture the gate would SKIP still has rates, and once it is being played
    the live score is carrying most of the signal anyway.
    """
    config = default_config()
    strength_table = compute_league_strength(inputs.history)
    home_advantage = measure_home_advantage(inputs.history, inputs.target_fixture.league_id)
    team_table = compute_team_strength(inputs.history, home_advantage=home_advantage)
    features = FeatureAssembler(
        form_windows=config.form.lookback_windows,
        decay_rate=config.form.decay_rate,
    ).assemble(
        inputs.target_fixture,
        inputs.history,
        inputs.statistics_by_fixture,
        as_of_timestamp=inputs.as_of_timestamp,
        odds=inputs.odds,
        odds_provisional=inputs.odds_provisional,
        league_strength=_league_strength_scales(strength_table, inputs.target_fixture),
        home_advantage=home_advantage,
        team_strength=_team_strength_ratings(team_table, inputs.target_fixture),
    )
    home_lambda, away_lambda = _expected_goals(features)
    return PreMatchRates(
        home_lambda=home_lambda,
        away_lambda=away_lambda,
        data_completeness=features.data_completeness,
        fallback_flags=tuple(features.fallback_flags),
        count_means=_count_means(features.statistics),
    )


# Which pair of rolling per-team averages gives each counting statistic's
# expected match total. The metric names are the pre-match pipeline's own.
_COUNT_MEAN_METRICS: dict[str, tuple[str, str]] = {
    "total_cards": ("home_card_points", "away_card_points"),
    "total_corners": ("home_corners_for", "away_corners_for"),
    "total_shots": ("home_shots_total", "away_shots_total"),
    "shots_on_target": ("home_shots_on_target", "away_shots_on_target"),
    "total_fouls": ("home_fouls", "away_fouls"),
}


def _count_means(statistics: dict[str, float | None]) -> dict[str, float]:
    """Expected full-match totals per counting statistic, omitting the unknown.

    A statistic is included only when BOTH sides' averages are present and the
    sum is positive. A half-known total would be a systematically low
    expectation -- one team's corners standing in for both -- and a prior that
    is wrong in a known direction is worse than no prior, because the match's
    own rate would have to overcome it rather than merely inform it.
    """
    means: dict[str, float] = {}
    for market, (home_metric, away_metric) in _COUNT_MEAN_METRICS.items():
        home_value = statistics.get(home_metric)
        away_value = statistics.get(away_metric)
        if home_value is None or away_value is None:
            continue
        total = float(home_value) + float(away_value)
        if total > 0.0:
            means[market] = total
    return means


def run_fixture_engine(inputs: FixtureEngineInput) -> list[EnginePrediction]:
    config = default_config()
    strength_table = compute_league_strength(inputs.history)
    home_advantage = measure_home_advantage(inputs.history, inputs.target_fixture.league_id)
    team_table = compute_team_strength(inputs.history, home_advantage=home_advantage)
    features = FeatureAssembler(
        form_windows=config.form.lookback_windows,
        decay_rate=config.form.decay_rate,
    ).assemble(
        inputs.target_fixture,
        inputs.history,
        inputs.statistics_by_fixture,
        as_of_timestamp=inputs.as_of_timestamp,
        odds=inputs.odds,
        odds_provisional=inputs.odds_provisional,
        league_strength=_league_strength_scales(strength_table, inputs.target_fixture),
        home_advantage=home_advantage,
        team_strength=_team_strength_ratings(team_table, inputs.target_fixture),
    )
    gate_input = MatchQualityInput(
        fixture_status=inputs.target_fixture.status,
        home_recent_sample=_effective_history_sample(features, "home"),
        away_recent_sample=_effective_history_sample(features, "away"),
        completeness_score=features.data_completeness,
        group_completeness=features.group_completeness,
        one_x_two_odds_present=bool(features.odds_implied_1x2),
        early_season="early_season" in features.fallback_flags,
    )
    gate = MatchQualityGate(config).evaluate(gate_input)
    if gate.outcome is GateOutcome.SKIP:
        return []

    home_lambda, away_lambda = _expected_goals(features)
    model_a = run_model_a(ModelAInputs(home_lambda=home_lambda, away_lambda=away_lambda))
    # Model B's form adjustment and the ensemble blend were previously defined,
    # configured and tested but never called, leaving Model A's raw Poisson as
    # the whole prediction and config.model_weights tuning nothing.
    tier_a_markets = blend_tier_a_markets(
        model_a.markets,
        run_model_b(_model_b_inputs(features, model_a.markets)).market_deltas,
        config,
    )
    # A settled bookmaker 1X2 line is the strongest public estimate available;
    # it used to reach only the quality gate and the confidence score, never
    # the published probability.
    tier_a_markets = _anchor_one_x_two_to_market(
        tier_a_markets,
        features.odds_implied_1x2 if not inputs.odds_provisional else {},
    )
    market_candidates: list[tuple[str, str, float]] = []
    if MarketTier.A in gate.allowed_market_tiers:
        market_candidates.extend(_model_a_prediction_candidates(tier_a_markets))
    if MarketTier.B in gate.allowed_market_tiers:
        market_candidates.extend(_tier_b_prediction_candidates(features, inputs.coverage))
    if MarketTier.C in gate.allowed_market_tiers:
        market_candidates.extend(_tier_c_prediction_candidates(features, inputs.coverage))
    if MarketTier.D in gate.allowed_market_tiers:
        market_candidates.extend(_tier_d_prediction_candidates(tier_a_markets, model_a.score_matrix))
    market_candidates = _score_consistent_candidates(
        market_candidates,
        alternatives=_score_candidate_alternatives(tier_a_markets, model_a.score_matrix),
    )
    features_used = {
        "feature_vector": {
            "fallback_flags": features.fallback_flags,
            "group_completeness": features.group_completeness,
        },
        **model_a.features_used,
        "expected_goals": {"home": home_lambda, "away": away_lambda},
        "league_strength": features.league_strength,
        "team_strength": features.team_strength,
        "home_advantage": features.home_advantage,
        "as_of_timestamp": features.as_of_timestamp.isoformat(),
        "kickoff_at": inputs.target_fixture.kickoff_at.isoformat(),
        "league_id": inputs.target_fixture.league_id,
    }
    predictions: list[EnginePrediction] = []
    for market, selection, raw_probability in market_candidates:
        probability = _clamp_probability(raw_probability, config.probability_clamp.minimum, config.probability_clamp.maximum)
        market_accuracy = _historical_market_accuracy(inputs.market_accuracy, market)
        league_accuracy = _historical_league_accuracy(inputs.league_accuracy)
        historical_accuracy = (market_accuracy * 0.75) + (league_accuracy * 0.25)
        confidence = ConfidenceScorer().score(
            ConfidenceInput(
                data_completeness=features.data_completeness,
                home_sample_size=int(features.home_recent["sample_size_5"]),
                away_sample_size=int(features.away_recent["sample_size_5"]),
                model_a_probability=probability,
                blended_probability=probability,
                external_probabilities=(
                    tuple(features.odds_implied_1x2.values())
                    if market == "1x2" and not inputs.odds_provisional
                    else ()
                ),
                lineups_confirmed=False,
                market_volatility=0.5,
                historical_accuracy=historical_accuracy,
            )
        )
        ranking_score = rank_score(
            RankingInput(
                probability=probability,
                confidence=confidence.raw_score,
                data_completeness=features.data_completeness,
                market_accuracy=market_accuracy,
                league_accuracy=league_accuracy,
                edge_vs_odds=0.0,
            )
        )
        row_features_used = {
            **features_used,
            "ranking_score": ranking_score,
            "market_accuracy": market_accuracy,
            "league_accuracy": league_accuracy,
            "historical_accuracy": historical_accuracy,
        }
        explanation = explain_prediction(
            ExplanationInput(
                market=market,
                prediction=selection,
                probability=probability,
                drivers={
                    "home expected goals": home_lambda,
                    "away expected goals": away_lambda,
                    "data completeness": features.data_completeness,
                },
                fallback_flags=features.fallback_flags,
            )
        )
        predictions.append(
            EnginePrediction(
                fixture_id=inputs.target_fixture.id,
                market=market,
                prediction=selection,
                probability=probability,
                confidence=confidence.calibrated_score,
                confidence_tier=confidence.tier,
                is_best_bet=False,
                lifecycle_stage=inputs.lifecycle_stage,
                data_completeness=features.data_completeness,
                features_used=row_features_used,
                explanation=explanation,
                generated_at=inputs.generated_at,
                gate_tier=GateTier(gate.outcome.value),
                horizon_eligible=True,
                embargo_active=False,
                model_version=inputs.model_version,
                # Persistence replaces these neutral first-version values when
                # it locks and reads an immediately preceding current version.
                max_lifecycle_probability_shift=0.0,
                prediction_direction_changed=False,
                lifecycle_stability_score=0.5,
            )
        )
    return predictions


def _model_a_prediction_candidates(markets: dict[str, object]) -> list[tuple[str, str, float]]:
    candidates: list[tuple[str, str, float]] = []
    for market_name, raw_market in (
        ("1x2", markets["match_result"]),
        ("double_chance", markets["double_chance"]),
        ("draw_no_bet", markets["draw_no_bet"]),
        ("btts", markets["btts"]),
        ("half_time_result", markets["half_time_result"]),
        ("highest_scoring_half", markets["highest_scoring_half"]),
        ("team_to_score_first", markets["team_to_score_first"]),
        ("odd_even_total_goals", markets["odd_even_total_goals"]),
    ):
        selection, probability = _top_dict_selection(raw_market)
        candidates.append((market_name, selection, probability))

    total_goals = markets["total_goals"]
    candidates.append(_top_total_goals_2_5_selection(total_goals))

    correct_score = markets["correct_score"]
    if isinstance(correct_score, list) and correct_score:
        top_score = correct_score[0]
        candidates.append(("correct_score", str(top_score["score"]), float(top_score["probability"])))
    return candidates


_SCORE_MARKET_PRIORITY = {
    "1x2": 0,
    "btts": 1,
    "total_goals": 2,
    "double_chance": 3,
    "draw_no_bet": 4,
    "team_to_score_first": 5,
    "odd_even_total_goals": 6,
    "btts_total_goals": 7,
    "btts_match_result": 8,
    "result_total_goals": 9,
    "correct_score": 10,
}


def _score_consistent_candidates(
    candidates: list[tuple[str, str, float]],
    *,
    alternatives: dict[str, list[tuple[str, str, float]]] | None = None,
) -> list[tuple[str, str, float]]:
    """Keep one jointly satisfiable public story for score-derived markets.

    Marginal markets are authoritative. Composite and exact-score candidates
    are published only when at least one plausible scoreline can satisfy every
    score-derived selection already accepted for the fixture. Markets based on
    corners, cards, fouls, shots, possession, or half-time state remain in the
    input order because they do not assert a full-time scoreline here.
    """
    indexed = list(enumerate(candidates))
    score_candidates = sorted(
        (item for item in indexed if item[1][0] in _SCORE_MARKET_PRIORITY),
        key=lambda item: (_SCORE_MARKET_PRIORITY[item[1][0]], item[0]),
    )
    accepted_indexes: set[int] = {index for index, candidate in indexed if candidate[0] not in _SCORE_MARKET_PRIORITY}
    accepted_score: list[tuple[str, str, float]] = []
    for index, candidate in score_candidates:
        options = [candidate]
        options.extend(option for option in (alternatives or {}).get(candidate[0], []) if option[1] != candidate[1])
        for option in options:
            proposed = [*accepted_score, option]
            if any(all(_selection_matches_score(market, selection, home, away) for market, selection, _ in proposed) for home in range(13) for away in range(13)):
                accepted_score.append(option)
                candidates[index] = option
                accepted_indexes.add(index)
                break
    return [candidates[index] for index, _ in indexed if index in accepted_indexes]


def _score_candidate_alternatives(markets: dict[str, object], score_matrix: list[list[float]]) -> dict[str, list[tuple[str, str, float]]]:
    sources = {
        "1x2": markets["match_result"],
        "double_chance": markets["double_chance"],
        "draw_no_bet": markets["draw_no_bet"],
        "btts": markets["btts"],
        "team_to_score_first": markets["team_to_score_first"],
        "odd_even_total_goals": markets["odd_even_total_goals"],
        "btts_total_goals": markets["btts_total_goals"],
        "btts_match_result": markets["btts_match_result"],
    }
    result: dict[str, list[tuple[str, str, float]]] = {}
    for market, raw_values in sources.items():
        if isinstance(raw_values, dict):
            result[market] = sorted(
                ((market, str(selection), float(probability)) for selection, probability in raw_values.items()),
                key=lambda item: item[2],
                reverse=True,
            )
    total_goals = markets["total_goals"]
    if isinstance(total_goals, dict):
        result["total_goals"] = sorted(
            (("total_goals", selection, float(total_goals[selection])) for selection in ("over_2.5", "under_2.5")),
            key=lambda item: item[2],
            reverse=True,
        )
    result["correct_score"] = sorted(
        (("correct_score", f"{home}-{away}", probability) for home, row in enumerate(score_matrix) for away, probability in enumerate(row)),
        key=lambda item: item[2],
        reverse=True,
    )
    result["result_total_goals"] = sorted(
        (
            ("result_total_goals", f"{result_side}_{total_side}_2.5", joint_probability(score_matrix, MarketLeg.match_result(result_side), MarketLeg.total_goals(total_side, 2.5)))
            for result_side in ("home", "draw", "away")
            for total_side in ("over", "under")
        ),
        key=lambda item: item[2],
        reverse=True,
    )
    return result


def _selection_matches_score(market: str, selection: str, home: int, away: int) -> bool:
    result = "home" if home > away else "away" if away > home else "draw"
    btts = "yes" if home > 0 and away > 0 else "no"
    total = home + away
    if market == "1x2":
        return selection == result
    if market == "double_chance":
        return result in selection.split("_")
    if market == "draw_no_bet":
        return result == "draw" or result == selection
    if market == "btts":
        return selection == btts
    if market == "team_to_score_first":
        return total == 0 if selection == "no_goal" else total > 0
    if market == "odd_even_total_goals":
        return selection == ("even" if total % 2 == 0 else "odd")
    if market == "correct_score":
        return selection == f"{home}-{away}"
    if market == "total_goals":
        return _line_selection_matches(selection, total)
    if market == "btts_total_goals":
        prefix, side, raw_line = selection.rsplit("_", 2)
        return prefix == btts and _line_matches(side, float(raw_line), total)
    if market == "btts_match_result":
        selected_result, selected_btts = selection.split("_", 1)
        return selected_result == result and selected_btts == btts
    if market == "result_total_goals":
        selected_result, side, raw_line = selection.rsplit("_", 2)
        return selected_result == result and _line_matches(side, float(raw_line), total)
    return True


def _line_selection_matches(selection: str, total: int) -> bool:
    side, raw_line = selection.split("_", 1)
    return _line_matches(side, float(raw_line), total)


def _line_matches(side: str, line: float, total: int) -> bool:
    return total > line if side == "over" else total < line


def _top_total_goals_2_5_selection(total_goals: object) -> tuple[str, str, float]:
    if not isinstance(total_goals, dict) or not all(side in total_goals for side in ("over_2.5", "under_2.5")):
        raise TypeError("total_goals market must include over_2.5 and under_2.5")
    selection = max(("over_2.5", "under_2.5"), key=lambda side: float(total_goals[side]))
    return "total_goals", selection, float(total_goals[selection])


def _tier_b_prediction_candidates(
    features: FeatureVector,
    coverage: dict[str, bool] | None,
) -> list[tuple[str, str, float]]:
    statistics = features.statistics
    candidates: list[tuple[str, str, float]] = []
    if _statistics_market_eligible(
        features,
        coverage,
        market_tier="B",
        required_metrics=("home_corners_for", "away_corners_for"),
    ):
        corner_results = (
            combined_total_market(
                market_key="total_corners",
                expected_total_lambda=_statistic_value(statistics, "home_corners_for")
                + _statistic_value(statistics, "away_corners_for"),
                lines=(7.5, 8.5, 9.5, 10.5),
            ),
            team_total_market(
                market_key="team_corners_home",
                team_key="home",
                team_lambda=_statistic_value(statistics, "home_corners_for"),
                lines=(3.5, 4.5, 5.5),
            ),
            team_total_market(
                market_key="team_corners_away",
                team_key="away",
                team_lambda=_statistic_value(statistics, "away_corners_for"),
                lines=(3.5, 4.5, 5.5),
            ),
        )
        candidates.extend(_top_rate_market_selection(result) for result in corner_results)

    if _statistics_market_eligible(
        features,
        coverage,
        market_tier="B",
        required_metrics=("home_card_points", "away_card_points"),
    ):
        candidates.append(
            _top_rate_market_selection(
                combined_total_market(
                    market_key="total_cards",
                    expected_total_lambda=_statistic_value(statistics, "home_card_points")
                    + _statistic_value(statistics, "away_card_points"),
                    lines=(2.5, 3.5, 4.5, 5.5),
                )
            )
        )

    if _statistics_market_eligible(
        features,
        coverage,
        market_tier="B",
        required_metrics=("home_fouls", "away_fouls"),
    ):
        candidates.append(
            _top_rate_market_selection(
                combined_total_market(
                    market_key="total_fouls",
                    expected_total_lambda=_statistic_value(statistics, "home_fouls")
                    + _statistic_value(statistics, "away_fouls"),
                    lines=(18.5, 21.5, 24.5),
                )
            )
        )
    return candidates


def _tier_c_prediction_candidates(
    features: FeatureVector,
    coverage: dict[str, bool] | None,
) -> list[tuple[str, str, float]]:
    statistics = features.statistics
    candidates: list[tuple[str, str, float]] = []
    if _statistics_market_eligible(
        features,
        coverage,
        market_tier="C",
        required_metrics=("home_shots_total", "away_shots_total"),
    ):
        total_shots = combined_total_market(
            market_key="total_shots",
            expected_total_lambda=_statistic_value(statistics, "home_shots_total")
            + _statistic_value(statistics, "away_shots_total"),
            lines=(18.5, 21.5, 24.5),
        )
        candidates.append(_top_rate_market_selection(total_shots))

    if _statistics_market_eligible(
        features,
        coverage,
        market_tier="C",
        required_metrics=("home_shots_on_target", "away_shots_on_target"),
    ):
        shots_on_target = shots_on_target_market(
            expected_total_lambda=_statistic_value(statistics, "home_shots_on_target")
            + _statistic_value(statistics, "away_shots_on_target"),
            lines=(6.5, 7.5, 8.5),
        )
        candidates.append(_top_shot_market_selection(shots_on_target))

    if _statistics_market_eligible(
        features,
        coverage,
        market_tier="C",
        required_metrics=("home_possession", "away_possession"),
    ):
        possession = possession_market(
            expected_home_possession_pct=_statistic_value(statistics, "home_possession")
        )
        candidates.append(_top_possession_market_selection(possession))
    return candidates


def _tier_d_prediction_candidates(markets: dict[str, object], score_matrix: list[list[float]]) -> list[tuple[str, str, float]]:
    candidates: list[tuple[str, str, float]] = []
    for market_name in ("btts_total_goals", "btts_match_result"):
        selection, probability = _top_dict_selection(markets[market_name])
        candidates.append((market_name, selection, probability))
    candidates.append(_top_result_total_goals_selection(score_matrix))
    return candidates


def _top_result_total_goals_selection(score_matrix: list[list[float]]) -> tuple[str, str, float]:
    choices: list[tuple[str, float]] = []
    for result in ("home", "draw", "away"):
        result_leg = MarketLeg.match_result(result)
        for side in ("over", "under"):
            total_leg = MarketLeg.total_goals(side, 2.5)
            choices.append((f"{result}_{side}_2.5", joint_probability(score_matrix, result_leg, total_leg)))
    selection, probability = max(choices, key=lambda item: item[1])
    return "result_total_goals", selection, probability


def _top_rate_market_selection(result: RateMarketResult | TeamRateMarketResult) -> tuple[str, str, float]:
    choices = (
        (line, "over", line_probabilities.over)
        if line_probabilities.over >= line_probabilities.under
        else (line, "under", line_probabilities.under)
        for line, line_probabilities in result.lines.items()
    )
    line, side, probability = max(choices, key=lambda item: item[2])
    return result.market_key, f"{side}_{line:g}", probability


def _top_shot_market_selection(result: ShotMarketResult) -> tuple[str, str, float]:
    choices = (
        (line, "over", line_probabilities.over)
        if line_probabilities.over >= line_probabilities.under
        else (line, "under", line_probabilities.under)
        for line, line_probabilities in result.lines.items()
    )
    line, side, probability = max(choices, key=lambda item: item[2])
    return result.market_key, f"{side}_{line:g}", probability


def _top_possession_market_selection(result: PossessionMarketResult) -> tuple[str, str, float]:
    selection, probability = max(result.side_probabilities.items(), key=lambda item: item[1])
    return "possession_winner", selection, probability


def _has_statistics_coverage(
    coverage: dict[str, bool] | None,
    statistics: dict[str, float | None],
) -> bool:
    if not statistics:
        return False
    # Rich rows are direct evidence of provider statistics support.  The
    # coarse league_coverage flag can lag ingestion and previously suppressed
    # corners/cards even after those rows had landed. Supplemental score-only
    # providers never create these rows, so this remains provenance-safe.
    return True


def _effective_history_sample(features: FeatureVector, side: str) -> int:
    sample = int(features.home_recent["sample_size_5"] if side == "home" else features.away_recent["sample_size_5"])
    if f"{side}_league_baseline_fallback_used" in features.fallback_flags:
        return max(sample, 5)
    return sample


# Goals per team per match when a competition has no usable baseline of its own.
_NEUTRAL_GOAL_RATE = 1.35

# Lambdas outside this band are not football scorelines; they are a symptom of a
# thin or corrupt sample, so bound them rather than publishing the consequence.
_MIN_EXPECTED_GOALS = 0.15
_MAX_EXPECTED_GOALS = 5.0

# Share of the 1X2 probability taken from a settled bookmaker line when one is
# available. The market is the better estimator, but not so completely that the
# model's own read of a fixture is discarded.
_MARKET_ANCHOR_WEIGHT = 0.50

def _expected_goals(features: FeatureVector) -> tuple[float, float]:
    """Expected goals for each side, from attack against the opponent's defence.

    The previous derivation used a team's own goals-scored average and nothing
    else: the opponent's defence was never consulted (goals_against was
    computed and discarded), goals were never rescaled between leagues, and the
    home side got no home edge. That reduced every fixture to "who scores more
    per game in their own league", which is why a side that dominates a weak
    domestic league was picked over a European heavyweight.

    This is the standard Poisson form, lambda = mu * attack * defence * gamma,
    with attack and defence expressed relative to the competition's own goal
    baseline and restated on a neutral-league scale first.
    """
    baseline = features.league_baseline.total_goals_per_match / 2
    if baseline <= 0:
        baseline = _NEUTRAL_GOAL_RATE

    home_attack, home_defence = _side_rates(features, "home", baseline)
    away_attack, away_defence = _side_rates(features, "away", baseline)
    home_advantage = features.home_advantage or DEFAULT_HOME_ADVANTAGE

    home_lambda = (home_attack * away_defence / baseline) * home_advantage
    away_lambda = (away_attack * home_defence / baseline) / home_advantage
    return (
        _clamp_probability(home_lambda, _MIN_EXPECTED_GOALS, _MAX_EXPECTED_GOALS),
        _clamp_probability(away_lambda, _MIN_EXPECTED_GOALS, _MAX_EXPECTED_GOALS),
    )


def _side_rates(features: FeatureVector, side: str, baseline: float) -> tuple[float, float]:
    """One side's scoring and conceding rates, on a neutral-league scale."""
    if f"{side}_league_baseline_fallback_used" in features.fallback_flags:
        # No usable team history: the competition's own home/away goal rates are
        # the last rung of the early-season hierarchy.
        scored = (
            features.league_baseline.home_goals_per_match
            if side == "home"
            else features.league_baseline.away_goals_per_match
        )
        conceded = (
            features.league_baseline.away_goals_per_match
            if side == "home"
            else features.league_baseline.home_goals_per_match
        )
        return (scored or baseline, conceded or baseline)

    recent = features.home_recent if side == "home" else features.away_recent
    strength = features.league_strength
    attack, defence = _side_goal_rates(features, side, recent, baseline)
    attack *= strength[f"{side}_attack_scale"]
    defence *= strength[f"{side}_defence_scale"]
    # A team can genuinely be shut out across a five-match window; a zero rate
    # would collapse the whole fixture, so fall back to the competition rate.
    return (attack or baseline, defence or baseline)


def _side_goal_rates(
    features: FeatureVector,
    side: str,
    recent: dict[str, float],
    baseline: float,
) -> tuple[float, float]:
    """Scoring and conceding rates before any between-league rescaling.

    Prefer the opponent-adjusted rating: a team's raw goals-per-game treats a
    fixture against the leaders and one against the bottom club as identical
    evidence, which is what made same-league predictions flat. Fall back to the
    observed average when the league has too little history to fit a rating.
    """
    ratings = features.team_strength
    league_goal_rate = float(ratings.get(f"{side}_league_goal_rate", 0.0))
    if league_goal_rate > 0:
        return (
            league_goal_rate * float(ratings[f"{side}_attack_rating"]),
            league_goal_rate * float(ratings[f"{side}_defence_rating"]),
        )
    return (
        float(recent["goals_for_per_match_5"]),
        float(recent["goals_against_per_match_5"]),
    )


def _team_strength_ratings(table: TeamStrengthTable, fixture: FixtureRecord) -> dict[str, float]:
    home = table.for_team(fixture.home_team_id)
    away = table.for_team(fixture.away_team_id)
    return {
        "home_attack_rating": home.attack,
        "home_defence_rating": home.defence,
        "away_attack_rating": away.attack,
        "away_defence_rating": away.defence,
        "home_league_goal_rate": home.league_goal_rate,
        "away_league_goal_rate": away.league_goal_rate,
    }


def _league_strength_scales(table: LeagueStrengthTable, fixture: FixtureRecord) -> dict[str, float]:
    home = table.for_team(fixture.home_team_id)
    away = table.for_team(fixture.away_team_id)
    return {
        "home_attack_scale": home.attack_scale,
        "home_defence_scale": home.defence_scale,
        "away_attack_scale": away.attack_scale,
        "away_defence_scale": away.defence_scale,
    }


def _model_b_inputs(features: FeatureVector, model_a_markets: dict[str, object]) -> ModelBInputs:
    """Model B's form read, with points rescaled between leagues.

    Points per game is as league-relative as goals are: a side that sweeps a
    weak domestic league outscores a heavyweight on raw points too, so feeding
    it in unscaled would reintroduce the same bias through the form adjustment.
    """
    btts = model_a_markets["btts"]
    total_goals = model_a_markets["total_goals"]
    strength = features.league_strength
    return ModelBInputs(
        home_recent_ppg=_scaled_points(features.home_recent, strength["home_attack_scale"]),
        away_recent_ppg=_scaled_points(features.away_recent, strength["away_attack_scale"]),
        home_btts_rate=_clamp_probability(features.home_recent["btts_rate_5"], 0.0, 1.0),
        away_btts_rate=_clamp_probability(features.away_recent["btts_rate_5"], 0.0, 1.0),
        model_a_btts_yes=_clamp_probability(float(btts["yes"]), 0.0, 1.0),
        home_over_2_5_rate=_clamp_probability(features.home_recent["over_2_5_rate_5"], 0.0, 1.0),
        away_over_2_5_rate=_clamp_probability(features.away_recent["over_2_5_rate_5"], 0.0, 1.0),
        model_a_over_2_5=_clamp_probability(float(total_goals["over_2.5"]), 0.0, 1.0),
    )


def _scaled_points(recent: dict[str, float], attack_scale: float) -> float:
    return _clamp_probability(float(recent["points_per_match_5"]) * attack_scale, 0.0, 3.0)


def _anchor_one_x_two_to_market(
    markets: dict[str, object],
    odds_implied: dict[str, float],
) -> dict[str, object]:
    """Pull the 1X2 probabilities toward a settled bookmaker line.

    The dependent markets are recomputed from the anchored result so double
    chance and draw-no-bet cannot contradict the 1X2 pick they derive from.
    """
    match_result = markets.get("match_result")
    if not odds_implied or not isinstance(match_result, dict):
        return markets

    anchored = {
        outcome: ((1 - _MARKET_ANCHOR_WEIGHT) * float(probability))
        + (_MARKET_ANCHOR_WEIGHT * odds_implied.get(outcome, float(probability)))
        for outcome, probability in match_result.items()
    }
    total = sum(anchored.values())
    if total <= 0:
        return markets
    anchored = {outcome: probability / total for outcome, probability in anchored.items()}

    decisive = anchored["home"] + anchored["away"]
    return {
        **markets,
        "match_result": anchored,
        "double_chance": {
            "home_or_draw": anchored["home"] + anchored["draw"],
            "draw_or_away": anchored["draw"] + anchored["away"],
            "home_or_away": decisive,
        },
        "draw_no_bet": (
            {"home": anchored["home"] / decisive, "away": anchored["away"] / decisive}
            if decisive > 0
            else markets["draw_no_bet"]
        ),
    }


def _statistics_market_eligible(
    features: FeatureVector,
    coverage: dict[str, bool] | None,
    *,
    market_tier: str,
    required_metrics: tuple[str, ...],
) -> bool:
    minimum_sample_size = 5
    sample_size = min(
        (features.statistic_sample_sizes.get(metric, 0) for metric in required_metrics),
        default=0,
    )
    # Eligibility is market-specific: five observed corner samples for both
    # sides are enough to model corners even when unrelated xG/possession rows
    # are sparse. Using whole-group completeness here made one missing rich
    # field suppress every otherwise-supported Tier B/C market.
    metric_completeness = min(sample_size / minimum_sample_size, 1.0)
    return tier_market_eligibility(
        market_tier=market_tier,
        has_statistics=_has_statistics_coverage(coverage, features.statistics),
        group_completeness=metric_completeness,
        statistic_sample_size=sample_size,
        minimum_sample_size=minimum_sample_size,
    ).eligible


def _statistic_value(statistics: dict[str, float | None], metric: str) -> float:
    value = statistics.get(metric)
    if value is None:
        raise ValueError(f'missing required statistic "{metric}"')
    return value


def _top_dict_selection(raw_market: object) -> tuple[str, float]:
    if not isinstance(raw_market, dict):
        raise TypeError("market must be a dict")
    selection, probability = max(raw_market.items(), key=lambda item: float(item[1]))
    return str(selection), float(probability)


def _clamp_probability(value: float, minimum: float, maximum: float) -> float:
    return min(max(float(value), minimum), maximum)


def _historical_market_accuracy(values: dict[str, float], market: str) -> float:
    value = values.get(market)
    if value is None:
        return 0.5
    return _clamp_probability(value, 0.0, 1.0)


def _historical_league_accuracy(value: float | None) -> float:
    if value is None:
        return 0.5
    return _clamp_probability(value, 0.0, 1.0)
