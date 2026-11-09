"""Shadow forecasts for Models B and C across every market they can speak to.

`challengers` runs the 1X2 shadow. This module runs beside it, attached to the
same three commands. Nothing here publishes: `promoted_markets` reads the stored
forecasts for the markets where they beat the champion.

**What each model forecasts.** Model C predicts goals with four Poisson tree
models and each counting statistic with one more; every market comes from those
expected counts. Model B keeps Elo's own 1X2, double chance and draw no bet, and
maps the rating gap and the league's goal rate to expected goals for the rest. It
never reads a team's goal rate, which is the input Elo exists to avoid, and it
forecasts no counting statistic: results say nothing about corners.

**Not covered.** `possession_winner` has no training data. `team_to_score_first`
cannot be graded fairly: production results carry no first-goal team, so only
0-0 matches would ever grade, and a market scored only on 0-0 matches is noise.

**Grading.** Every market is graded by `settle_prediction`, the published path's
own function, so a challenger cannot be scored by a different rule from the
champion it is compared with.

**One row per model and stage.** A document of market -> selection -> probability
stays small, where a row per market would add tens of thousands of rows a day. The
champion gets a row under its own registry id. Each model's own version identifies
the artifacts, and comparisons are paired on the fixture and lifecycle stage.
"""

from __future__ import annotations

from collections import defaultdict
from collections.abc import Callable, Iterator, Mapping, Sequence
from dataclasses import dataclass
from datetime import datetime, timedelta
import hashlib
import json
import math
from pathlib import Path

from prediction_engine.combination_markets import MarketLeg, joint_probability
from prediction_engine.elo import DEFAULT_ELO_CONFIG, EloRatingBook
from prediction_engine.features import FixtureRecord
from prediction_engine.form_features import FormFeatureBook
from prediction_engine.gbdt import TreeEnsemble
from prediction_engine.model_a import ModelAInputs, run_model_a
from prediction_engine.settlement import ActualFixtureResult, settle_prediction
from prediction_engine.stat_features import StatFeatureBook


ARTIFACT_DIRECTORY = Path(__file__).resolve().parent / "artifacts" / "challenger_markets"
MODEL_C_KEY = "model-c-markets"
MODEL_B_KEY = "elo-markets"
REGISTRY_FAMILY = "all-markets"
LIVE = "live"
REPLAY = "replay"
RESULT_LAG = timedelta(hours=3)
REPLAY_LEAD = timedelta(hours=1)
SETTLEMENT_CONTRACT = "shadow-markets-v1"
EVALUATION_CONTRACT = "shadow-markets-v1"
PROBABILITY_FLOOR = 1e-6
MIN_EXPECTED_GOALS = 0.02
MINIMUM_CHAMPION_SAMPLES = 5
_ROUNDING = 5
_EXTRA_TIME_STATUSES = ("aet", "pen")
_FINISHED_STATUSES = ("ft", "aet", "pen", "finished", "match finished", "full time", "fulltime")

TOTAL_GOAL_LINES = (1.5, 2.5, 3.5, 4.5)
CORRECT_SCORE_GOALS = 5
# Selections here are not mutually exclusive, so each is scored as its own yes/no.
INDEPENDENT_SELECTIONS = {"double_chance"}
# Market -> published lines. The pipeline's own ladders.
COUNT_LINES: dict[str, tuple[float, ...]] = {
    "total_corners": (7.5, 8.5, 9.5, 10.5),
    "team_corners_home": (3.5, 4.5, 5.5),
    "team_corners_away": (3.5, 4.5, 5.5),
    "total_cards": (2.5, 3.5, 4.5, 5.5),
    "total_fouls": (18.5, 21.5, 24.5),
    "total_shots": (18.5, 21.5, 24.5),
    "shots_on_target": (6.5, 7.5, 8.5),
}
# The trained count models each market sums.
COUNT_TARGETS: dict[str, tuple[str, ...]] = {
    "total_corners": ("home_corners", "away_corners"),
    "team_corners_home": ("home_corners",),
    "team_corners_away": ("away_corners",),
    "total_cards": ("total_cards",),
    "total_fouls": ("total_fouls",),
    "total_shots": ("total_shots",),
    "shots_on_target": ("shots_on_target",),
}
# The champion's rolling averages for the same markets.
CHAMPION_COUNT_METRICS: dict[str, tuple[str, ...]] = {
    "total_corners": ("home_corners_for", "away_corners_for"),
    "team_corners_home": ("home_corners_for",),
    "team_corners_away": ("away_corners_for",),
    "total_cards": ("home_card_points", "away_card_points"),
    "total_fouls": ("home_fouls", "away_fouls"),
    "total_shots": ("home_shots_total", "away_shots_total"),
    "shots_on_target": ("home_shots_on_target", "away_shots_on_target"),
}
GOAL_TARGETS = ("home", "away", "half_time_home", "half_time_away")


# --- distributions and documents ------------------------------------------------


def under_probability(mean: float, dispersion: float, line: float) -> float:
    """P(count < line) for a negative binomial with variance mean + dispersion * mean^2.

    A dispersion of zero is the Poisson distribution.
    """
    mean = max(mean, 1e-6)
    limit = math.floor(line)
    if dispersion <= 1e-9:
        term = math.exp(-mean)
        total = term
        for count in range(1, limit + 1):
            term *= mean / count
            total += term
        return min(total, 1.0)
    size = 1.0 / dispersion
    success = size / (size + mean)
    term = success**size
    total = term
    for count in range(1, limit + 1):
        term *= (count - 1 + size) / count * (1.0 - success)
        total += term
    return min(total, 1.0)


