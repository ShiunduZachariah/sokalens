from __future__ import annotations

from collections import Counter, defaultdict
from dataclasses import dataclass, field
from datetime import UTC, datetime
from typing import Iterable


# Fallback home edge as a goal multiplier, used until a competition has enough
# finished matches to measure its own home/away split.
DEFAULT_HOME_ADVANTAGE = 1.15

_LEAGUE_STRENGTH_KEYS = (
    "home_attack_scale",
    "home_defence_scale",
    "away_attack_scale",
    "away_defence_scale",
)


_TEAM_STRENGTH_KEYS = (
    "home_attack_rating",
    "home_defence_rating",
    "away_attack_rating",
    "away_defence_rating",
    "home_league_goal_rate",
    "away_league_goal_rate",
)


def _neutral_league_strength() -> dict[str, float]:
    """Scales that leave goal rates untouched, for when no rating is known."""
    return {key: 1.0 for key in _LEAGUE_STRENGTH_KEYS}


def _neutral_team_strength() -> dict[str, float]:
    """Ratings that leave goal rates untouched, for when no fit is available.

    The league goal rates are zero rather than one: zero is the sentinel for
    "unknown", and the pipeline falls back to the team's observed average.
    """
    return {
        "home_attack_rating": 1.0,
        "home_defence_rating": 1.0,
        "away_attack_rating": 1.0,
        "away_defence_rating": 1.0,
        "home_league_goal_rate": 0.0,
        "away_league_goal_rate": 0.0,
    }


# Competitions that are not a team's competitive level. A pre-season friendly
# says little about a side's standing, and providers file them all under one
# global bucket that can rival a real league for match count -- Bayern had five
# friendlies against five Bundesliga matches, and the tie handed its baseline to
# the friendlies bucket.
_NON_COMPETITIVE_LEAGUE_MARKERS = ("friendly", "friendlies")

# A league averaging outside this many goals per match is not football; it is
# corrupt data. Production holds a "Super Cup" whose three finished rows average
# 57 goals per match, almost certainly penalty shootout aggregates written into
# the score columns. Left unfiltered it dominates every rating it touches.
MIN_PLAUSIBLE_GOALS_PER_MATCH = 0.5
MAX_PLAUSIBLE_GOALS_PER_MATCH = 6.0

# Finished matches a competition needs before it can define a team's baseline.
MIN_LEAGUE_SAMPLE = 10


def is_competitive_league(league_name: str) -> bool:
    lowered = league_name.lower()
    return not any(marker in lowered for marker in _NON_COMPETITIVE_LEAGUE_MARKERS)


# Confederation competitions: the only fixtures in which teams from two
# different domestic leagues meet on merit. Matched on the organising body's
# name, which is how every provider labels these competitions.
_CONTINENTAL_COMPETITION_MARKERS = (
    "uefa", "conmebol", "concacaf", "caf ", "afc ", "ofc ", "fifa",
    "copa libertadores", "copa sudamericana", "club world cup",
)


def is_continental_competition(league_name: str) -> bool:
    lowered = league_name.lower()
    if not is_competitive_league(lowered):
        return False
    return any(marker in lowered for marker in _CONTINENTAL_COMPETITION_MARKERS)


def plausible_league_goal_rate(records: list[FixtureRecord]) -> bool:
    """Reject a competition whose scorelines are not credible football."""
    finished = [record for record in records if _is_finished(record)]
    if len(finished) < MIN_LEAGUE_SAMPLE:
        return False
    average = sum(_total_goals(record) for record in finished) / len(finished)
    return MIN_PLAUSIBLE_GOALS_PER_MATCH <= average <= MAX_PLAUSIBLE_GOALS_PER_MATCH


def credible_leagues(history: Iterable[FixtureRecord]) -> set[int]:
    """League ids large enough, and sane enough, to rate a team against."""
    by_league: dict[int, list[FixtureRecord]] = defaultdict(list)
    for record in history:
        if is_competitive_league(record.league_name):
            by_league[record.league_id].append(record)
    return {
        league_id
        for league_id, records in by_league.items()
        if plausible_league_goal_rate(records)
    }


