"""Shadow challengers: architecture v2 Models B and C beside the champion.

Elo (Model B, `elo`) and the trained trees (Model C, `model_c`) carry zero
ensemble weight. This module runs them on the fixtures the champion
publishes, keeps their forecasts out of every public table, settles them and
compares them with the champion on the same fixtures (sections 18 and 21).
Nothing here can change a published prediction.

Three commands, each attached to the worker job that owns its stage
(`services/worker/internal/predictionenginepython/runner.go`):

- `run-challengers` after each upcoming run. It forecasts every fixture whose
  current champion 1X2 has no challenger forecast at the same lifecycle stage,
  and records the champion's full 1X2 at the same as-of time beside it.
- `settle-challengers` after each settlement run. One immutable
  `shadow_settlements` row per forecast.
- `challenger-report` after the daily accuracy snapshot. Paired log loss
  against the champion, written to `champion_challenger_evaluations`. It
  never promotes: below `minimum_shadow_settled_samples` the decision is
  `extend`, and above it `pending`, which a person resolves.

**Both models need league-wide history.** Ratings and form are built by one
replay of every finished fixture in a bounded window, in kick-off order. A
fixture is admitted for a target only once it kicked off `RESULT_LAG` before
the target's as-of time, so a result that was not yet known cannot leak in.
The replay is the expensive step, so the forecast command does nothing until
there is a target and at most once per `--min-interval-minutes`.

**Replay origin.** `run-challengers --origin replay` forecasts recently
finished fixtures as they would have been seen an hour before kick-off. It
exercises the whole path where no match is being played (QA's provider is a
static mock), and its rows are stored and settled under origin `replay` and
excluded from evaluation, so a backtest never counts as shadow evidence.
"""

from __future__ import annotations

from collections.abc import Callable, Iterable, Iterator, Sequence
from dataclasses import asdict, dataclass, field
from datetime import datetime, timedelta
import hashlib
import json
import math
import os

from prediction_engine.elo import DEFAULT_ELO_CONFIG, MODEL_ID as ELO_MODEL_ID, EloConfig, EloRatingBook
from prediction_engine.features import FixtureRecord
from prediction_engine.form_features import FEATURE_VERSION, FormFeatureBook
from prediction_engine.gbdt import TreeEnsemble
from prediction_engine.model_c import DEFAULT_ARTIFACT, MARKET_FAMILY, MODEL_KEY as MODEL_C_KEY, run_model_c


LIVE = "live"
REPLAY = "replay"
ORIGINS = (LIVE, REPLAY)
MARKET = "1x2"
OUTCOMES = ("home", "draw", "away")
ELO_MODEL_KEY = "elo"
# A fixture counts as known history for a target only if it kicked off this
# long before the target's as-of time: long enough for full time and extra
# time, so a result can never be used before it existed.
RESULT_LAG = timedelta(hours=3)
REPLAY_LEAD = timedelta(hours=1)
SETTLEMENT_CONTRACT = "shadow-1x2-v1"
EVALUATION_CONTRACT = "shadow-1x2-v1"
# Matches that went to extra time settle 1X2 on the 90-minute score, which
# the fixture row does not hold apart from the extra-time goals.
_EXTRA_TIME_STATUSES = ("aet", "pen")
_FINISHED_STATUSES = ("ft", "aet", "pen", "finished", "match finished", "full time", "fulltime")


@dataclass(frozen=True)
class Registration:
    model_key: str
    model_version: str
    runtime: str
    feature_version: str
    hyperparameters: dict[str, object]
    configuration_hash: str
    code_commit: str
    artifact_uri: str | None = None
    artifact_hash: str | None = None
    training_start: str | None = None
    training_end: str | None = None
    validation_window: dict[str, object] = field(default_factory=dict)
    training_dataset_hash: str | None = None


@dataclass(frozen=True)
class Target:
    fixture: FixtureRecord
    as_of: datetime
    lifecycle_stage: str
    champion: dict[str, float] | None = None


@dataclass(frozen=True)
class Forecast:
    model_key: str
    target: Target
    probabilities: dict[str, float]
    explanation_factors: dict[str, object]
    fallback_flags: tuple[str, ...] = ()