def _rounded(values: Mapping[str, float]) -> dict[str, float]:
    return {key: round(float(value), _ROUNDING) for key, value in values.items()}


def goal_document(markets: Mapping[str, object], matrix: list[list[float]]) -> dict[str, dict[str, float]]:
    """Every goal-based market, named as settlement expects its selections."""
    document: dict[str, dict[str, float]] = {
        "1x2": _rounded(_pick(markets["match_result"], ("home", "draw", "away"))),
        "double_chance": _rounded(_pick(markets["double_chance"], ("home_or_draw", "draw_or_away", "home_or_away"))),
        "draw_no_bet": _rounded(_pick(markets["draw_no_bet"], ("home", "away"))),
        "btts": _rounded(_pick(markets["btts"], ("yes", "no"))),
        "odd_even_total_goals": _rounded(_pick(markets["odd_even_total_goals"], ("odd", "even"))),
        "half_time_result": _rounded(_pick(markets["half_time_result"], ("home", "draw", "away"))),
        "highest_scoring_half": _rounded(_pick(markets["highest_scoring_half"], ("first_half", "second_half", "equal"))),
        "btts_match_result": _rounded(
            _pick(markets["btts_match_result"], ("home_yes", "draw_yes", "away_yes", "home_no", "draw_no", "away_no"))
        ),
    }
    totals = markets["total_goals"]
    for line in TOTAL_GOAL_LINES:
        document[f"total_goals:{line:g}"] = _rounded(
            {f"over_{line:g}": totals[f"over_{line}"], f"under_{line:g}": totals[f"under_{line}"]}  # type: ignore[index]
        )
    btts_total = markets["btts_total_goals"]
    document["btts_total_goals:2.5"] = _rounded(
        {name: btts_total[name] for name in ("yes_over_2.5", "yes_under_2.5", "no_over_2.5", "no_under_2.5")}  # type: ignore[index]
    )
    document["result_total_goals:2.5"] = _rounded(
        {
            f"{result}_{side}_2.5": joint_probability(matrix, MarketLeg.match_result(result), MarketLeg.total_goals(side, 2.5))
            for result in ("home", "draw", "away")
            for side in ("over", "under")
        }
    )
    document["correct_score"] = _correct_score(matrix)
    return document


def _pick(values: object, names: Sequence[str]) -> dict[str, float]:
    return {name: float(values[name]) for name in names}  # type: ignore[index]


def _correct_score(matrix: list[list[float]]) -> dict[str, float]:
    total = sum(sum(row) for row in matrix) or 1.0
    cells = {
        f"{home}-{away}": matrix[home][away] / total
        for home in range(min(CORRECT_SCORE_GOALS + 1, len(matrix)))
        for away in range(min(CORRECT_SCORE_GOALS + 1, len(matrix[home])))
    }
    cells["other"] = max(1.0 - sum(cells.values()), 0.0)
    return _rounded(cells)


def count_document(market: str, mean: float, dispersion: float) -> dict[str, dict[str, float]]:
    document: dict[str, dict[str, float]] = {}
    for line in COUNT_LINES[market]:
        under = under_probability(mean, dispersion, line)
        document[f"{market}:{line:g}"] = _rounded({f"over_{line:g}": 1.0 - under, f"under_{line:g}": under})
    return document


def champion_count_document(statistics: Mapping[str, float | None], samples: Mapping[str, int]) -> dict[str, dict[str, float]]:
    """The champion's counts: rolling averages and Poisson, where it would publish them."""
    document: dict[str, dict[str, float]] = {}
    for market, metrics in CHAMPION_COUNT_METRICS.items():
        values = [statistics.get(metric) for metric in metrics]
        if any(value is None for value in values) or min(samples.get(metric, 0) for metric in metrics) < MINIMUM_CHAMPION_SAMPLES:
            continue
        document.update(count_document(market, float(sum(values)), 0.0))  # type: ignore[arg-type]
    return document


def _score_matrix_result(home: float, away: float, half_home: float, half_away: float) -> object:
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


# --- artifacts ------------------------------------------------------------------


@dataclass(frozen=True)
class MarketModels:
    goal_models: Mapping[str, TreeEnsemble]
    count_models: Mapping[str, TreeEnsemble]
    dispersions: Mapping[str, float]
    elo_goal_map: Mapping[str, object]
    manifest: Mapping[str, object]
    model_c_version: str
    model_b_version: str
    artifact_hash: str