def team_primary_leagues(history: Iterable[FixtureRecord]) -> dict[int, int]:
    """Map each team to the league it appears in most often.

    A team's home league is the scale its goal rates are actually measured on,
    so friendlies, one-off cups and competitions with corrupt scorelines are
    excluded before counting. Only when a team has played nothing else does it
    fall back to its full appearance history, which keeps an obscure side
    mapped somewhere rather than dropped.

    Ties break on the lower league id so the mapping is deterministic and
    replays stay reproducible.
    """
    records = list(history)
    eligible = credible_leagues(records)
    appearances: dict[int, Counter[int]] = defaultdict(Counter)
    fallback: dict[int, Counter[int]] = defaultdict(Counter)
    for record in records:
        for team_id in (record.home_team_id, record.away_team_id):
            fallback[team_id][record.league_id] += 1
            if record.league_id in eligible:
                appearances[team_id][record.league_id] += 1

    resolved: dict[int, int] = {}
    for team_id, counts in fallback.items():
        preferred = appearances.get(team_id) or counts
        resolved[team_id] = min(
            preferred.most_common(), key=lambda item: (-item[1], item[0])
        )[0]
    return resolved


@dataclass(frozen=True)
class FixtureRecord:
    id: int
    kickoff_at: datetime
    league_id: int
    league_name: str
    season: int
    home_team_id: int
    away_team_id: int
    home_goals: int | None
    away_goals: int | None
    status: str
    source: str = ""


@dataclass(frozen=True)
class TeamFixtureStats:
    fixture_id: int
    team_id: int
    shots_total: int | None = None
    shots_on_target: int | None = None
    corners: int | None = None
    fouls: int | None = None
    card_points: int | None = None
    possession: float | None = None
    xg: float | None = None


@dataclass(frozen=True)
class OddsSnapshot:
    fixture_id: int
    home_win: float
    draw: float
    away_win: float


@dataclass(frozen=True)
class LeagueBaseline:
    league_id: int
    season: int
    home_goals_per_match: float
    away_goals_per_match: float
    total_goals_per_match: float


@dataclass(frozen=True)
class FeatureVector:
    as_of_timestamp: datetime
    fixture_id: int
    league_id: int
    season: int
    home_team_id: int
    away_team_id: int
    home_recent: dict[str, float]
    away_recent: dict[str, float]
    home_away_splits: dict[str, float]
    h2h: dict[str, float]
    league_baseline: LeagueBaseline
    statistics: dict[str, float | None]
    statistic_sample_sizes: dict[str, int]
    odds_implied_1x2: dict[str, float]
    group_completeness: dict[str, float]
    data_completeness: float
    fallback_flags: tuple[str, ...] = field(default_factory=tuple)
    # Multiplicative scales restating each side's domestic goal rates on a
    # neutral-league scale, plus the competition's measured home edge. These
    # are what stop a weak-league goal counting as a strong-league goal.
    league_strength: dict[str, float] = field(default_factory=_neutral_league_strength)
    home_advantage: float = DEFAULT_HOME_ADVANTAGE
    # Opponent-adjusted attack/defence multipliers for the two sides, relative
    # to their own league. These separate teams inside a single league, which
    # raw goals-per-game cannot do.
    team_strength: dict[str, float] = field(default_factory=_neutral_team_strength)


