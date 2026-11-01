from __future__ import annotations

from dataclasses import dataclass
from datetime import date, datetime

from prediction_engine.persistence import ConfidenceTier


@dataclass(frozen=True)
class ActualFixtureResult:
    home_goals: int
    away_goals: int
    home_goals_ht: int | None = None
    away_goals_ht: int | None = None
    first_goal_team: str | None = None
    home_corners: int | None = None
    away_corners: int | None = None
    home_card_points: int | None = None
    away_card_points: int | None = None
    home_fouls: int | None = None
    away_fouls: int | None = None
    home_shots_total: int | None = None
    away_shots_total: int | None = None
    home_shots_on_target: int | None = None
    away_shots_on_target: int | None = None
    home_possession: float | None = None
    away_possession: float | None = None
    settled_at: datetime | None = None


@dataclass(frozen=True)
class InPlayAnchor:
    """What was already true when an in-play prediction was published.

    Two in-play markets are *relative* to the moment of publication rather than
    to the fixture as a whole, so the full-time score alone cannot settle them:

      - `total_goals_remaining` counts goals scored after the cutoff.
      - `next_goal` asks which side scored first after the cutoff.

    Both are read from the event timeline against the prediction's own
    `data_cutoff_at`, which every in-play row is required to carry. Grading them
    against the fixture total instead would silently score a 67th-minute
    prediction as though it had been made at kickoff.
    """

    # Score as at the prediction's data cutoff.
    home_goals: int
    away_goals: int
    # Which side scored the first goal strictly after the cutoff. None means no
    # further goal was scored -- a real outcome for `next_goal`, not missing
    # data.
    next_goal_team: str | None = None
    # `fixture_events.identity_key` values for the goals after the cutoff, in
    # order. These are what a settlement on this anchor depended on, and they are
    # recorded on the result so a later retraction can be traced to the grades it
    # invalidated.
    deciding_event_keys: tuple[str, ...] = ()


@dataclass(frozen=True)
class SettlementResult:
    market: str
    predicted: str
    actual: str | None
    settled: bool
    is_correct: bool | None
    status: str
    # Why a void was written. None means the market rule voided it -- a drawn
    # draw-no-bet is a graded outcome, not a failure. A reason is present only
    # when no trustworthy result ever existed.
    void_reason: str | None = None


# The two ways a prediction can run out of time.
#
# RESULT_NEVER_ARRIVED: the fixture was played, or at least started, and no
# confirmed full-time result was ever fetched. The stored score, if there is
# one, is the last live poll -- an in-progress snapshot, not a result.
VOID_RESULT_NEVER_ARRIVED = "result_never_arrived"
# FIXTURE_NOT_PLAYED: the fixture reached a terminal state that is not a
# played-out result (postponed, cancelled, abandoned, awarded, walkover).
# There is nothing left to wait for.
VOID_FIXTURE_NOT_PLAYED = "fixture_not_played"
# SETTLEMENT_UNAVAILABLE: the fixture has a confirmed full-time result and
# settlement still produced nothing. That is a defect, not an absence -- the
# likeliest cause is a market with no entry in `_settlement_outcome`, which
# raises and rolls back the *whole* fixture, stranding every prediction on it.
#
# It is voided anyway, because the alternative is the defect this module
# exists to end: a prediction pending forever. The distinct reason is what
# stops that being silent -- these rows mean "go add the market mapping", not
# "the provider let us down".
VOID_SETTLEMENT_UNAVAILABLE = "settlement_unavailable"


def expire_prediction(market: str, prediction: str, reason: str) -> SettlementResult:
    """Terminate a prediction that will never get a result.

    This is deliberately *not* `settle_prediction` with a fallback score. The
    reaper writes `unknown` precisely because it stopped trusting the feed, so
    the goals on those rows are a mid-match snapshot -- on 2026-09-21 every
    one of the 922 stuck fixtures in production had a last-observed provider
    status of 2H, 1H, HT, P, ET, SUSP, INT or BT, and not one of FT. Grading
    "over 2.5 goals" against a half-time 1-1 would invent a result, which is
    the one thing this product must never do.

    So the outcome is void: no `actual`, no `is_correct`, excluded from
    accuracy, and labelled with why.
    """
    if reason not in {
        VOID_RESULT_NEVER_ARRIVED,
        VOID_FIXTURE_NOT_PLAYED,
        VOID_SETTLEMENT_UNAVAILABLE,
    }:
        raise ValueError(f"unsupported void reason: {reason}")
    return SettlementResult(
        market=market,
        predicted=prediction,
        actual=None,
        settled=False,
        is_correct=None,
        status="void",
        void_reason=reason,
    )


