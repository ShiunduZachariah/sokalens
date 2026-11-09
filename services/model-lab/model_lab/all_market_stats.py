"""Score Model C's counting markets against the champion's rolling averages.

    python -m model_lab.all_market_stats <corpus> --report stats.json

The champion's expected count is the sum of the two teams' rolling averages
with a Poisson distribution, read through the assembler it serves from, and
only where it has the five samples per side that it needs to publish. Model C
trains one Poisson tree model per count, configured on 2021-07 to 2022-07 and
refitted before the holdout (2022-07-01). A dispersion is measured on the same
validation window, because counts such as shots vary more than Poisson allows.

Possession is not scored: the corpus does not have it. Model B is not scored
here at all: a result-only rating has no basis for these counts.
"""

from __future__ import annotations

import argparse
from collections import Counter, defaultdict
from dataclasses import dataclass
import json
import math
from pathlib import Path
import sys
from typing import Any, Callable

from prediction_engine.challenger_markets import under_probability
from prediction_engine.features import TeamFixtureStats
from prediction_engine.form_features import FEATURE_NAMES, FormFeatureBook
from prediction_engine.stat_features import STAT_FEATURE_NAMES, StatFeatureBook

from model_lab import elo_walk_forward as walk
from model_lab.all_market_eval import (
    HOLDOUT_FROM,
    PROBABILITY_FLOOR,
    VALIDATION_FROM,
    _corpus,
    _match_for,
    export_ensemble,
    write_manifest_part,
)
from model_lab.elo_walk_forward import CorpusFixture, _kickoff_slots
from model_lab.football_data import Match
from model_lab.model_c_train import BASE_PARAMETERS, EARLY_STOPPING_ROUNDS, MAX_ROUNDS, PARAMETER_GRID


ALL_FEATURES = (*FEATURE_NAMES, *STAT_FEATURE_NAMES)
MINIMUM_CHAMPION_SAMPLES = 5


def _cards(yellow: int | None, red: int | None) -> int | None:
    # Settlement grades on cards shown, yellow plus red.
    return None if yellow is None else yellow + (red or 0)


def team_statistics(match: Match) -> dict[str, tuple[int | None, int | None]]:
    return {
        "corners": (match.home_corners, match.away_corners),
        "cards": (_cards(match.home_cards, match.home_red_cards), _cards(match.away_cards, match.away_red_cards)),
        "fouls": (match.home_fouls, match.away_fouls),
        "shots": (match.home_shots, match.away_shots),
        "shots_on_target": (match.home_shots_on_target, match.away_shots_on_target),
    }


def _total(pair: tuple[int | None, int | None]) -> float | None:
    return None if pair[0] is None or pair[1] is None else float(pair[0] + pair[1])


# Target name -> how to read it from a fixture's statistics.
TARGETS: dict[str, Callable[[dict[str, tuple[int | None, int | None]]], float | None]] = {
    "home_corners": lambda stats: None if stats["corners"][0] is None else float(stats["corners"][0]),
    "away_corners": lambda stats: None if stats["corners"][1] is None else float(stats["corners"][1]),
    "total_cards": lambda stats: _total(stats["cards"]),
    "total_fouls": lambda stats: _total(stats["fouls"]),
    "total_shots": lambda stats: _total(stats["shots"]),
    "shots_on_target": lambda stats: _total(stats["shots_on_target"]),
}


@dataclass(frozen=True)
class MarketDefinition:
    lines: tuple[float, ...]
    actual: Callable[[dict[str, tuple[int | None, int | None]]], float | None]
    model_c: tuple[str, ...]
    champion_metrics: tuple[str, ...]


# The lines the pipeline publishes.
MARKETS: dict[str, MarketDefinition] = {
    "total_corners": MarketDefinition(
        (7.5, 8.5, 9.5, 10.5), lambda stats: _total(stats["corners"]), ("home_corners", "away_corners"), ("home_corners_for", "away_corners_for")
    ),
    "team_corners_home": MarketDefinition((3.5, 4.5, 5.5), TARGETS["home_corners"], ("home_corners",), ("home_corners_for",)),
    "team_corners_away": MarketDefinition((3.5, 4.5, 5.5), TARGETS["away_corners"], ("away_corners",), ("away_corners_for",)),
    "total_cards": MarketDefinition((2.5, 3.5, 4.5, 5.5), TARGETS["total_cards"], ("total_cards",), ("home_card_points", "away_card_points")),
    "total_fouls": MarketDefinition((18.5, 21.5, 24.5), TARGETS["total_fouls"], ("total_fouls",), ("home_fouls", "away_fouls")),
    "total_shots": MarketDefinition((18.5, 21.5, 24.5), TARGETS["total_shots"], ("total_shots",), ("home_shots_total", "away_shots_total")),
    "shots_on_target": MarketDefinition((6.5, 7.5, 8.5), TARGETS["shots_on_target"], ("shots_on_target",), ("home_shots_on_target", "away_shots_on_target")),
}