class FeatureAssembler:
    def __init__(self, form_windows: Iterable[int] = (5, 10), decay_rate: float = 0.85) -> None:
        windows = tuple(form_windows)
        if not windows or any(window <= 0 for window in windows):
            raise ValueError("form_windows must contain positive values")
        if not 0 < decay_rate < 1:
            raise ValueError("decay_rate must be between 0 and 1")
        self._form_windows = windows
        self._decay_rate = decay_rate
        self._baseline_cache: dict[tuple[int, int, datetime], LeagueBaseline] = {}

    def assemble(
        self,
        target: FixtureRecord,
        history: Iterable[FixtureRecord],
        statistics_by_fixture: dict[int, list[TeamFixtureStats]],
        *,
        as_of_timestamp: datetime,
        odds: OddsSnapshot | None = None,
        odds_provisional: bool = False,
        league_strength: dict[str, float] | None = None,
        home_advantage: float | None = None,
        team_strength: dict[str, float] | None = None,
    ) -> FeatureVector:
        as_of = _require_as_of_timestamp(as_of_timestamp)
        prior_fixtures = [
            record
            for record in history
            if record.kickoff_at < target.kickoff_at
            and record.kickoff_at < as_of
            and _is_finished(record)
        ]
        baseline = self.league_baseline(
            target.league_id,
            target.season,
            prior_fixtures,
            as_of_timestamp=as_of,
        )
        neutral_goal_rate = baseline.total_goals_per_match / 2 if baseline.total_goals_per_match > 0 else 1.25
        home_reference_league, away_reference_league = _reference_leagues(
            target,
            team_primary_leagues(prior_fixtures),
        )
        home_recent = self._recent_form(
            target.home_team_id,
            target.season,
            home_reference_league,
            prior_fixtures,
            "home",
            neutral_goal_rate,
        )
        away_recent = self._recent_form(
            target.away_team_id,
            target.season,
            away_reference_league,
            prior_fixtures,
            "away",
            neutral_goal_rate,
        )
        h2h = self._head_to_head(target.home_team_id, target.away_team_id, prior_fixtures)
        home_away_splits = self._home_away_splits(target, prior_fixtures)
        statistics, statistic_sample_sizes, statistics_completeness = self._statistics_summary(
            target.home_team_id,
            target.away_team_id,
            prior_fixtures,
            statistics_by_fixture,
        )
        odds_implied = _implied_1x2(odds) if odds is not None else {}

        flags: list[str] = []
        minimum_sample = max(self._form_windows)
        if home_recent[f"sample_size_{minimum_sample}"] < minimum_sample:
            flags.append("home_recent_sample_insufficient")
        if away_recent[f"sample_size_{minimum_sample}"] < minimum_sample:
            flags.append("away_recent_sample_insufficient")
        if h2h["sample_size"] < 2:
            flags.append("h2h_insufficient")
        if h2h["sample_size"] == 0:
            flags.append("h2h_structurally_unavailable")
        if not statistics:
            flags.append("statistics_missing")
        if not odds_implied:
            flags.append("odds_missing")
        active_strength = league_strength or _neutral_league_strength()
        if abs(active_strength["home_attack_scale"] - active_strength["away_attack_scale"]) >= 0.10:
            flags.append("cross_league_strength_adjustment_applied")
        if odds_provisional:
            flags.append("provisional_odds_display_only")
        if any(record.source in {"football-data.org", "thesportsdb"} for record in prior_fixtures):
            flags.append("supplemental_basic_history")
        if statistics.get("combined_xg") in (None, 0.0):
            flags.append("xg_fallback_used")

        current_sample = min(
            home_recent["current_season_sample_size"],
            away_recent["current_season_sample_size"],
        )
        if current_sample < minimum_sample:
            flags.append("early_season")
        if (
            home_recent["prior_season_sample_size"] > 0
            or away_recent["prior_season_sample_size"] > 0
        ):
            flags.append("prior_season_fallback_used")
        if any(record.season == target.season - 1 and record.league_id != target.league_id for record in prior_fixtures):
            flags.append("tier_adjusted_prior_season_fallback_used")

        # A league baseline is the final rung of the early-season hierarchy.
        # Keep the real sample sizes for auditability, but let a team with no
        # usable N/N-1 history inherit conservative league scoring rates so a
        # broad opening-week slate can still receive Tier-A predictions while
        # team-history backfill continues in the background.
        league_baseline_available = baseline.total_goals_per_match > 0
        home_baseline_fallback = home_recent[f"sample_size_{minimum_sample}"] == 0 and league_baseline_available
        away_baseline_fallback = away_recent[f"sample_size_{minimum_sample}"] == 0 and league_baseline_available
        if home_baseline_fallback:
            flags.append("home_league_baseline_fallback_used")
        if away_baseline_fallback:
            flags.append("away_league_baseline_fallback_used")

        group_completeness = {
            "recent_form": min(
                max(
                    0.60 if home_baseline_fallback and away_baseline_fallback else 0.30 if home_baseline_fallback or away_baseline_fallback else 0.0,
                    (
                        home_recent[f"sample_size_{minimum_sample}"]
                        + away_recent[f"sample_size_{minimum_sample}"]
                    )
                    / (minimum_sample * 2),
                ),
                1.0,
            ),
            "home_away_splits": home_away_splits["sample_completeness"],
            "h2h": min(h2h["sample_size"] / 2, 1.0),
            "statistics": statistics_completeness,
            "odds": 1.0 if odds_implied and not odds_provisional else 0.0,
            "league_baseline": 1.0 if baseline.total_goals_per_match > 0 else 0.0,
        }
        weights = {
            "recent_form": 0.35,
            "home_away_splits": 0.15,
            "h2h": 0.05,
            "statistics": 0.25,
            "odds": 0.05,
            "league_baseline": 0.15,
        }
        if h2h["sample_size"] == 0:
            weights.pop("h2h")
        active_weight = sum(weights.values())
        data_completeness = sum(
            group_completeness[group] * weight / active_weight
            for group, weight in weights.items()
        )

        return FeatureVector(
            as_of_timestamp=as_of,
            fixture_id=target.id,
            league_id=target.league_id,
            season=target.season,
            home_team_id=target.home_team_id,
            away_team_id=target.away_team_id,
            home_recent=home_recent,
            away_recent=away_recent,
            home_away_splits=home_away_splits,
            h2h=h2h,
            league_baseline=baseline,
            statistics=statistics,
            statistic_sample_sizes=statistic_sample_sizes,
            odds_implied_1x2=odds_implied,
            group_completeness=group_completeness,
            data_completeness=data_completeness,
            fallback_flags=tuple(flags),
            league_strength=active_strength,
            home_advantage=home_advantage if home_advantage is not None else DEFAULT_HOME_ADVANTAGE,
            team_strength=team_strength or _neutral_team_strength(),
        )

    def league_baseline(
        self,
        league_id: int,
        season: int,
        history: Iterable[FixtureRecord],
        *,
        as_of_timestamp: datetime,
    ) -> LeagueBaseline:
        as_of = _require_as_of_timestamp(as_of_timestamp)
        key = (league_id, season, as_of)
        cached = self._baseline_cache.get(key)
        if cached is not None:
            return cached

        fixtures = [
            record
            for record in history
            if record.league_id == league_id
            and record.season == season
            and record.kickoff_at < as_of
            and _is_finished(record)
        ]
        if not fixtures:
            fixtures = [
                record
                for record in history
                if record.league_id == league_id
                and record.season == season - 1
                and record.kickoff_at < as_of
                and _is_finished(record)
            ]
        if not fixtures:
            baseline = LeagueBaseline(league_id, season, 0.0, 0.0, 0.0)
            self._baseline_cache[key] = baseline
            return baseline

        home_average = sum(record.home_goals or 0 for record in fixtures) / len(fixtures)
        away_average = sum(record.away_goals or 0 for record in fixtures) / len(fixtures)
        baseline = LeagueBaseline(
            league_id=league_id,
            season=season,
            home_goals_per_match=home_average,
            away_goals_per_match=away_average,
            total_goals_per_match=home_average + away_average,
        )
        self._baseline_cache[key] = baseline
        return baseline

    def _recent_form(
        self,
        team_id: int,
        target_season: int,
        reference_league_id: int,
        history: list[FixtureRecord],
        venue_label: str,
        neutral_goal_rate: float,
    ) -> dict[str, float]:
        team_fixtures = sorted(
            [record for record in history if record.home_team_id == team_id or record.away_team_id == team_id],
            key=lambda record: record.kickoff_at,
            reverse=True,
        )
        current_season = [record for record in team_fixtures if record.season == target_season]
        prior_season = [record for record in team_fixtures if record.season == target_season - 1]
        ordered_history = [*current_season, *prior_season]
        values: dict[str, float] = {
            "current_season_sample_size": float(len(current_season)),
            "prior_season_sample_size": float(len(prior_season)),
        }
        for window in self._form_windows:
            sample = ordered_history[:window]
            points = [_tier_adjusted_value(record, _points_for(record, team_id), reference_league_id, 1.0) for record in sample]
            goals_for = [_tier_adjusted_value(record, _goals_for(record, team_id), reference_league_id, neutral_goal_rate) for record in sample]
            goals_against = [_tier_adjusted_value(record, _goals_against(record, team_id), reference_league_id, neutral_goal_rate) for record in sample]
            values[f"sample_size_{window}"] = float(len(sample))
            values[f"points_per_match_{window}"] = _average(points)
            values[f"goals_for_per_match_{window}"] = _average(goals_for)
            values[f"goals_against_per_match_{window}"] = _average(goals_against)
            values[f"btts_rate_{window}"] = _average([_tier_adjusted_value(record, 1.0 if _btts(record) else 0.0, reference_league_id, 0.5) for record in sample])
            values[f"over_2_5_rate_{window}"] = _average([_tier_adjusted_value(record, 1.0 if _total_goals(record) > 2 else 0.0, reference_league_id, 0.5) for record in sample])
            values[f"decayed_points_per_match_{window}"] = _decayed_average(points, self._decay_rate)
        values["venue_is_home"] = 1.0 if venue_label == "home" else 0.0
        return values

    def _head_to_head(self, home_team_id: int, away_team_id: int, history: list[FixtureRecord]) -> dict[str, float]:
        fixtures = [
            record
            for record in history
            if {record.home_team_id, record.away_team_id} == {home_team_id, away_team_id}
        ]
        if not fixtures:
            return {"sample_size": 0.0, "avg_total_goals": 0.0, "home_points_per_match": 0.0}
        return {
            "sample_size": float(len(fixtures)),
            "avg_total_goals": _average([float(_total_goals(record)) for record in fixtures]),
            "home_points_per_match": _average([float(_points_for(record, home_team_id)) for record in fixtures]),
        }

    def _home_away_splits(self, target: FixtureRecord, history: list[FixtureRecord]) -> dict[str, float]:
        home_samples = [record for record in history if record.home_team_id == target.home_team_id]
        away_samples = [record for record in history if record.away_team_id == target.away_team_id]
        denominator = max(self._form_windows)
        return {
            "home_goals_for": _average([float(record.home_goals or 0) for record in home_samples]),
            "home_goals_against": _average([float(record.away_goals or 0) for record in home_samples]),
            "away_goals_for": _average([float(record.away_goals or 0) for record in away_samples]),
            "away_goals_against": _average([float(record.home_goals or 0) for record in away_samples]),
            "sample_completeness": min((len(home_samples) + len(away_samples)) / (denominator * 2), 1.0),
        }

    def _statistics_summary(
        self,
        home_team_id: int,
        away_team_id: int,
        history: list[FixtureRecord],
        statistics_by_fixture: dict[int, list[TeamFixtureStats]],
    ) -> tuple[dict[str, float | None], dict[str, int], float]:
        home_stats: list[TeamFixtureStats] = []
        away_stats: list[TeamFixtureStats] = []
        for record in history:
            for stats in statistics_by_fixture.get(record.id, []):
                if stats.team_id == home_team_id:
                    home_stats.append(stats)
                if stats.team_id == away_team_id:
                    away_stats.append(stats)
        if not any(_has_statistic_value(stats) for stats in [*home_stats, *away_stats]):
            return {}, {}, 0.0

        statistics = {
            "home_corners_for": _average_optional([stats.corners for stats in home_stats]),
            "away_corners_for": _average_optional([stats.corners for stats in away_stats]),
            "home_shots_total": _average_optional([stats.shots_total for stats in home_stats]),
            "away_shots_total": _average_optional([stats.shots_total for stats in away_stats]),
            "home_shots_on_target": _average_optional([stats.shots_on_target for stats in home_stats]),
            "away_shots_on_target": _average_optional([stats.shots_on_target for stats in away_stats]),
            "home_fouls": _average_optional([stats.fouls for stats in home_stats]),
            "away_fouls": _average_optional([stats.fouls for stats in away_stats]),
            "home_card_points": _average_optional([stats.card_points for stats in home_stats]),
            "away_card_points": _average_optional([stats.card_points for stats in away_stats]),
            "home_possession": _average_optional([stats.possession for stats in home_stats]),
            "away_possession": _average_optional([stats.possession for stats in away_stats]),
            "combined_xg": _average_optional([stats.xg for stats in [*home_stats, *away_stats]]),
        }
        sample_sizes = {
            "home_corners_for": _present_count(stats.corners for stats in home_stats),
            "away_corners_for": _present_count(stats.corners for stats in away_stats),
            "home_shots_total": _present_count(stats.shots_total for stats in home_stats),
            "away_shots_total": _present_count(stats.shots_total for stats in away_stats),
            "home_shots_on_target": _present_count(stats.shots_on_target for stats in home_stats),
            "away_shots_on_target": _present_count(stats.shots_on_target for stats in away_stats),
            "home_fouls": _present_count(stats.fouls for stats in home_stats),
            "away_fouls": _present_count(stats.fouls for stats in away_stats),
            "home_card_points": _present_count(stats.card_points for stats in home_stats),
            "away_card_points": _present_count(stats.card_points for stats in away_stats),
            "home_possession": _present_count(stats.possession for stats in home_stats),
            "away_possession": _present_count(stats.possession for stats in away_stats),
            "home_xg": _present_count(stats.xg for stats in home_stats),
            "away_xg": _present_count(stats.xg for stats in away_stats),
        }
        home_fixture_count = sum(
            record.home_team_id == home_team_id or record.away_team_id == home_team_id
            for record in history
        )
        away_fixture_count = sum(
            record.home_team_id == away_team_id or record.away_team_id == away_team_id
            for record in history
        )
        completeness = _statistics_completeness(
            sample_sizes,
            home_fixture_count=home_fixture_count,
            away_fixture_count=away_fixture_count,
        )
        return statistics, sample_sizes, completeness


