"""The wiring that makes in-play predictions exist (architecture v2 7.4-7.6).

The analytic model, the feature assembly and the candidate scope were all
built and tested before this file, and production still had zero
`engine_predictions` rows in an in-play lifecycle stage -- because nothing
called them. This module is the missing call: per candidate fixture it loads
the newest snapshot and the card timeline, borrows the pre-match rates from
the existing pipeline, runs the analytic model, and writes what the model says
is publishable into `engine_predictions` through the 7.4 columns.

Three things it deliberately does NOT do:

* It does not touch the pre-match path. `run_and_save_fixture` is unchanged
  and the pre-match candidate query keeps its `kickoff_at >= as_of` guard;
  this path is reached only by asking for the in-play scope by name (7.5).
  `tests/prediction-engine/test_inplay_runner.py` asserts that.
* It does not extrapolate. When the model suppresses -- stale snapshot, extra
  time, a period it does not model -- the run records the reason and publishes
  nothing (14). A suppression is an outcome, not a failure, and the CLI
  summary counts it by reason so an operator can see *why* a live match has no
  prediction rather than guessing.
* It does not decide the cadence itself. 7.6 lives in
  `inplay_model.should_regenerate`, and this module hands it the previous
  published state and the current features.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime

from prediction_engine.config import default_config
from prediction_engine.inplay_counts import (
    COUNTING_STATISTICS,
    CountObservation,
    CountPrior,
)
from prediction_engine.inplay_features import (
    InPlayFeatures,
    MatchPeriod,
    assemble_in_play_features,
)
from prediction_engine.inplay_model import (
    SNAPSHOT_TRIGGER_TIMER,
    SnapshotDecision,
    model_health_for,
    should_snapshot,
    snapshot_markets,
    DEFAULT_IN_PLAY_CONFIG,
    IN_PLAY_MODEL_VERSION,
    CadenceState,
    InPlayMarket,
    InPlayModelConfig,
    InPlayModelInputs,
    InPlayModelResult,
    RegenerationTier,
    SuppressionReason,
    publishable_markets,
    run_in_play_model,
    should_regenerate,
)
from prediction_engine.persistence import (
    ConfidenceTier,
    EnginePrediction,
    GateTier,
    PredictionLifecycleStage,
)
from prediction_engine.pipeline import fixture_expected_goals


# Reasons a run published nothing that are about the *run* rather than about
# the match state the model saw. Kept in the same vocabulary as
# SuppressionReason so the CLI summary has one dimension, not two.
NO_SNAPSHOT = "NO_SNAPSHOT"
CADENCE_UNCHANGED = "CADENCE_UNCHANGED"
NO_PUBLISHABLE_MARKETS = "NO_PUBLISHABLE_MARKETS"
PRE_MATCH_RATES_UNAVAILABLE = "PRE_MATCH_RATES_UNAVAILABLE"

# In-play rows are not best-bet candidates and never enter the accumulator
# pool: `qualifies_for_best_bet_pool` already refuses anything past kickoff, so
# this is belt and braces rather than a second rule.
_IN_PLAY_GATE_TIER = GateTier.BASIC

_LIFECYCLE_BY_PERIOD = {
    MatchPeriod.FIRST_HALF: PredictionLifecycleStage.IN_PLAY_1H,
    MatchPeriod.HALF_TIME: PredictionLifecycleStage.HALF_TIME,
    MatchPeriod.SECOND_HALF: PredictionLifecycleStage.IN_PLAY_2H,
    MatchPeriod.EXTRA_TIME: PredictionLifecycleStage.EXTRA_TIME,
}

# Markets whose selections are one pair per line rather than one set of
# outcomes. They are split into a row per line, because "over 2.5" and
# "over 4.5" are different bets and collapsing them onto one row would publish
# whichever happened to be most likely and silently drop the rest.
# Markets whose selections are lines rather than outcomes, so one model market
# becomes one published row per line. The line has to end up in the market KEY:
# supersede is keyed on (fixture_id, market), so a ladder published under one
# key would have each line supersede the one before it and only the last would
# survive.
_LINE_MARKETS = frozenset(
    {"total_goals_remaining", "total_goals_match", *COUNTING_STATISTICS}
)


@dataclass(frozen=True)
class InPlayRunOutcome:
    """What one fixture's in-play run did, and why."""

    fixture_id: int
    published: tuple[EnginePrediction, ...] = ()
    reason: str = SuppressionReason.NONE.value
    regeneration_reason: str = ""
    match_minute: int | None = None
    period: str | None = None
    fixture_live_snapshot_id: int | None = None
    snapshot_age_seconds: float | None = None
    model_result: InPlayModelResult | None = None
    # Set when a graph snapshot is due for this run, independently of whether any
    # prediction published. A flat passage of play still needs its point on the
    # line, or the graph reads as "the model stopped".
    snapshot_trigger: str | None = None
    snapshot_reason: str = ""
    # Match state as the run saw it. On the outcome rather than read back off the
    # model result, which carries the model's outputs and versions but not the
    # score behind them.
    home_goals: int | None = None
    away_goals: int | None = None
    home_red_cards: int | None = None
    away_red_cards: int | None = None
    elapsed_extra: int | None = None
    raw_status: str = ""
    publication_due: bool = False

    @property
    def suppressed(self) -> bool:
        return not self.published

    def summary(self) -> dict[str, object]:
        return {
            "fixture_id": self.fixture_id,
            "fixture_live_snapshot_id": self.fixture_live_snapshot_id,
            "markets_published": len(self.published),
            "match_minute": self.match_minute,
            "period": self.period,
            "reason": self.reason,
            "regeneration_reason": self.regeneration_reason,
            "snapshot_age_seconds": (
                round(self.snapshot_age_seconds, 3) if self.snapshot_age_seconds is not None else None
            ),
        }


