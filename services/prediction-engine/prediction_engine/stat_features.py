"""Point-in-time match-statistic features for Model C's counting markets.

`form_features` leaves statistics out because the 1X2 model has to work without
them. A corners or cards forecast has nothing to stand on otherwise, so these
sit beside it and feed only the counting models.

The contract matches `FormFeatureBook`: ask `features` for a fixture before
`record` folds it in. A statistic production lacks for a fixture is simply not
recorded, and a value nobody can know yet is `None`, never a guessed zero.
"""

from __future__ import annotations

from collections import defaultdict, deque
from typing import Deque, Mapping

from prediction_engine.features import FixtureRecord, _is_finished, is_competitive_league


STAT_FEATURE_VERSION = "stat-features-v1"
METRICS = ("corners", "cards", "fouls", "shots", "shots_on_target")

_HISTORY = 10
_LEAGUE_DECAY = 0.998

STAT_FEATURE_NAMES: tuple[str, ...] = tuple(
    name
    for metric in METRICS
    for name in (
        f"home_{metric}_for_10",
        f"home_{metric}_against_10",
        f"away_{metric}_for_10",
        f"away_{metric}_against_10",
        f"league_{metric}_per_team",
    )
)


class StatFeatureBook:
    """Team statistics and league rates as they stand after the fixtures recorded."""

    def __init__(self) -> None:
        self._recent: dict[tuple[int, str], Deque[tuple[int, int]]] = defaultdict(lambda: deque(maxlen=_HISTORY))
        # (league, metric) -> decayed [team-matches, count].
        self._league: dict[tuple[int, str], list[float]] = defaultdict(lambda: [0.0, 0.0])

    def features(self, fixture: FixtureRecord) -> dict[str, float | None]:
        row: dict[str, float | None] = {}
        for metric in METRICS:
            for side, team_id in (("home", fixture.home_team_id), ("away", fixture.away_team_id)):
                recent = self._recent.get((team_id, metric), ())
                row[f"{side}_{metric}_for_10"] = _mean([value[0] for value in recent])
                row[f"{side}_{metric}_against_10"] = _mean([value[1] for value in recent])
            matches, count = self._league.get((fixture.league_id, metric), (0.0, 0.0))
            row[f"league_{metric}_per_team"] = count / matches if matches else None
        return row

    def record(self, fixture: FixtureRecord, statistics: Mapping[str, tuple[int | None, int | None]]) -> None:
        """Fold one finished competitive fixture's statistics, metric -> (home, away), into the book."""
        if not _is_finished(fixture) or not is_competitive_league(fixture.league_name):
            return
        for metric, (home, away) in statistics.items():
            if metric not in METRICS or home is None or away is None:
                continue
            self._recent[(fixture.home_team_id, metric)].append((home, away))
            self._recent[(fixture.away_team_id, metric)].append((away, home))
            league = self._league[(fixture.league_id, metric)]
            league[0] = league[0] * _LEAGUE_DECAY + 2.0
            league[1] = league[1] * _LEAGUE_DECAY + home + away


def _mean(values: list[int]) -> float | None:
    return sum(values) / len(values) if values else None
