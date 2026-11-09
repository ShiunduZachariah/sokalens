"""Score the champion, Model B and Model C on every goal-based market.

Each model's forecast is reduced to one number per fixture and market: the
probability it gave what actually happened. Log loss comes straight from that,
and so does any linear mix of models, without replaying anything.

    python -m model_lab.all_market_eval champion <corpus> --out champion.jsonl
    python -m model_lab.all_market_eval challengers <corpus> --out challengers.jsonl --report-dir <dir>
    python -m model_lab.all_market_eval report <corpus> --champion champion.jsonl --challengers challengers.jsonl

Model C here predicts goals with four Poisson tree models, and every market
comes from the score matrix they imply. Model B maps the Elo rating gap and the
league's average goals to expected goals. It never reads a team's goal rate,
which is the input elo.py exists to avoid. Both are fitted before the holdout
(2022-07-01), and hyperparameters are chosen on 2021-07 to 2022-07 only.

`team_to_score_first` is not scored: the corpus has no goal times.
"""

from __future__ import annotations

import argparse
from collections import Counter, defaultdict
from dataclasses import dataclass
from datetime import datetime, timezone
import json
import math
from pathlib import Path
import sys
from typing import Any, Iterable, Mapping

from prediction_engine.combination_markets import MarketLeg, joint_probability
from prediction_engine.elo import DEFAULT_ELO_CONFIG, EloRatingBook
from prediction_engine.form_features import FEATURE_NAMES, FormFeatureBook
from prediction_engine.gbdt import ARTIFACT_FORMAT, TreeEnsemble
from prediction_engine.model_a import ModelAInputs, run_model_a
from prediction_engine.model_c import DEFAULT_ARTIFACT

from model_lab import elo_walk_forward as walk
from model_lab.elo_walk_forward import CorpusFixture, _csv_paths, _kickoff_slots, corpus_fixtures
from model_lab.football_data import Match, load_matches
from model_lab.model_c_train import (
    BASE_PARAMETERS,
    EARLY_STOPPING_ROUNDS,
    MAX_ROUNDS,
    PARAMETER_GRID,
    PARITY_SAMPLE,
    _flatten,
    dataset_hash,
)


VALIDATION_FROM = datetime(2021, 7, 1, tzinfo=timezone.utc)
HOLDOUT_FROM = datetime(2022, 7, 1, tzinfo=timezone.utc)
TOTAL_LINES = (1.5, 2.5, 3.5, 4.5)
# A scoreline beyond the matrix, or a cell a model thinks impossible, still has
# to cost something finite. Every model gets the same floor.
PROBABILITY_FLOOR = 1e-6
GOAL_TARGETS = ("home", "away", "half_time_home", "half_time_away")
MIN_EXPECTED_GOALS = 0.02


# --- outcomes ------------------------------------------------------------------


def _share(values: Mapping[str, Any], key: str) -> float:
    total = sum(float(value) for value in values.values())
    return max(float(values[key]) / total, PROBABILITY_FLOOR) if total > 0 else PROBABILITY_FLOOR


def _side(value: float, hit: bool) -> float:
    return max(value if hit else 1.0 - value, PROBABILITY_FLOOR)