@dataclass
class InPlayRunStats:
    """Aggregate of a scope run, for the CLI summary."""

    fixtures_processed: int = 0
    fixtures_published: int = 0
    predictions_generated: int = 0
    suppressed_by_reason: dict[str, int] = field(default_factory=dict)

    def record(self, outcome: InPlayRunOutcome) -> None:
        self.fixtures_processed += 1
        if outcome.published:
            self.fixtures_published += 1
            self.predictions_generated += len(outcome.published)
            return
        self.suppressed_by_reason[outcome.reason] = self.suppressed_by_reason.get(outcome.reason, 0) + 1


def run_in_play_fixture(
    fixture_id: int,
    *,
    as_of_timestamp: datetime,
    input_repository: object,
    tier: RegenerationTier = RegenerationTier.A,
    config: InPlayModelConfig = DEFAULT_IN_PLAY_CONFIG,
) -> InPlayRunOutcome:
    """Produce (but do not persist) the in-play predictions for one fixture."""
    state = input_repository.load_in_play_state(  # type: ignore[attr-defined]
        fixture_id, as_of_timestamp=as_of_timestamp
    )
    if state is None:
        return InPlayRunOutcome(fixture_id=fixture_id, reason=NO_SNAPSHOT)

    features = assemble_in_play_features(
        snapshot=state.snapshot,
        now=as_of_timestamp,
        home_team_id=state.home_team_id,
        away_team_id=state.away_team_id,
        events=state.events,
    )

    previous = input_repository.load_in_play_cadence_state(fixture_id)  # type: ignore[attr-defined]
    decision = should_regenerate(
        features=features,
        previous=previous,
        now=as_of_timestamp,
        tier=tier,
        config=config,
    )

    # The snapshot cadence is separate and finer: the clients need a point every
    # 60 seconds whether or not anything published. `load_in_play_snapshot_state`
    # is optional so a repository that predates snapshots keeps working -- it then
    # reports no snapshot due rather than crashing the scope.
    snapshot_previous = None
    loader = getattr(input_repository, "load_in_play_snapshot_state", None)
    if loader is not None:
        snapshot_previous = loader(fixture_id)
    snapshot_decision = should_snapshot(
        features=features,
        previous=snapshot_previous,
        now=as_of_timestamp,
    ) if loader is not None else SnapshotDecision(False, SNAPSHOT_TRIGGER_TIMER, "snapshots_unavailable")

    # Only skip the whole fixture when NEITHER is due. This used to return before
    # the model ran, which is why a quiet tick had no result to snapshot.
    if not decision.should_regenerate and not snapshot_decision.should_snapshot:
        return _outcome(
            fixture_id,
            features,
            reason=CADENCE_UNCHANGED,
            regeneration_reason=decision.reason,
            snapshot_reason=snapshot_decision.reason,
        )

    # The pre-match rates the analytic model conditions on come from the
    # existing pipeline rather than from a second derivation. If the pre-match
    # path cannot produce them -- a fixture with no usable history -- there is
    # nothing to condition, and inventing a league-average rate here would be
    # exactly the extrapolation 14 forbids.
    try:
        pre_match = fixture_expected_goals(
            input_repository.load_fixture_inputs(  # type: ignore[attr-defined]
                fixture_id, as_of_timestamp=as_of_timestamp
            )
        )
    except LookupError:
        return _outcome(fixture_id, features, reason=PRE_MATCH_RATES_UNAVAILABLE)

    # Counting-statistic observations, for the corner/shot/foul ladders.
    #
    # Optional on the repository, like the loaders above, so a repository that
    # predates the statistics series keeps working: it then supplies no
    # observations, and the only counting market is cards, which comes free
    # from the event list. That is exactly today's state -- the statistics poll
    # is off until the provider's embedded shape is confirmed -- so this path
    # must degrade to "cards only" rather than to an error.
    count_observations: tuple[CountObservation, ...] = ()
    observations_loader = getattr(input_repository, "load_in_play_count_observations", None)
    if observations_loader is not None:
        count_observations = tuple(
            observations_loader(fixture_id, as_of_timestamp=as_of_timestamp)
        )

    result = run_in_play_model(
        InPlayModelInputs(
            features=features,
            pre_match_home_lambda=pre_match.home_lambda,
            pre_match_away_lambda=pre_match.away_lambda,
            config=config,
            count_observations=count_observations,
            count_priors=_count_priors(pre_match.count_means),
        )
    )
    if result.suppressed:
        # Still snapshot-worthy: a suspended or stale stretch is information a
        # client must be able to render, and §14 wants the suppression visible
        # rather than silent.
        return _outcome(
            fixture_id,
            features,
            reason=result.suppression_reason.value,
            regeneration_reason=decision.reason,
            model_result=result,
            snapshot_trigger=snapshot_decision.trigger if snapshot_decision.should_snapshot else None,
            snapshot_reason=snapshot_decision.reason,
        )

    markets = publishable_markets(result)
    predictions = _predictions_from(
        result=result,
        markets=markets,
        features=features,
        generated_at=as_of_timestamp,
        data_completeness=pre_match.data_completeness,
        regeneration_reason=decision.reason,
    )
    if not predictions:
        return _outcome(
            fixture_id,
            features,
            reason=NO_PUBLISHABLE_MARKETS,
            regeneration_reason=decision.reason,
            model_result=result,
            snapshot_trigger=snapshot_decision.trigger if snapshot_decision.should_snapshot else None,
            snapshot_reason=snapshot_decision.reason,
            publication_due=decision.should_regenerate,
        )
    # `reason` reports the PUBLICATION decision, not the snapshot one. A run that
    # computed the model only because a graph point was due published nothing, and
    # must still say CADENCE_UNCHANGED -- reporting NONE would claim a prediction
    # was published and nothing suppressed it.
    return _outcome(
        fixture_id,
        features,
        reason=SuppressionReason.NONE.value if decision.should_regenerate else CADENCE_UNCHANGED,
        regeneration_reason=decision.reason,
        model_result=result,
        published=tuple(predictions) if decision.should_regenerate else (),
        snapshot_trigger=snapshot_decision.trigger if snapshot_decision.should_snapshot else None,
        snapshot_reason=snapshot_decision.reason,
        publication_due=decision.should_regenerate,
    )


