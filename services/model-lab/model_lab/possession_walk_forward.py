"""Walk-forward evidence for the `possession_winner` probability.

The Beta curve in `prediction_engine.shot_possession_markets` has one measured
parameter. This module is the measurement, and it is what to re-run when more
statistics have accumulated. See docs/prediction-engine/possession-market.md.

Every expected share uses only matches that finished before the fixture's
kick-off, built the way the production repository loads history: the two teams'
latest 80 finished fixtures from this season and last. A fixture needs
`min_samples` possession readings per side, and the engine's own eligibility
threshold is 5.

The query is read-only and the session is opened read-only:

    python -m model_lab.possession_walk_forward --database-url "$DATABASE_URL"
"""

from __future__ import annotations

import argparse
from collections import Counter, defaultdict
from dataclasses import dataclass
import json
import math
import os
import random
import statistics
import sys
from typing import Iterable

from prediction_engine.shot_possession_markets import expected_home_possession_share, possession_market

SIDES = ("home_more", "level", "away_more")

_FIXTURES_SQL = """
SELECT id, extract(epoch FROM kickoff_at)::bigint AS kickoff, season_id, league_id, home_team_id, away_team_id
FROM fixtures
WHERE home_goals_ft IS NOT NULL AND away_goals_ft IS NOT NULL
  AND home_team_id IS NOT NULL AND away_team_id IS NOT NULL
"""

_POSSESSION_SQL = """
SELECT fixture_id, team_id, possession_pct
FROM fixture_statistics
WHERE possession_pct IS NOT NULL
"""


@dataclass(frozen=True)
class Fixture:
    id: int
    kickoff: int
    season: int | None
    league: int
    home: int
    away: int


@dataclass(frozen=True)
class Observation:
    kickoff: int
    league: int
    expected: float
    home_share: float
    away_share: float
    # The home side's own average, which is all the previous market read.
    home_average: float = 50.0

    @property
    def outcome(self) -> str:
        if self.home_share > self.away_share:
            return "home_more"
        return "away_more" if self.home_share < self.away_share else "level"


def walk_forward_observations(
    fixtures: Iterable[Fixture],
    possession: dict[int, dict[int, float]],
    *,
    min_samples: int = 5,
    history_limit: int = 80,
) -> list[Observation]:
    """One observation per fixture with an actual split and enough prior readings."""
    ordered = sorted(fixtures, key=lambda fixture: fixture.kickoff)
    by_team: dict[int, list[int]] = defaultdict(list)
    for index, fixture in enumerate(ordered):
        by_team[fixture.home].append(index)
        by_team[fixture.away].append(index)

    observations: list[Observation] = []
    for fixture in ordered:
        actual = possession.get(fixture.id, {})
        if fixture.home not in actual or fixture.away not in actual:
            continue
        # A split that does not add up to a whole match is a broken row, such as
        # two zeros, and not a result.
        if actual[fixture.home] + actual[fixture.away] < 90:
            continue
        history = sorted(
            {
                index
                for index in by_team[fixture.home] + by_team[fixture.away]
                if ordered[index].kickoff < fixture.kickoff
                and fixture.season is not None
                and ordered[index].season in (fixture.season, fixture.season - 1)
            },
            reverse=True,
        )[:history_limit]
        home_values = _readings(history, ordered, possession, fixture.home)
        away_values = _readings(history, ordered, possession, fixture.away)
        if len(home_values) < min_samples or len(away_values) < min_samples:
            continue
        observations.append(
            Observation(
                kickoff=fixture.kickoff,
                league=fixture.league,
                expected=expected_home_possession_share(statistics.fmean(home_values), statistics.fmean(away_values)),
                home_share=actual[fixture.home],
                away_share=actual[fixture.away],
                home_average=statistics.fmean(home_values),
            )
        )
    return observations


def _readings(history: list[int], ordered: list[Fixture], possession: dict[int, dict[int, float]], team: int) -> list[float]:
    readings = (possession.get(ordered[index].id, {}).get(team) for index in history)
    return [value for value in readings if value is not None]


def share_fit(observations: list[Observation]) -> dict[str, float]:
    """How the actual share relates to the expected one, about an even game."""
    xs = [row.expected - 50.0 for row in observations]
    ys = [row.home_share - 50.0 for row in observations]
    slope = sum(x * y for x, y in zip(xs, ys)) / sum(x * x for x in xs)
    residuals = [y - x for x, y in zip(xs, ys)]
    sd = math.sqrt(sum(value * value for value in residuals) / (len(residuals) - 1))
    return {
        "slope": round(slope, 3),
        "residual_sd_points": round(sd, 3),
        "mean_residual_points": round(statistics.fmean(residuals), 3),
        "correlation": round(statistics.correlation(xs, ys), 3),
        "implied_precision": round(0.25 / (sd / 100.0) ** 2 - 1.0, 1),
    }