def load_market_models(directory: Path = ARTIFACT_DIRECTORY) -> MarketModels:
    raw_manifest = (directory / "manifest.json").read_text(encoding="utf-8")
    manifest = json.loads(raw_manifest)
    digest = hashlib.sha256(raw_manifest.encode("utf-8"))

    def load(name: str) -> TreeEnsemble:
        ensemble = TreeEnsemble.load(directory / f"{name}.json")
        digest.update(ensemble.artifact_hash.encode("utf-8"))
        return ensemble

    goal_models = {name: load(f"goals_{name}") for name in GOAL_TARGETS}
    count_models = {name: load(f"count_{name}") for name in manifest["count_targets"]}
    artifact_hash = digest.hexdigest()
    return MarketModels(
        goal_models=goal_models,
        count_models=count_models,
        dispersions={name: float(value) for name, value in manifest["dispersions"].items()},
        elo_goal_map=manifest["elo_goal_map"],
        manifest=manifest,
        model_c_version=f"{MODEL_C_KEY}-{artifact_hash[:12]}",
        model_b_version=f"{MODEL_B_KEY}-{_hash({'elo': manifest['elo_goal_map'], 'config': repr(DEFAULT_ELO_CONFIG)})[:12]}",
        artifact_hash=artifact_hash,
    )


def _hash(value: object) -> str:
    return hashlib.sha256(json.dumps(value, sort_keys=True, default=str).encode("utf-8")).hexdigest()


def model_c_document(features: Mapping[str, float | None], models: MarketModels) -> dict[str, dict[str, float]]:
    expected = {
        name: ensemble.expected_count(ensemble.vector(features)) for name, ensemble in models.goal_models.items()
    }
    result = _score_matrix_result(expected["home"], expected["away"], expected["half_time_home"], expected["half_time_away"])
    document = goal_document(result.markets, result.score_matrix)  # type: ignore[attr-defined]
    counts = {name: ensemble.expected_count(ensemble.vector(features)) for name, ensemble in models.count_models.items()}
    for market, targets in COUNT_TARGETS.items():
        mean = sum(counts[target] for target in targets)
        # Two summed sides each with dispersion d give about d/2 on the total.
        dispersion = sum(models.dispersions[target] for target in targets) / len(targets) / len(targets)
        document.update(count_document(market, mean, dispersion))
    return document


def elo_expected_goals(rating_differential: float, league_goals: float, goal_map: Mapping[str, object]) -> tuple[float, float]:
    gap = rating_differential / 400.0
    terms = (1.0, gap, gap * gap, math.log(max(league_goals, 0.1)))
    home = math.exp(sum(a * float(b) for a, b in zip(terms, goal_map["home"])))  # type: ignore[arg-type]
    away = math.exp(sum(a * float(b) for a, b in zip(terms, goal_map["away"])))  # type: ignore[arg-type]
    return home, away


def model_b_document(prediction: object, league_goals: float | None, goal_map: Mapping[str, object]) -> dict[str, dict[str, float]]:
    home, away = elo_expected_goals(prediction.rating_differential, league_goals or 2.7, goal_map)  # type: ignore[attr-defined]
    result = _score_matrix_result(
        home,
        away,
        home * float(goal_map["half_time_home_share"]),  # type: ignore[arg-type]
        away * float(goal_map["half_time_away_share"]),  # type: ignore[arg-type]
    )
    # Elo's own 1X2 stays its 1X2; the goal map only adds what Elo cannot say.
    markets = {
        **result.markets,  # type: ignore[attr-defined]
        "match_result": prediction.match_result,  # type: ignore[attr-defined]
        "double_chance": prediction.double_chance,  # type: ignore[attr-defined]
        "draw_no_bet": prediction.draw_no_bet,  # type: ignore[attr-defined]
    }
    return goal_document(markets, result.score_matrix)  # type: ignore[attr-defined]


# --- grading --------------------------------------------------------------------


def grade(document: Mapping[str, Mapping[str, float]], actual: ActualFixtureResult) -> dict[str, list[object]]:
    """Market -> [what happened, the probability given to it], by the published rules.

    A market settlement cannot resolve (no statistic, a drawn draw-no-bet) is left
    out, exactly as it stays out of published accuracy.
    """
    scored: dict[str, list[object]] = {}
    for key, selections in document.items():
        market = key.split(":", 1)[0]
        if market in INDEPENDENT_SELECTIONS:
            for selection, probability in selections.items():
                result = settle_prediction(market, selection, actual)
                if result.status == "settled" and result.is_correct is not None:
                    hit = result.is_correct
                    scored[f"{key}:{selection}"] = ["yes" if hit else "no", _clip(probability if hit else 1.0 - probability)]
            continue
        winner: str | None = None
        graded = True
        for selection in selections:
            if selection == "other":
                continue
            result = settle_prediction(market, selection, actual)
            if result.status != "settled" or result.is_correct is None:
                graded = False
                break
            if result.is_correct:
                winner = selection
                break
        if not graded:
            continue
        if winner is None:
            if "other" not in selections:
                continue
            winner = "other"
        total = sum(float(value) for value in selections.values())
        if total > 0:
            scored[key] = [winner, _clip(float(selections[winner]) / total)]
    return scored


def _clip(probability: float) -> float:
    return round(max(float(probability), PROBABILITY_FLOOR), 7)


def market_group(key: str) -> str:
    """The reporting market: double chance's three yes/no scores count as one."""
    return "double_chance" if key.startswith("double_chance:") else key