def outcome_probabilities(markets: Mapping[str, Any], matrix: list[list[float]], match: Match) -> dict[str, list[Any]]:
    """Market -> [the outcome that happened, the probability given to it]."""
    home, away = match.home_goals, match.away_goals
    total = home + away
    result = "home" if home > away else "away" if away > home else "draw"
    both = home > 0 and away > 0
    scored: dict[str, list[Any]] = {"1x2": [result, _share(markets["match_result"], result)]}

    for selection, hit in (
        ("home_or_draw", result != "away"),
        ("draw_or_away", result != "home"),
        ("home_or_away", result != "draw"),
    ):
        scored[f"double_chance:{selection}"] = [
            "yes" if hit else "no",
            _side(float(markets["double_chance"][selection]), hit),
        ]
    if result != "draw":
        scored["draw_no_bet"] = [result, _share(markets["draw_no_bet"], result)]
    for line in TOTAL_LINES:
        side = "over" if total > line else "under"
        goals = markets["total_goals"]
        scored[f"total_goals:{line}"] = [
            side,
            _share({"over": goals[f"over_{line}"], "under": goals[f"under_{line}"]}, side),
        ]
    yes_no = "yes" if both else "no"
    scored["btts"] = [yes_no, _share(markets["btts"], yes_no)]
    parity = "even" if total % 2 == 0 else "odd"
    scored["odd_even_total_goals"] = [parity, _share(markets["odd_even_total_goals"], parity)]

    btts_total = {
        f"{flag}_{side}": markets["btts_total_goals"][f"{flag}_{side}_2.5"]
        for flag in ("yes", "no")
        for side in ("over", "under")
    }
    total_side = "over" if total > 2.5 else "under"
    scored["btts_total_goals:2.5"] = [f"{yes_no}_{total_side}", _share(btts_total, f"{yes_no}_{total_side}")]
    combined = f"{result}_{yes_no}"
    scored["btts_match_result"] = [combined, _share(markets["btts_match_result"], combined)]

    legs = {
        f"{name}_{side}": joint_probability(matrix, MarketLeg.match_result(name), MarketLeg.total_goals(side, 2.5))
        for name in ("home", "draw", "away")
        for side in ("over", "under")
    }
    scored["result_total_goals:2.5"] = [f"{result}_{total_side}", _share(legs, f"{result}_{total_side}")]

    cell = matrix[home][away] if home < len(matrix) and away < len(matrix[home]) else 0.0
    scored["correct_score"] = [f"{home}-{away}", max(float(cell), PROBABILITY_FLOOR)]

    half_home, half_away = match.half_time_home_goals, match.half_time_away_goals
    if half_home is not None and half_away is not None and half_home <= home and half_away <= away:
        half_result = "home" if half_home > half_away else "away" if half_away > half_home else "draw"
        scored["half_time_result"] = [half_result, _share(markets["half_time_result"], half_result)]
        first, second = half_home + half_away, total - half_home - half_away
        half = "first_half" if first > second else "second_half" if second > first else "equal"
        scored["highest_scoring_half"] = [half, _share(markets["highest_scoring_half"], half)]
    return scored


def _write_row(handle: Any, fixture_id: int, scored: Mapping[str, list[Any]]) -> None:
    handle.write(json.dumps({"id": fixture_id, "scored": scored}, separators=(",", ":")) + "\n")


def _corpus(paths: Iterable[Path]) -> tuple[list[CorpusFixture], list[Match]]:
    matches = load_matches(_csv_paths(list(paths))).matches
    return corpus_fixtures(matches), matches


def _match_for(fixture: CorpusFixture, matches: list[Match]) -> Match:
    # corpus_fixtures numbers fixtures from one in load order.
    return matches[fixture.record.id - 1]


# --- champion ------------------------------------------------------------------


def run_champion(paths: Iterable[Path], out: Path) -> None:
    """The published markets, replayed through the production pipeline's own steps."""
    fixtures, matches = _corpus(paths)
    captured: dict[str, Any] = {}
    real_model_a, real_published = walk.run_model_a, walk._published_result_markets

    def model_a(inputs: ModelAInputs) -> Any:
        captured["model_a"] = real_model_a(inputs)
        return captured["model_a"]

    def published(markets: Any, odds_implied: Any, clamp: Any) -> Any:
        # Take the markets as they leave the pipeline's last step. Reading them any
        # earlier scores double chance and draw no bet without the odds anchor.
        captured["published"] = real_published(markets, odds_implied, clamp)
        return captured["published"]

    walk.run_model_a, walk._published_result_markets = model_a, published
    try:
        replay = walk.ChampionReplay(fixtures)
        with out.open("w", encoding="utf-8") as handle:
            for fixture in fixtures:
                if fixture.record.kickoff_at < HOLDOUT_FROM:
                    continue
                replay.predict(fixture)
                _write_row(
                    handle,
                    fixture.record.id,
                    outcome_probabilities(captured["published"], captured["model_a"].score_matrix, _match_for(fixture, matches)),
                )
    finally:
        walk.run_model_a, walk._published_result_markets = real_model_a, real_published


