"""Train, validate and export architecture v2 Model C (section 8.3).

Model C is a gradient-boosted tree model over the point-in-time features in
`prediction_engine.form_features`. This script is the whole of its training
pipeline:

1. **One chronological pass** over the corpus builds a feature row for every
   fixture before that fixture is recorded, with the same `FormFeatureBook`
   the engine serves from, and scores Elo (Model B) on the same pass.
2. **Three time-ordered windows, never a random split** (section 24.1):
   training before `--validation-from`, validation up to `--holdout-from`,
   holdout after. `split_windows` is the only function that assigns rows to
   windows and it refuses unordered boundaries, so the prohibition on random
   splitting is enforced in code rather than in a comment.
3. **Hyperparameters are chosen on validation only** (section 24.3). The
   chosen configuration is then refitted on training plus validation and
   scored once on the holdout, which no choice ever looked at.
4. **Export with a parity check.** The trees are written in the stdlib format
   `prediction_engine.gbdt` reads, and the export is refused unless that
   evaluator reproduces LightGBM's own probabilities on holdout rows.

LightGBM and NumPy are offline training dependencies only
(`services/model-lab/requirements-train.txt`); the serving engine never
imports them (ADR 0002).

Run `task model:c-train`; the README explains where the corpus lives.
"""

from __future__ import annotations

import argparse
from dataclasses import dataclass
from datetime import datetime, timezone
import hashlib
import json
import math
from pathlib import Path
import sys
from typing import Iterable, Sequence

from prediction_engine.elo import DEFAULT_ELO_CONFIG, EloRatingBook
from prediction_engine.form_features import FEATURE_NAMES, FEATURE_VERSION, FormFeatureBook
from prediction_engine.gbdt import ARTIFACT_FORMAT, TreeEnsemble
from prediction_engine.model_c import MARKET_FAMILY, MODEL_KEY

from model_lab.elo_walk_forward import (
    CorpusFixture,
    _csv_paths,
    _kickoff_slots,
    corpus_fixtures,
    league_frequency_predictions,
    paired_log_loss,
    score,
)
from model_lab.football_data import load_matches


OUTCOMES = ("home", "draw", "away")
DEFAULT_VALIDATION_FROM = "2021-07-01"
DEFAULT_HOLDOUT_FROM = "2022-07-01"
DEFAULT_ARTIFACT = (
    Path(__file__).resolve().parents[2] / "prediction-engine" / "prediction_engine" / "artifacts" / "model_c_1x2.json"
)
# Validation candidates. Small on purpose: every extra configuration is one
# more chance to fit the validation window instead of football.
PARAMETER_GRID: tuple[dict[str, object], ...] = (
    {"num_leaves": 7, "min_data_in_leaf": 400},
    {"num_leaves": 15, "min_data_in_leaf": 400},
    {"num_leaves": 15, "min_data_in_leaf": 1500},
    {"num_leaves": 31, "min_data_in_leaf": 1500},
)
BASE_PARAMETERS: dict[str, object] = {
    "objective": "multiclass",
    "num_class": 3,
    "learning_rate": 0.05,
    "feature_fraction": 0.8,
    "bagging_fraction": 0.8,
    "bagging_freq": 1,
    "lambda_l2": 1.0,
    "seed": 20261104,
    "deterministic": True,
    "force_row_wise": True,
    "num_threads": 4,
    "verbosity": -1,
}
MAX_ROUNDS = 2000
EARLY_STOPPING_ROUNDS = 50
PARITY_SAMPLE = 2000
PARITY_TOLERANCE = 1e-9


@dataclass(frozen=True)
class FeatureRow:
    fixture_id: int
    kickoff_at: datetime
    league_key: str
    features: dict[str, float | None]
    label: str
    elo: dict[str, float]
    market: dict[str, float] | None


def feature_rows(fixtures: list[CorpusFixture]) -> list[FeatureRow]:
    """A feature row and an Elo forecast for every fixture, from one pass.

    Every fixture in a kick-off slot is described before any of that slot's
    results is recorded, the same rule the Elo walk-forward uses, so
    simultaneous matches never see each other.
    """
    form = FormFeatureBook()
    elo = EloRatingBook(DEFAULT_ELO_CONFIG)
    rows: list[FeatureRow] = []
    for slot in _kickoff_slots(fixtures):
        for fixture in slot:
            rows.append(
                FeatureRow(
                    fixture_id=fixture.record.id,
                    kickoff_at=fixture.record.kickoff_at,
                    league_key=fixture.league_key,
                    features=form.features(fixture.record),
                    label=fixture.result,
                    elo=elo.predict(fixture.record).match_result,
                    market=_devig(fixture),
                )
            )
        for fixture in slot:
            form.record(fixture.record)
            elo.record(fixture.record)
    return rows