def registrations(model_c: TreeEnsemble, *, config: EloConfig = DEFAULT_ELO_CONFIG) -> list[Registration]:
    """The registry identities of both challengers (section 21: register first)."""
    elo_parameters = asdict(config)
    elo_hash = _hash(elo_parameters)
    meta = model_c.metadata
    return [
        Registration(
            model_key=ELO_MODEL_KEY,
            model_version=f"{ELO_MODEL_ID}-{elo_hash[:12]}",
            runtime="python-stdlib",
            feature_version="elo-results-v1",
            hyperparameters=elo_parameters,
            configuration_hash=elo_hash,
            # Elo is fitted online, so its identity is its configuration; the
            # commit that first registered it is recorded for the audit trail.
            code_commit=os.environ.get("PREDICTION_ENGINE_CODE_COMMIT", "").strip() or "unrecorded",
        ),
        Registration(
            model_key=MODEL_C_KEY,
            model_version=str(meta["model_version"]),
            runtime=str(meta.get("runtime", "python-stdlib-gbdt")),
            feature_version=str(meta.get("feature_version", FEATURE_VERSION)),
            hyperparameters=dict(meta.get("hyperparameters", {})),  # type: ignore[arg-type]
            configuration_hash=_hash(meta.get("hyperparameters", {})),
            code_commit=str(meta.get("code_commit") or "unrecorded"),
            artifact_uri=f"prediction_engine/artifacts/{DEFAULT_ARTIFACT.name}",
            artifact_hash=model_c.artifact_hash,
            training_start=str(meta.get("training_start") or "") or None,
            training_end=str(meta.get("training_end") or "") or None,
            validation_window=dict(meta.get("validation_window", {})),  # type: ignore[arg-type]
            training_dataset_hash=str(meta.get("training_dataset_hash") or "") or None,
        ),
    ]


def forecast(history: Iterable[FixtureRecord], targets: Sequence[Target], *, model_c: TreeEnsemble) -> list[Forecast]:
    """Both challengers for every target, from one ordered pass over history.

    `history` must be finished fixtures in (kick-off, id) order. Each target is
    forecast just before the first fixture it is not allowed to see is
    recorded, so targets with different as-of times share one pass.
    """
    pending = sorted(targets, key=lambda target: (target.as_of, target.fixture.id))
    elo = EloRatingBook(DEFAULT_ELO_CONFIG)
    form = FormFeatureBook()
    forecasts: list[Forecast] = []
    position = 0
    previous: tuple[datetime, int] | None = None
    for record in history:
        key = (record.kickoff_at, record.id)
        if previous is not None and key < previous:
            raise ValueError("history must be ordered by kick-off")
        previous = key
        while position < len(pending) and record.kickoff_at > pending[position].as_of - RESULT_LAG:
            forecasts.extend(_forecast_one(pending[position], elo, form, model_c))
            position += 1
        elo.record(record)
        form.record(record)
    for target in pending[position:]:
        forecasts.extend(_forecast_one(target, elo, form, model_c))
    return forecasts


def _forecast_one(target: Target, elo: EloRatingBook, form: FormFeatureBook, model_c: TreeEnsemble) -> list[Forecast]:
    rating = elo.predict(target.fixture)
    trees = run_model_c(form.features(target.fixture), model_c)
    return [
        Forecast(
            model_key=ELO_MODEL_KEY,
            target=target,
            probabilities=dict(rating.match_result),
            explanation_factors={
                "method": "rating_difference",
                "home_rating": round(rating.home_rating, 2),
                "away_rating": round(rating.away_rating, 2),
                "rating_differential": round(rating.rating_differential, 2),
                "draw_rate": round(rating.draw_rate, 4),
                "home_sample": rating.home_sample,
                "away_sample": rating.away_sample,
            },
            fallback_flags=rating.fallback_flags,
        ),
        Forecast(
            model_key=MODEL_C_KEY,
            target=target,
            probabilities=dict(trees.match_result),
            explanation_factors=trees.explanation_factors,
        ),
    ]


def outcome(home_goals: int, away_goals: int) -> str:
    if home_goals > away_goals:
        return "home"
    return "draw" if home_goals == away_goals else "away"


