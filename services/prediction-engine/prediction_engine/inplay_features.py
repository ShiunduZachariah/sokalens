"""Match-state features for the analytic in-play model (architecture v2 §7.2).

This module turns the three live tables into the handful of numbers the
analytic model actually conditions on: how much of the match is left, what the
score is, and how many players each side has lost. It deliberately does not
compute anything that needs snapshot history to calibrate -- there is none yet.

Three things about the real tables drove the shape of this code, and none of
them match the schema printed in the architecture document. They were read off
production on 2026-09-21:

1. `fixture_live_snapshots.home_red_cards` / `away_red_cards` are declared
   nullable and are **NULL in every row** -- ingestion is not populating them.
   Red cards therefore have to be counted from `fixture_events`, with the
   snapshot columns used only as a floor for when ingestion starts filling
   them. See `count_red_cards`.
2. `fixture_events` uses `observed_at` (not `captured_at`) and `identity_key`
   (not `provider_event_key`), and its `event_type` / `event_detail` carry raw
   provider casing: `Goal` / `Normal Goal`, `subst` / `Substitution 1`. All
   matching here is case-insensitive.
3. The same real-world event can appear under two different `identity_key`s
   when the provider revises a minute or a scorer. Fixture 3981689 had its
   20th-minute goal filed twice that way. Goals are therefore taken from the
   snapshot score, which is authoritative, and events are used only for the
   card timeline -- where a structural de-duplication key is applied.

The match clock is not extrapolated here. §6.2 projects a *display* minute from
wall clock; that belongs to the delivery layer. §14 requires that a prediction
on stale data be suppressed rather than extrapolated, so the model reads the
minute the provider last reported and the caller refuses to publish when that
reading is too old.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from enum import StrEnum
from typing import Iterable, Sequence


# Bumped whenever the meaning of a field below changes. Written to
# `engine_predictions.feature_version` (§7.4) so an archived in-play
# prediction can be replayed against the code that produced it.
IN_PLAY_FEATURE_VERSION = "inplay-analytic-v1"

# Regulation length the remaining-time fraction is measured against (§7.2).
REGULATION_MINUTES = 90

# Period ceilings for the provider minute. Stoppage time is reported
# separately in `elapsed_extra` and is deliberately excluded from the modelling
# minute: 45+2 is added time inside the first half, not minute 47 of the match,
# and treating it as minute 47 would silently discount six minutes of football
# that is still to be played.
FIRST_HALF_CEILING_MINUTE = 45
SECOND_HALF_CEILING_MINUTE = REGULATION_MINUTES

# §6.4 staleness tiers, expressed in seconds. FRESH below the first, STALE
# between the two (clock frozen, last prediction kept), EXPIRED beyond the
# second (fixture leaves the in-play surface entirely).
STALE_AFTER_SECONDS = 90.0
EXPIRED_AFTER_SECONDS = 900.0


class MatchPeriod(StrEnum):
    """Normalised period, independent of provider status spelling."""

    PRE_MATCH = "PRE_MATCH"
    FIRST_HALF = "FIRST_HALF"
    HALF_TIME = "HALF_TIME"
    SECOND_HALF = "SECOND_HALF"
    EXTRA_TIME = "EXTRA_TIME"
    PENALTIES = "PENALTIES"
    FINISHED = "FINISHED"
    UNKNOWN = "UNKNOWN"


class Freshness(StrEnum):
    FRESH = "FRESH"
    STALE = "STALE"
    EXPIRED = "EXPIRED"


# Provider status/period strings seen in production plus the rest of the
# upstream vocabulary they are drawn from. Matched case-insensitively after
# trimming, because the engine must not care which provider wrote the row.
_PERIOD_BY_TOKEN: dict[str, MatchPeriod] = {
    "ns": MatchPeriod.PRE_MATCH,
    "tbd": MatchPeriod.PRE_MATCH,
    "scheduled": MatchPeriod.PRE_MATCH,
    "upcoming": MatchPeriod.PRE_MATCH,
    "pre_live": MatchPeriod.PRE_MATCH,
    "1h": MatchPeriod.FIRST_HALF,
    "first_half": MatchPeriod.FIRST_HALF,
    "live": MatchPeriod.FIRST_HALF,
    "ht": MatchPeriod.HALF_TIME,
    "half_time": MatchPeriod.HALF_TIME,
    "halftime": MatchPeriod.HALF_TIME,
    "break": MatchPeriod.HALF_TIME,
    "2h": MatchPeriod.SECOND_HALF,
    "second_half": MatchPeriod.SECOND_HALF,
    "et": MatchPeriod.EXTRA_TIME,
    "extra_time": MatchPeriod.EXTRA_TIME,
    "bt": MatchPeriod.EXTRA_TIME,
    "p": MatchPeriod.PENALTIES,
    "pen": MatchPeriod.PENALTIES,
    "penalties": MatchPeriod.PENALTIES,
    "ft": MatchPeriod.FINISHED,
    "aet": MatchPeriod.FINISHED,
    "finished": MatchPeriod.FINISHED,
    "awarded": MatchPeriod.FINISHED,
    "walkover": MatchPeriod.FINISHED,
    "cancelled": MatchPeriod.FINISHED,
    "canceled": MatchPeriod.FINISHED,
    "postponed": MatchPeriod.FINISHED,
    "abandoned": MatchPeriod.FINISHED,
    "susp": MatchPeriod.UNKNOWN,
    "int": MatchPeriod.UNKNOWN,
    "suspended": MatchPeriod.UNKNOWN,
    "interrupted": MatchPeriod.UNKNOWN,
}

# Periods in which the analytic model has something to say. Extra time and
# penalties are excluded on purpose: every §7.3 launch market settles on the
# 90-minute result, which is already decided once a match reaches extra time.
IN_PLAY_PERIODS = (MatchPeriod.FIRST_HALF, MatchPeriod.HALF_TIME, MatchPeriod.SECOND_HALF)

_RED_CARD_DETAIL_MARKERS = ("red", "second yellow")


@dataclass(frozen=True)
class LiveSnapshot:
    """One row of `fixture_live_snapshots`.

    `home_goals`, `away_goals`, `home_red_cards` and `away_red_cards` are
    optional because the production table declares them nullable and the red
    card columns are, as of 2026-09-21, null in every row.
    """

    fixture_id: int
    captured_at: datetime
    status: str
    period: str | None = None
    elapsed_minute: int | None = None
    elapsed_extra: int | None = None
    home_goals: int | None = None
    away_goals: int | None = None
    home_red_cards: int | None = None
    away_red_cards: int | None = None
    provider: str = ""
    provider_timestamp: datetime | None = None
    snapshot_id: int | None = None


@dataclass(frozen=True)
class LiveEvent:
    """One row of `fixture_events`, with the production column names."""

    fixture_id: int
    observed_at: datetime
    elapsed_minute: int
    event_type: str
    event_detail: str
    team_id: int | None = None
    elapsed_extra: int | None = None
    player_id: int | None = None
    identity_key: str | None = None
    is_retracted: bool = False


@dataclass(frozen=True)
class RedCardCount:
    home: int
    away: int
    source: str


@dataclass(frozen=True)
class CardCount:
    """Every card shown, per side: yellows, seconds and straight reds.

    Separate from RedCardCount because the two answer different questions and
    one cannot be derived from the other. Dismissals change the model's goal
    rates (`rho_red`); total cards is a market in its own right, and a match
    with six yellows and no red has a busy card market and an untouched goal
    model.
    """

    home: int
    away: int
    source: str

    @property
    def total(self) -> int:
        return self.home + self.away


@dataclass(frozen=True)
class InPlayFeatures:
    """Everything the analytic model reads, and nothing it does not."""

    fixture_id: int

    # Audit anchors (§7.4). `data_cutoff_at` is the newest observation that fed
    # the prediction -- the answer to "what did SokaLens know when it published
    # this?" -- and is the value §14 wants on every row.
    data_cutoff_at: datetime
    snapshot_captured_at: datetime
    snapshot_id: int | None
    snapshot_age_seconds: float
    freshness: Freshness

    period: MatchPeriod
    raw_status: str
    elapsed_minute: int
    elapsed_extra: int
    clock_minute: int
    model_minute: int
    remaining_fraction: float

    home_goals: int
    away_goals: int
    goal_difference: int

    home_red_cards: int
    away_red_cards: int
    red_cards_source: str

    fallback_flags: tuple[str, ...]

    # Every card shown, from the same free event list as the dismissals above.
    # Defaulted -- and placed after the required fields, where a default is
    # allowed -- so callers predating the card market (replay manifests,
    # features built by hand in tests) keep working and simply read zero with a
    # source of "none", which the market treats as "not observed".
    home_cards: int = 0
    away_cards: int = 0
    cards_source: str = "none"

    feature_version: str = IN_PLAY_FEATURE_VERSION

    @property
    def is_in_play(self) -> bool:
        return self.period in IN_PLAY_PERIODS

    @property
    def total_goals(self) -> int:
        return self.home_goals + self.away_goals

    @property
    def total_cards(self) -> int:
        return self.home_cards + self.away_cards


def normalize_period(status: str | None, period: str | None = None) -> MatchPeriod:
    """Map a provider status/period pair onto a normalised period.

    `period` wins when both are present and recognised: production writes the
    finer-grained value there, while `status` can lag behind on a half-time
    transition.
    """
    for token in (period, status):
        resolved = _PERIOD_BY_TOKEN.get(_token(token))
        if resolved is not None:
            return resolved
    return MatchPeriod.UNKNOWN


def period_ceiling_minute(period: MatchPeriod) -> int:
    if period is MatchPeriod.FIRST_HALF:
        return FIRST_HALF_CEILING_MINUTE
    if period is MatchPeriod.HALF_TIME:
        return FIRST_HALF_CEILING_MINUTE
    if period is MatchPeriod.PRE_MATCH:
        return 0
    return SECOND_HALF_CEILING_MINUTE


def remaining_fraction(model_minute: int) -> float:
    """f = max(0, (90 - m) / 90), exactly as §7.2 specifies.

    At m = 90 this is zero, which collapses the remaining-goals distribution
    onto "no further goals" and makes every launch market a restatement of the
    current score. That is the document's formula and it is deliberate here;
    stoppage time is not modelled. It is the first thing to revisit once there
    is snapshot history to measure added time against.
    """
    if model_minute < 0:
        raise ValueError("model_minute must be non-negative")
    return max(0.0, (REGULATION_MINUTES - model_minute) / REGULATION_MINUTES)


def count_red_cards(
    events: Iterable[LiveEvent] | None,
    *,
    home_team_id: int,
    away_team_id: int,
    snapshot_home_red_cards: int | None = None,
    snapshot_away_red_cards: int | None = None,
) -> RedCardCount:
    """Count dismissals per side, preferring the event timeline.

    The snapshot columns are used as a floor rather than a source: they are
    null in production today, but they will start carrying values without this
    code changing, and a red card can only ever be added to a match. Taking the
    larger of the two readings is therefore monotone and cannot lose a
    dismissal to either feed lagging behind the other.

    A second yellow is a dismissal. The provider files it as
    `Card` / `Second Yellow card`, which contains no "red", so it is matched
    explicitly.
    """
    event_home = 0
    event_away = 0
    have_events = events is not None
    if events is not None:
        seen: set[tuple[object, ...]] = set()
        for event in events:
            if event.is_retracted or not _is_red_card(event):
                continue
            # Structural de-duplication: the same dismissal can arrive under
            # two identity keys when the provider revises the minute or the
            # player, and identity_key alone will not collapse those.
            key = (
                event.team_id,
                event.elapsed_minute,
                event.elapsed_extra or 0,
                event.player_id,
            )
            if key in seen:
                continue
            seen.add(key)
            if event.team_id == home_team_id:
                event_home += 1
            elif event.team_id == away_team_id:
                event_away += 1

    snapshot_home = max(0, snapshot_home_red_cards or 0)
    snapshot_away = max(0, snapshot_away_red_cards or 0)
    home = max(event_home, snapshot_home)
    away = max(event_away, snapshot_away)

    have_snapshot = snapshot_home_red_cards is not None or snapshot_away_red_cards is not None
    if have_events and have_snapshot:
        source = "events+snapshot"
    elif have_events:
        source = "events"
    elif have_snapshot:
        source = "snapshot"
    else:
        source = "none"
    return RedCardCount(home=home, away=away, source=source)


def count_cards(
    events: Iterable[LiveEvent] | None,
    *,
    home_team_id: int,
    away_team_id: int,
    snapshot_home_cards: int | None = None,
    snapshot_away_cards: int | None = None,
) -> CardCount:
    """Count every card shown per side, preferring the event timeline.

    THIS IS WHY THE CARD MARKET NEEDS NO STATISTICS POLL. Cards are in the
    event list that `fixtures?live=all` already embeds, so the count is free
    and arrives at the same fifteen-second cadence as the score. The statistics
    feed carries the same numbers a minute later; it is used here only as a
    floor, exactly as `count_red_cards` uses the snapshot columns, because a
    card can only ever be added to a match. Taking the larger reading is
    monotone and cannot lose a card to either feed lagging.

    The de-duplication matters more here than for dismissals, because there are
    simply more cards: the provider revises a minute or a player and the same
    booking arrives under a second identity key. Counting it twice would push
    the market over a line nobody crossed.
    """
    event_home = 0
    event_away = 0
    have_events = events is not None
    if events is not None:
        seen: set[tuple[object, ...]] = set()
        for event in events:
            if event.is_retracted or not _is_card(event):
                continue
            key = (
                event.team_id,
                event.elapsed_minute,
                event.elapsed_extra or 0,
                event.player_id,
                _token(event.event_detail),
            )
            if key in seen:
                continue
            seen.add(key)
            if event.team_id == home_team_id:
                event_home += 1
            elif event.team_id == away_team_id:
                event_away += 1

    snapshot_home = max(0, snapshot_home_cards or 0)
    snapshot_away = max(0, snapshot_away_cards or 0)
    home = max(event_home, snapshot_home)
    away = max(event_away, snapshot_away)

    have_snapshot = snapshot_home_cards is not None or snapshot_away_cards is not None
    if have_events and have_snapshot:
        source = "events+snapshot"
    elif have_events:
        source = "events"
    elif have_snapshot:
        source = "snapshot"
    else:
        source = "none"
    return CardCount(home=home, away=away, source=source)


def classify_freshness(age_seconds: float) -> Freshness:
    if age_seconds >= EXPIRED_AFTER_SECONDS:
        return Freshness.EXPIRED
    if age_seconds >= STALE_AFTER_SECONDS:
        return Freshness.STALE
    return Freshness.FRESH


def latest_snapshot(snapshots: Sequence[LiveSnapshot]) -> LiveSnapshot:
    if not snapshots:
        raise ValueError("at least one snapshot is required")
    return max(snapshots, key=lambda snapshot: snapshot.captured_at)


def assemble_in_play_features(
    *,
    snapshot: LiveSnapshot,
    now: datetime,
    home_team_id: int,
    away_team_id: int,
    events: Sequence[LiveEvent] | None = None,
) -> InPlayFeatures:
    """Build the feature vector from the newest snapshot and the event timeline.

    `now` must be timezone-aware, as must every timestamp on the rows: the
    engine has an existing rule against naive timestamps and mixing the two
    raises here rather than producing a silently wrong age.
    """
    _require_aware(now, "now")
    _require_aware(snapshot.captured_at, "snapshot.captured_at")

    flags: list[str] = []
    period = normalize_period(snapshot.status, snapshot.period)
    if period is MatchPeriod.UNKNOWN:
        flags.append("unknown_period")

    age_seconds = (now - snapshot.captured_at).total_seconds()
    if age_seconds < 0:
        # A snapshot from the future is a clock-skew defect, not fresh data.
        flags.append("snapshot_ahead_of_clock")
        age_seconds = 0.0
    freshness = classify_freshness(age_seconds)

    reported_minute = snapshot.elapsed_minute
    if reported_minute is None:
        flags.append("missing_elapsed_minute")
        reported_minute = 0
    elapsed_minute = max(0, int(reported_minute))
    elapsed_extra = max(0, int(snapshot.elapsed_extra or 0))

    ceiling = period_ceiling_minute(period)
    if period is MatchPeriod.HALF_TIME:
        # Half time is minute 45 whatever the provider last reported.
        model_minute = FIRST_HALF_CEILING_MINUTE
    else:
        model_minute = min(elapsed_minute, ceiling) if ceiling else elapsed_minute
    if elapsed_minute > ceiling and ceiling:
        flags.append("elapsed_minute_above_period_ceiling")

    home_goals = snapshot.home_goals
    away_goals = snapshot.away_goals
    if home_goals is None or away_goals is None:
        flags.append("missing_score")
    home_goals = max(0, int(home_goals or 0))
    away_goals = max(0, int(away_goals or 0))

    reds = count_red_cards(
        events,
        home_team_id=home_team_id,
        away_team_id=away_team_id,
        snapshot_home_red_cards=snapshot.home_red_cards,
        snapshot_away_red_cards=snapshot.away_red_cards,
    )
    if reds.source == "none":
        flags.append("red_cards_unknown")

    cards = count_cards(
        events,
        home_team_id=home_team_id,
        away_team_id=away_team_id,
    )
    if cards.source == "none":
        flags.append("cards_unknown")

    data_cutoff_at = snapshot.captured_at
    for event in events or ():
        _require_aware(event.observed_at, "event.observed_at")
        if event.observed_at > data_cutoff_at:
            data_cutoff_at = event.observed_at

    return InPlayFeatures(
        fixture_id=snapshot.fixture_id,
        data_cutoff_at=data_cutoff_at,
        snapshot_captured_at=snapshot.captured_at,
        snapshot_id=snapshot.snapshot_id,
        snapshot_age_seconds=age_seconds,
        freshness=freshness,
        period=period,
        raw_status=snapshot.status,
        elapsed_minute=elapsed_minute,
        elapsed_extra=elapsed_extra,
        clock_minute=elapsed_minute + elapsed_extra,
        model_minute=model_minute,
        remaining_fraction=remaining_fraction(model_minute),
        home_goals=home_goals,
        away_goals=away_goals,
        goal_difference=home_goals - away_goals,
        home_red_cards=reds.home,
        away_red_cards=reds.away,
        red_cards_source=reds.source,
        home_cards=cards.home,
        away_cards=cards.away,
        cards_source=cards.source,
        fallback_flags=tuple(flags),
    )


def _is_red_card(event: LiveEvent) -> bool:
    if _token(event.event_type) != "card":
        return False
    detail = _token(event.event_detail).replace("_", " ")
    return any(marker in detail for marker in _RED_CARD_DETAIL_MARKERS)


def _is_card(event: LiveEvent) -> bool:
    """Any card, whatever colour.

    Deliberately keyed on the event TYPE alone rather than on a list of
    recognised details. A detail this code has never seen -- a wording change,
    a competition-specific label -- would be dropped by a whitelist, and
    dropping a card understates the market. The type is the provider's own
    classification and is the stable part.
    """
    return _token(event.event_type) == "card"


def _token(value: str | None) -> str:
    return (value or "").strip().lower().replace("-", "_")


def _require_aware(value: datetime, name: str) -> None:
    if value.tzinfo is None or value.tzinfo.utcoffset(value) is None:
        raise ValueError(f"{name} must be timezone-aware")