def evaluate(observations: list[Observation], *, holdout_fraction: float = 0.4, seed: int = 7) -> dict[str, object]:
    """Score the shipped curve on the latest fixtures against two references.

    `base_rate` is the three outcome rates from the earlier window. `previous` is
    what the market used to publish: 0.99 on whichever side the home team's own
    average favoured, the rest split.
    """
    ordered = sorted(observations, key=lambda row: row.kickoff)
    cut = int(len(ordered) * (1.0 - holdout_fraction))
    tuning, holdout = ordered[:cut], ordered[cut:]
    counts = Counter(row.outcome for row in tuning)
    base_rate = {side: (counts[side] + 1) / (len(tuning) + 3) for side in SIDES}

    shipped = [possession_market(expected_home_possession_pct=row.expected).side_probabilities for row in holdout]
    shipped_loss = [-math.log(max(p[row.outcome], 1e-12)) for p, row in zip(shipped, holdout)]
    base_loss = [-math.log(base_rate[row.outcome]) for row in holdout]
    previous_loss = [-math.log(max(_previous_market(row.home_average)[row.outcome], 1e-12)) for row in holdout]

    return {
        "fixtures": len(ordered),
        "leagues": len({row.league for row in ordered}),
        "expected_range": [round(min(row.expected for row in ordered), 1), round(max(row.expected for row in ordered), 1)],
        "outcome_rates": {side: round(sum(row.outcome == side for row in ordered) / len(ordered), 3) for side in SIDES},
        "fit_all": share_fit(ordered),
        "fit_tuning": share_fit(tuning),
        "fit_holdout": share_fit(holdout),
        "holdout_fixtures": len(holdout),
        "holdout_log_loss": {
            "shipped": round(statistics.fmean(shipped_loss), 4),
            "base_rate": round(statistics.fmean(base_loss), 4),
            "previous": round(statistics.fmean(previous_loss), 4),
        },
        "shipped_gain_over_base_rate_95ci": _bootstrap_gain(base_loss, shipped_loss, seed),
        "holdout_top_pick": _calibration(shipped, holdout),
        "previous_top_pick_hit_rate": round(
            sum(max(_previous_market(row.home_average), key=_previous_market(row.home_average).get) == row.outcome for row in ordered)
            / len(ordered),
            3,
        ),
    }


def _previous_market(expected: float) -> dict[str, float]:
    if expected > 50.0:
        return {"home_more": 0.99, "level": 0.005, "away_more": 0.005}
    if expected < 50.0:
        return {"home_more": 0.005, "level": 0.005, "away_more": 0.99}
    return {"home_more": 0.005, "level": 0.99, "away_more": 0.005}


def _bootstrap_gain(reference: list[float], candidate: list[float], seed: int, rounds: int = 4000) -> list[float]:
    rng = random.Random(seed)
    size = len(reference)
    gains = sorted(
        sum(reference[index] - candidate[index] for index in (rng.randrange(size) for _ in range(size))) / size
        for _ in range(rounds)
    )
    return [round(gains[int(rounds * 0.025)], 4), round(gains[int(rounds * 0.975)], 4)]


def _calibration(predictions: list[dict[str, float]], rows: list[Observation]) -> dict[str, object]:
    bins: dict[str, list[float]] = defaultdict(lambda: [0, 0, 0.0])
    hits = 0
    for probabilities, row in zip(predictions, rows):
        selection = max(probabilities, key=probabilities.get)
        probability = probabilities[selection]
        hit = selection == row.outcome
        hits += hit
        lower = max(int(probability * 10) / 10, 0.4)
        cell = bins[f"{lower:.1f}-{lower + 0.1:.1f}"]
        cell[0] += 1
        cell[1] += hit
        cell[2] += probability
    return {
        "hit_rate": round(hits / len(rows), 3),
        "bins": {
            key: {"n": int(cell[0]), "predicted": round(cell[2] / cell[0], 3), "observed": round(cell[1] / cell[0], 3)}
            for key, cell in sorted(bins.items())
        },
    }


def load_from_database(url: str) -> tuple[list[Fixture], dict[int, dict[int, float]]]:
    import psycopg

    with psycopg.connect(url) as connection:
        connection.read_only = True
        fixtures = [
            Fixture(int(row[0]), int(row[1]), None if row[2] is None else int(row[2]), int(row[3]), int(row[4]), int(row[5]))
            for row in connection.execute(_FIXTURES_SQL).fetchall()
        ]
        possession: dict[int, dict[int, float]] = defaultdict(dict)
        for fixture_id, team_id, share in connection.execute(_POSSESSION_SQL).fetchall():
            possession[int(fixture_id)][int(team_id)] = float(share)
    return fixtures, dict(possession)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--database-url", default=os.environ.get("DATABASE_URL", ""))
    parser.add_argument("--min-samples", type=int, nargs="+", default=[5, 3])
    args = parser.parse_args(argv)
    if not args.database_url:
        parser.error("--database-url or DATABASE_URL is required")
    fixtures, possession = load_from_database(args.database_url)
    report = {
        str(minimum): evaluate(walk_forward_observations(fixtures, possession, min_samples=minimum))
        for minimum in args.min_samples
    }
    json.dump(report, sys.stdout, indent=2)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