def evaluate(rows: Sequence[dict[str, object]]) -> dict[str, object]:
    """Log loss, Brier and accuracy for a challenger and the champion on the same rows.

    Each row has the challenger's three probabilities, the champion's (or
    None) and the final outcome. The paired difference is over the rows that
    have both; negative means the challenger is better.
    """
    challenger_losses: list[float] = []
    champion_losses: list[float] = []
    differences: list[float] = []
    briers: list[float] = []
    hits = 0
    for row in rows:
        actual = str(row["final_outcome"])
        challenger = _distribution(row, "")
        loss = _log_loss(challenger, actual)
        challenger_losses.append(loss)
        briers.append(sum((challenger[name] - (1.0 if name == actual else 0.0)) ** 2 for name in OUTCOMES))
        hits += max(OUTCOMES, key=challenger.__getitem__) == actual
        if row.get("champion_home_probability") is not None:
            champion_loss = _log_loss(_distribution(row, "champion_"), actual)
            champion_losses.append(champion_loss)
            differences.append(loss - champion_loss)
    count = len(challenger_losses)
    summary: dict[str, object] = {
        "sample_count": count,
        "log_loss": _round(_mean(challenger_losses)),
        "brier": _round(_mean(briers)),
        "accuracy": _round(hits / count) if count else None,
        "paired_with_champion": len(differences),
        "champion_log_loss": _round(_mean(champion_losses)),
    }
    if len(differences) >= 2:
        mean = sum(differences) / len(differences)
        variance = sum((value - mean) ** 2 for value in differences) / (len(differences) - 1)
        standard_error = math.sqrt(variance / len(differences))
        summary.update(
            mean_log_loss_difference=_round(mean),
            standard_error=_round(standard_error),
            z=_round(mean / standard_error) if standard_error > 0 else None,
        )
    return summary


def decision(summary: dict[str, object], *, minimum_samples: int) -> tuple[str, str]:
    """Never `promote`: a person reads the evidence and decides (section 21.5)."""
    paired = int(summary.get("paired_with_champion") or 0)
    if paired < minimum_samples:
        return "extend", f"{paired} settled forecasts paired with the champion; {minimum_samples} are required"
    return "pending", f"{paired} paired forecasts meet the sample minimum; promotion is a reviewed decision"


def _distribution(row: dict[str, object], prefix: str) -> dict[str, float]:
    return {name: float(row[f"{prefix}{name}_probability"]) for name in OUTCOMES}  # type: ignore[arg-type]


def _log_loss(probabilities: dict[str, float], actual: str) -> float:
    return -math.log(max(probabilities[actual], 1e-12))


def _mean(values: Sequence[float]) -> float | None:
    return sum(values) / len(values) if values else None


def _round(value: float | None) -> float | None:
    return None if value is None else round(value, 6)


def _hash(value: object) -> str:
    return hashlib.sha256(json.dumps(value, sort_keys=True, default=str).encode("utf-8")).hexdigest()


# --- commands ----------------------------------------------------------------


def run_forecasts(
    repository: "ChallengerRepository",
    *,
    as_of: datetime,
    origin: str,
    limit: int,
    history_days: int,
    min_interval: timedelta,
    champion: "Callable[[FixtureRecord, datetime], dict[str, float] | None]",
    model_c: TreeEnsemble,
) -> dict[str, object]:
    """Forecast and store both challengers for the next batch of targets."""
    if origin not in ORIGINS:
        raise ValueError(f"origin must be one of {ORIGINS}")
    model_ids = repository.ensure_registered(registrations(model_c))
    # Committed before anything else runs: the champion read below rolls the
    # transaction back when it fails, and must not take the registry with it.
    repository.commit()
    summary: dict[str, object] = {"scope": f"challengers_{origin}", "origin": origin, "challenger_forecasts": 0}
    if origin == LIVE:
        last = repository.last_live_forecast_at()
        if last is not None and as_of - last < min_interval:
            repository.commit()
            return {**summary, "skipped": "min_interval", "last_forecast_at": last.isoformat()}
        pairs = repository.live_targets(as_of=as_of, limit=limit, model_ids=list(model_ids.values()))
        targets = [Target(fixture, as_of, stage) for fixture, stage in pairs]
    else:
        fixtures = repository.replay_targets(as_of=as_of, limit=limit, model_ids=list(model_ids.values()))
        targets = [Target(fixture, fixture.kickoff_at - REPLAY_LEAD, "PRE_MATCH") for fixture in fixtures]
    if not targets:
        repository.commit()
        return {**summary, "targets": 0}
    targets = [
        Target(target.fixture, target.as_of, target.lifecycle_stage, champion(target.fixture, target.as_of))
        for target in targets
    ]
    earliest = min(target.as_of for target in targets)
    latest = max(target.as_of for target in targets)
    history = repository.history(since=earliest - timedelta(days=history_days), until=latest - RESULT_LAG)
    forecasts = forecast(history, targets, model_c=model_c)
    inserted = repository.insert_forecasts(forecasts, model_ids=model_ids, origin=origin)
    repository.commit()
    return {
        **summary,
        "targets": len(targets),
        "champion_paired": sum(1 for target in targets if target.champion is not None),
        "challenger_forecasts": inserted,
        "model_registry_ids": model_ids,
    }