def split_windows(
    rows: Sequence[FeatureRow], *, validation_from: datetime, holdout_from: datetime
) -> tuple[list[FeatureRow], list[FeatureRow], list[FeatureRow]]:
    """Training, validation and holdout rows by kick-off, never by shuffle."""
    if not validation_from < holdout_from:
        raise ValueError("validation must start before the holdout")
    training = [row for row in rows if row.kickoff_at < validation_from]
    validation = [row for row in rows if validation_from <= row.kickoff_at < holdout_from]
    holdout = [row for row in rows if row.kickoff_at >= holdout_from]
    if training and validation and max(row.kickoff_at for row in training) >= min(row.kickoff_at for row in validation):
        raise AssertionError("training rows overlap validation")
    return training, validation, holdout


def export_trees(booster: object, *, metadata: dict[str, object]) -> str:
    """LightGBM trees in the stdlib `sokalens-gbdt-v1` format."""
    dumped = booster.dump_model()  # type: ignore[attr-defined]
    if dumped["num_class"] != len(OUTCOMES):
        raise ValueError("Model C exports a three-class 1X2 model")
    if list(dumped["feature_names"]) != list(FEATURE_NAMES):
        raise ValueError("booster feature order differs from form_features.FEATURE_NAMES")
    trees = [_flatten(info["tree_structure"]) for info in dumped["tree_info"]]
    payload = {
        "format": ARTIFACT_FORMAT,
        "feature_names": list(FEATURE_NAMES),
        "classes": list(OUTCOMES),
        **metadata,
        "trees": trees,
    }
    return json.dumps(payload, separators=(",", ":"), sort_keys=True)


def _flatten(root: dict[str, object]) -> list[list[float]]:
    nodes: list[list[float]] = []

    def visit(node: dict[str, object]) -> int:
        position = len(nodes)
        if "leaf_value" in node:
            nodes.append([float(node["leaf_value"])])  # type: ignore[arg-type]
            return position
        if node.get("decision_type") != "<=":
            raise ValueError(f"unsupported split type {node.get('decision_type')!r}")
        nodes.append([])
        left = visit(node["left_child"])  # type: ignore[arg-type]
        right = visit(node["right_child"])  # type: ignore[arg-type]
        missing = {"None": 0, "Zero": 1, "NaN": 2}[str(node["missing_type"])]
        nodes[position] = [
            float(node["split_feature"]),  # type: ignore[arg-type]
            float(node["threshold"]),  # type: ignore[arg-type]
            float(left),
            float(right),
            1.0 if node["default_left"] else 0.0,
            float(missing),
            float(node["internal_value"]),  # type: ignore[arg-type]
        ]
        return position

    visit(root)
    return nodes