def _require_as_of_timestamp(value: datetime) -> datetime:
    if value.tzinfo is None or value.utcoffset() is None:
        raise ValueError("as_of_timestamp must be timezone-aware")
    return value.astimezone(UTC)


def _implied_1x2(odds: OddsSnapshot) -> dict[str, float]:
    if odds.home_win <= 1 or odds.draw <= 1 or odds.away_win <= 1:
        # Providers sometimes publish 0/1 placeholders while a market is
        # forming. Odds are optional, so ignore that snapshot instead of
        # blocking every prediction market for the fixture.
        return {}
    raw = {
        "home": 1 / odds.home_win,
        "draw": 1 / odds.draw,
        "away": 1 / odds.away_win,
    }
    total = sum(raw.values())
    return {key: value / total for key, value in raw.items()}


def _is_finished(record: FixtureRecord) -> bool:
    normalized_status = record.status.strip().lower().replace("_", " ").replace("-", " ")
    return record.home_goals is not None and record.away_goals is not None and normalized_status in {
        "ft",
        "aet",
        "pen",
        "finished",
        "match finished",
        "full time",
        "fulltime",
    }


def _points_for(record: FixtureRecord, team_id: int) -> int:
    goals_for = _goals_for(record, team_id)
    goals_against = _goals_against(record, team_id)
    if goals_for > goals_against:
        return 3
    if goals_for == goals_against:
        return 1
    return 0