def compare(challenger: Mapping[int, Mapping[str, float]], champion: Mapping[int, Mapping[str, float]]) -> dict[str, dict[str, object]]:
    """Paired log loss per market. Each fixture counts once; negative favours the challenger."""
    losses: dict[str, dict[int, list[tuple[float, float]]]] = defaultdict(lambda: defaultdict(list))
    for fixture, scores in challenger.items():
        reference = champion.get(fixture)
        if reference is None:
            continue
        for key, probability in scores.items():
            if key in reference:
                losses[market_group(key)][fixture].append((-math.log(probability), -math.log(reference[key])))
    summary: dict[str, dict[str, object]] = {}
    for market, by_fixture in losses.items():
        own = [sum(pair[0] for pair in pairs) / len(pairs) for pairs in by_fixture.values()]
        other = [sum(pair[1] for pair in pairs) / len(pairs) for pairs in by_fixture.values()]
        differences = [a - b for a, b in zip(own, other)]
        count = len(differences)
        mean = sum(differences) / count
        entry: dict[str, object] = {
            "paired_with_champion": count,
            "log_loss": round(sum(own) / count, 6),
            "champion_log_loss": round(sum(other) / count, 6),
            "mean_log_loss_difference": round(mean, 6),
        }
        if count >= 2:
            variance = sum((value - mean) ** 2 for value in differences) / (count - 1)
            error = math.sqrt(variance / count)
            entry["standard_error"] = round(error, 6)
            entry["z"] = round(mean / error, 3) if error > 0 else None
        summary[market] = entry
    return summary


def decision(paired: int, *, minimum_samples: int) -> tuple[str, str]:
    """Never `promote`: a person reads the evidence and decides (section 21.5)."""
    if paired < minimum_samples:
        return "extend", f"{paired} settled forecasts paired with the champion; {minimum_samples} are required"
    return "pending", f"{paired} paired forecasts meet the sample minimum; promotion is a reviewed decision"


# --- forecasting ----------------------------------------------------------------


@dataclass(frozen=True)
class MarketTarget:
    fixture: FixtureRecord
    as_of: datetime
    lifecycle_stage: str


def forecast_documents(
    history: Iterator[tuple[FixtureRecord, Mapping[str, tuple[int | None, int | None]]]],
    targets: Sequence[MarketTarget],
    models: MarketModels,
) -> list[tuple[MarketTarget, dict[str, dict[str, float]], dict[str, dict[str, float]], tuple[str, ...]]]:
    """Model C and Model B documents per target, from one ordered pass over history."""
    pending = sorted(targets, key=lambda target: (target.as_of, target.fixture.id))
    elo = EloRatingBook(DEFAULT_ELO_CONFIG)
    form = FormFeatureBook()
    stats = StatFeatureBook()
    results = []
    position = 0

    def emit(target: MarketTarget) -> None:
        features = {**form.features(target.fixture), **stats.features(target.fixture)}
        rating = elo.predict(target.fixture)
        results.append(
            (
                target,
                model_c_document(features, models),
                model_b_document(rating, features.get("league_goals_per_match"), models.elo_goal_map),
                rating.fallback_flags,
            )
        )

    previous: tuple[datetime, int] | None = None
    for record, statistics in history:
        key = (record.kickoff_at, record.id)
        if previous is not None and key < previous:
            raise ValueError("history must be ordered by kick-off")
        previous = key
        while position < len(pending) and record.kickoff_at > pending[position].as_of - RESULT_LAG:
            emit(pending[position])
            position += 1
        elo.record(record)
        form.record(record)
        stats.record(record, statistics)
    for target in pending[position:]:
        emit(target)
    return results


# --- commands -------------------------------------------------------------------


ChampionDocument = Callable[[FixtureRecord, datetime], "dict[str, dict[str, float]] | None"]


def run_market_forecasts(
    repository: "MarketRepository",
    *,
    as_of: datetime,
    origin: str,
    limit: int,
    history_days: int,
    min_interval: timedelta,
    champion: ChampionDocument,
    models: MarketModels,
    code_commit: str,
) -> dict[str, object]:
    model_ids = repository.ensure_registered(models, code_commit=code_commit)
    champion_id = repository.champion_model_id("1x2")
    repository.commit()
    summary: dict[str, object] = {"origin": origin, "market_forecasts": 0}
    if origin == LIVE:
        last = repository.last_live_forecast_at()
        if last is not None and as_of - last < min_interval:
            repository.commit()
            return {**summary, "skipped": "min_interval"}
        targets = repository.live_targets(as_of=as_of, limit=limit, model_ids=list(model_ids.values()))
    else:
        targets = repository.replay_targets(as_of=as_of, limit=limit, model_ids=list(model_ids.values()))
    if not targets:
        repository.commit()
        return {**summary, "targets": 0}
    earliest = min(target.as_of for target in targets)
    latest = max(target.as_of for target in targets)
    forecasts = forecast_documents(
        repository.history(since=earliest - timedelta(days=history_days), until=latest - RESULT_LAG), targets, models
    )
    champion_documents = {
        (target.fixture.id, target.lifecycle_stage): champion(target.fixture, target.as_of) for target in targets
    }
    inserted = 0
    for target, model_c, model_b, flags in forecasts:
        inserted += repository.insert(model_ids[MODEL_C_KEY], target, origin, model_c, ())
        inserted += repository.insert(model_ids[MODEL_B_KEY], target, origin, model_b, flags)
        document = champion_documents.get((target.fixture.id, target.lifecycle_stage))
        if document and champion_id:
            repository.insert(champion_id, target, origin, document, ())
    repository.commit()
    return {
        **summary,
        "targets": len(targets),
        "champion_paired": sum(1 for value in champion_documents.values() if value),
        "market_forecasts": inserted,
    }