@dataclass(frozen=True)
class SettledPrediction:
    market: str
    confidence_tier: ConfidenceTier
    league_id: int
    is_correct: bool


@dataclass(frozen=True)
class AccuracySnapshot:
    snapshot_date: date
    scope: str
    scope_id: str | None
    total_predictions: int
    correct: int
    accuracy: float


def settle_prediction(
    market: str,
    prediction: str,
    actual: ActualFixtureResult,
    anchor: InPlayAnchor | None = None,
) -> SettlementResult:
    """Grade one prediction against the finished fixture.

    Full time closes every market. A market this function cannot resolve --
    because the provider never supplied the statistic it needs, or because an
    in-play row is missing its anchor -- returns `unsettled_data_unavailable`
    rather than staying pending. That is a closed, auditable state excluded from
    accuracy, and it is the deliberate answer for leagues that carry no corner
    or foul data: publish the market, and close it honestly if it cannot be
    graded.
    """
    outcome = _settlement_outcome(market, prediction, actual, anchor)
    if outcome is None:
        return SettlementResult(market, prediction, None, False, None, "unsettled_data_unavailable")
    actual_outcome, is_correct, status = outcome
    return SettlementResult(
        market=market,
        predicted=prediction,
        actual=actual_outcome,
        settled=True,
        is_correct=is_correct,
        status=status,
    )


# --- reversible settlement (14, operator decision 2026-09-29) ---------------
#
# The provider is the source of truth. If a goal is withdrawn, the settlement it
# decided must be withdrawn too, and the withdrawal must be visible -- however
# long it takes, including after full time. The 2026-09-23 Lesotho U20 fixture
# is the recorded case: a retracted goal on an already finished match.
#
# So settlement is immediate and reversible rather than delayed. An earlier
# draft proposed a 90-120s confirmation window instead; it was rejected because
# it is slow always, still retracts sometimes, and cannot cover a decision that
# arrives after the whistle.

# Why a revision was written. Mirrors prediction_result_revisions.revision_reason.
REVISION_INITIAL = "initial"
REVISION_EVENT_RETRACTED = "event_retracted"
REVISION_RESCORED = "rescored"
REVISION_MANUAL = "manual_correction"

STATUS_REVERSED = "settlement_reversed"


@dataclass(frozen=True)
class Resettlement:
    """The outcome of re-grading a prediction whose evidence moved.

    `changed` is what decides whether anything is written at all. Re-settlement
    runs on every retraction for a fixture, and most predictions on that fixture
    are unaffected -- writing a revision for each of them would bury the ones
    that matter in noise and make `prediction_result_revisions` useless as an
    audit trail.
    """

    changed: bool
    revision_reason: str | None
    result: SettlementResult


def resettle_prediction(
    *,
    previous_status: str,
    previous_actual: str | None,
    previous_is_correct: bool | None,
    recomputed: SettlementResult,
    triggered_by_retraction: bool = False,
) -> Resettlement:
    """Compare a stored settlement against a fresh one and classify the move.

    Three outcomes matter, and they are not the same thing to a user:

      - Unchanged. The retraction touched a different market. Nothing is written.
      - Rescored. The market still settles, but to a different answer -- a goal
        withdrawn from a 3-1 leaves 2-1, and `over_2.5` flips from won to lost.
      - Reversed. The market no longer settles at all, because the evidence it
        was graded on is gone. This is the one that must be *shown*: a user saw
        "won" and it is no longer true.

    A previously unsettleable row that now settles is a `rescored` improvement,
    not a reversal -- it is the retraction making the timeline consistent again.
    """
    was_settled = previous_status == "settled"
    now_settled = recomputed.status == "settled"

    if was_settled and not now_settled:
        # The grade is withdrawn. Carry the recomputed status -- usually
        # unsettled_data_unavailable -- but record it as a reversal so the
        # history says why, and so the UI can show the correction rather than a
        # market quietly going blank.
        return Resettlement(
            changed=True,
            revision_reason=REVISION_EVENT_RETRACTED if triggered_by_retraction else REVISION_RESCORED,
            result=SettlementResult(
                market=recomputed.market,
                predicted=recomputed.predicted,
                actual=None,
                settled=False,
                is_correct=None,
                status=STATUS_REVERSED,
                void_reason=recomputed.void_reason,
            ),
        )

    unchanged = (
        recomputed.status == previous_status
        and recomputed.actual == previous_actual
        and recomputed.is_correct == previous_is_correct
    )
    if unchanged:
        return Resettlement(changed=False, revision_reason=None, result=recomputed)

    return Resettlement(changed=True, revision_reason=REVISION_RESCORED, result=recomputed)