def run_settlement(repository: "ChallengerRepository", *, settled_at: datetime, limit: int) -> dict[str, object]:
    settled = repository.settle(settled_at=settled_at, limit=limit)
    repository.commit()
    return {"scope": "challengers_settlement", "challenger_settled": settled}


def run_report(
    repository: "ChallengerRepository",
    *,
    now: datetime,
    origin: str,
    minimum_samples: int,
    write: bool,
) -> dict[str, object]:
    """Paired comparison per challenger and lifecycle stage; written only for live evidence."""
    rows = repository.settled_rows(origin=origin)
    groups: dict[tuple[str, str, str], list[dict[str, object]]] = {}
    for row in rows:
        key = (str(row["model_key"]), str(row["lifecycle_stage"]), str(row["model_registry_id"]))
        groups.setdefault(key, []).append(row)
    champion_id = repository.champion_model_id()
    evaluations: list[dict[str, object]] = []
    written = 0
    for (model_key, stage, model_id), group in sorted(groups.items()):
        summary = evaluate(group)
        verdict, reason = decision(summary, minimum_samples=minimum_samples)
        evaluations.append({"model_key": model_key, "lifecycle_stage": stage, "decision": verdict, **summary})
        if not write or origin != LIVE or champion_id is None:
            continue
        window_start = min(row["predicted_at"] for row in group)  # type: ignore[type-var]
        written += repository.write_evaluation(
            {
                "market_family": MARKET_FAMILY,
                "lifecycle_stage": stage,
                "champion_model_id": champion_id,
                "challenger_model_id": model_id,
                "window_start": window_start,
                "window_end": max(now, window_start + timedelta(seconds=1)),  # type: ignore[operator]
                "evaluation_contract_version": EVALUATION_CONTRACT,
                "sample_count": summary["sample_count"],
                "champion_metrics": json.dumps({"log_loss": summary["champion_log_loss"]}),
                "challenger_metrics": json.dumps(
                    {key: summary[key] for key in ("log_loss", "brier", "accuracy")}
                ),
                "gate_results": json.dumps(
                    {
                        key: summary.get(key)
                        for key in ("paired_with_champion", "mean_log_loss_difference", "standard_error", "z")
                    }
                    | {"minimum_samples": minimum_samples}
                ),
                "decision": verdict,
                "decision_reason": reason,
            }
        )
    repository.commit()
    return {
        "scope": "challengers_report",
        "origin": origin,
        "settled_rows": len(rows),
        "evaluations": evaluations,
        "evaluations_written": written,
    }


# --- persistence -------------------------------------------------------------