@dataclass(frozen=True)
class StatRow:
    fixture: CorpusFixture
    statistics: dict[str, tuple[int | None, int | None]]
    features: dict[str, float | None]


def stat_rows(fixtures: list[CorpusFixture], matches: list[Match]) -> list[StatRow]:
    form = FormFeatureBook()
    stats = StatFeatureBook()
    rows: list[StatRow] = []
    for slot in _kickoff_slots(fixtures):
        for fixture in slot:
            rows.append(
                StatRow(
                    fixture,
                    team_statistics(_match_for(fixture, matches)),
                    {**form.features(fixture.record), **stats.features(fixture.record)},
                )
            )
        for fixture in slot:
            form.record(fixture.record)
            stats.record(fixture.record, team_statistics(_match_for(fixture, matches)))
    return rows


def train_stat_models(rows: list[StatRow], *, log: Callable[[str], None]) -> tuple[dict[str, Any], dict[str, Any]]:
    import lightgbm
    import numpy

    def grid(window: list[StatRow]) -> Any:
        return numpy.array(
            [[math.nan if row.features[name] is None else row.features[name] for name in ALL_FEATURES] for row in window],
            dtype=numpy.float64,
        )

    base = {key: value for key, value in BASE_PARAMETERS.items() if key != "num_class"}
    base.update(objective="poisson", metric="poisson")
    models: dict[str, Any] = {}
    report: dict[str, Any] = {}
    for name, read in TARGETS.items():
        usable = [row for row in rows if read(row.statistics) is not None]
        training = [row for row in usable if row.fixture.record.kickoff_at < VALIDATION_FROM]
        validation = [row for row in usable if VALIDATION_FROM <= row.fixture.record.kickoff_at < HOLDOUT_FROM]
        x_train, x_valid = grid(training), grid(validation)
        y_train = numpy.array([read(row.statistics) for row in training])
        y_valid = numpy.array([read(row.statistics) for row in validation])
        trials: list[dict[str, Any]] = []
        best_booster = None
        for candidate in PARAMETER_GRID:
            booster = lightgbm.train(
                {**base, **candidate},
                lightgbm.Dataset(x_train, y_train, feature_name=list(ALL_FEATURES), free_raw_data=False),
                num_boost_round=MAX_ROUNDS,
                valid_sets=[lightgbm.Dataset(x_valid, y_valid, feature_name=list(ALL_FEATURES))],
                callbacks=[lightgbm.early_stopping(EARLY_STOPPING_ROUNDS, verbose=False)],
            )
            trial = {**candidate, "rounds": int(booster.best_iteration), "validation_poisson": float(booster.best_score["valid_0"]["poisson"])}
            if not trials or trial["validation_poisson"] < min(item["validation_poisson"] for item in trials):
                best_booster = booster
            trials.append(trial)
        best = min(trials, key=lambda trial: trial["validation_poisson"])
        # Method of moments on data the training-only model did not see.
        means = best_booster.predict(x_valid, num_iteration=best["rounds"])
        dispersion = max(float(numpy.sum((y_valid - means) ** 2 - means) / numpy.sum(means**2)), 0.0)
        chosen = {key: best[key] for key in PARAMETER_GRID[0]}
        rounds = max(1, round(best["rounds"] * (len(training) + len(validation)) / len(training)))
        final_rows = [*training, *validation]
        models[name] = lightgbm.train(
            {**base, **chosen},
            lightgbm.Dataset(grid(final_rows), numpy.array([read(row.statistics) for row in final_rows]), feature_name=list(ALL_FEATURES)),
            num_boost_round=rounds,
        )
        report[name] = {"chosen": chosen, "rounds": rounds, "rows": len(final_rows), "dispersion": round(dispersion, 5)}
        log(f"{name}: {report[name]}")
    return models, report


def champion_statistics_table(fixtures: list[CorpusFixture], matches: list[Match]) -> dict[int, list[TeamFixtureStats]]:
    table: dict[int, list[TeamFixtureStats]] = {}
    for fixture in fixtures:
        stats = team_statistics(_match_for(fixture, matches))
        record = fixture.record
        table[record.id] = [
            TeamFixtureStats(
                fixture_id=record.id,
                team_id=team_id,
                shots_total=stats["shots"][side],
                shots_on_target=stats["shots_on_target"][side],
                corners=stats["corners"][side],
                fouls=stats["fouls"][side],
                card_points=stats["cards"][side],
            )
            for side, team_id in enumerate((record.home_team_id, record.away_team_id))
        ]
    return table


