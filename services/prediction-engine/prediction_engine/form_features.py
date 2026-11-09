"""Point-in-time tabular features for architecture v2 Model C (section 8.3).

Model C is a trained model, so the one property that decides whether it can be
trusted is that the features it is trained on are the features it is served.
This module is the only place they are computed: `model_lab.model_c_train`
drives a `FormFeatureBook` through the historical corpus to build training
rows, and `prediction_engine.challengers` drives the same class through the
production fixture history to build serving rows. There is no second
implementation to drift.

The book follows the `EloRatingBook` contract: feed finished fixtures in
kick-off order through `record`, and ask `features` for a fixture before it is
recorded. A fixture's own result therefore never reaches its own features
(section 3.4), by construction rather than by a filter someone has to
remember.

**What is deliberately not here.**

- *Bookmaker odds.* A model that reads the closing price learns to repeat the
  market, and the champion already anchors half its 1X2 to odds. Model C's
  value to the ensemble (section 8.5) is a third view built from the matches
  themselves.
- *Elo ratings.* They would be the strongest single feature, and including
  them would make Models B and C agree because one reads the other, which is
  the circularity section 8.6 removed from v1.
- *Match statistics, lineups and player strength.* The historical corpus has
  no lineups and production has statistics for only part of its history; a
  feature present in training and absent in serving is worse than none.

A value that cannot be known yet (a team's first match, a league's first
fixture) is `None`, never a guessed zero. The tree model learns a direction
for missing values from data, which a fabricated zero would corrupt.
"""

from __future__ import annotations

from collections import defaultdict, deque
from dataclasses import dataclass
from datetime import datetime
from typing import Deque

from prediction_engine.features import FixtureRecord, _is_finished, is_competitive_league


FEATURE_VERSION = "form-features-v1"

# Rest days beyond this carry no extra information and are mostly off-season.
_MAX_REST_DAYS = 30.0
# Matches kept per team; the longest window below.
_HISTORY = 10
# Per-league outcome counts decay by this factor per recorded match, so a
# league's base rates follow it across seasons without a hard window edge.
_LEAGUE_DECAY = 0.998
# Prior strength for the league base rates, in matches.
_LEAGUE_PRIOR_MATCHES = 50.0
_LEAGUE_PRIOR = {"home": 0.45, "draw": 0.26, "goals": 2.7}

_SIDE_FEATURES = (
    "ppg_5",
    "goals_for_5",
    "goals_against_5",
    "ppg_10",
    "goal_difference_10",
    "venue_ppg_10",
    "season_ppg",
    "season_goals_for",
    "season_goals_against",
    "season_played",
    "rest_days",
    "known_matches",
)

FEATURE_NAMES: tuple[str, ...] = (
    *(f"home_{name}" for name in _SIDE_FEATURES),
    *(f"away_{name}" for name in _SIDE_FEATURES),
    "ppg_5_difference",
    "ppg_10_difference",
    "goal_difference_10_difference",
    "season_ppg_difference",
    "league_home_win_rate",
    "league_draw_rate",
    "league_goals_per_match",
)


@dataclass(frozen=True)
class _Played:
    kickoff_at: datetime
    at_home: bool
    goals_for: int
    goals_against: int

    @property
    def points(self) -> int:
        if self.goals_for > self.goals_against:
            return 3
        return 1 if self.goals_for == self.goals_against else 0