def run_market_settlement(repository: "MarketRepository", *, settled_at: datetime, limit: int) -> dict[str, object]:
    settled = 0
    for fixture_id, rows, actual, extra_time in repository.settlement_batches(limit=limit):
        for row in rows:
            scored = {} if extra_time else grade(row["markets"], actual)  # type: ignore[arg-type]
            live = row["origin"] == LIVE
            exclusion = "extra_time_score" if extra_time else None if live else "replay_backtest"
            settled += repository.insert_settlement(row, fixture_id, scored, exclusion, settled_at)
    repository.commit()
    return {"market_settled": settled}


def run_market_report(
    repository: "MarketRepository", *, now: datetime, origin: str, minimum_samples: int, write: bool
) -> dict[str, object]:
    rows = repository.settled_rows(origin=origin)
    champion_id = repository.champion_model_id("1x2")
    by_model: dict[tuple[str, str], dict[int, dict[str, float]]] = defaultdict(dict)
    champion_scores: dict[str, dict[int, dict[str, float]]] = defaultdict(dict)
    keys: dict[tuple[str, str], str] = {}
    window_start: dict[tuple[str, str], datetime] = {}
    for row in rows:
        scores = {key: float(value[1]) for key, value in row["scored"].items()}  # type: ignore[attr-defined]
        stage, fixture = str(row["lifecycle_stage"]), int(row["fixture_id"])  # type: ignore[arg-type]
        if str(row["model_registry_id"]) == champion_id:
            champion_scores[stage][fixture] = scores
            continue
        identity = (str(row["model_registry_id"]), stage)
        by_model[identity][fixture] = scores
        keys[identity] = str(row["model_key"])
        started = row["predicted_at"]
        window_start[identity] = min(window_start.get(identity, started), started)  # type: ignore[type-var,arg-type]
    evaluations: list[dict[str, object]] = []
    written = 0
    for identity, scores in sorted(by_model.items()):
        model_id, stage = identity
        for market, summary in sorted(compare(scores, champion_scores.get(stage, {})).items()):
            verdict, reason = decision(int(summary["paired_with_champion"]), minimum_samples=minimum_samples)  # type: ignore[arg-type]
            evaluations.append({"model_key": keys[identity], "lifecycle_stage": stage, "market": market, "decision": verdict, **summary})
            if not write or origin != LIVE:
                continue
            family_champion = repository.champion_model_id_for_market(market.split(":", 1)[0])
            if family_champion is None:
                continue
            start = window_start[identity]
            written += repository.write_evaluation(
                {
                    "market_family": repository.family_for_market(market.split(":", 1)[0]),
                    "lifecycle_stage": stage,
                    "champion_model_id": family_champion,
                    "challenger_model_id": model_id,
                    "window_start": start,
                    "window_end": max(now, start + timedelta(seconds=1)),  # type: ignore[operator]
                    "evaluation_contract_version": f"{EVALUATION_CONTRACT}:{market}",
                    "sample_count": summary["paired_with_champion"],
                    "champion_metrics": json.dumps({"log_loss": summary["champion_log_loss"]}),
                    "challenger_metrics": json.dumps({"log_loss": summary["log_loss"]}),
                    "gate_results": json.dumps(
                        {key: summary.get(key) for key in ("paired_with_champion", "mean_log_loss_difference", "standard_error", "z")}
                        | {"minimum_samples": minimum_samples}
                    ),
                    "decision": verdict,
                    "decision_reason": reason,
                }
            )
    repository.commit()
    return {"origin": origin, "settled_rows": len(rows), "market_evaluations": evaluations, "market_evaluations_written": written}


# --- persistence ----------------------------------------------------------------