def run(paths: list[Path], report_path: Path, export_dir: Path | None = None) -> dict[str, Any]:
    import numpy

    def log(message: str) -> None:
        print(message, file=sys.stderr, flush=True)

    fixtures, matches = _corpus(paths)
    rows = stat_rows(fixtures, matches)
    models, training = train_stat_models(rows, log=log)
    holdout = [row for row in rows if row.fixture.record.kickoff_at >= HOLDOUT_FROM]
    grid = numpy.array(
        [[math.nan if row.features[name] is None else row.features[name] for name in ALL_FEATURES] for row in holdout],
        dtype=numpy.float64,
    )
    expected = {name: models[name].predict(grid) for name in TARGETS}
    log(f"scoring {len(holdout)} holdout fixtures")
    if export_dir is not None:
        for name in TARGETS:
            export_ensemble(models[name], ALL_FEATURES, export_dir / f"count_{name}.json", grid, expected[name])
        write_manifest_part(
            export_dir,
            "counts",
            {
                "count_targets": list(TARGETS),
                "dispersions": {name: training[name]["dispersion"] for name in TARGETS},
                "count_training": training,
            },
        )

    replay = walk.ChampionReplay(fixtures)
    table = champion_statistics_table(fixtures, matches)
    assembler = replay.new_assembler()
    series: dict[str, dict[str, list[float]]] = defaultdict(lambda: defaultdict(list))
    outcomes: dict[str, Counter[str]] = defaultdict(Counter)
    errors: dict[str, dict[str, list[float]]] = defaultdict(lambda: defaultdict(list))
    for index, row in enumerate(holdout):
        record = row.fixture.record
        history = replay.history(record)
        summary, samples, _ = assembler._statistics_summary(record.home_team_id, record.away_team_id, history, table)
        for market, definition in MARKETS.items():
            actual = definition.actual(row.statistics)
            if actual is None:
                continue
            values = [summary.get(metric) for metric in definition.champion_metrics]
            if any(value is None for value in values) or min(samples.get(metric, 0) for metric in definition.champion_metrics) < MINIMUM_CHAMPION_SAMPLES:
                continue
            champion_mean = float(sum(values))
            c_mean = float(sum(expected[name][index] for name in definition.model_c))
            dispersion = _market_dispersion(training, definition)
            errors[market]["champion"].append(champion_mean - actual)
            errors[market]["model_c"].append(c_mean - actual)
            fixture_losses: dict[str, list[float]] = defaultdict(list)
            for line in definition.lines:
                over = actual > line
                key = f"{market}:{line:g}"
                outcomes[key]["over" if over else "under"] += 1
                for name, probability in (
                    ("champion", under_probability(champion_mean, 0.0, line)),
                    ("model_c_poisson", under_probability(c_mean, 0.0, line)),
                    ("model_c", under_probability(c_mean, dispersion, line)),
                ):
                    side = 1.0 - probability if over else probability
                    fixture_losses[name].append(-math.log(max(side, PROBABILITY_FLOOR)))
            # Lines on one fixture are not independent, so a fixture counts once.
            for name, losses in fixture_losses.items():
                series[market][name].append(sum(losses) / len(losses))

    report: dict[str, Any] = {"training": training, "markets": {}}
    for market, models_series in series.items():
        weights = [(key, counts) for key, counts in outcomes.items() if key.startswith(f"{market}:")]
        base_total = sum(sum(counts.values()) for _, counts in weights)
        base = sum(
            -sum(count * math.log(count / sum(counts.values())) for count in counts.values())
            for _, counts in weights
        ) / base_total
        entry: dict[str, Any] = {"fixtures": len(models_series["champion"]), "base_rate": round(base, 4)}
        for name, values in models_series.items():
            entry[name] = round(sum(values) / len(values), 4)
            if name != "champion":
                entry[f"{name}_z"] = round(_z(values, models_series["champion"]), 1)
        for name, values in errors[market].items():
            entry[f"{name}_bias"] = round(sum(values) / len(values), 3)
        report["markets"][market] = entry
    report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return report


def _z(first: list[float], second: list[float]) -> float:
    differences = [a - b for a, b in zip(first, second)]
    mean = sum(differences) / len(differences)
    variance = sum((value - mean) ** 2 for value in differences) / max(len(differences) - 1, 1)
    return mean / math.sqrt(variance / len(differences)) if variance > 0 else 0.0


def _market_dispersion(training: dict[str, Any], definition: MarketDefinition) -> float:
    # A sum of two counts: the mean of the sides is a close enough single figure.
    values = [training[name]["dispersion"] for name in definition.model_c]
    return sum(values) / len(values) / (len(values) if len(values) > 1 else 1)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("paths", nargs="+", type=Path, help="Football-Data CSV files or directories")
    parser.add_argument("--report", type=Path, required=True)
    parser.add_argument("--export-dir", type=Path, help="write the trained count models here")
    args = parser.parse_args(argv)
    print(json.dumps(run(args.paths, args.report, args.export_dir), indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