def _goals_for(record: FixtureRecord, team_id: int) -> int:
    if record.home_team_id == team_id:
        return int(record.home_goals or 0)
    if record.away_team_id == team_id:
        return int(record.away_goals or 0)
    raise ValueError("team did not play fixture")


def _goals_against(record: FixtureRecord, team_id: int) -> int:
    if record.home_team_id == team_id:
        return int(record.away_goals or 0)
    if record.away_team_id == team_id:
        return int(record.home_goals or 0)
    raise ValueError("team did not play fixture")


def _total_goals(record: FixtureRecord) -> int:
    return int(record.home_goals or 0) + int(record.away_goals or 0)


def _btts(record: FixtureRecord) -> bool:
    return int(record.home_goals or 0) > 0 and int(record.away_goals or 0) > 0


def _reference_leagues(target: FixtureRecord, primary_leagues: dict[int, int]) -> tuple[int, int]:
    """Pick the league each side's form should be judged against.

    Two different situations look alike from a single fixture row, and the
    engine has to tell them apart:

    * A promoted or relegated side playing its new league. Its old results are
      from a different standard and should be shrunk toward a neutral prior.
    * An established side playing a continental tie. Its domestic record is its
      true level and must not be shrunk, or the gap between a heavyweight and a
      small-league opponent is flattened away.

    The tell is whether the two sides come from the same league. When their home
    leagues differ, the fixture is cross-league and each side is judged against
    its own league. Otherwise the competition is the shared frame of reference,
    which preserves the promotion/relegation shrink.
    """
    home_primary = primary_leagues.get(target.home_team_id)
    away_primary = primary_leagues.get(target.away_team_id)
    if home_primary is not None and away_primary is not None and home_primary != away_primary:
        return home_primary, away_primary
    return target.league_id, target.league_id