class ChallengerRepository:
    def __init__(self, connection: object) -> None:
        self._connection = connection

    def ensure_registered(self, entries: Sequence[Registration]) -> dict[str, str]:
        ids: dict[str, str] = {}
        for entry in entries:
            params = {
                **asdict(entry),
                "market_family": MARKET_FAMILY,
                "hyperparameters": json.dumps(entry.hyperparameters, sort_keys=True, default=str),
                "validation_window": json.dumps(entry.validation_window, sort_keys=True, default=str),
                "audit_metadata": json.dumps(
                    {"registered_by": "prediction_engine.challengers", "ensemble_weight": 0}, sort_keys=True
                ),
            }
            self._execute(_REGISTER_SQL, params)
            row = self._execute(_REGISTRY_ID_SQL, params).fetchone()
            if row is None:
                raise LookupError(f"registry row for {entry.model_key} {entry.model_version} was not created")
            ids[entry.model_key] = str(row["id"])
        return ids

    def last_live_forecast_at(self) -> datetime | None:
        # The as-of time of the last live run, on the same clock the next run
        # is measured by; the database's own now() may not be that clock.
        row = self._execute(_LAST_LIVE_SQL, {}).fetchone()
        return None if row is None else row["as_of_timestamp"]  # type: ignore[return-value]

    def live_targets(self, *, as_of: datetime, limit: int, model_ids: Sequence[str]) -> list[tuple[FixtureRecord, str]]:
        from prediction_engine.db_repository import map_fixture_record

        rows = self._execute(
            _LIVE_TARGETS_SQL, {"as_of": as_of, "limit": limit, "model_ids": list(model_ids)}
        ).fetchall()
        return [(map_fixture_record(row), str(row["lifecycle_stage"])) for row in rows]

    def replay_targets(self, *, as_of: datetime, limit: int, model_ids: Sequence[str]) -> list[FixtureRecord]:
        from prediction_engine.db_repository import map_fixture_record

        rows = self._execute(
            _REPLAY_TARGETS_SQL,
            {"cutoff": as_of - RESULT_LAG, "limit": limit, "model_ids": list(model_ids)},
        ).fetchall()
        return [map_fixture_record(row) for row in rows]

    def history(self, *, since: datetime, until: datetime) -> Iterator[FixtureRecord]:
        from prediction_engine.db_repository import map_fixture_record

        params = {"since": since, "until": until}
        cursor_factory = getattr(self._connection, "cursor", None)
        if cursor_factory is None:
            for row in self._execute(_HISTORY_SQL, params).fetchall():
                yield map_fixture_record(row)
            return
        # A named cursor streams from the server: the window can hold far more
        # fixtures than are worth materialising at once.
        with cursor_factory(name="challenger_history") as cursor:
            cursor.itersize = 5000
            cursor.execute(_HISTORY_SQL, params)
            for row in cursor:
                yield map_fixture_record(row)

    def insert_forecasts(self, forecasts: Sequence[Forecast], *, model_ids: dict[str, str], origin: str) -> int:
        inserted = 0
        for item in forecasts:
            target = item.target
            champion = target.champion
            params = {
                "model_registry_id": model_ids[item.model_key],
                "fixture_id": target.fixture.id,
                "origin": origin,
                "lifecycle_stage": target.lifecycle_stage,
                "market": MARKET,
                **{f"{name}_probability": round(item.probabilities[name], 6) for name in OUTCOMES},
                **{
                    f"champion_{name}_probability": round(champion[name], 6) if champion else None
                    for name in OUTCOMES
                },
                "explanation_factors": json.dumps(item.explanation_factors, sort_keys=True, default=str),
                "fallback_flags": list(item.fallback_flags),
                "as_of_timestamp": target.as_of,
                "data_cutoff_at": target.as_of - RESULT_LAG,
                "kickoff_at": target.fixture.kickoff_at,
            }
            _renormalise_rounded(params, "")
            if champion:
                _renormalise_rounded(params, "champion_")
            row = self._execute(_INSERT_FORECAST_SQL, params).fetchone()
            inserted += row is not None
        return inserted

    def settle(self, *, settled_at: datetime, limit: int) -> int:
        rows = self._execute(
            _SETTLE_SQL,
            {
                "settled_at": settled_at,
                "limit": limit,
                "contract": SETTLEMENT_CONTRACT,
                "finished": list(_FINISHED_STATUSES),
                "extra_time": list(_EXTRA_TIME_STATUSES),
            },
        ).fetchall()
        return len(rows)

    def settled_rows(self, *, origin: str) -> list[dict[str, object]]:
        return list(
            self._execute(
                _SETTLED_ROWS_SQL,
                {"origin": origin, "contract": SETTLEMENT_CONTRACT, "market_family": MARKET_FAMILY},
            ).fetchall()
        )

    def champion_model_id(self) -> str | None:
        row = self._execute(_CHAMPION_SQL, {"market_family": MARKET_FAMILY}).fetchone()
        return None if row is None else str(row["id"])

    def write_evaluation(self, params: dict[str, object]) -> bool:
        return self._execute(_INSERT_EVALUATION_SQL, params).fetchone() is not None

    def commit(self) -> None:
        self._connection.commit()  # type: ignore[attr-defined]

    def _execute(self, sql: str, params: dict[str, object]) -> object:
        return self._connection.execute(sql, params)  # type: ignore[attr-defined]