# --- challengers ---------------------------------------------------------------


@dataclass(frozen=True)
class GoalRow:
    fixture: CorpusFixture
    match: Match
    features: dict[str, float | None]
    elo: Any


def goal_rows(fixtures: list[CorpusFixture], matches: list[Match]) -> list[GoalRow]:
    """Features and Elo for each fixture, before any of its kick-off slot is recorded."""
    form = FormFeatureBook()
    elo = EloRatingBook(DEFAULT_ELO_CONFIG)
    rows: list[GoalRow] = []
    for slot in _kickoff_slots(fixtures):
        for fixture in slot:
            rows.append(GoalRow(fixture, _match_for(fixture, matches), form.features(fixture.record), elo.predict(fixture.record)))
        for fixture in slot:
            form.record(fixture.record)
            elo.record(fixture.record)
    return rows


def _target(row: GoalRow, name: str) -> float | None:
    if name == "home":
        return float(row.match.home_goals)
    if name == "away":
        return float(row.match.away_goals)
    value = row.match.half_time_home_goals if name == "half_time_home" else row.match.half_time_away_goals
    return None if value is None else float(value)


def train_goal_models(rows: list[GoalRow], *, log: Any) -> tuple[dict[str, Any], dict[str, Any]]:
    """One Poisson tree model per target, configured on validation, refitted before the holdout."""
    import lightgbm
    import numpy

    def matrix(window: list[GoalRow]) -> Any:
        return numpy.array(
            [[math.nan if row.features[name] is None else row.features[name] for name in FEATURE_NAMES] for row in window],
            dtype=numpy.float64,
        )

    base = {key: value for key, value in BASE_PARAMETERS.items() if key != "num_class"}
    base.update(objective="poisson", metric="poisson")
    models: dict[str, Any] = {}
    report: dict[str, Any] = {}
    for name in GOAL_TARGETS:
        usable = [row for row in rows if _target(row, name) is not None]
        training = [row for row in usable if row.fixture.record.kickoff_at < VALIDATION_FROM]
        validation = [row for row in usable if VALIDATION_FROM <= row.fixture.record.kickoff_at < HOLDOUT_FROM]
        x_train, x_valid = matrix(training), matrix(validation)
        y_train = numpy.array([_target(row, name) for row in training])
        y_valid = numpy.array([_target(row, name) for row in validation])
        trials = []
        for candidate in PARAMETER_GRID:
            booster = lightgbm.train(
                {**base, **candidate},
                lightgbm.Dataset(x_train, y_train, feature_name=list(FEATURE_NAMES), free_raw_data=False),
                num_boost_round=MAX_ROUNDS,
                valid_sets=[lightgbm.Dataset(x_valid, y_valid, feature_name=list(FEATURE_NAMES))],
                callbacks=[lightgbm.early_stopping(EARLY_STOPPING_ROUNDS, verbose=False)],
            )
            trials.append(
                {**candidate, "rounds": int(booster.best_iteration), "validation_poisson": float(booster.best_score["valid_0"]["poisson"])}
            )
        best = min(trials, key=lambda trial: trial["validation_poisson"])
        chosen = {key: best[key] for key in PARAMETER_GRID[0]}
        rounds = max(1, round(best["rounds"] * (len(training) + len(validation)) / len(training)))
        final_rows = [*training, *validation]
        models[name] = lightgbm.train(
            {**base, **chosen},
            lightgbm.Dataset(matrix(final_rows), numpy.array([_target(row, name) for row in final_rows]), feature_name=list(FEATURE_NAMES)),
            num_boost_round=rounds,
        )
        report[name] = {"chosen": chosen, "rounds": rounds, "rows": len(final_rows), "trials": trials}
        log(f"{name}: {report[name]['chosen']} rounds {rounds}")
    return models, report