def _tier_adjusted_value(record: FixtureRecord, value: float, reference_league_id: int, neutral: float) -> float:
    """Conservatively shrink form played outside the team's own league.

    A promoted/relegated team's old results are still useful, but treating a
    Championship result as identical Premier League evidence overstates the
    signal. Pull those observations 25% toward a neutral prior; results from
    the team's own league remain untouched.

    The reference is the team's *home* league, not the competition being
    predicted. Comparing against the target fixture's league meant that for
    every continental tie both sides' entire domestic records counted as
    off-tier evidence and were shrunk toward the same neutral rate, flattening
    precisely the gap between a heavyweight and a small-league side that the
    prediction turns on.
    """
    if record.league_id == reference_league_id:
        return float(value)
    return (float(value) * 0.75) + (neutral * 0.25)


def _average(values: list[float] | list[int]) -> float:
    if not values:
        return 0.0
    return float(sum(values) / len(values))


def _average_optional(values: Iterable[float | int | None]) -> float | None:
    present = [float(value) for value in values if value is not None]
    return _average(present) if present else None


def _present_count(values: Iterable[float | int | None]) -> int:
    return sum(value is not None for value in values)


def _statistics_completeness(
    sample_sizes: dict[str, int],
    *,
    home_fixture_count: int,
    away_fixture_count: int,
) -> float:
    expected_samples = home_fixture_count + away_fixture_count
    if expected_samples == 0:
        return 0.0

    metric_keys = (
        ("home_shots_total", "away_shots_total"),
        ("home_shots_on_target", "away_shots_on_target"),
        ("home_corners_for", "away_corners_for"),
        ("home_fouls", "away_fouls"),
        ("home_card_points", "away_card_points"),
        ("home_possession", "away_possession"),
        ("home_xg", "away_xg"),
    )
    metric_completeness = [
        min(
            (sample_sizes[home_key] + sample_sizes[away_key]) / expected_samples,
            1.0,
        )
        for home_key, away_key in metric_keys
    ]
    return _average(metric_completeness)


def _has_statistic_value(stats: TeamFixtureStats) -> bool:
    return any(
        value is not None
        for value in (
            stats.shots_total,
            stats.shots_on_target,
            stats.corners,
            stats.fouls,
            stats.card_points,
            stats.possession,
            stats.xg,
        )
    )


def _decayed_average(values: list[float] | list[int], decay_rate: float) -> float:
    if not values:
        return 0.0
    weights = [decay_rate**index for index in range(len(values))]
    return sum(value * weight for value, weight in zip(values, weights, strict=True)) / sum(weights)