class MarketRepository:
    def __init__(self, connection: object) -> None:
        self._connection = connection

    def ensure_registered(self, models: MarketModels, *, code_commit: str) -> dict[str, str]:
        manifest = models.manifest
        ids: dict[str, str] = {}
        for key, version, runtime, feature_version, parameters, artifact_hash in (
            (
                MODEL_C_KEY,
                models.model_c_version,
                "python-stdlib-gbdt",
                str(manifest["feature_version"]),
                dict(manifest["hyperparameters"]),  # type: ignore[call-overload]
                models.artifact_hash,
            ),
            (MODEL_B_KEY, models.model_b_version, "python-stdlib", "elo-results-v1", dict(models.elo_goal_map), None),
        ):
            params = {
                "model_key": key,
                "model_version": version,
                "market_family": REGISTRY_FAMILY,
                "runtime": runtime,
                "artifact_uri": "prediction_engine/artifacts/challenger_markets" if artifact_hash else None,
                "artifact_hash": artifact_hash,
                "configuration_hash": _hash(parameters),
                "feature_version": feature_version,
                "training_end": manifest.get("training_end"),
                "validation_window": json.dumps(manifest.get("validation_window", {}), sort_keys=True),
                "hyperparameters": json.dumps(parameters, sort_keys=True, default=str),
                "training_dataset_hash": manifest.get("training_dataset_hash"),
                "code_commit": str(manifest.get("code_commit") or code_commit or "unrecorded"),
                "audit_metadata": json.dumps(
                    {"registered_by": "prediction_engine.challenger_markets", "ensemble_weight": 0}, sort_keys=True
                ),
            }
            self._execute(_REGISTER_SQL, params)
            row = self._execute(_REGISTRY_ID_SQL, params).fetchone()
            if row is None:
                raise LookupError(f"registry row for {key} {version} was not created")
            ids[key] = str(row["id"])
        return ids

    def champion_model_id(self, market_family: str) -> str | None:
        row = self._execute(_CHAMPION_SQL, {"market_family": market_family}).fetchone()
        return None if row is None else str(row["id"])

    def family_for_market(self, market: str) -> str:
        row = self._execute("SELECT engine_prediction_market_family(%(market)s) AS family", {"market": market}).fetchone()
        return str(row["family"]) if row and row["family"] else market

    def champion_model_id_for_market(self, market: str) -> str | None:
        return self.champion_model_id(self.family_for_market(market))

    def last_live_forecast_at(self) -> datetime | None:
        row = self._execute(_LAST_LIVE_SQL, {}).fetchone()
        return None if row is None else row["as_of_timestamp"]  # type: ignore[return-value]

    def live_targets(self, *, as_of: datetime, limit: int, model_ids: Sequence[str]) -> list[MarketTarget]:
        from prediction_engine.db_repository import map_fixture_record

        rows = self._execute(_LIVE_TARGETS_SQL, {"as_of": as_of, "limit": limit, "model_ids": list(model_ids)}).fetchall()
        return [MarketTarget(map_fixture_record(row), as_of, str(row["lifecycle_stage"])) for row in rows]

    def replay_targets(self, *, as_of: datetime, limit: int, model_ids: Sequence[str]) -> list[MarketTarget]:
        from prediction_engine.db_repository import map_fixture_record

        rows = self._execute(_REPLAY_TARGETS_SQL, {"cutoff": as_of - RESULT_LAG, "limit": limit, "model_ids": list(model_ids)}).fetchall()
        return [
            MarketTarget(record, record.kickoff_at - REPLAY_LEAD, "PRE_MATCH") for record in map(map_fixture_record, rows)
        ]

    def history(self, *, since: datetime, until: datetime) -> Iterator[tuple[FixtureRecord, dict[str, tuple[int | None, int | None]]]]:
        from prediction_engine.db_repository import map_fixture_record

        params = {"since": since, "until": until}
        cursor_factory = getattr(self._connection, "cursor", None)
        if cursor_factory is None:
            for row in self._execute(_HISTORY_SQL, params).fetchall():
                yield map_fixture_record(row), _row_statistics(row)
            return
        with cursor_factory(name="challenger_market_history") as cursor:
            cursor.itersize = 5000
            cursor.execute(_HISTORY_SQL, params)
            for row in cursor:
                yield map_fixture_record(row), _row_statistics(row)

    def insert(
        self,
        model_id: str,
        target: MarketTarget,
        origin: str,
        document: Mapping[str, Mapping[str, float]],
        flags: Sequence[str],
    ) -> int:
        row = self._execute(
            _INSERT_SQL,
            {
                "model_registry_id": model_id,
                "fixture_id": target.fixture.id,
                "origin": origin,
                "lifecycle_stage": target.lifecycle_stage,
                "markets": json.dumps(document, sort_keys=True, separators=(",", ":")),
                "fallback_flags": list(flags),
                "as_of_timestamp": target.as_of,
                "data_cutoff_at": target.as_of - RESULT_LAG,
                "kickoff_at": target.fixture.kickoff_at,
            },
        ).fetchone()
        return int(row is not None)

    def settlement_batches(self, *, limit: int) -> Iterator[tuple[int, list[dict[str, object]], ActualFixtureResult, bool]]:
        from prediction_engine.db_repository import PredictionSettlementRepository, _map_actual_fixture_result

        rows = self._execute(
            _READY_SQL, {"limit": limit, "contract": SETTLEMENT_CONTRACT, "finished": list(_FINISHED_STATUSES)}
        ).fetchall()
        grouped: dict[int, list[dict[str, object]]] = defaultdict(list)
        for row in rows:
            grouped[int(row["fixture_id"])].append(row)
        statistics = PredictionSettlementRepository(self._connection)
        for fixture_id, fixture_rows in grouped.items():
            first = fixture_rows[0]
            totals = statistics._load_fixture_stat_totals(fixture_id, first)
            yield (
                fixture_id,
                fixture_rows,
                _map_actual_fixture_result(first, totals),
                str(first["status"]).lower() in _EXTRA_TIME_STATUSES,
            )

    def insert_settlement(
        self, row: Mapping[str, object], fixture_id: int, scored: Mapping[str, list[object]], exclusion: str | None, settled_at: datetime
    ) -> int:
        inserted = self._execute(
            _INSERT_SETTLEMENT_SQL,
            {
                "prediction_id": row["id"],
                "model_registry_id": row["model_registry_id"],
                "fixture_id": fixture_id,
                "origin": row["origin"],
                "lifecycle_stage": row["lifecycle_stage"],
                "scored": json.dumps(scored, sort_keys=True, separators=(",", ":")),
                "evaluation_eligible": exclusion is None,
                "exclusion_reason": exclusion,
                "contract": SETTLEMENT_CONTRACT,
                "predicted_at": row["as_of_timestamp"],
                "settled_at": max(settled_at, row["as_of_timestamp"]),  # type: ignore[type-var]
            },
        ).fetchone()
        return int(inserted is not None)

    def settled_rows(self, *, origin: str) -> list[dict[str, object]]:
        return list(self._execute(_SETTLED_ROWS_SQL, {"origin": origin, "contract": SETTLEMENT_CONTRACT}).fetchall())

    def write_evaluation(self, params: dict[str, object]) -> int:
        return int(self._execute(_INSERT_EVALUATION_SQL, params).fetchone() is not None)

    def commit(self) -> None:
        self._connection.commit()  # type: ignore[attr-defined]

    def _execute(self, sql: str, params: dict[str, object]) -> object:
        return self._connection.execute(sql, params)  # type: ignore[attr-defined]