def elo_goal_terms(row: GoalRow) -> list[float]:
    gap = float(row.elo.rating_differential) / 400.0
    league = float(row.features["league_goals_per_match"] or 2.7)
    return [1.0, gap, gap * gap, math.log(league)]


def fit_elo_goal_map(rows: list[GoalRow]) -> dict[str, Any]:
    """A Poisson regression of goals on the rating gap and the league's goal rate."""
    import numpy

    fitted = [row for row in rows if row.fixture.record.kickoff_at < HOLDOUT_FROM]
    terms = numpy.array([elo_goal_terms(row) for row in fitted])

    def irls(y: Any) -> list[float]:
        beta = numpy.zeros(terms.shape[1])
        beta[0] = math.log(float(y.mean()))
        for _ in range(25):
            rate = numpy.exp(terms @ beta)
            step = numpy.linalg.solve((terms.T * rate) @ terms, terms.T @ (y - rate))
            beta = beta + step
            if float(numpy.abs(step).max()) < 1e-9:
                break
        return [float(value) for value in beta]

    home = numpy.array([float(row.match.home_goals) for row in fitted])
    away = numpy.array([float(row.match.away_goals) for row in fitted])
    with_half = [row for row in fitted if row.match.half_time_home_goals is not None and row.match.half_time_away_goals is not None]
    return {
        "terms": ["1", "gap", "gap_squared", "log_league_goals"],
        "home": irls(home),
        "away": irls(away),
        "half_time_home_share": sum(row.match.half_time_home_goals for row in with_half) / max(sum(row.match.home_goals for row in with_half), 1),
        "half_time_away_share": sum(row.match.half_time_away_goals for row in with_half) / max(sum(row.match.away_goals for row in with_half), 1),
        "rows": len(fitted),
    }


def _score_matrix_markets(home: float, away: float, half_home: float, half_away: float) -> Any:
    home = max(home, MIN_EXPECTED_GOALS)
    away = max(away, MIN_EXPECTED_GOALS)
    return run_model_a(
        ModelAInputs(
            home_lambda=home,
            away_lambda=away,
            half_time_home_lambda=min(max(half_home, MIN_EXPECTED_GOALS / 2), home),
            half_time_away_lambda=min(max(half_away, MIN_EXPECTED_GOALS / 2), away),
        )
    )


def export_ensemble(booster: Any, feature_names: Iterable[str], path: Path, grid: Any, expected: Any) -> None:
    """Write a Poisson model in the stdlib format, refused unless it reproduces LightGBM."""
    names = list(feature_names)
    dumped = booster.dump_model()
    if list(dumped["feature_names"]) != names:
        raise ValueError("booster feature order differs from the feature list")
    payload = {
        "format": ARTIFACT_FORMAT,
        "feature_names": names,
        "classes": ["count"],
        "objective": "poisson",
        "trees": [_flatten(info["tree_structure"]) for info in dumped["tree_info"]],
    }
    text = json.dumps(payload, separators=(",", ":"), sort_keys=True)
    ensemble = TreeEnsemble.from_json(text)
    step = max(1, len(grid) // PARITY_SAMPLE)
    for index in range(0, len(grid), step):
        ours = ensemble.expected_count([None if math.isnan(value) else float(value) for value in grid[index]])
        theirs = float(expected[index])
        if abs(ours - theirs) > 1e-9 * max(theirs, 1.0):
            raise AssertionError(f"stdlib evaluator disagrees with LightGBM on {path.name}: {ours} vs {theirs}")
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text + "\n", encoding="utf-8")