def _renormalise_rounded(params: dict[str, object], prefix: str) -> None:
    """Fold six-decimal rounding drift into the largest outcome so the row sums to 1."""
    keys = [f"{prefix}{name}_probability" for name in OUTCOMES]
    drift = round(1.0 - sum(float(params[key]) for key in keys), 6)  # type: ignore[arg-type]
    largest = max(keys, key=lambda key: float(params[key]))  # type: ignore[arg-type]
    params[largest] = round(float(params[largest]) + drift, 6)  # type: ignore[arg-type]


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

_REGISTER_SQL = """
INSERT INTO model_registry (
  model_key, model_version, market_family, runtime, artifact_uri, artifact_hash,
  configuration_hash, feature_version, training_start, training_end, training_cutoff_at,
  validation_window, hyperparameters, training_dataset_hash, code_commit, status, audit_metadata
)
VALUES (
  %(model_key)s, %(model_version)s, %(market_family)s, %(runtime)s, %(artifact_uri)s, %(artifact_hash)s,
  %(configuration_hash)s, %(feature_version)s, %(training_start)s::timestamptz, %(training_end)s::timestamptz,
  %(training_end)s::timestamptz, %(validation_window)s::jsonb, %(hyperparameters)s::jsonb,
  %(training_dataset_hash)s, %(code_commit)s, 'shadow', %(audit_metadata)s::jsonb
)
ON CONFLICT (model_key, model_version, market_family) DO NOTHING
"""

_REGISTRY_ID_SQL = """
SELECT id FROM model_registry
WHERE model_key = %(model_key)s AND model_version = %(model_version)s AND market_family = %(market_family)s
"""

_LAST_LIVE_SQL = """
SELECT max(as_of_timestamp) AS as_of_timestamp FROM challenger_predictions WHERE origin = 'live'
"""

# Fixtures the champion currently publishes a pre-match 1X2 for, with no
# forecast yet from every challenger at that lifecycle stage.
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
    FROM challenger_predictions AS cp
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
    SELECT 1 FROM challenger_predictions AS cp
    WHERE cp.fixture_id = f.id AND cp.origin = 'replay' AND cp.model_registry_id = ANY(%(model_ids)s::uuid[])
  )
ORDER BY f.kickoff_at DESC, f.id DESC
LIMIT %(limit)s
"""

_HISTORY_SQL = f"""
SELECT {_FIXTURE_COLUMNS}
FROM fixtures AS f
LEFT JOIN leagues AS l ON l.id = f.league_id
WHERE f.kickoff_at >= %(since)s
  AND f.kickoff_at < %(until)s
  AND f.home_goals_ft IS NOT NULL
  AND f.away_goals_ft IS NOT NULL