class FormFeatureBook:
    """Team form and league base rates as they stand after the fixtures recorded."""

    def __init__(self) -> None:
        self._recent: dict[int, Deque[_Played]] = defaultdict(lambda: deque(maxlen=_HISTORY))
        self._season: dict[tuple[int, int], list[int]] = defaultdict(lambda: [0, 0, 0, 0])
        self._known: dict[int, int] = defaultdict(int)
        self._last_kickoff: dict[int, datetime] = {}
        # League -> decayed [matches, home wins, draws, goals].
        self._league: dict[int, list[float]] = defaultdict(lambda: [0.0, 0.0, 0.0, 0.0])

    def features(self, fixture: FixtureRecord) -> dict[str, float | None]:
        home = self._side(fixture.home_team_id, fixture, at_home=True)
        away = self._side(fixture.away_team_id, fixture, at_home=False)
        row: dict[str, float | None] = {}
        for name in _SIDE_FEATURES:
            row[f"home_{name}"] = home[name]
            row[f"away_{name}"] = away[name]
        for name in ("ppg_5", "ppg_10", "goal_difference_10", "season_ppg"):
            row[f"{name}_difference"] = _difference(home[name], away[name])
        matches, home_wins, draws, goals = self._league[fixture.league_id]
        weight = matches + _LEAGUE_PRIOR_MATCHES
        row["league_home_win_rate"] = (home_wins + _LEAGUE_PRIOR["home"] * _LEAGUE_PRIOR_MATCHES) / weight
        row["league_draw_rate"] = (draws + _LEAGUE_PRIOR["draw"] * _LEAGUE_PRIOR_MATCHES) / weight
        row["league_goals_per_match"] = (goals + _LEAGUE_PRIOR["goals"] * _LEAGUE_PRIOR_MATCHES) / weight
        return {name: row[name] for name in FEATURE_NAMES}

    def record(self, fixture: FixtureRecord) -> None:
        """Fold one finished competitive fixture into the book. Others are ignored."""
        if not _is_finished(fixture) or not is_competitive_league(fixture.league_name):
            return
        home_goals = int(fixture.home_goals or 0)
        away_goals = int(fixture.away_goals or 0)
        for team_id, at_home, scored, conceded in (
            (fixture.home_team_id, True, home_goals, away_goals),
            (fixture.away_team_id, False, away_goals, home_goals),
        ):
            played = _Played(fixture.kickoff_at, at_home, scored, conceded)
            self._recent[team_id].append(played)
            season = self._season[(team_id, fixture.season)]
            season[0] += 1
            season[1] += played.points
            season[2] += scored
            season[3] += conceded
            self._known[team_id] += 1
            self._last_kickoff[team_id] = fixture.kickoff_at

        league = self._league[fixture.league_id]
        for index in range(4):
            league[index] *= _LEAGUE_DECAY
        league[0] += 1.0
        league[1] += 1.0 if home_goals > away_goals else 0.0
        league[2] += 1.0 if home_goals == away_goals else 0.0
        league[3] += home_goals + away_goals

    def _side(self, team_id: int, fixture: FixtureRecord, *, at_home: bool) -> dict[str, float | None]:
        recent = list(self._recent.get(team_id, ()))
        last_five = recent[-5:]
        venue = [played for played in recent if played.at_home == at_home]
        played, points, scored, conceded = self._season.get((team_id, fixture.season), (0, 0, 0, 0))
        last_kickoff = self._last_kickoff.get(team_id)
        rest_days = None
        if last_kickoff is not None:
            rest_days = min((fixture.kickoff_at - last_kickoff).total_seconds() / 86400.0, _MAX_REST_DAYS)
        return {
            "ppg_5": _mean([match.points for match in last_five]),
            "goals_for_5": _mean([match.goals_for for match in last_five]),
            "goals_against_5": _mean([match.goals_against for match in last_five]),
            "ppg_10": _mean([match.points for match in recent]),
            "goal_difference_10": _mean([match.goals_for - match.goals_against for match in recent]),
            "venue_ppg_10": _mean([match.points for match in venue]),
            "season_ppg": points / played if played else None,
            "season_goals_for": scored / played if played else None,
            "season_goals_against": conceded / played if played else None,
            "season_played": float(played),
            "rest_days": rest_days,
            "known_matches": float(min(self._known.get(team_id, 0), 50)),
        }


def _mean(values: list[int]) -> float | None:
    return sum(values) / len(values) if values else None


def _difference(home: float | None, away: float | None) -> float | None:
    if home is None or away is None:
        return None
    return home - away