def accuracy_snapshot(
    rows: list[SettledPrediction],
    *,
    snapshot_date: date,
    scope: str,
    scope_id: str | None = None,
) -> AccuracySnapshot:
    total = len(rows)
    correct = sum(1 for row in rows if row.is_correct)
    return AccuracySnapshot(
        snapshot_date=snapshot_date,
        scope=scope,
        scope_id=scope_id,
        total_predictions=total,
        correct=correct,
        accuracy=(correct / total) if total else 0.0,
    )


def _settlement_outcome(
    market: str,
    prediction: str,
    actual: ActualFixtureResult,
    anchor: InPlayAnchor | None = None,
) -> tuple[str, bool | None, str] | None:
    # --- in-play markets (architecture v2 7.4) ------------------------------
    #
    # Three of the five are the same question the pre-match markets ask, just
    # published mid-match, so they settle identically and deliberately reuse the
    # same helpers: a 1X2 called at 67 minutes is still graded on the full-time
    # result. The other two are relative to publication and need the anchor.
    if market == "match_result_in_play":
        outcome = _full_time_result(actual)
        return outcome, prediction == outcome, "settled"
    if market == "btts_in_play":
        outcome = _btts_outcome(actual)
        return outcome, prediction == outcome, "settled"
    if market == "total_goals_match":
        return _settle_line_value(prediction, actual.home_goals + actual.away_goals)
    # A LADDER PUBLISHES ONE MARKET PER LINE, and the line is in the market key.
    #
    # Not a quirk: `supersede_current_predictions` keys on (fixture_id, market),
    # so if every line shared the key `total_goals_match` they would supersede
    # each other and only one line could ever be current. The runner therefore
    # publishes `total_goals_match_9.5` with the side alone as the prediction.
    #
    # Settlement had no arm for that shape, so in-play total-goals rows never
    # graded -- they raised. The ladder made it visible by producing many of them;
    # the gap predates it.
    if market.startswith("total_goals_match_"):
        return _settle_side_against_line(
            market, prediction, actual.home_goals + actual.away_goals
        )
    if market.startswith("total_goals_remaining_"):
        if anchor is None:
            return None
        remaining = max(
            0,
            (actual.home_goals + actual.away_goals) - (anchor.home_goals + anchor.away_goals),
        )
        return _settle_side_against_line(market, prediction, remaining)
    if market == "total_goals_remaining":
        if anchor is None:
            return None
        # Goals after the cutoff. Clamped at zero: a retracted goal can leave the
        # anchor above the final score, and a negative remaining count is not a
        # number this market can be graded on.
        remaining = max(
            0,
            (actual.home_goals + actual.away_goals) - (anchor.home_goals + anchor.away_goals),
        )
        return _settle_line_value(prediction, remaining)
    if market == "next_goal":
        if anchor is None:
            return None
        outcome = anchor.next_goal_team or "none"
        return outcome, prediction == outcome, "settled"

    if market == "1x2":
        outcome = _full_time_result(actual)
        return outcome, prediction == outcome, "settled"
    if market == "double_chance":
        result = _full_time_result(actual)
        winning_selections = {
            "home": {"home_or_draw", "home_or_away"},
            "draw": {"home_or_draw", "draw_or_away"},
            "away": {"draw_or_away", "home_or_away"},
        }[result]
        return result, prediction in winning_selections, "settled"
    if market == "draw_no_bet":
        outcome = _full_time_result(actual)
        if outcome == "draw":
            return outcome, None, "void"
        return outcome, prediction == outcome, "settled"
    if market == "btts":
        outcome = "yes" if actual.home_goals > 0 and actual.away_goals > 0 else "no"
        return outcome, prediction == outcome, "settled"
    if market == "total_goals":
        line = _line_from_prediction(prediction)
        total = actual.home_goals + actual.away_goals
        outcome = f"over_{line:g}" if total > line else f"under_{line:g}"
        return outcome, prediction == outcome, "settled"
    if market == "correct_score":
        outcome = f"{actual.home_goals}-{actual.away_goals}"
        return outcome, prediction == outcome, "settled"
    if market == "half_time_result":
        if actual.home_goals_ht is None or actual.away_goals_ht is None:
            return None
        outcome = _result_from_goals(actual.home_goals_ht, actual.away_goals_ht)
        return outcome, prediction == outcome, "settled"
    if market == "highest_scoring_half":
        if actual.home_goals_ht is None or actual.away_goals_ht is None:
            return None
        first_half = actual.home_goals_ht + actual.away_goals_ht
        second_half = (actual.home_goals - actual.home_goals_ht) + (actual.away_goals - actual.away_goals_ht)
        if first_half > second_half:
            outcome = "first_half"
        elif second_half > first_half:
            outcome = "second_half"
        else:
            outcome = "equal"
        return outcome, prediction == outcome, "settled"
    if market == "team_to_score_first":
        if actual.home_goals == 0 and actual.away_goals == 0:
            outcome = "no_goals"
        elif actual.first_goal_team in {"home", "away"}:
            outcome = actual.first_goal_team
        else:
            return None
        return outcome, prediction == outcome, "settled"
    if market == "odd_even_total_goals":
        outcome = "even" if (actual.home_goals + actual.away_goals) % 2 == 0 else "odd"
        return outcome, prediction == outcome, "settled"
    if market == "btts_total_goals":
        outcome = _btts_total_goals_outcome(actual, _line_from_prediction(prediction))
        return outcome, prediction == outcome, "settled"
    if market == "btts_match_result":
        outcome = f"{_full_time_result(actual)}_{_btts_outcome(actual)}"
        return outcome, prediction == outcome, "settled"
    if market == "result_total_goals":
        line = _line_from_prediction(prediction)
        outcome = f"{_full_time_result(actual)}_{_total_goals_side(actual, line)}_{line:g}"
        return outcome, prediction == outcome, "settled"
    if market in {"corners_total", "total_corners"}:
        return _settle_total_line_market(prediction, actual.home_corners, actual.away_corners)
    if market == "team_corners_home":
        return _settle_team_line_market(prediction, actual.home_corners)
    if market == "team_corners_away":
        return _settle_team_line_market(prediction, actual.away_corners)
    if market == "total_cards":
        return _settle_total_line_market(prediction, actual.home_card_points, actual.away_card_points)
    if market == "total_fouls":
        return _settle_total_line_market(prediction, actual.home_fouls, actual.away_fouls)
    if market == "total_shots":
        return _settle_total_line_market(prediction, actual.home_shots_total, actual.away_shots_total)
    if market == "shots_on_target":
        return _settle_total_line_market(prediction, actual.home_shots_on_target, actual.away_shots_on_target)
    if market == "possession_winner":
        if actual.home_possession is None or actual.away_possession is None:
            return None
        if actual.home_possession > actual.away_possession:
            outcome = "home_more"
        elif actual.home_possession < actual.away_possession:
            outcome = "away_more"
        else:
            outcome = "level"
        return outcome, prediction == outcome, "settled"

    # The in-play counting ladders: `total_cards_4.5`, `total_corners_9.5`,
    # `total_shots_22.5`, `shots_on_target_7.5`, `total_fouls_24.5`.
    #
    # Placed after the unsuffixed arms above and keyed on the prefix, because
    # the ladder has to carry its line in the market key -- supersede is keyed
    # on (fixture_id, market), so one key per ladder would have each line
    # supersede the previous one. Without these arms every published card row
    # would reach full time and RAISE, which is precisely the defect Phase 0
    # was opened to stop: one ungraded market shape aborted the whole
    # settlement batch.
    #
    # A side with no data returns None, which the caller closes as
    # `unsettled_data_unavailable`. That is the contract: full time closes
    # every market, and one that cannot be graded is voided rather than guessed.
    for prefix, (home_value, away_value) in _count_ladder_sources(actual).items():
        if market.startswith(prefix):
            if home_value is None or away_value is None:
                return None
            return _settle_side_against_line(market, prediction, home_value + away_value)

    raise ValueError(f"unsupported settlement market: {market}")