def run_and_save_in_play_fixture(
    fixture_id: int,
    *,
    as_of_timestamp: datetime,
    input_repository: object,
    output_repository: object,
    tier: RegenerationTier = RegenerationTier.A,
    config: InPlayModelConfig = DEFAULT_IN_PLAY_CONFIG,
    snapshot_repository: object | None = None,
) -> InPlayRunOutcome:
    """Run one fixture, publish what is publishable, and record the graph point.

    The two writes are independent by design. A complete prediction set
    publishes on the regeneration cadence, while a snapshot is written on its
    own finer cadence. Either can happen without the other.

    `snapshot_repository` is optional so the pre-snapshot call sites keep working
    unchanged; passing None simply records no graph point.
    """
    outcome = run_in_play_fixture(
        fixture_id,
        as_of_timestamp=as_of_timestamp,
        input_repository=input_repository,
        tier=tier,
        config=config,
    )
    if outcome.publication_due and outcome.model_result is not None and not outcome.model_result.suppressed:
        # A valid empty set hides obsolete lines from the public membership;
        # their rows stay available for settlement. Suppression changes neither.
        output_repository.replace_in_play_predictions(  # type: ignore[attr-defined]
            fixture_id, list(outcome.published), generated_at=as_of_timestamp
        )
    if snapshot_repository is not None and outcome.snapshot_trigger and outcome.model_result is not None:
        _write_snapshot(
            snapshot_repository,
            fixture_id=fixture_id,
            captured_at=as_of_timestamp,
            outcome=outcome,
        )
    return outcome