def _row_statistics(row: Mapping[str, object]) -> dict[str, tuple[int | None, int | None]]:
    return {
        "corners": (row.get("home_corners"), row.get("away_corners")),  # type: ignore[dict-item]
        "cards": (row.get("home_cards"), row.get("away_cards")),  # type: ignore[dict-item]
        "fouls": (row.get("home_fouls"), row.get("away_fouls")),  # type: ignore[dict-item]
        "shots": (row.get("home_shots"), row.get("away_shots")),  # type: ignore[dict-item]
        "shots_on_target": (row.get("home_shots_on_target"), row.get("away_shots_on_target")),  # type: ignore[dict-item]
    }


_FIXTURE_COLUMNS = """
  f.id,
  f.kickoff_at,
  f.league_id,
  COALESCE(l.name, '') AS league_name,
  f.season_id,
  f.home_team_id,
  f.away_team_id,
  f.home_goals_ft,
  f.away_goals_ft,
  f.status,
  COALESCE(f.source, '') AS source
"""


def _cards_sql(alias: str) -> str:
    # Cards shown, yellow plus red: the unit settlement grades on.
    return f"""CASE
    WHEN {alias}.yellow_cards IS NULL AND {alias}.red_cards IS NULL THEN
      CASE WHEN {alias}.fouls IS NOT NULL OR {alias}.shots_total IS NOT NULL OR {alias}.corners IS NOT NULL THEN 0 END
    ELSE COALESCE({alias}.yellow_cards, 0) + COALESCE({alias}.red_cards, 0)
  END"""


_REGISTER_SQL = """
INSERT INTO model_registry (
  model_key, model_version, market_family, runtime, artifact_uri, artifact_hash,
  configuration_hash, feature_version, training_end, training_cutoff_at,
  validation_window, hyperparameters, training_dataset_hash, code_commit, status, audit_metadata
)
VALUES (
  %(model_key)s, %(model_version)s, %(market_family)s, %(runtime)s, %(artifact_uri)s, %(artifact_hash)s,
  %(configuration_hash)s, %(feature_version)s, %(training_end)s::timestamptz, %(training_end)s::timestamptz,
  %(validation_window)s::jsonb, %(hyperparameters)s::jsonb, %(training_dataset_hash)s, %(code_commit)s,
  'shadow', %(audit_metadata)s::jsonb
)
ON CONFLICT (model_key, model_version, market_family) DO NOTHING
"""

_REGISTRY_ID_SQL = """
SELECT id FROM model_registry
WHERE model_key = %(model_key)s AND model_version = %(model_version)s AND market_family = %(market_family)s
"""

_CHAMPION_SQL = """
SELECT id FROM model_registry
WHERE market_family = %(market_family)s AND status = 'production'
ORDER BY created_at DESC
LIMIT 1
"""

_LAST_LIVE_SQL = """
SELECT max(as_of_timestamp) AS as_of_timestamp FROM challenger_market_predictions WHERE origin = 'live'
"""

_LIVE_TARGETS_SQL = f"""
SELECT {_FIXTURE_COLUMNS}, ep.lifecycle_stage
FROM engine_predictions AS ep
JOIN fixtures AS f ON f.id = ep.fixture_id
LEFT JOIN leagues AS l ON l.id = f.league_id
WHERE ep.market = '1x2'
  AND ep.superseded_at IS NULL
  AND ep.lifecycle_stage IN ('EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED')
  AND f.kickoff_at > %(as_of)s
  AND f.kickoff_at < %(as_of)s + interval '7 days'
  AND (
    SELECT count(DISTINCT cp.model_registry_id)
    FROM challenger_market_predictions AS cp
    WHERE cp.fixture_id = f.id
      AND cp.origin = 'live'
      AND cp.lifecycle_stage = ep.lifecycle_stage
      AND cp.model_registry_id = ANY(%(model_ids)s::uuid[])
  ) < cardinality(%(model_ids)s::uuid[])
ORDER BY f.kickoff_at ASC, f.id ASC
LIMIT %(limit)s
"""

_REPLAY_TARGETS_SQL = f"""
SELECT {_FIXTURE_COLUMNS}
FROM fixtures AS f
LEFT JOIN leagues AS l ON l.id = f.league_id
WHERE f.kickoff_at < %(cutoff)s
  AND f.home_goals_ft IS NOT NULL
  AND f.away_goals_ft IS NOT NULL
  AND lower(f.status) IN ('ft', 'finished', 'match finished', 'full time', 'fulltime')
  AND NOT EXISTS (
    SELECT 1 FROM challenger_market_predictions AS cp
    WHERE cp.fixture_id = f.id AND cp.origin = 'replay' AND cp.model_registry_id = ANY(%(model_ids)s::uuid[])
  )
ORDER BY f.kickoff_at DESC, f.id DESC
LIMIT %(limit)s
"""