def train(
    fixtures: list[CorpusFixture],
    *,
    validation_from: datetime,
    holdout_from: datetime,
    dataset_hash: str,
    code_commit: str,
    progress: bool = False,
) -> tuple[str, dict[str, object]]:
    """The exported artifact and the evaluation report."""
    import lightgbm
    import numpy

    def log(message: str) -> None:
        if progress:
            print(message, file=sys.stderr, flush=True)

    log(f"building features for {len(fixtures)} fixtures")
    rows = feature_rows(fixtures)
    training, validation, holdout = split_windows(rows, validation_from=validation_from, holdout_from=holdout_from)
    if not training or not validation or not holdout:
        raise ValueError("every window needs fixtures; move the boundaries")
    log(f"training {len(training)}, validation {len(validation)}, holdout {len(holdout)}")

    def matrix(window: Sequence[FeatureRow]) -> object:
        return numpy.array(
            [[math.nan if row.features[name] is None else row.features[name] for name in FEATURE_NAMES] for row in window],
            dtype=numpy.float64,
        )

    def labels(window: Sequence[FeatureRow]) -> object:
        return numpy.array([OUTCOMES.index(row.label) for row in window], dtype=numpy.int32)

    training_x, validation_x, holdout_x = matrix(training), matrix(validation), matrix(holdout)
    training_y, validation_y = labels(training), labels(validation)

    trials: list[dict[str, object]] = []
    for candidate in PARAMETER_GRID:
        parameters = {**BASE_PARAMETERS, **candidate}
        booster = lightgbm.train(
            parameters,
            lightgbm.Dataset(training_x, training_y, feature_name=list(FEATURE_NAMES), free_raw_data=False),
            num_boost_round=MAX_ROUNDS,
            valid_sets=[lightgbm.Dataset(validation_x, validation_y, feature_name=list(FEATURE_NAMES))],
            callbacks=[lightgbm.early_stopping(EARLY_STOPPING_ROUNDS, verbose=False)],
        )
        trial = {
            **candidate,
            "rounds": int(booster.best_iteration),
            "validation_log_loss": round(float(booster.best_score["valid_0"]["multi_logloss"]), 5),
        }
        trials.append(trial)
        log(f"trial {trial}")
    best = min(trials, key=lambda trial: trial["validation_log_loss"])
    chosen = {key: best[key] for key in PARAMETER_GRID[0]}
    # Refit on training plus validation. A larger window supports a few more
    # rounds; the scaling is the usual one and is fixed, not tuned.
    rounds = max(1, round(int(best["rounds"]) * (len(training) + len(validation)) / len(training)))
    final_rows = [*training, *validation]
    final = lightgbm.train(
        {**BASE_PARAMETERS, **chosen},
        lightgbm.Dataset(matrix(final_rows), labels(final_rows), feature_name=list(FEATURE_NAMES)),
        num_boost_round=rounds,
    )

    holdout_probabilities = final.predict(holdout_x)
    metadata: dict[str, object] = {
        "model_key": MODEL_KEY,
        "market_family": MARKET_FAMILY,
        "feature_version": FEATURE_VERSION,
        "runtime": "python-stdlib-gbdt",
        "training_start": training[0].kickoff_at.isoformat(),
        "training_end": final_rows[-1].kickoff_at.isoformat(),
        "validation_window": {
            "from": validation_from.isoformat(),
            "until": holdout_from.isoformat(),
            "fixtures": len(validation),
        },
        "hyperparameters": {**{k: v for k, v in BASE_PARAMETERS.items() if k != "num_threads"}, **chosen, "rounds": rounds},
        "training_dataset_hash": dataset_hash,
        "code_commit": code_commit,
    }
    metadata["model_version"] = "model-c-" + hashlib.sha256(
        json.dumps({k: metadata[k] for k in ("hyperparameters", "training_dataset_hash", "feature_version")}, sort_keys=True).encode()
    ).hexdigest()[:12]
    artifact = export_trees(final, metadata=metadata)

    ensemble = TreeEnsemble.from_json(artifact)
    step = max(1, len(holdout) // PARITY_SAMPLE)
    worst = 0.0
    for index in range(0, len(holdout), step):
        ours = ensemble.probabilities(ensemble.vector(holdout[index].features))
        for class_index, outcome in enumerate(OUTCOMES):
            worst = max(worst, abs(ours[outcome] - float(holdout_probabilities[index][class_index])))
    if worst > PARITY_TOLERANCE:
        raise AssertionError(f"stdlib evaluator disagrees with LightGBM by {worst}")
    log(f"parity ok, worst difference {worst:.2e}")

    holdout_labels = {row.fixture_id: row.label for row in holdout}
    model_c = {
        row.fixture_id: dict(zip(OUTCOMES, (float(value) for value in holdout_probabilities[index])))
        for index, row in enumerate(holdout)
    }
    elo = {row.fixture_id: row.elo for row in holdout}
    market = {row.fixture_id: row.market for row in holdout if row.market is not None}
    frequency = league_frequency_predictions(fixtures, score_from=holdout_from)
    frequency = {fixture_id: value for fixture_id, value in frequency.items() if fixture_id in holdout_labels}
    mix = {
        fixture_id: {outcome: (model_c[fixture_id][outcome] + elo[fixture_id][outcome]) / 2 for outcome in OUTCOMES}
        for fixture_id in model_c
    }
    with_odds = set(market)
    gain = final.feature_importance(importance_type="gain")
    total_gain = float(sum(gain)) or 1.0
    report: dict[str, object] = {
        "model_version": metadata["model_version"],
        "artifact_bytes": len(artifact),
        "trees": len(ensemble.trees),
        "parity_worst_difference": worst,
        "windows": {"training": len(training), "validation": len(validation), "holdout": len(holdout)},
        "validation_trials": trials,
        "chosen": {**chosen, "rounds": rounds},
        "all_holdout": {
            "model_c": score(model_c, holdout_labels),
            "elo": score(elo, holdout_labels),
            "model_c_elo_mix": score(mix, holdout_labels),
            "league_frequency": score(frequency, holdout_labels),
        },
        "holdout_with_odds": {
            "model_c": score({k: v for k, v in model_c.items() if k in with_odds}, holdout_labels),
            "elo": score({k: v for k, v in elo.items() if k in with_odds}, holdout_labels),
            "market": score(market, holdout_labels),
        },
        "paired_log_loss": {
            "model_c_vs_elo": paired_log_loss(model_c, elo, holdout_labels),
            "model_c_vs_league_frequency": paired_log_loss(model_c, frequency, holdout_labels),
            "model_c_vs_market": paired_log_loss({k: v for k, v in model_c.items() if k in with_odds}, market, holdout_labels),
            "mix_vs_elo": paired_log_loss(mix, elo, holdout_labels),
        },
        "agreement": _agreement(model_c, elo),
        "feature_gain_share": {
            name: round(float(value) / total_gain, 4)
            for name, value in sorted(zip(FEATURE_NAMES, gain), key=lambda item: -item[1])
        },
    }
    return artifact, report


def _agreement(first: dict[int, dict[str, float]], second: dict[int, dict[str, float]]) -> dict[str, float]:
    shared = sorted(set(first) & set(second))
    same_pick = sum(
        1 for key in shared if max(OUTCOMES, key=first[key].get) == max(OUTCOMES, key=second[key].get)
    )
    spread = [max(abs(first[key][o] - second[key][o]) for o in OUTCOMES) for key in shared]
    return {
        "fixtures": len(shared),
        "same_pick_share": round(same_pick / len(shared), 4) if shared else 0.0,
        "mean_max_spread": round(sum(spread) / len(spread), 4) if spread else 0.0,
    }


def _devig(fixture: CorpusFixture) -> dict[str, float] | None:
    odds = fixture.odds
    if odds is None or min(odds.home_win, odds.draw, odds.away_win) <= 1.0:
        return None
    inverse = {"home": 1 / odds.home_win, "draw": 1 / odds.draw, "away": 1 / odds.away_win}
    total = sum(inverse.values())
    return {outcome: value / total for outcome, value in inverse.items()}


def dataset_hash(files: Iterable[Path]) -> str:
    digest = hashlib.sha256()
    for path in sorted(files, key=lambda item: item.as_posix()):
        digest.update(path.name.encode())
        digest.update(hashlib.sha256(path.read_bytes()).digest())
    return digest.hexdigest()


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("paths", nargs="+", type=Path, help="Football-Data CSV files or directories")
    parser.add_argument("--validation-from", default=DEFAULT_VALIDATION_FROM)
    parser.add_argument("--holdout-from", default=DEFAULT_HOLDOUT_FROM)
    parser.add_argument("--artifact", type=Path, default=DEFAULT_ARTIFACT, help="where to write the exported trees")
    parser.add_argument("--report", type=Path, help="write the JSON report here as well as to stdout")
    parser.add_argument("--code-commit", default="unknown", help="commit the artifact is trained from")
    args = parser.parse_args(argv)

    files = _csv_paths(args.paths)
    fixtures = corpus_fixtures(load_matches(files).matches)
    artifact, report = train(
        fixtures,
        validation_from=_date(args.validation_from),
        holdout_from=_date(args.holdout_from),
        dataset_hash=dataset_hash(files),
        code_commit=args.code_commit,
        progress=True,
    )
    args.artifact.parent.mkdir(parents=True, exist_ok=True)
    args.artifact.write_text(artifact + "\n", encoding="utf-8")
    report = {"files": len(files), "artifact": str(args.artifact.name), **report}
    text = json.dumps(report, indent=2, sort_keys=True)
    if args.report:
        args.report.write_text(text + "\n", encoding="utf-8")
    print(text)
    return 0


def _date(value: str) -> datetime:
    return datetime.strptime(value, "%Y-%m-%d").replace(tzinfo=timezone.utc)


if __name__ == "__main__":
    raise SystemExit(main())