def _write_snapshot(
    snapshot_repository: object,
    *,
    fixture_id: int,
    captured_at: datetime,
    outcome: InPlayRunOutcome,
) -> None:
    """Record one point on the graph from a completed model run.

    Markets come from `result.markets`, not `publishable_markets`: the graph wants
    every market the model produced, including the ones that have collapsed onto
    certainty. A determined market is a fact a client still draws -- suppressing it
    here would make the line disappear exactly when the outcome became clear.
    """
    result = outcome.model_result
    if result is None:  # pragma: no cover - guarded by the caller
        return
    snapshot_repository.write_snapshot(  # type: ignore[attr-defined]
        fixture_id=fixture_id,
        captured_at=captured_at,
        data_cutoff_at=result.data_cutoff_at,
        trigger=outcome.snapshot_trigger or SNAPSHOT_TRIGGER_TIMER,
        status=outcome.raw_status,
        period=outcome.period,
        elapsed_minute=outcome.match_minute,
        elapsed_extra=outcome.elapsed_extra,
        home_goals=outcome.home_goals,
        away_goals=outcome.away_goals,
        home_red_cards=outcome.home_red_cards,
        away_red_cards=outcome.away_red_cards,
        model_health=model_health_for(result.suppression_reason),
        suppression_reason=(
            None if result.suppression_reason is SuppressionReason.NONE else result.suppression_reason.value
        ),
        markets=snapshot_markets(result.markets),
        model_version=result.model_version,
        feature_version=result.feature_version,
        fixture_live_snapshot_id=result.fixture_live_snapshot_id,
    )


def lifecycle_stage_for(period: MatchPeriod) -> PredictionLifecycleStage:
    """The 7.4 lifecycle stage matching the match period.

    Raises for a period the in-play path must never reach, rather than
    defaulting to PRE_MATCH: a mislabelled row is the leak 7.5 is written to
    prevent, and it would look valid on both surfaces.
    """
    stage = _LIFECYCLE_BY_PERIOD.get(period)
    if stage is None:
        raise ValueError(f"no in-play lifecycle stage for period {period}")
    return stage