def _count_ladder_sources(
    actual: ActualFixtureResult,
) -> dict[str, tuple[int | None, int | None]]:
    """Which pair of columns each counting ladder grades against.

    Card lines grade on `card_points`, which is cards shown (yellow + red), the
    same unit the in-play model counts from the event list. The column was once
    `yellow*10 + red*25` in one writer while the engine settled 2.5-5.5 lines
    against it, so a single yellow graded "over 5.5" as won; keeping the model
    and this table in one unit is what stops that returning.
    """
    return {
        "total_cards_": (actual.home_card_points, actual.away_card_points),
        "total_corners_": (actual.home_corners, actual.away_corners),
        "shots_on_target_": (actual.home_shots_on_target, actual.away_shots_on_target),
        "total_shots_": (actual.home_shots_total, actual.away_shots_total),
        "total_fouls_": (actual.home_fouls, actual.away_fouls),
    }


def _full_time_result(actual: ActualFixtureResult) -> str:
    return _result_from_goals(actual.home_goals, actual.away_goals)


def _result_from_goals(home_goals: int, away_goals: int) -> str:
    if home_goals > away_goals:
        return "home"
    if home_goals < away_goals:
        return "away"
    return "draw"


def _line_from_prediction(prediction: str) -> float:
    try:
        return float(prediction.rsplit("_", 1)[1])
    except (IndexError, ValueError) as exc:
        raise ValueError(f"prediction does not contain a supported line: {prediction}") from exc