_HISTORY_SQL = f"""
SELECT
  {_FIXTURE_COLUMNS},
  hs.corners AS home_corners, away_stats.corners AS away_corners,
  {_cards_sql("hs")} AS home_cards, {_cards_sql("away_stats")} AS away_cards,
  hs.fouls AS home_fouls, away_stats.fouls AS away_fouls,
  hs.shots_total AS home_shots, away_stats.shots_total AS away_shots,
  hs.shots_on_target AS home_shots_on_target, away_stats.shots_on_target AS away_shots_on_target
FROM fixtures AS f
LEFT JOIN leagues AS l ON l.id = f.league_id
LEFT JOIN fixture_statistics AS hs ON hs.fixture_id = f.id AND hs.team_id = f.home_team_id
LEFT JOIN fixture_statistics AS away_stats ON away_stats.fixture_id = f.id AND away_stats.team_id = f.away_team_id
WHERE f.kickoff_at >= %(since)s
  AND f.kickoff_at < %(until)s
  AND f.home_goals_ft IS NOT NULL
  AND f.away_goals_ft IS NOT NULL
ORDER BY f.kickoff_at ASC, f.id ASC
"""

_INSERT_SQL = """
INSERT INTO challenger_market_predictions (
  model_registry_id, fixture_id, origin, lifecycle_stage, markets, fallback_flags,
  as_of_timestamp, data_cutoff_at, kickoff_at
)
VALUES (
  %(model_registry_id)s::uuid, %(fixture_id)s, %(origin)s, %(lifecycle_stage)s, %(markets)s::jsonb,
  %(fallback_flags)s::text[], %(as_of_timestamp)s, %(data_cutoff_at)s, %(kickoff_at)s
)
ON CONFLICT (model_registry_id, fixture_id, origin, lifecycle_stage) DO NOTHING
RETURNING id
"""

_READY_SQL = """
SELECT
  cp.id, cp.model_registry_id, cp.fixture_id, cp.origin, cp.lifecycle_stage, cp.markets, cp.as_of_timestamp,
  f.home_team_id, f.away_team_id, f.home_goals_ft, f.away_goals_ft, f.home_goals_ht, f.away_goals_ht, f.status
FROM challenger_market_predictions AS cp
JOIN fixtures AS f ON f.id = cp.fixture_id
WHERE f.home_goals_ft IS NOT NULL
  AND f.away_goals_ft IS NOT NULL
  AND lower(f.status) = ANY(%(finished)s::text[])
  AND NOT EXISTS (
    SELECT 1 FROM challenger_market_settlements AS ss
    WHERE ss.prediction_id = cp.id AND ss.settlement_contract_version = %(contract)s
  )
ORDER BY cp.kickoff_at ASC, cp.fixture_id ASC
LIMIT %(limit)s
"""

_INSERT_SETTLEMENT_SQL = """
INSERT INTO challenger_market_settlements (
  prediction_id, model_registry_id, fixture_id, origin, lifecycle_stage, scored,
  evaluation_eligible, exclusion_reason, settlement_contract_version, predicted_at, settled_at
)
VALUES (
  %(prediction_id)s::uuid, %(model_registry_id)s::uuid, %(fixture_id)s, %(origin)s, %(lifecycle_stage)s,
  %(scored)s::jsonb, %(evaluation_eligible)s, %(exclusion_reason)s, %(contract)s, %(predicted_at)s, %(settled_at)s
)
ON CONFLICT DO NOTHING
RETURNING prediction_id
"""

# Live evidence counts from the current champion's promotion, like the 1X2 report.
_SETTLED_ROWS_SQL = """
SELECT
  mr.model_key, ss.model_registry_id, ss.fixture_id, ss.lifecycle_stage, ss.scored, ss.predicted_at
FROM challenger_market_settlements AS ss
JOIN model_registry AS mr ON mr.id = ss.model_registry_id
WHERE ss.origin = %(origin)s
  AND ss.settlement_contract_version = %(contract)s
  AND (ss.evaluation_eligible OR ss.exclusion_reason = 'replay_backtest')
  AND (
    ss.origin <> 'live'
    OR ss.predicted_at >= COALESCE(
      (
        SELECT champion.promoted_at
        FROM model_registry AS champion
        WHERE champion.market_family = '1x2' AND champion.status = 'production'
        ORDER BY champion.created_at DESC
        LIMIT 1
      ),
      '-infinity'::timestamptz
    )
  )
"""

_INSERT_EVALUATION_SQL = """
INSERT INTO champion_challenger_evaluations (
  market_family, lifecycle_stage, champion_model_id, challenger_model_id, window_start, window_end,
  evaluation_contract_version, sample_count, champion_metrics, challenger_metrics, gate_results,
  decision, decision_reason
)
VALUES (
  %(market_family)s, %(lifecycle_stage)s, %(champion_model_id)s::uuid, %(challenger_model_id)s::uuid,
  %(window_start)s, %(window_end)s, %(evaluation_contract_version)s, %(sample_count)s,
  %(champion_metrics)s::jsonb, %(challenger_metrics)s::jsonb, %(gate_results)s::jsonb,
  %(decision)s, %(decision_reason)s
)
ON CONFLICT DO NOTHING
RETURNING id
"""