def _predictions_from(
    *,
    result: InPlayModelResult,
    markets: dict[str, InPlayMarket],
    features: InPlayFeatures,
    generated_at: datetime,
    data_completeness: float,
    regeneration_reason: str,
) -> list[EnginePrediction]:
    lifecycle_stage = lifecycle_stage_for(features.period)
    clamp = default_config().probability_clamp
    shared_features = _shared_features(result, features, regeneration_reason)
    predictions: list[EnginePrediction] = []
    for market in markets.values():
        for market_key, selection, probability in _rows_for(market):
            if not clamp.minimum <= probability <= clamp.maximum:
                # Outside the publishable band the number is a statement of
                # fact, not a prediction. `EnginePrediction.validate` would
                # reject it; dropping it here keeps one honest market from
                # failing the whole fixture.
                continue
            predictions.append(
                EnginePrediction(
                    fixture_id=features.fixture_id,
                    market=market_key,
                    prediction=selection,
                    probability=probability,
                    confidence=_confidence(probability, features),
                    confidence_tier=_confidence_tier(probability, features),
                    is_best_bet=False,
                    lifecycle_stage=lifecycle_stage,
                    data_completeness=data_completeness,
                    features_used={**shared_features, "market_method": market.method},
                    explanation=_explanation(market_key, selection, probability, features),
                    generated_at=generated_at,
                    gate_tier=_IN_PLAY_GATE_TIER,
                    horizon_eligible=True,
                    embargo_active=False,
                    model_version=IN_PLAY_MODEL_VERSION,
                    max_lifecycle_probability_shift=0.0,
                    prediction_direction_changed=False,
                    lifecycle_stability_score=0.5,
                    # --- 7.4 audit envelope ---
                    fixture_live_snapshot_id=result.fixture_live_snapshot_id,
                    data_cutoff_at=result.data_cutoff_at,
                    match_minute=result.match_minute,
                    period=features.period.value,
                    feature_version=result.feature_version,
                )
            )
    return predictions


def _rows_for(market: InPlayMarket) -> list[tuple[str, str, float]]:
    """One (market, selection, probability) per bet the market contains."""
    if market.market_key not in _LINE_MARKETS:
        selection, probability = max(market.selections.items(), key=lambda item: item[1])
        return [(market.market_key, selection, probability)]

    by_line: dict[str, dict[str, float]] = {}
    for key, probability in market.selections.items():
        side, _, line = key.partition("_")
        if not line:
            continue
        by_line.setdefault(line, {})[side] = probability
    rows: list[tuple[str, str, float]] = []
    for line in sorted(by_line, key=float):
        sides = by_line[line]
        selection, probability = max(sides.items(), key=lambda item: item[1])
        rows.append((f"{market.market_key}_{line}", selection, probability))
    return rows


def _shared_features(
    result: InPlayModelResult, features: InPlayFeatures, regeneration_reason: str
) -> dict[str, object]:
    """The audit payload every row in this run carries.

    The `in_play` block is not decoration: `load_in_play_cadence_state` reads
    the score and card counts back out of it to answer 7.6's "has anything
    happened since?". Storing them makes the cadence survive a worker restart,
    a redeploy, or the job being run from a different host.
    """
    return {
        "in_play": {
            "away_goals": features.away_goals,
            "away_red_cards": features.away_red_cards,
            "data_cutoff_at": features.data_cutoff_at.isoformat(),
            "elapsed_extra": features.elapsed_extra,
            "elapsed_minute": features.elapsed_minute,
            "fallback_flags": list(features.fallback_flags),
            "freshness": features.freshness.value,
            "home_goals": features.home_goals,
            "home_red_cards": features.home_red_cards,
            "model_minute": features.model_minute,
            "period": features.period.value,
            "raw_status": features.raw_status,
            "red_cards_source": features.red_cards_source,
            "regeneration_reason": regeneration_reason,
            "snapshot_age_seconds": round(features.snapshot_age_seconds, 3),
            "snapshot_captured_at": features.snapshot_captured_at.isoformat(),
            "snapshot_id": features.snapshot_id,
        },
        "lambdas": dict(result.lambdas),
        "multipliers": dict(result.multipliers),
        "model_version": result.model_version,
        "feature_version": result.feature_version,
    }


