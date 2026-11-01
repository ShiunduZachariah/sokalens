"""How a pre-match pick is doing, judged server-side (client contract §8).

WHY THE SERVER COMPUTES IT. Web and the Flutter app deriving "is my pick winning"
separately guarantees they disagree eventually -- one of them will round a line
differently, or treat a draw as neutral rather than off-track, and the same pick
will read as winning in one app and losing in the other. It is sent as a value.

WHY THIS REUSES SETTLEMENT RATHER THAN RE-DECIDING. "Is this pick winning" and
"did this pick win" are the same question asked at different moments. Writing a
second set of market rules here would mean twenty-one arms maintained twice, and
the first divergence would show up as a pick that tracked green all match and
settled lost.

So the tracker grades the pick against the CURRENT score as though the match had
ended, through `settle_prediction` itself, and then asks one further question:
can that answer still change?

  * It cannot -> the pick is decided, and the state is terminal. FT over 1.5 with
    two goals at minute 10 is WON, not "on track": nothing in the remaining
    eighty minutes can take it away.
  * It can -> the state is a live judgement. `on_track` means "would win if the
    match ended now".

That definition is worth stating plainly because the obvious alternative --
on_track meaning "is likely to win" -- would make the tracker a second, weaker
prediction rather than a report on the pick already made.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum

from prediction_engine.inplay_counts import COUNTING_STATISTICS
from prediction_engine.settlement import ActualFixtureResult, settle_prediction


class TrackerState(StrEnum):
    """The five states a client renders. Nothing else is emitted."""

    ON_TRACK = "on_track"
    OFF_TRACK = "off_track"
    WON = "won"
    LOST = "lost"
    VOID = "void"
    # A pick whose market cannot be judged from the state we hold. Distinct from
    # VOID, which is a settled outcome: this says "we do not know", and a client
    # shows no tracker rather than a misleading green or red.
    UNKNOWN = "unknown"


# Terminal settlement statuses, mapped onto tracker states. Anything terminal
# comes from settlement and is never re-derived here.
_TERMINAL_FROM_SETTLEMENT = {
    "void": TrackerState.VOID,
    "unsettled_data_unavailable": TrackerState.VOID,
    "settlement_reversed": TrackerState.UNKNOWN,
}


@dataclass(frozen=True)
class MatchState:
    """The live state a tracker judges against."""

    home_goals: int
    away_goals: int
    # True once no further play can change the score. The tracker needs this
    # because most markets are only decided at the whistle -- a 1X2 pick on a
    # one-goal lead is on_track at 80 minutes and won at 90, and the difference
    # is not something the score alone can express.
    finished: bool = False
    home_goals_ht: int | None = None
    away_goals_ht: int | None = None
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

    def as_result(self) -> ActualFixtureResult:
        """The state as settlement sees a finished fixture.

        Handing the live score to `settle_prediction` is the whole trick: it
        answers "what would this pick be graded as if the match ended now" using
        the same rules that will grade it for real.
        """
        return ActualFixtureResult(
            home_goals=self.home_goals,
            away_goals=self.away_goals,
            home_goals_ht=self.home_goals_ht,
            away_goals_ht=self.away_goals_ht,
            home_corners=self.home_corners,
            away_corners=self.away_corners,
            home_card_points=self.home_card_points,
            away_card_points=self.away_card_points,
            home_fouls=self.home_fouls,
            away_fouls=self.away_fouls,
            home_shots_total=self.home_shots_total,
            away_shots_total=self.away_shots_total,
            home_shots_on_target=self.home_shots_on_target,
            away_shots_on_target=self.away_shots_on_target,
            home_possession=self.home_possession,
            away_possession=self.away_possession,
        )


def tracker_state(
    *,
    market: str,
    prediction: str,
    state: MatchState,
    settlement_status: str | None = None,
    settled_is_correct: bool | None = None,
) -> TrackerState:
    """The state of one pick right now.

    `settlement_status` short-circuits everything: once a pick has really been
    settled, the tracker reports that and does not second-guess it from the live
    feed. A reversal reads as UNKNOWN rather than as a grade, because that is
    exactly what a reversal means -- the evidence was withdrawn and the answer is
    not yet back.
    """
    if settlement_status == "settled":
        return TrackerState.WON if settled_is_correct else TrackerState.LOST
    if settlement_status in _TERMINAL_FROM_SETTLEMENT:
        return _TERMINAL_FROM_SETTLEMENT[settlement_status]

    try:
        graded = settle_prediction(market, prediction, state.as_result())
    except ValueError:
        # A market with no settlement arm cannot be tracked either. Saying
        # UNKNOWN is honest; guessing would put a colour on a pick nobody can
        # grade.
        return TrackerState.UNKNOWN
    if graded.status != "settled" or graded.is_correct is None:
        # The statistic this market needs is not in the state we hold -- a
        # corners pick before any corner data arrives. Not a judgement.
        return TrackerState.UNKNOWN

    if state.finished or _is_decided(market, prediction, state):
        return TrackerState.WON if graded.is_correct else TrackerState.LOST
    return TrackerState.ON_TRACK if graded.is_correct else TrackerState.OFF_TRACK


def _is_decided(market: str, prediction: str, state: MatchState) -> bool:
    """Whether the current state already fixes the outcome, whatever follows.

    Only counting markets can be decided early, and only in one direction: a
    count can rise but never fall, so once it passes the line the `over` side is
    permanently won and the `under` side permanently lost. Nothing else is
    decided before the whistle -- a 1X2 lead can be equalised, and BTTS "no" can
    be broken by the last kick.

    BTTS is the one non-line market that decides early, and it decides for BOTH
    sides at once: once each team has scored, "yes" is permanently won and "no"
    is permanently lost. Treating only "yes" as decided would leave a "no" pick
    reading off_track for the rest of a match it can never recover from -- which
    tells a user their pick is behind when it is in fact gone.
    """
    if market in {"btts", "btts_in_play"} and prediction in {"yes", "no"}:
        return state.home_goals > 0 and state.away_goals > 0

    line = _line_for(market, prediction)
    if line is None:
        return False
    count = _count_for(market, state)
    if count is None:
        return False
    # Strictly greater: a count exactly at the line has not passed it, and
    # half-integer lines mean this is only reachable for whole-number lines
    # anyway.
    return count > line


def _line_for(market: str, prediction: str) -> float | None:
    """The line this pick is about, from wherever it is carried.

    Two shapes exist and both are load-bearing. The pre-match markets carry the
    line in the PREDICTION (`total_goals` / `over_2.5`). The in-play ladder
    carries it in the MARKET (`total_goals_match_9.5` / `over`), because
    supersede keys on (fixture_id, market) and lines sharing a key would
    supersede each other.
    """
    if prediction in {"over", "under"}:
        _, _, suffix = market.rpartition("_")
        try:
            return float(suffix)
        except ValueError:
            return None
    side, _, suffix = prediction.partition("_")
    if side not in {"over", "under"} or not suffix:
        return None
    try:
        return float(suffix)
    except ValueError:
        return None


# Which running count a line market is measured against. Absent from this map
# means "not a counting market", so it cannot be decided early.
# Market-key prefixes whose line is carried as a suffix, longest first.
#
# Order matters: `total_goals_match_9.5` must reduce to `total_goals_match`, not
# to `total_goals`, and `shots_on_target_7.5` must not be read as `total_shots`.
# Sorting by length descending makes that a property of the data rather than
# something a future edit can break by inserting in the wrong place.
_COUNT_MARKET_PREFIXES = tuple(
    sorted(
        ("total_goals_match", "total_goals_remaining", "total_goals", *COUNTING_STATISTICS),
        key=len,
        reverse=True,
    )
)


def _count_for(market: str, state: MatchState) -> int | None:
    base = market
    for prefix in _COUNT_MARKET_PREFIXES:
        if market.startswith(prefix):
            base = prefix
            break
    if base in {"total_goals", "total_goals_match"}:
        return state.home_goals + state.away_goals
    if base == "total_goals_remaining":
        # Relative to publication, and the tracker has no anchor. Refusing is
        # right: guessing the anchor would grade against goals the pick never
        # claimed.
        return None
    if base in {"corners_total", "total_corners"}:
        return _pair_sum(state.home_corners, state.away_corners)
    if base == "total_cards":
        return _pair_sum(state.home_card_points, state.away_card_points)
    if base == "total_fouls":
        return _pair_sum(state.home_fouls, state.away_fouls)
    if base == "total_shots":
        return _pair_sum(state.home_shots_total, state.away_shots_total)
    if base == "shots_on_target":
        return _pair_sum(state.home_shots_on_target, state.away_shots_on_target)
    if base == "team_corners_home":
        return state.home_corners
    if base == "team_corners_away":
        return state.away_corners
    return None


def _pair_sum(home: int | None, away: int | None) -> int | None:
    if home is None or away is None:
        return None
    return home + away


__all__ = ["MatchState", "TrackerState", "tracker_state"]