def _btts_outcome(actual: ActualFixtureResult) -> str:
    return "yes" if actual.home_goals > 0 and actual.away_goals > 0 else "no"


def _btts_total_goals_outcome(actual: ActualFixtureResult, line: float) -> str:
    total = actual.home_goals + actual.away_goals
    return f"{_btts_outcome(actual)}_{'over' if total > line else 'under'}_{line:g}"


def _total_goals_side(actual: ActualFixtureResult, line: float) -> str:
    return "over" if actual.home_goals + actual.away_goals > line else "under"


def _settle_total_line_market(
    prediction: str,
    home_value: int | None,
    away_value: int | None,
) -> tuple[str, bool | None, str] | None:
    if home_value is None or away_value is None:
        return None
    return _settle_line_value(prediction, home_value + away_value)


def _settle_team_line_market(
    prediction: str,
    value: int | None,
) -> tuple[str, bool | None, str] | None:
    if value is None:
        return None
    return _settle_line_value(prediction, value)


def _settle_side_against_line(market: str, prediction: str, value: int) -> tuple[str, bool | None, str] | None:
    """Grade an `over`/`under` side against a line carried on the market key.

    The outcome is reported as the SIDE, matching the prediction's vocabulary, so
    a reader comparing predicted against actual sees `over` against `under`
    rather than `over` against `over_9.5`. The line is already in the market
    name; repeating it in the outcome would be noise.
    """
    _, _, suffix = market.rpartition("_")
    try:
        line = float(suffix)
    except ValueError:
        # An unparseable line is a market this function cannot grade. Returning
        # None closes it as unsettled_data_unavailable rather than guessing a
        # line, which would grade a real prediction against a number nobody
        # published.
        return None
    if prediction not in {"over", "under"}:
        return None
    outcome = "over" if value > line else "under"
    return outcome, prediction == outcome, "settled"


def _settle_line_value(prediction: str, value: int) -> tuple[str, bool | None, str]:
    line = _line_from_prediction(prediction)
    outcome = f"over_{line:g}" if value > line else f"under_{line:g}"
    return outcome, prediction == outcome, "settled"