def _confidence(probability: float, features: InPlayFeatures) -> float:
    """How much to trust this number, given how much match is left to model.

    Two things move it and both are about the model's own footing rather than
    the market: how much of the match remains (a minute-88 remaining-goals
    distribution is a near-empty one, and the 1X2 read off it is close to a
    restatement of the score) and whether the feature assembly had to fall
    back. It is deliberately not the pre-match ConfidenceScorer: that one
    weighs history sample sizes and bookmaker agreement, neither of which
    says anything about a match already in progress.
    """
    margin = abs(probability - 0.5) * 2.0
    # Certainty that comes from the clock rather than from the model is not
    # confidence. Late in a match every market tightens; discount for it.
    settled_by_clock = 1.0 - features.remaining_fraction
    score = 0.35 + (0.45 * margin) - (0.15 * settled_by_clock)
    if features.fallback_flags:
        score -= 0.05 * min(len(features.fallback_flags), 3)
    return max(0.0, min(1.0, round(score, 6)))


def _confidence_tier(probability: float, features: InPlayFeatures) -> ConfidenceTier:
    score = _confidence(probability, features)
    if score >= 0.70:
        return ConfidenceTier.STRONG
    if score >= 0.55:
        return ConfidenceTier.MEDIUM
    if score >= 0.40:
        return ConfidenceTier.LOW
    return ConfidenceTier.NONE


def _explanation(market: str, selection: str, probability: float, features: InPlayFeatures) -> str:
    state = f"{features.home_goals}-{features.away_goals} at {features.clock_minute}'"
    cards = ""
    if features.home_red_cards or features.away_red_cards:
        cards = f", {features.home_red_cards}-{features.away_red_cards} on red cards"
    return (
        f"{market}: {selection} at {probability:.1%} from the remaining-goals "
        f"distribution with the match {state}{cards}."
    )


def _outcome(
    fixture_id: int,
    features: InPlayFeatures,
    *,
    reason: str,
    regeneration_reason: str = "",
    model_result: InPlayModelResult | None = None,
    published: tuple[EnginePrediction, ...] = (),
    snapshot_trigger: str | None = None,
    snapshot_reason: str = "",
    publication_due: bool = False,
) -> InPlayRunOutcome:
    return InPlayRunOutcome(
        fixture_id=fixture_id,
        published=published,
        reason=reason,
        regeneration_reason=regeneration_reason,
        match_minute=features.clock_minute,
        period=features.period.value,
        fixture_live_snapshot_id=features.snapshot_id,
        snapshot_age_seconds=features.snapshot_age_seconds,
        model_result=model_result,
        snapshot_trigger=snapshot_trigger,
        snapshot_reason=snapshot_reason,
        home_goals=features.home_goals,
        away_goals=features.away_goals,
        home_red_cards=features.home_red_cards,
        away_red_cards=features.away_red_cards,
        elapsed_extra=features.elapsed_extra,
        raw_status=features.raw_status,
        publication_due=publication_due,
    )


__all__ = [
    "CADENCE_UNCHANGED",
    "NO_PUBLISHABLE_MARKETS",
    "NO_SNAPSHOT",
    "PRE_MATCH_RATES_UNAVAILABLE",
    "CadenceState",
    "InPlayRunOutcome",
    "InPlayRunStats",
    "lifecycle_stage_for",
    "run_and_save_in_play_fixture",
    "run_in_play_fixture",
]


def _count_priors(count_means: dict[str, float]) -> tuple[CountPrior, ...]:
    """Pre-match expectations turned into priors the in-play rate update can use.

    Only the statistics the pre-match path could actually estimate appear. An
    absent prior is not a gap to fill with a league average: `CountPrior`
    exists to weight a known expectation against the match's own rate, and
    inventing the expectation would be the extrapolation the model is forbidden
    to do. Without one, `remaining_count_distribution` declines to publish
    until the match has produced enough play to speak for itself.
    """
    priors: list[CountPrior] = []
    for statistic, mean in count_means.items():
        if statistic not in COUNTING_STATISTICS or mean <= 0.0:
            continue
        priors.append(CountPrior(statistic=statistic, full_match_mean=mean))
    return tuple(priors)