ORDER BY f.kickoff_at ASC, f.id ASC
"""

_INSERT_FORECAST_SQL = """
INSERT INTO challenger_predictions (
  model_registry_id, fixture_id, origin, lifecycle_stage, market,
  home_probability, draw_probability, away_probability,
  champion_home_probability, champion_draw_probability, champion_away_probability,
  explanation_factors, fallback_flags, as_of_timestamp, data_cutoff_at, kickoff_at
)
VALUES (
  %(model_registry_id)s::uuid, %(fixture_id)s, %(origin)s, %(lifecycle_stage)s, %(market)s,
  %(home_probability)s, %(draw_probability)s, %(away_probability)s,
  %(champion_home_probability)s, %(champion_draw_probability)s, %(champion_away_probability)s,
  %(explanation_factors)s::jsonb, %(fallback_flags)s::text[], %(as_of_timestamp)s, %(data_cutoff_at)s, %(kickoff_at)s
)
ON CONFLICT (model_registry_id, fixture_id, origin, lifecycle_stage, market) DO NOTHING
RETURNING id
"""

# One immutable evidence row per forecast once its fixture has a result. The
# selection is the forecast's most likely outcome; the full distribution
# stays on the forecast row for log loss.
_SETTLE_SQL = """
WITH ready AS (
  SELECT
    cp.*,
    CASE
      WHEN f.home_goals_ft > f.away_goals_ft THEN 'home'
      WHEN f.home_goals_ft = f.away_goals_ft THEN 'draw'
      ELSE 'away'
    END AS final_outcome,
    CASE
      WHEN cp.home_probability >= cp.draw_probability AND cp.home_probability >= cp.away_probability THEN 'home'
      WHEN cp.draw_probability >= cp.away_probability THEN 'draw'
      ELSE 'away'
    END AS selection,
    lower(f.status) = ANY(%(extra_time)s::text[]) AS extra_time
  FROM challenger_predictions AS cp
  JOIN fixtures AS f ON f.id = cp.fixture_id
  WHERE f.home_goals_ft IS NOT NULL
    AND f.away_goals_ft IS NOT NULL
    AND lower(f.status) = ANY(%(finished)s::text[])
    AND NOT EXISTS (
      SELECT 1 FROM shadow_settlements AS ss
      WHERE ss.prediction_id = cp.id
        AND ss.model_registry_id = cp.model_registry_id
        AND ss.settlement_contract_version = %(contract)s
    )
  ORDER BY cp.kickoff_at ASC
  LIMIT %(limit)s
)
INSERT INTO shadow_settlements (
  prediction_id, model_registry_id, fixture_id, market, selection, lifecycle_stage,
  raw_probability, calibrated_probability, final_outcome, settlement_classification,
  evaluation_eligible, exclusion_reason, settlement_contract_version, predicted_at, settled_at
)
SELECT
  id, model_registry_id, fixture_id, market, selection, lifecycle_stage,
  CASE selection WHEN 'home' THEN home_probability WHEN 'draw' THEN draw_probability ELSE away_probability END,
  NULL, final_outcome,
  CASE WHEN extra_time THEN 'excluded' WHEN selection = final_outcome THEN 'correct' ELSE 'incorrect' END,
  origin = 'live' AND NOT extra_time,
  CASE WHEN extra_time THEN 'extra_time_score' WHEN origin <> 'live' THEN 'replay_backtest' END,
  %(contract)s, as_of_timestamp, GREATEST(%(settled_at)s, as_of_timestamp)
FROM ready
ON CONFLICT DO NOTHING
RETURNING prediction_id
"""

_SETTLED_ROWS_SQL = """
SELECT
  mr.model_key,
  cp.lifecycle_stage,
  cp.home_probability, cp.draw_probability, cp.away_probability,
  cp.champion_home_probability, cp.champion_draw_probability, cp.champion_away_probability,
  ss.final_outcome,
  ss.predicted_at,
  cp.model_registry_id
FROM shadow_settlements AS ss
JOIN challenger_predictions AS cp ON cp.id = ss.prediction_id AND cp.model_registry_id = ss.model_registry_id
JOIN model_registry AS mr ON mr.id = cp.model_registry_id
WHERE cp.origin = %(origin)s
  AND ss.settlement_contract_version = %(contract)s
  AND ss.settlement_classification <> 'excluded'
  -- A live row stores the champion that was running when it was forecast. Only
  -- rows from the current champion's promotion onward compare against that model.
  AND (
    cp.origin <> 'live'
    OR ss.predicted_at >= COALESCE(
      (
        SELECT champion.promoted_at
        FROM model_registry AS champion
        WHERE champion.market_family = %(market_family)s AND champion.status = 'production'
        ORDER BY champion.created_at DESC
        LIMIT 1
      ),
      '-infinity'::timestamptz
    )
  )
"""

_CHAMPION_SQL = """
SELECT id FROM model_registry
WHERE market_family = %(market_family)s AND status = 'production'
ORDER BY created_at DESC
LIMIT 1
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
