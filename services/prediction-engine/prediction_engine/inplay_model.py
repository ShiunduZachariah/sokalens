"""The analytic in-play model (architecture v2 §7.1-§7.3, §7.6).

§7.1 is the whole point of this file: *ship analytic first*. There is no
trained model here and no training data behind it -- production only began
writing `fixture_live_snapshots` on 2026-09-21. What there is is Model A's
Poisson/Dixon-Coles machinery conditioned on match state, which derives
analytically from the pre-match rates the existing pipeline already produces
and needs zero snapshot history:

    f        = max(0, (90 - m) / 90)
    lambda'_home = lambda_home * f * rho_red(r_h, r_a) * rho_state(g_h - g_a)
    lambda'_away = lambda_away * f * rho_red(r_a, r_h) * rho_state(g_a - g_h)

The remaining-goals matrix is Poisson(lambda'_home) x Poisson(lambda'_away)
with the existing Dixon-Coles low-score correction, and the launch markets are
read off it shifted by the current score.

`rho_intensity` is **not** applied. §15 decision 5 leaves it disabled until
snapshot data supports fitting it, and §7.2 calls it the most likely of the
three to be wrong. `intensity_multiplier` exists as the seam and returns 1.0;
`INTENSITY_CALIBRATION_ENABLED` is False and there is a test that fails if that
changes without a calibration table behind it.

§14: a prediction on stale data is suppressed, never extrapolated. The model
refuses to produce markets when the newest snapshot is older than the
freshness limit, and every result carries `data_cutoff_at` so a published row
can be audited against what was known at the time.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timedelta
from enum import StrEnum
import math

from prediction_engine.inplay_features import (
    IN_PLAY_FEATURE_VERSION,
    IN_PLAY_PERIODS,
    STALE_AFTER_SECONDS,
    Freshness,
    InPlayFeatures,
    MatchPeriod,
)
from prediction_engine.inplay_counts import (
    CARDS,
    CountObservation,
    CountPrior,
    FULL_MATCH_MINUTES,
    over_probability,
    remaining_count_distribution,
)
from prediction_engine.model_a import Matrix, apply_dixon_coles_correction, poisson_score_matrix


IN_PLAY_MODEL_VERSION = "inplay-analytic-v1"

# §15 decision 5. Do not flip this without a fitted intensity table and the
# backtest that justifies it; `intensity_multiplier` asserts on it.
INTENSITY_CALIBRATION_ENABLED = False

# A probability this close to 0 or 1 is not a prediction, it is a statement of
# fact about a market that is already decided.
_DETERMINED_EPSILON = 1e-9


class SuppressionReason(StrEnum):
    NONE = "NONE"
    STALE_DATA = "STALE_DATA"
    NOT_IN_PLAY = "NOT_IN_PLAY"
    EXTRA_TIME_NOT_MODELLED = "EXTRA_TIME_NOT_MODELLED"
    MISSING_SCORE = "MISSING_SCORE"
    MISSING_PRE_MATCH_RATES = "MISSING_PRE_MATCH_RATES"


@dataclass(frozen=True)
class InPlayModelConfig:
    max_remaining_goals: int = 8
    # Same correlation the pre-match Model A uses. Applied to the *remaining*
    # goals matrix, which is an approximation: Dixon-Coles was fitted on
    # full-match low scores, not on the tail of a match. Kept because §7.2 asks
    # for it and because the alternative -- an uncorrected independent Poisson
    # -- is known to be worse on 0-0 and 1-1.
    dixon_coles_rho: float = -0.08

    # rho_red. §7.2: "red card cuts that team ~25-30%, lifts opponent ~10-15%;
    # scale by remaining time". Midpoints of both ranges.
    red_card_own_reduction: float = 0.275
    red_card_opponent_boost: float = 0.125
    red_card_multiplier_floor: float = 0.20
    red_card_multiplier_ceiling: float = 2.00

    # rho_state. §7.2: "trailing teams push, leading teams sit deep. Modest
    # (+/-10-15%), stronger late." The effect grows from the minimum at kickoff
    # to the maximum at the final whistle, and saturates at a two-goal margin.
    game_state_min_effect: float = 0.05
    game_state_max_effect: float = 0.15
    game_state_goal_saturation: int = 2

    # --- dynamic line ladder ------------------------------------------------
    #
    # Lines are GENERATED from the live count, not taken from a fixed list. A
    # fixed list capped at 5.5 cannot offer over 9.5 in a nine-goal match, which
    # is the case that matters most: by then the over is the clearest read of the
    # game and the only line anyone wants.
    #
    # A line is published when its over-probability is inside the band below --
    # far enough from 0 and 1 to still be a prediction rather than a statement of
    # fact. The band slides with the match on its own: at 0-0 it sits around 0.5
    # to 4.5, and at 9-0 around 9.5 to 12.5 with everything below already won.
    ladder_band_epsilon: float = 0.02
    # The lines closest to even money, so a freak scoreline cannot generate an
    # unbounded row count. Six covers the interesting range at any score.
    ladder_max_lines: int = 6
    # A floor on how many lines are published even when the band is narrow late
    # in a match, so a client always has something to draw.
    ladder_min_lines: int = 2

    # --- material-change filter ----------------------------------------------
    #
    # How far a published probability must move before it is worth a new row.
    #
    # Regeneration is decided per FIXTURE (§7.6): a goal, a card or the timer
    # makes the whole fixture due. That is the right grain for deciding whether
    # to run the model, and the wrong grain for deciding what to write. A ladder
    # market can carry twelve selections, and on a quiet timer tick most of them
    # have not moved enough for a reader to notice -- writing them all produces
    # rows that say the same thing as the rows they supersede.
    #
    # Two percentage points is below what a client renders distinguishably at
    # basis-point precision and well below what a reader would act on.
    #
    # This governs PUBLISHED PREDICTIONS ONLY. Snapshots are unconditional on
    # their own 60-second cadence, because a graph needs a point for every tick
    # whether the numbers moved or not -- a filtered graph has gaps that read as
    # "the model stopped".
    material_change_threshold: float = 0.02

    # §14. Beyond this the newest snapshot no longer describes the match.
    freshness_limit_seconds: float = STALE_AFTER_SECONDS

    # §7.6 timer backstop.
    tier_a_interval: timedelta = timedelta(minutes=2)
    tier_b_interval: timedelta = timedelta(minutes=5)

    def __post_init__(self) -> None:
        problems: list[str] = []
        if self.max_remaining_goals < 1:
            problems.append("max_remaining_goals must be at least 1")
        if not 0.0 <= self.red_card_own_reduction < 1.0:
            problems.append("red_card_own_reduction must be in [0, 1)")
        if self.red_card_opponent_boost < 0.0:
            problems.append("red_card_opponent_boost must be non-negative")
        if not 0.0 < self.red_card_multiplier_floor <= 1.0:
            problems.append("red_card_multiplier_floor must be in (0, 1]")
        if self.red_card_multiplier_ceiling < 1.0:
            problems.append("red_card_multiplier_ceiling must be at least 1")
        if not 0.0 <= self.game_state_min_effect <= self.game_state_max_effect < 1.0:
            problems.append("game_state effects must satisfy 0 <= min <= max < 1")
        if self.game_state_goal_saturation < 1:
            problems.append("game_state_goal_saturation must be at least 1")
        if self.freshness_limit_seconds <= 0:
            problems.append("freshness_limit_seconds must be positive")
        if problems:
            raise ValueError("; ".join(problems))


DEFAULT_IN_PLAY_CONFIG = InPlayModelConfig()


@dataclass(frozen=True)
class InPlayMarket:
    market_key: str
    selections: dict[str, float]
    method: str
    # True when the market has already been settled by the current score (both
    # teams have scored, so BTTS is yes) or by there being no time left. A
    # determined market is a fact, not a prediction, and `publishable_markets`
    # drops it -- `EnginePrediction.validate` rejects probabilities outside
    # [0.01, 0.99] and would reject it anyway.
    is_determined: bool = False


@dataclass(frozen=True)
class InPlayModelInputs:
    features: InPlayFeatures
    pre_match_home_lambda: float
    pre_match_away_lambda: float
    config: InPlayModelConfig = DEFAULT_IN_PLAY_CONFIG

    # Counting statistics other than goals and cards: corners, shots, shots on
    # target, fouls. Empty by default, and empty is the normal case today --
    # they come from the batched statistics poll, which ships switched off until
    # the provider's embedded shape is confirmed.
    #
    # Cards are NOT passed here. They come free from the live event list, which
    # every bulk poll already carries, so the model reads them off the features
    # rather than waiting to be handed them. Routing them through this field
    # would make the one counting market that works today look as though it
    # depended on the poll that is off.
    count_observations: tuple[CountObservation, ...] = ()

    # Prior full-match rates per statistic. Without one, a statistic needs
    # enough elapsed play to infer a rate from the match alone, and declines to
    # publish before that -- see inplay_counts.MIN_MINUTES_WITHOUT_PRIOR.
    count_priors: tuple[CountPrior, ...] = ()

    @property
    def priors_by_statistic(self) -> dict[str, CountPrior]:
        return {prior.statistic: prior for prior in self.count_priors}


@dataclass(frozen=True)
class InPlayModelResult:
    fixture_id: int
    suppressed: bool
    suppression_reason: SuppressionReason
    markets: dict[str, InPlayMarket] = field(default_factory=dict)
    lambdas: dict[str, float] = field(default_factory=dict)
    multipliers: dict[str, float] = field(default_factory=dict)
    remaining_score_matrix: Matrix = field(default_factory=list)
    final_score_distribution: dict[str, float] = field(default_factory=dict)
    # Audit envelope for §7.4's new columns.
    data_cutoff_at: datetime | None = None
    fixture_live_snapshot_id: int | None = None
    match_minute: int | None = None
    period: MatchPeriod | None = None
    feature_version: str = IN_PLAY_FEATURE_VERSION
    model_version: str = IN_PLAY_MODEL_VERSION


def red_card_multiplier(
    own_red_cards: int,
    opponent_red_cards: int,
    remaining_fraction: float,
    config: InPlayModelConfig = DEFAULT_IN_PLAY_CONFIG,
) -> float:
    """rho_red: a dismissal cuts your rate and lifts theirs, scaled by time left.

    Scaling by the remaining fraction is what §7.2 asks for and is why an
    85th-minute red barely moves the number while a 20th-minute one moves it a
    lot -- there is no time for ten men to change the shape of the match. Each
    further dismissal compounds, bounded so two reds can never drive the rate
    to something absurd.
    """
    _validate_counts(own_red_cards, opponent_red_cards)
    fraction = _validated_fraction(remaining_fraction)
    own = (1.0 - config.red_card_own_reduction * fraction) ** own_red_cards
    opponent = (1.0 + config.red_card_opponent_boost * fraction) ** opponent_red_cards
    return _clamp(
        own * opponent,
        config.red_card_multiplier_floor,
        config.red_card_multiplier_ceiling,
    )


def game_state_multiplier(
    goal_difference: int,
    remaining_fraction: float,
    config: InPlayModelConfig = DEFAULT_IN_PLAY_CONFIG,
) -> float:
    """rho_state: trailing teams push, leading teams sit deep, more so late.

    `goal_difference` is from the perspective of the team whose rate is being
    adjusted. The effect is symmetric in sign and saturates at a two-goal
    margin: the difference between three down and four down is not a betting
    signal, it is noise.
    """
    fraction = _validated_fraction(remaining_fraction)
    if goal_difference == 0:
        return 1.0
    lateness = 1.0 - fraction
    strength = config.game_state_min_effect + (
        config.game_state_max_effect - config.game_state_min_effect
    ) * lateness
    magnitude = min(abs(goal_difference), config.game_state_goal_saturation) / config.game_state_goal_saturation
    if goal_difference < 0:
        return 1.0 + strength * magnitude
    return 1.0 - strength * magnitude


def intensity_multiplier(*_args: object, **_kwargs: object) -> float:
    """rho_intensity -- the seam, deliberately inert.

    §15 decision 5 keeps this disabled until there is enough snapshot history
    to fit shot rate against pre-match expectation. Enabling it means supplying
    a fitted table, not flipping the flag.
    """
    if INTENSITY_CALIBRATION_ENABLED:
        raise NotImplementedError(
            "rho_intensity is enabled but no calibration table is fitted (architecture v2 §15.5)"
        )
    return 1.0


def remaining_lambdas(
    features: InPlayFeatures,
    pre_match_home_lambda: float,
    pre_match_away_lambda: float,
    config: InPlayModelConfig = DEFAULT_IN_PLAY_CONFIG,
) -> tuple[float, float, dict[str, float]]:
    """Expected goals for the rest of the match, per §7.2."""
    _validate_lambda(pre_match_home_lambda, "pre_match_home_lambda")
    _validate_lambda(pre_match_away_lambda, "pre_match_away_lambda")
    fraction = features.remaining_fraction

    home_red = red_card_multiplier(
        features.home_red_cards, features.away_red_cards, fraction, config
    )
    away_red = red_card_multiplier(
        features.away_red_cards, features.home_red_cards, fraction, config
    )
    home_state = game_state_multiplier(features.goal_difference, fraction, config)
    away_state = game_state_multiplier(-features.goal_difference, fraction, config)
    intensity = intensity_multiplier()

    home_lambda = pre_match_home_lambda * fraction * home_red * home_state * intensity
    away_lambda = pre_match_away_lambda * fraction * away_red * away_state * intensity
    multipliers = {
        "remaining_fraction": fraction,
        "home_rho_red": home_red,
        "away_rho_red": away_red,
        "home_rho_state": home_state,
        "away_rho_state": away_state,
        "rho_intensity": intensity,
    }
    return home_lambda, away_lambda, multipliers


def suppression_for(
    features: InPlayFeatures,
    config: InPlayModelConfig = DEFAULT_IN_PLAY_CONFIG,
) -> SuppressionReason:
    """Why this fixture must not get a fresh in-play prediction, if it must not."""
    if features.period in (MatchPeriod.EXTRA_TIME, MatchPeriod.PENALTIES):
        return SuppressionReason.EXTRA_TIME_NOT_MODELLED
    if features.period not in IN_PLAY_PERIODS:
        return SuppressionReason.NOT_IN_PLAY
    if features.snapshot_age_seconds >= config.freshness_limit_seconds:
        return SuppressionReason.STALE_DATA
    if features.freshness is not Freshness.FRESH:
        return SuppressionReason.STALE_DATA
    if "missing_score" in features.fallback_flags:
        return SuppressionReason.MISSING_SCORE
    return SuppressionReason.NONE


def run_in_play_model(inputs: InPlayModelInputs) -> InPlayModelResult:
    features = inputs.features
    config = inputs.config

    reason = suppression_for(features, config)
    if reason is not SuppressionReason.NONE:
        return _suppressed(features, reason)
    if inputs.pre_match_home_lambda <= 0.0 and inputs.pre_match_away_lambda <= 0.0:
        return _suppressed(features, SuppressionReason.MISSING_PRE_MATCH_RATES)

    home_lambda, away_lambda, multipliers = remaining_lambdas(
        features, inputs.pre_match_home_lambda, inputs.pre_match_away_lambda, config
    )
    remaining = poisson_score_matrix(home_lambda, away_lambda, config.max_remaining_goals)
    remaining = apply_dixon_coles_correction(remaining, home_lambda, away_lambda, config.dixon_coles_rho)

    markets = {
        market.market_key: market
        for market in (
            _match_result_market(remaining, features),
            _next_goal_market(home_lambda, away_lambda),
            _remaining_total_market(remaining, config),
            _match_total_market(remaining, features, config),
            _btts_market(remaining, features),
        )
    }
    for market in _counting_markets(inputs):
        markets[market.market_key] = market

    return InPlayModelResult(
        fixture_id=features.fixture_id,
        suppressed=False,
        suppression_reason=SuppressionReason.NONE,
        markets=markets,
        lambdas={
            "pre_match_home": inputs.pre_match_home_lambda,
            "pre_match_away": inputs.pre_match_away_lambda,
            "remaining_home": home_lambda,
            "remaining_away": away_lambda,
        },
        multipliers=multipliers,
        remaining_score_matrix=remaining,
        final_score_distribution=_final_score_distribution(remaining, features),
        data_cutoff_at=features.data_cutoff_at,
        fixture_live_snapshot_id=features.snapshot_id,
        match_minute=features.clock_minute,
        period=features.period,
        feature_version=features.feature_version,
    )


def publishable_markets(result: InPlayModelResult) -> dict[str, InPlayMarket]:
    """Markets that are still genuine predictions.

    Drops anything already settled by the current score, and anything whose
    every selection has collapsed onto certainty because no time remains.
    """
    if result.suppressed:
        return {}
    return {key: market for key, market in result.markets.items() if not market.is_determined}



# --- client-facing projections (client data contract) -----------------------

# What a client may do with a snapshot's numbers. Derived from SuppressionReason
# rather than judged separately, so there is exactly one opinion about freshness.
MODEL_HEALTH_OK = "ok"
MODEL_HEALTH_STALE = "stale"
MODEL_HEALTH_SUSPENDED = "suspended"

BASIS_POINT_TOTAL = 10000


def model_health_for(reason: SuppressionReason) -> str:
    """Map a suppression reason onto the health a client can act on.

    `stale` means "this is what we last knew" and must not be rendered as live:
    §14 suppresses on stale data rather than extrapolating. Everything else that
    suppresses means there is nothing to show at all, which is `suspended` -- a
    client showing a stale figure and a client showing no figure are different
    behaviours, and collapsing them would let a suspended fixture look current.
    """
    if reason is SuppressionReason.NONE:
        return MODEL_HEALTH_OK
    if reason is SuppressionReason.STALE_DATA:
        return MODEL_HEALTH_STALE
    return MODEL_HEALTH_SUSPENDED


def _selection_groups(selections: dict[str, float]) -> list[list[str]]:
    """Split a market's selections into mutually exclusive sets.

    Most markets are one set: 1X2's three outcomes sum to 1, and so do BTTS's
    two. A LINE LADDER IS NOT. `total_goals_match` carries an over/under pair per
    line, so ten selections across five lines sum to 5, and normalising them
    together divides every number by five -- which is exactly the defect this
    function exists to prevent. Before it, an `over_1.5` the model put at 81.65%
    went onto the wire as 1633 basis points, or 16.33%.

    Lines are grouped by their suffix, so each over/under pair is normalised on
    its own and every pair sums to the full total. Anything without a parseable
    line is one group, which is the ordinary case.
    """
    lines: dict[str, list[str]] = {}
    plain: list[str] = []
    for name in sorted(selections):
        prefix, _, suffix = name.rpartition("_")
        if prefix in {"over", "under"} and suffix:
            try:
                float(suffix)
            except ValueError:
                plain.append(name)
                continue
            lines.setdefault(suffix, []).append(name)
        else:
            plain.append(name)
    groups: list[list[str]] = []
    if plain:
        groups.append(plain)
    groups.extend(lines[key] for key in sorted(lines, key=float))
    return groups


def basis_points(selections: dict[str, float]) -> dict[str, int]:
    """Selections as integers out of 10,000, per mutually exclusive group.

    Integers so web and the Flutter app cannot round the same market differently.
    Largest remainder rather than per-selection rounding, because rounding each
    independently does not preserve the total: three selections at one third floor
    to 3333 and sum to 9999, and a client rendering 99.99% looks broken.

    Mirrors the Go implementation in
    `services/api/internal/predictions/basispoints`; the two are exercised against
    the same cases so a snapshot and a live payload cannot disagree about the same
    market. Ties break on selection name so the result is deterministic.
    """
    if not selections:
        return {}
    out: dict[str, int] = {}
    for group in _selection_groups(selections):
        # A line group of one is HALF a pair, not a complete market.
        # Apportioning it would scale it to the full total and assert certainty
        # -- turning an over_2.5 the model put at 98% into 100%. Convert it
        # directly instead. A plain group of one is a genuine single-outcome
        # market and does normalise.
        if len(group) == 1 and _is_line_selection(group[0]):
            out[group[0]] = _from_probability(selections[group[0]])
            continue
        out.update(_apportion({name: selections[name] for name in group}))
    return out


def _is_line_selection(name: str) -> bool:
    prefix, _, suffix = name.rpartition("_")
    if prefix not in {"over", "under"} or not suffix:
        return False
    try:
        float(suffix)
    except ValueError:
        return False
    return True


def _from_probability(probability: float) -> int:
    """One probability as basis points, clamped. No normalisation."""
    if probability <= 0:
        return 0
    if probability >= 1:
        return BASIS_POINT_TOTAL
    return int(probability * BASIS_POINT_TOTAL + 0.5)


def _apportion(group: dict[str, float]) -> dict[str, int]:
    """Largest-remainder apportionment of one mutually exclusive set."""
    total = sum(value for value in group.values() if value > 0)
    names = sorted(group)
    if total <= 0:
        return {name: 0 for name in names}

    out: dict[str, int] = {}
    remainders: list[tuple[float, str]] = []
    assigned = 0
    for name in names:
        value = max(0.0, group[name])
        exact = value / total * BASIS_POINT_TOTAL
        floor = int(exact)
        out[name] = floor
        assigned += floor
        remainders.append((exact - floor, name))
    remainders.sort(key=lambda item: (-item[0], item[1]))
    index = 0
    while assigned < BASIS_POINT_TOTAL:
        out[remainders[index % len(remainders)][1]] += 1
        assigned += 1
        index += 1
    return out


def snapshot_markets(markets: dict[str, InPlayMarket]) -> dict[str, dict[str, int]]:
    """The market set as a client receives it: basis points, per market."""
    return {key: basis_points(market.selections) for key, market in markets.items()}

# --- §7.6 cadence -----------------------------------------------------------


class RegenerationTier(StrEnum):
    A = "A"
    B = "B"


@dataclass(frozen=True)
class CadenceState:
    """What the last published in-play prediction saw."""

    generated_at: datetime
    home_goals: int
    away_goals: int
    home_red_cards: int
    away_red_cards: int
    period: MatchPeriod


@dataclass(frozen=True)
class RegenerationDecision:
    should_regenerate: bool
    reason: str


def cadence_state_from(features: InPlayFeatures, generated_at: datetime) -> CadenceState:
    return CadenceState(
        generated_at=generated_at,
        home_goals=features.home_goals,
        away_goals=features.away_goals,
        home_red_cards=features.home_red_cards,
        away_red_cards=features.away_red_cards,
        period=features.period,
    )


def should_regenerate(
    *,
    features: InPlayFeatures,
    previous: CadenceState | None,
    now: datetime,
    tier: RegenerationTier = RegenerationTier.A,
    config: InPlayModelConfig = DEFAULT_IN_PLAY_CONFIG,
) -> RegenerationDecision:
    """§7.6: event-driven first, timer as backstop.

    Regenerate on any goal, any red card, or the half-time transition;
    otherwise every two minutes for Tier A and five for Tier B.
    """
    if previous is None:
        return RegenerationDecision(True, "first_prediction")
    if (features.home_goals, features.away_goals) != (previous.home_goals, previous.away_goals):
        return RegenerationDecision(True, "goal")
    if (features.home_red_cards, features.away_red_cards) != (
        previous.home_red_cards,
        previous.away_red_cards,
    ):
        return RegenerationDecision(True, "red_card")
    if features.period is MatchPeriod.HALF_TIME and previous.period is not MatchPeriod.HALF_TIME:
        return RegenerationDecision(True, "half_time")
    if features.period is not previous.period:
        return RegenerationDecision(True, "period_change")

    interval = config.tier_a_interval if tier is RegenerationTier.A else config.tier_b_interval
    if now - previous.generated_at >= interval:
        return RegenerationDecision(True, "timer")
    return RegenerationDecision(False, "unchanged")



# --- snapshot cadence (client data contract §2) -----------------------------
#
# WHY THIS IS NOT `should_regenerate`. Regeneration decides whether to publish a
# new settleable prediction; snapshots decide whether to record a point on the
# graph. They are different rates on purpose:
#
#   * Publication is expensive -- a row per market, superseded and settled
#     individually -- so it is gated on the match state actually having moved.
#   * A snapshot is one row with a JSONB payload, and the clients need an
#     unconditional point every 60 seconds so a flat passage of play draws as a
#     flat line rather than as a gap.
#
# Gating snapshots on `should_regenerate` would leave the graph blank through
# every quiet two minutes, which reads as "the model stopped" rather than
# "nothing changed".

# The clients' timer. Finer than either regeneration interval.
DEFAULT_SNAPSHOT_INTERVAL = timedelta(seconds=60)


@dataclass(frozen=True)
class SnapshotState:
    """What the last written snapshot recorded."""

    captured_at: datetime
    home_goals: int
    away_goals: int
    home_red_cards: int
    away_red_cards: int
    period: MatchPeriod


@dataclass(frozen=True)
class SnapshotDecision:
    should_snapshot: bool
    # One of the `inplay_snapshots.trigger` values.
    trigger: str
    reason: str


# Trigger vocabulary, matching the CHECK on inplay_snapshots.trigger. The pre_*
# values exist for a writer that has to synthesize a before-state; the runner does
# not, because "just before" is a link to the newest prior snapshot (§2 of the
# contract) rather than a row it invents.
SNAPSHOT_TRIGGER_TIMER = "timer"
SNAPSHOT_TRIGGER_POST_GOAL = "post_goal"
SNAPSHOT_TRIGGER_POST_RED_CARD = "post_red_card"
SNAPSHOT_TRIGGER_PERIOD_CHANGE = "period_change"
SNAPSHOT_TRIGGER_FINAL = "final"


def snapshot_state_from(features: InPlayFeatures, captured_at: datetime) -> SnapshotState:
    return SnapshotState(
        captured_at=captured_at,
        home_goals=features.home_goals,
        away_goals=features.away_goals,
        home_red_cards=features.home_red_cards,
        away_red_cards=features.away_red_cards,
        period=features.period,
    )


def should_snapshot(
    *,
    features: InPlayFeatures,
    previous: SnapshotState | None,
    now: datetime,
    interval: timedelta = DEFAULT_SNAPSHOT_INTERVAL,
) -> SnapshotDecision:
    """Whether to record a point, and which trigger to record it under.

    Event-driven first, timer as backstop -- the same shape as §7.6, at a finer
    rate. A goal, a red card or a period change is recorded immediately and
    labelled, because those are the points a client draws a marker on and shows a
    "before -> after" figure for. Everything else waits for the timer.
    """
    if previous is None:
        return SnapshotDecision(True, SNAPSHOT_TRIGGER_TIMER, "first_snapshot")
    if (features.home_goals, features.away_goals) != (previous.home_goals, previous.away_goals):
        return SnapshotDecision(True, SNAPSHOT_TRIGGER_POST_GOAL, "goal")
    if (features.home_red_cards, features.away_red_cards) != (
        previous.home_red_cards,
        previous.away_red_cards,
    ):
        return SnapshotDecision(True, SNAPSHOT_TRIGGER_POST_RED_CARD, "red_card")
    if features.period is not previous.period:
        return SnapshotDecision(True, SNAPSHOT_TRIGGER_PERIOD_CHANGE, "period_change")
    if now - previous.captured_at >= interval:
        return SnapshotDecision(True, SNAPSHOT_TRIGGER_TIMER, "timer")
    return SnapshotDecision(False, SNAPSHOT_TRIGGER_TIMER, "unchanged")

# --- market derivation ------------------------------------------------------


def _match_result_market(remaining: Matrix, features: InPlayFeatures) -> InPlayMarket:
    """1X2 on the 90-minute result, from the score-shifted remaining matrix."""
    lead = features.goal_difference
    home = _sum_cells(remaining, lambda extra_home, extra_away: extra_home - extra_away + lead > 0)
    draw = _sum_cells(remaining, lambda extra_home, extra_away: extra_home - extra_away + lead == 0)
    away = _sum_cells(remaining, lambda extra_home, extra_away: extra_home - extra_away + lead < 0)
    selections = {"home": home, "draw": draw, "away": away}
    return InPlayMarket(
        market_key="match_result_in_play",
        selections=selections,
        method="poisson_dixon_coles_remaining_shifted",
        is_determined=_is_certain(selections),
    )


def _next_goal_market(home_lambda: float, away_lambda: float) -> InPlayMarket:
    """Which side scores next, from competing homogeneous Poisson processes.

    Read off the rates rather than the matrix: for two independent Poisson
    processes the first arrival is the home side's with probability
    lambda_h / (lambda_h + lambda_a), exactly. The Dixon-Coles correction is
    a low-score adjustment on the joint distribution and has no bearing on
    which process fires first, so it is not applied here.
    """
    total = home_lambda + away_lambda
    if total <= 0.0:
        return InPlayMarket(
            market_key="next_goal",
            selections={"home": 0.0, "away": 0.0, "none": 1.0},
            method="competing_poisson",
            is_determined=True,
        )
    none = math.exp(-total)
    scored = 1.0 - none
    selections = {
        "home": scored * (home_lambda / total),
        "away": scored * (away_lambda / total),
        "none": none,
    }
    return InPlayMarket(
        market_key="next_goal",
        selections=selections,
        method="competing_poisson",
        is_determined=_is_certain(selections),
    )


def is_material_change(
    *,
    probability: float,
    previous: float | None,
    config: InPlayModelConfig = DEFAULT_IN_PLAY_CONFIG,
) -> bool:
    """Whether a published probability has moved enough to be worth a new row.

    `previous` is the probability of the CURRENT published row for the same
    market and selection, or None when nothing is published yet.

    Three cases publish unconditionally, and each has a reason:

      * NOTHING PUBLISHED YET. There is no claim on the record, so silence is not
        "we still think this" -- it is nothing at all.
      * A DIFFERENT SELECTION. The favourite has flipped sides, which is the most
        material thing a line can do even when the number barely moved: 50.5%
        over becoming 49.5% under crosses from one call to the opposite one.
        Handled by the caller, which keys on (market, selection) and so sees no
        previous for the new side.
      * THE MOVE CLEARS THE THRESHOLD.

    Skipping a row is not dropping information. The previous row stays current
    and un-superseded, so it still reads as the standing claim and still settles
    -- it simply carries the earlier `data_cutoff_at`, which is honest: that is
    when we last had reason to change our mind.
    """
    if previous is None:
        return True
    return abs(probability - previous) >= config.material_change_threshold


def generate_ladder(
    *,
    observed: int,
    remaining: dict[int, float],
    config: InPlayModelConfig = DEFAULT_IN_PLAY_CONFIG,
) -> dict[float, float]:
    """The lines worth publishing for a counting market, and their over-probability.

    THE LADDER IS GENERATED, NOT ENUMERATED. Given a count already observed and a
    distribution over what remains, this returns `{line: P(over line)}` for the
    lines that are still genuine predictions. A fixed list cannot do this: capped
    at 5.5 it has nothing to say about a nine-goal match, which is precisely when
    the over is the clearest read of the game.

    Three sets fall out of the arithmetic, and only one of them is published:

      * BELOW the count -- `over_L` for `L < observed` -- is a FACT, already won.
        It is not returned here; settlement closes it (`_settle_line_value`
        parses the line off the prediction string, so any line settles with no
        catalogue and no schema change).
      * ABOVE the reach of the remaining distribution, where P(over) rounds to
        nothing. Publishing it would be noise.
      * The BAND between them, which is what a client draws.

    The band moves with the match without being told to, because `observed`
    enters through the shift: at 0-0 it sits low, and at 9-0 it sits at 9.5 and
    above with everything below settled.

    Generic over the count source. Goals today; cards, corners, shots and fouls
    are the same function with a different distribution, which is the point of
    writing it this way rather than once per market.
    """
    if not remaining:
        return {}

    reachable = max(remaining)
    # Half-integer lines from just above the count to the far end of what the
    # remaining distribution can reach. Below `observed` is already decided, so
    # the ladder starts there rather than at zero.
    candidates = [observed + offset + 0.5 for offset in range(0, reachable + 1)]

    banded: list[tuple[float, float]] = []
    for line in candidates:
        needed = line - observed
        under = sum(probability for total, probability in remaining.items() if total < needed)
        over = 1.0 - under
        if config.ladder_band_epsilon <= over <= 1.0 - config.ladder_band_epsilon:
            banded.append((line, over))

    if not banded:
        # Late in a match every line can fall outside the band -- the next goal
        # is either near-certain or near-impossible. Publishing nothing would
        # blank the market entirely, so the lines closest to even money are kept
        # even though they are lopsided: a 97% over is still a prediction, and a
        # client with no line at all has nothing to draw.
        scored = []
        for line in candidates:
            needed = line - observed
            under = sum(probability for total, probability in remaining.items() if total < needed)
            scored.append((line, 1.0 - under))
        scored.sort(key=lambda item: abs(item[1] - 0.5))
        banded = sorted(scored[: config.ladder_min_lines])

    # Cap by proximity to even money, then restore line order: the closest to a
    # coin flip carry the most information, and a client wants them ascending.
    banded.sort(key=lambda item: abs(item[1] - 0.5))
    kept = banded[: config.ladder_max_lines]
    return {line: over for line, over in sorted(kept)}


def settled_lines(observed: int) -> tuple[float, ...]:
    """Lines already decided by the count, as facts rather than predictions.

    At 9-0 this is 0.5 through 8.5: every one of them won, and none of them a
    prediction any more. Exposed so a surface can show them as settled without
    re-deriving which lines the ladder chose to skip.
    """
    if observed <= 0:
        return ()
    return tuple(line + 0.5 for line in range(observed))


def _remaining_total_market(remaining: Matrix, config: InPlayModelConfig) -> InPlayMarket:
    """Goals still to come. Observed count is zero by definition: it is remaining."""
    distribution = _total_distribution(remaining)
    selections: dict[str, float] = {}
    for line, over in generate_ladder(observed=0, remaining=distribution, config=config).items():
        selections[f"over_{_line_label(line)}"] = over
        selections[f"under_{_line_label(line)}"] = 1.0 - over
    return InPlayMarket(
        market_key="total_goals_remaining",
        selections=selections,
        method="poisson_dixon_coles_remaining_ladder",
        is_determined=not selections or _is_certain(selections),
    )


def _match_total_market(
    remaining: Matrix, features: InPlayFeatures, config: InPlayModelConfig
) -> InPlayMarket:
    """Match-total lines generated around the score, not taken from a list.

    A nine-goal match gets over 9.5 because the ladder starts above the count;
    lines below it are facts rather than predictions and settlement closes them.
    """
    distribution = _total_distribution(remaining)
    selections: dict[str, float] = {}
    for line, over in generate_ladder(
        observed=features.total_goals, remaining=distribution, config=config
    ).items():
        selections[f"over_{_line_label(line)}"] = over
        selections[f"under_{_line_label(line)}"] = 1.0 - over
    return InPlayMarket(
        market_key="total_goals_match",
        selections=selections,
        method="poisson_dixon_coles_remaining_ladder",
        is_determined=not selections or _is_certain(selections),
    )


def _line_label(line: float) -> str:
    """A line as it appears in a prediction string.

    `%g` so 2.5 stays "2.5" and a whole-numbered line would not become "3.0" --
    `_line_from_prediction` parses this back with float(), and the settled
    outcome is rebuilt with the same formatting, so the two must agree exactly or
    a correct prediction grades as wrong.
    """
    return f"{line:g}"


def _btts_market(remaining: Matrix, features: InPlayFeatures) -> InPlayMarket:
    home_scored = features.home_goals > 0
    away_scored = features.away_goals > 0
    if home_scored and away_scored:
        return InPlayMarket(
            market_key="btts_in_play",
            selections={"yes": 1.0, "no": 0.0},
            method="settled_by_current_score",
            is_determined=True,
        )

    no_home_goal = sum(remaining[0])
    no_away_goal = sum(row[0] for row in remaining)
    goalless = remaining[0][0]
    if home_scored:
        yes = 1.0 - no_away_goal
    elif away_scored:
        yes = 1.0 - no_home_goal
    else:
        yes = 1.0 - no_home_goal - no_away_goal + goalless
    yes = _clamp(yes, 0.0, 1.0)
    selections = {"yes": yes, "no": 1.0 - yes}
    return InPlayMarket(
        market_key="btts_in_play",
        selections=selections,
        method="poisson_dixon_coles_remaining_shifted",
        is_determined=_is_certain(selections),
    )


def _final_score_distribution(remaining: Matrix, features: InPlayFeatures) -> dict[str, float]:
    distribution: dict[str, float] = {}
    for extra_home, row in enumerate(remaining):
        for extra_away, probability in enumerate(row):
            key = f"{features.home_goals + extra_home}-{features.away_goals + extra_away}"
            distribution[key] = distribution.get(key, 0.0) + probability
    return distribution


def _total_distribution(matrix: Matrix) -> dict[int, float]:
    distribution: dict[int, float] = {}
    for home, row in enumerate(matrix):
        for away, probability in enumerate(row):
            distribution[home + away] = distribution.get(home + away, 0.0) + probability
    return distribution


def _sum_cells(matrix: Matrix, predicate) -> float:
    return sum(
        probability
        for home, row in enumerate(matrix)
        for away, probability in enumerate(row)
        if predicate(home, away)
    )


def _is_certain(selections: dict[str, float]) -> bool:
    return any(probability >= 1.0 - _DETERMINED_EPSILON for probability in selections.values())


def _suppressed(features: InPlayFeatures, reason: SuppressionReason) -> InPlayModelResult:
    return InPlayModelResult(
        fixture_id=features.fixture_id,
        suppressed=True,
        suppression_reason=reason,
        data_cutoff_at=features.data_cutoff_at,
        fixture_live_snapshot_id=features.snapshot_id,
        match_minute=features.clock_minute,
        period=features.period,
        feature_version=features.feature_version,
    )


def _clamp(value: float, lower: float, upper: float) -> float:
    return max(lower, min(upper, value))


def _validated_fraction(value: float) -> float:
    if not math.isfinite(value) or not 0.0 <= value <= 1.0:
        raise ValueError("remaining_fraction must be between 0 and 1")
    return value


def _validate_counts(own_red_cards: int, opponent_red_cards: int) -> None:
    if own_red_cards < 0 or opponent_red_cards < 0:
        raise ValueError("red card counts must be non-negative")


def _validate_lambda(value: float, name: str) -> None:
    if not math.isfinite(value) or value < 0:
        raise ValueError(f"{name} must be non-negative")


def _counting_markets(inputs: InPlayModelInputs) -> tuple[InPlayMarket, ...]:
    """Ladders for the counting statistics, for whatever is actually observed.

    Cards first and unconditionally, because they cost nothing: the live event
    list carries them, so a card ladder is available on every fixture the model
    runs on. Everything else is published only where an observation was handed
    in, which today means nowhere -- the statistics poll is off until the
    provider's embedded shape is confirmed. That is the correct behaviour, not a
    stub: the contract says a fixture with no corner data shows no corner
    market, and an absent market is the honest rendering of an absent feed.
    """
    features = inputs.features
    priors = inputs.priors_by_statistic

    observations: list[CountObservation] = []
    if features.cards_source != "none":
        observations.append(
            CountObservation(
                statistic=CARDS,
                observed=features.total_cards,
                source=features.cards_source,
            )
        )
    observations.extend(inputs.count_observations)

    markets: list[InPlayMarket] = []
    for observation in observations:
        market = _counting_market(
            observation=observation,
            features=features,
            prior=priors.get(observation.statistic),
            config=inputs.config,
        )
        if market is not None:
            markets.append(market)
    return tuple(markets)


def _counting_market(
    *,
    observation: CountObservation,
    features: InPlayFeatures,
    prior: CountPrior | None,
    config: InPlayModelConfig,
) -> InPlayMarket | None:
    """One statistic's ladder, or None when there is nothing defensible to say.

    None rather than an empty market, so the caller omits the key entirely. An
    empty market would reach the snapshot as a market with no selections, and a
    client cannot tell that apart from a market whose lines all settled.
    """
    remaining_minutes = features.remaining_fraction * FULL_MATCH_MINUTES
    distribution = remaining_count_distribution(
        observed=observation.observed,
        elapsed_minutes=float(features.model_minute),
        remaining_minutes=remaining_minutes,
        prior=prior,
    )
    if not distribution:
        return None

    selections: dict[str, float] = {}
    for line in generate_ladder(
        observed=observation.observed, remaining=distribution, config=config
    ):
        over = over_probability(
            line=line, observed=observation.observed, remaining=distribution
        )
        selections[f"over_{_line_label(line)}"] = over
        selections[f"under_{_line_label(line)}"] = 1.0 - over
    if not selections:
        return None

    return InPlayMarket(
        market_key=observation.statistic,
        selections=selections,
        # The source is in the method string on purpose. "total_cards" published
        # off the event list and off a minute-old statistics payload are the
        # same market with different latency, and an audit of a published row
        # has to be able to tell which one it was.
        method=f"gamma_poisson_count_ladder:{observation.source}",
        is_determined=_is_certain(selections),
    )