def write_manifest_part(directory: Path, name: str, values: Mapping[str, Any]) -> None:
    directory.mkdir(parents=True, exist_ok=True)
    (directory / f"manifest.{name}.json").write_text(json.dumps(values, sort_keys=True) + "\n", encoding="utf-8")


def write_manifest(directory: Path, paths: Iterable[Path], code_commit: str) -> None:
    """Join the training runs' parts into the manifest the engine loads."""
    parts = sorted(directory.glob("manifest.*.json"))
    merged: dict[str, Any] = {}
    for part in parts:
        merged.update(json.loads(part.read_text(encoding="utf-8")))
    merged.update(
        feature_version="form-features-v1+stat-features-v1",
        hyperparameters={
            "objective": "poisson",
            "goal_models": merged.get("goal_training"),
            "count_models": merged.get("count_training"),
        },
        training_end=HOLDOUT_FROM.isoformat(),
        validation_window={"from": VALIDATION_FROM.isoformat(), "until": HOLDOUT_FROM.isoformat()},
        training_dataset_hash=dataset_hash(_csv_paths(list(paths))),
        code_commit=code_commit,
    )
    (directory / "manifest.json").write_text(json.dumps(merged, indent=1, sort_keys=True) + "\n", encoding="utf-8")
    for part in parts:
        part.unlink()


def run_challengers(paths: Iterable[Path], out: Path, report_dir: Path, export_dir: Path | None = None) -> None:
    import numpy

    def log(message: str) -> None:
        print(message, file=sys.stderr, flush=True)

    fixtures, matches = _corpus(paths)
    log(f"building features for {len(fixtures)} fixtures")
    rows = goal_rows(fixtures, matches)
    models, training_report = train_goal_models(rows, log=log)
    elo_map = fit_elo_goal_map(rows)
    log(f"elo goal map {elo_map}")
    report_dir.mkdir(parents=True, exist_ok=True)
    (report_dir / "all_market_training.json").write_text(
        json.dumps({"goal_models": training_report, "elo_goal_map": elo_map}, indent=2) + "\n", encoding="utf-8"
    )

    classifier = TreeEnsemble.load(DEFAULT_ARTIFACT)
    holdout = [row for row in rows if row.fixture.record.kickoff_at >= HOLDOUT_FROM]
    grid = numpy.array(
        [[math.nan if row.features[name] is None else row.features[name] for name in FEATURE_NAMES] for row in holdout],
        dtype=numpy.float64,
    )
    expected = {name: models[name].predict(grid) for name in GOAL_TARGETS}
    if export_dir is not None:
        for name in GOAL_TARGETS:
            export_ensemble(models[name], FEATURE_NAMES, export_dir / f"goals_{name}.json", grid, expected[name])
        write_manifest_part(export_dir, "goals", {"elo_goal_map": elo_map, "goal_training": training_report})
    log(f"scoring {len(holdout)} holdout fixtures")
    with out.open("w", encoding="utf-8") as handle:
        for index, row in enumerate(holdout):
            model_c = _score_matrix_markets(*(float(expected[name][index]) for name in GOAL_TARGETS))
            terms = elo_goal_terms(row)
            elo_home = math.exp(sum(a * b for a, b in zip(terms, elo_map["home"])))
            elo_away = math.exp(sum(a * b for a, b in zip(terms, elo_map["away"])))
            model_b = _score_matrix_markets(
                elo_home, elo_away, elo_home * elo_map["half_time_home_share"], elo_away * elo_map["half_time_away_share"]
            )
            # Elo's own 1X2 stays its 1X2; the goal map only adds what Elo cannot say.
            model_b_markets = {
                **model_b.markets,
                "match_result": row.elo.match_result,
                "double_chance": row.elo.double_chance,
                "draw_no_bet": row.elo.draw_no_bet,
            }
            c_scored = outcome_probabilities(model_c.markets, model_c.score_matrix, row.match)
            b_scored = outcome_probabilities(model_b_markets, model_b.score_matrix, row.match)
            result = row.fixture.result
            trees = classifier.probabilities(classifier.vector(row.features))
            handle.write(
                json.dumps(
                    {
                        "id": row.fixture.record.id,
                        "model_c": c_scored,
                        "model_b": b_scored,
                        "model_c_1x2_classifier": max(trees[result], PROBABILITY_FLOOR),
                        "lambda_c": [round(float(expected[name][index]), 4) for name in ("home", "away")],
                        "lambda_b": [round(elo_home, 4), round(elo_away, 4)],
                    },
                    separators=(",", ":"),
                )
                + "\n"
            )


# --- report --------------------------------------------------------------------


def _closing_totals(match: Match) -> dict[str, float] | None:
    over, under = match.odds_over_2_5, match.odds_under_2_5
    if over is None or under is None:
        return None
    over_share = (1 / over) / (1 / over + 1 / under)
    return {"over": over_share, "under": 1 - over_share}


def _group(market: str) -> str:
    return "double_chance" if market.startswith("double_chance:") else market


def _paired(first: list[float], second: list[float]) -> dict[str, float]:
    differences = [-math.log(a) + math.log(b) for a, b in zip(first, second)]
    count = len(differences)
    mean = sum(differences) / count
    variance = sum((value - mean) ** 2 for value in differences) / max(count - 1, 1)
    return {"mean_difference": mean, "z": mean / math.sqrt(variance / count) if variance > 0 else 0.0}


def _fixture_z(first: list[float], second: list[float], fixtures: list[int]) -> float:
    # Double chance scores three selections per fixture, and they are not independent.
    totals: dict[int, list[float]] = defaultdict(list)
    for a, b, fixture in zip(first, second, fixtures):
        totals[fixture].append(-math.log(a) + math.log(b))
    means = [sum(values) / len(values) for values in totals.values()]
    mean = sum(means) / len(means)
    variance = sum((value - mean) ** 2 for value in means) / max(len(means) - 1, 1)
    return mean / math.sqrt(variance / len(means)) if variance > 0 else 0.0


def _loss(values: list[float]) -> float:
    return sum(-math.log(value) for value in values) / len(values)


def _load(path: Path) -> dict[int, dict[str, Any]]:
    with path.open(encoding="utf-8") as handle:
        return {row["id"]: row for row in map(json.loads, handle)}


def build_report(paths: Iterable[Path], champion_path: Path, challengers_path: Path) -> dict[str, Any]:
    fixtures, matches = _corpus(paths)
    match_by_id = {fixture.record.id: _match_for(fixture, matches) for fixture in fixtures}
    champion = _load(champion_path)
    challengers = _load(challengers_path)
    shared = sorted(set(champion) & set(challengers))
    per_market: dict[str, dict[str, list[float]]] = defaultdict(lambda: defaultdict(list))
    fixtures_by_group: dict[str, list[int]] = defaultdict(list)
    outcomes: dict[str, Counter[str]] = defaultdict(Counter)
    for fixture_id in shared:
        row = challengers[fixture_id]
        for market, (outcome, champion_p) in champion[fixture_id]["scored"].items():
            if market not in row["model_c"] or market not in row["model_b"]:
                continue
            c_p, b_p = row["model_c"][market][1], row["model_b"][market][1]
            group = per_market[_group(market)]
            fixtures_by_group[_group(market)].append(fixture_id)
            group["champion"].append(champion_p)
            group["model_b"].append(b_p)
            group["model_c"].append(c_p)
            group["b_c_mix"].append((b_p + c_p) / 2)
            group["champion_c_mix"].append((champion_p + c_p) / 2)
            group["champion_b_c_mix"].append((champion_p + b_p + c_p) / 3)
            outcomes[market][outcome] += 1
    report: dict[str, Any] = {"fixtures": len(shared), "markets": {}}
    for group, models in per_market.items():
        base_losses = []
        for market, counts in outcomes.items():
            if _group(market) == group:
                total = sum(counts.values())
                base_losses.append((total, -sum(count / total * math.log(count / total) for count in counts.values())))
        base = sum(total * loss for total, loss in base_losses) / sum(total for total, _ in base_losses)
        entry: dict[str, Any] = {"rows": len(models["champion"]), "base_rate": round(base, 4)}
        for name, values in models.items():
            entry[name] = round(_loss(values), 4)
            if name != "champion":
                entry[f"{name}_z"] = round(_fixture_z(values, models["champion"], fixtures_by_group[group]), 1)
        report["markets"][group] = entry
    classifier = [challengers[i]["model_c_1x2_classifier"] for i in shared]
    report["markets"]["1x2"]["model_c_classifier"] = round(_loss(classifier), 4)
    report["markets"]["1x2"]["model_c_classifier_z"] = round(
        _paired(classifier, [champion[i]["scored"]["1x2"][1] for i in shared])["z"], 1
    )

    closing: dict[str, list[float]] = defaultdict(list)
    for fixture_id in shared:
        odds = _closing_totals(match_by_id[fixture_id])
        if odds is None:
            continue
        outcome, champion_p = champion[fixture_id]["scored"]["total_goals:2.5"]
        closing["market"].append(max(odds[outcome], PROBABILITY_FLOOR))
        closing["champion"].append(champion_p)
        closing["model_c"].append(challengers[fixture_id]["model_c"]["total_goals:2.5"][1])
        closing["model_b"].append(challengers[fixture_id]["model_b"]["total_goals:2.5"][1])
    if closing:
        report["total_goals_2.5_with_odds"] = {
            "rows": len(closing["market"]),
            **{name: round(_loss(values), 4) for name, values in closing.items()},
            "model_c_vs_market_z": round(_paired(closing["model_c"], closing["market"])["z"], 1),
            "champion_vs_market_z": round(_paired(closing["champion"], closing["market"])["z"], 1),
        }

    def calibration(model: str, market: str, positive: str) -> float:
        bins: dict[int, list[tuple[float, float]]] = defaultdict(list)
        for fixture_id in shared:
            source = champion[fixture_id]["scored"] if model == "champion" else challengers[fixture_id][model]
            outcome, p_actual = source[market]
            p = p_actual if outcome == positive else 1 - p_actual
            bins[min(int(p * 10), 9)].append((p, 1.0 if outcome == positive else 0.0))
        return sum(abs(sum(p for p, _ in values) - sum(hit for _, hit in values)) for values in bins.values()) / len(shared)

    report["calibration_error"] = {
        market: {model: round(calibration(model, market, positive), 4) for model in ("champion", "model_b", "model_c")}
        for market, positive in (("total_goals:2.5", "over"), ("btts", "yes"))
    }
    return report


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("command", choices=("champion", "challengers", "report", "manifest"))
    parser.add_argument("paths", nargs="+", type=Path, help="Football-Data CSV files or directories")
    parser.add_argument("--out", type=Path)
    parser.add_argument("--report-dir", type=Path, default=Path("."))
    parser.add_argument("--champion", type=Path)
    parser.add_argument("--challengers", type=Path)
    parser.add_argument("--export-dir", type=Path, help="write the trained goal models here")
    parser.add_argument("--code-commit", default="unknown")
    args = parser.parse_args(argv)
    if args.command == "champion":
        run_champion(args.paths, args.out)
    elif args.command == "challengers":
        run_challengers(args.paths, args.out, args.report_dir, args.export_dir)
    elif args.command == "manifest":
        write_manifest(args.export_dir, args.paths, args.code_commit)
    else:
        print(json.dumps(build_report(args.paths, args.champion, args.challengers), indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
