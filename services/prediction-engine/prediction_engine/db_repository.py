from __future__ import annotations

from dataclasses import dataclass, replace
from datetime import UTC, date, datetime, timedelta
import json
import os
from typing import Callable, Protocol

from prediction_engine.features import FixtureRecord, OddsSnapshot, TeamFixtureStats, team_primary_leagues
from prediction_engine.inplay_counts import (
    CORNERS,
    FOULS,
    SHOTS,
    SHOTS_ON_TARGET,
    CountObservation,
)
from prediction_engine.inplay_features import LiveEvent, LiveSnapshot, MatchPeriod
from prediction_engine.inplay_model import CadenceState, SnapshotState
from prediction_engine.lifecycle import calculate_lifecycle_stability
from prediction_engine.persistence import IN_PLAY_LIFECYCLE_STAGES, EnginePrediction
from prediction_engine.league_strength import measure_home_advantage
from prediction_engine.pipeline import FixtureEngineInput
from prediction_engine.promoted_markets import ModelForecasts, forecasts_from_rows
from prediction_engine.provisional_odds import synthesize_provisional_1x2
from prediction_engine.tracker import MatchState, tracker_state
from prediction_engine.settlement import (
    REVISION_INITIAL,
    REVISION_RESCORED,
    AccuracySnapshot,
    ActualFixtureResult,
    InPlayAnchor,
    Resettlement,
    SettlementResult,
    expire_prediction,
    resettle_prediction,
    settle_prediction,
)


# How long a prediction may stay pending after kickoff before it is voided.
#
# Seven days, and the number is chosen against the worker's results
# reconciliation job rather than against a user's patience. That job runs
# hourly and asks about thousands of fixtures a run, so it clears a backlog of
# thousands within a couple of hours. A week gives it roughly 168 attempts.
# A fixture still unresolved after that is not waiting on a retry; the
# provider is never going to supply it.
#
# The direction of the trade-off matters: too short and a void lands on a
# fixture whose real result was about to arrive, and the void is terminal.
# Too long and users stare at `pending`. Seven days is far enough past
# reconciliation's reach that the first failure mode effectively cannot
# happen, and still a bound.
DEFAULT_MAX_PENDING_AGE_HOURS = 24 * 7

# How long a terminal-but-not-played fixture is left alone before voiding.
#
# Postponed, cancelled, abandoned, awarded and walkover are decisions, not
# absences -- no amount of waiting produces a full-time score. The only thing
# the grace buys is room for a mis-reported postponement to be corrected, and
# twelve hours covers a provider fixing the same day's slate.
DEFAULT_NOT_PLAYED_GRACE_HOURS = 12


@dataclass(frozen=True)
class ExpirySweepSummary:
    """What one expiry sweep did."""

    examined: int
    voided: int
    facts_written: int
    fixtures_touched: int
    unevaluable_prediction_ids: tuple[str, ...]
    reasons: dict[str, int]


class CursorLike(Protocol):
    def fetchone(self) -> dict[str, object] | None: ...
    def fetchall(self) -> list[dict[str, object]]: ...


class ConnectionLike(Protocol):
    def execute(self, sql: str, params: dict[str, object]) -> CursorLike: ...
    def commit(self) -> object: ...
    def rollback(self) -> object: ...


class PredictionInputRepository:
    def __init__(self, connection: ConnectionLike) -> None:
        self._connection = connection

    def list_candidate_fixture_ids(self, *, scope: str, limit: int, as_of_timestamp: datetime) -> list[int]:
        if limit <= 0:
            raise ValueError("limit must be positive")
        as_of = _validated_as_of_timestamp(as_of_timestamp)
        normalized_scope = scope.strip().lower().replace("-", "_")
        if normalized_scope == "upcoming":
            sql = _UPCOMING_FIXTURE_IDS_SQL
        elif normalized_scope in IN_PLAY_SCOPES:
            sql = _IN_PLAY_FIXTURE_IDS_SQL
        elif normalized_scope in {"historical", "replay", "finished"}:
            sql = _HISTORICAL_FIXTURE_IDS_SQL
        else:
            raise ValueError(f"unsupported fixture scope: {scope}")
        params: dict[str, object] = {"limit": limit, "as_of_timestamp": as_of}
        rows = self._connection.execute(sql, params).fetchall()
        return [int(row["id"]) for row in rows]

    def load_fixture_inputs(self, fixture_id: int, *, as_of_timestamp: datetime) -> FixtureEngineInput:
        as_of_timestamp = _validated_as_of_timestamp(as_of_timestamp)
        target_row = self._connection.execute(_TARGET_FIXTURE_SQL, {"fixture_id": fixture_id}).fetchone()
        if target_row is None:
            raise LookupError(f"fixture {fixture_id} not found")
        target = map_fixture_record(target_row)
        history_rows = self._connection.execute(
            _HISTORY_FIXTURES_SQL,
            {
                "fixture_id": target.id,
                "league_id": target.league_id,
                "season_id": target.season,
                "target_kickoff_at": target.kickoff_at,
                "as_of_timestamp": as_of_timestamp,
                "home_team_id": target.home_team_id,
                "away_team_id": target.away_team_id,
            },
        ).fetchall()
        history = [map_fixture_record(row) for row in history_rows]
        statistics_rows = self._connection.execute(
            _FIXTURE_STATISTICS_SQL,
            {"fixture_ids": [record.id for record in history], "as_of_timestamp": as_of_timestamp},
        ).fetchall()
        statistics_by_fixture: dict[int, list[TeamFixtureStats]] = {}
        for row in statistics_rows:
            stats = map_team_fixture_stats(row)
            statistics_by_fixture.setdefault(stats.fixture_id, []).append(stats)

        odds_rows = self._connection.execute(
            _LATEST_ONE_X_TWO_ODDS_SQL,
            {"fixture_id": target.id, "as_of_timestamp": as_of_timestamp},
        ).fetchall()
        odds = _map_optional_odds_snapshot(target.id, odds_rows)
        odds_provisional = False
        if odds is None:
            odds = synthesize_provisional_1x2(
                history,
                target.home_team_id,
                target.away_team_id,
                target.id,
                home_advantage=measure_home_advantage(history, target.league_id),
            )
            odds_provisional = True
        coverage_row = self._connection.execute(
            _LEAGUE_COVERAGE_SQL,
            {
                "league_id": target.league_id,
                "season_id": target.season,
                "as_of_timestamp": as_of_timestamp,
            },
        ).fetchone()
        accuracy_rows = self._connection.execute(
            _LATEST_ACCURACY_SNAPSHOTS_SQL,
            {
                "league_id": str(target.league_id),
                "as_of_date": as_of_timestamp.date(),
                "as_of_timestamp": as_of_timestamp,
            },
        ).fetchall()

        return FixtureEngineInput(
            as_of_timestamp=as_of_timestamp,
            target_fixture=target,
            history=history,
            statistics_by_fixture=statistics_by_fixture,
            odds=odds,
            odds_provisional=odds_provisional,
            generated_at=as_of_timestamp,
            coverage=_map_coverage(coverage_row),
            market_accuracy=_map_market_accuracy(accuracy_rows),
            league_accuracy=_map_league_accuracy(accuracy_rows),
            league_history=self._load_league_history(target, history, as_of_timestamp=as_of_timestamp),
            model_forecasts=self._load_model_forecasts(target.id, as_of_timestamp=as_of_timestamp),
        )

    def _load_model_forecasts(self, fixture_id: int, *, as_of_timestamp: datetime) -> ModelForecasts | None:
        """The newest Model B and C forecasts made by the as-of time, for this fixture.

        Live rows only, so a replay of a finished fixture is not fed a forecast made
        for the real match. PREDICTION_ENGINE_PROMOTED_MARKETS_ENABLED=false publishes
        the champion alone, without a deploy.
        """
        if os.environ.get("PREDICTION_ENGINE_PROMOTED_MARKETS_ENABLED", "").strip().lower() == "false":
            return None
        rows = self._connection.execute(
            _MODEL_FORECASTS_SQL,
            {"fixture_id": fixture_id, "as_of_timestamp": as_of_timestamp},
        ).fetchall()
        return forecasts_from_rows(rows)

    def _load_league_history(
        self,
        target: FixtureRecord,
        history: list[FixtureRecord],
        *,
        as_of_timestamp: datetime,
    ) -> list[FixtureRecord]:
        """The two teams' own leagues, which the team-strength fit is made on."""
        primary = team_primary_leagues(history)
        league_ids = sorted(
            {primary[team_id] for team_id in (target.home_team_id, target.away_team_id) if team_id in primary}
        )
        if not league_ids:
            return []
        rows = self._connection.execute(
            _LEAGUE_HISTORY_FIXTURES_SQL,
            {
                "league_ids": league_ids,
                "season_id": target.season,
                "target_kickoff_at": target.kickoff_at,
                "as_of_timestamp": as_of_timestamp,
            },
        ).fetchall()
        return [map_fixture_record(row) for row in rows]

    # --- in-play reads (architecture v2 7.4) -------------------------------
    #
    # A parallel branch, not a replacement. `load_fixture_inputs` above is the
    # pre-match path and is untouched: the in-play runner calls it too, for the
    # pre-match lambdas the analytic model conditions on, and then calls these
    # for the match state. 7.5 is satisfied structurally -- nothing here is
    # reachable except by asking for it.

    def load_in_play_state(self, fixture_id: int, *, as_of_timestamp: datetime) -> InPlayFixtureState | None:
        """Newest snapshot plus the card timeline, or None when neither exists.

        Returns None rather than raising when a fixture has no snapshot: a
        fixture that stopped being polled is an ordinary outcome of a scope
        run, not an error, and the caller records it as a suppression.
        """
        as_of = _validated_as_of_timestamp(as_of_timestamp)
        row = self._connection.execute(
            _IN_PLAY_LATEST_SNAPSHOT_SQL,
            {"fixture_id": fixture_id, "as_of_timestamp": as_of},
        ).fetchone()
        if row is None:
            return None
        event_rows = self._connection.execute(
            _IN_PLAY_EVENTS_SQL,
            {"fixture_id": fixture_id, "as_of_timestamp": as_of},
        ).fetchall()
        return InPlayFixtureState(
            fixture_id=fixture_id,
            home_team_id=int(row["home_team_id"]),
            away_team_id=int(row["away_team_id"]),
            kickoff_at=row["kickoff_at"],  # type: ignore[arg-type]
            snapshot=_map_live_snapshot(fixture_id, row),
            events=tuple(_map_live_event(fixture_id, event_row) for event_row in event_rows),
        )

    def load_in_play_count_observations(
        self, fixture_id: int, *, as_of_timestamp: datetime
    ) -> tuple[CountObservation, ...]:
        """The newest statistics capture, as one observation per statistic.

        Empty is the normal answer today: the batched statistics poll is off
        until the provider's embedded shape is confirmed, so nothing fills this
        table. Empty means the in-play model publishes no corner, shot or foul
        ladder, which is the contract's own rule -- a fixture with no corner
        data shows no corner market -- rather than a degraded one.

        Cards are deliberately NOT returned here even though the table carries
        them. They come from the live event list at the bulk poll's cadence,
        which is a minute fresher, and the model reads them off the features.
        Returning them here as well would give one market two sources with no
        rule about which wins.
        """
        as_of = _validated_as_of_timestamp(as_of_timestamp)
        row = self._connection.execute(
            _LATEST_INPLAY_STATISTICS_SQL,
            {"fixture_id": fixture_id, "as_of_timestamp": as_of},
        ).fetchone()
        if row is None:
            return ()

        team_rows = _optional_int(row["team_rows"]) or 0
        if team_rows < 2:
            # One side only. The sums below would be half-totals presented as
            # match totals, so the whole capture is declined rather than
            # publishing every market low.
            return ()

        observations: list[CountObservation] = []
        for statistic, value_key, count_key in (
            (CORNERS, "corners", "corners_rows"),
            (SHOTS, "shots_total", "shots_total_rows"),
            (SHOTS_ON_TARGET, "shots_on_target", "shots_on_target_rows"),
            (FOULS, "fouls", "fouls_rows"),
        ):
            reported = _optional_int(row[count_key]) or 0
            if reported < team_rows:
                continue
            value = _optional_int(row[value_key])
            if value is None:
                continue
            observations.append(
                CountObservation(
                    statistic=statistic,
                    observed=max(0, value),
                    # The feed, not the vendor. The engine must not name a
                    # provider -- a boundary test enforces that, and rightly:
                    # the engine reads our database, and which upstream filled
                    # it is not its business. What a published row does need to
                    # record is WHICH feed it was conditioned on, because the
                    # statistics series lags the event list by about a minute.
                    source="statistics_snapshot",
                )
            )
        return tuple(observations)

    def load_in_play_snapshot_state(self, fixture_id: int) -> SnapshotState | None:
        """What the last graph point recorded, or None if there is none yet.

        Returning None means "first snapshot", which the cadence treats as due --
        a fixture that has never been drawn should get its first point on the next
        pass rather than waiting a full interval.
        """
        row = self._connection.execute(
            _LATEST_INPLAY_SNAPSHOT_STATE_SQL, {"fixture_id": fixture_id}
        ).fetchone()
        if row is None:
            return None
        period = row["period"]
        return SnapshotState(
            captured_at=row["captured_at"],
            home_goals=int(row["home_goals"] or 0),
            away_goals=int(row["away_goals"] or 0),
            home_red_cards=int(row["home_red_cards"] or 0),
            away_red_cards=int(row["away_red_cards"] or 0),
            period=MatchPeriod(period) if period else MatchPeriod.UNKNOWN,
        )

    def load_in_play_cadence_state(self, fixture_id: int) -> CadenceState | None:
        """What the last published in-play prediction saw, for 7.6.

        Read back out of the row it was written into rather than held in
        memory: the worker is not the only thing that can run the engine, and
        a cadence that lives in a process would regenerate everything after a
        restart. `features_used` carries the match state deliberately so this
        question has an answer from the audit record alone.
        """
        row = self._connection.execute(
            _IN_PLAY_LAST_PREDICTION_SQL, {"fixture_id": fixture_id}
        ).fetchone()
        if row is None:
            return None
        features_used = row.get("features_used")
        if isinstance(features_used, str):
            features_used = json.loads(features_used)
        if not isinstance(features_used, dict):
            return None
        state = features_used.get("in_play")
        if not isinstance(state, dict):
            return None
        try:
            return CadenceState(
                generated_at=row["generated_at"],  # type: ignore[arg-type]
                home_goals=int(state["home_goals"]),
                away_goals=int(state["away_goals"]),
                home_red_cards=int(state["home_red_cards"]),
                away_red_cards=int(state["away_red_cards"]),
                period=MatchPeriod(str(state["period"])),
            )
        except (KeyError, TypeError, ValueError):
            # An older or hand-written row that does not carry the state is
            # not a reason to refuse to predict; it is a reason to treat this
            # as the first prediction and regenerate.
            return None

    def ensure_history_backfill(
        self,
        inputs: FixtureEngineInput,
        *,
        matches_needed_by_feature_group: dict[str, int],
    ) -> tuple[str, ...]:
        if set(matches_needed_by_feature_group) != {"results", "rich_statistics"}:
            raise ValueError("matches_needed must cover results and rich_statistics")
        if any(value <= 0 for value in matches_needed_by_feature_group.values()):
            raise ValueError("matches_needed values must be positive")
        target = inputs.target_fixture
        counts = {target.home_team_id: 0, target.away_team_id: 0}
        statistics_counts = {target.home_team_id: 0, target.away_team_id: 0}
        for record in inputs.history:
            if record.season not in {target.season, target.season - 1}:
                continue
            for team_id in counts:
                if record.home_team_id == team_id or record.away_team_id == team_id:
                    counts[team_id] += 1
            for stats in inputs.statistics_by_fixture.get(record.id, []):
                if stats.team_id in statistics_counts:
                    statistics_counts[stats.team_id] += 1
        result_minimum = matches_needed_by_feature_group["results"]
        statistics_minimum = matches_needed_by_feature_group["rich_statistics"]
        requests = [
            {
                "target_fixture_id": target.id,
                "team_id": team_id,
                "competition_id": target.league_id,
                "feature_group": "results",
                "matches_needed": result_minimum,
            }
            for team_id, count in counts.items()
            if count < result_minimum
        ]
        requests.extend(
            {
                "target_fixture_id": target.id,
                "team_id": team_id,
                "competition_id": target.league_id,
                "feature_group": "rich_statistics",
                "matches_needed": statistics_minimum,
            }
            for team_id, count in statistics_counts.items()
            if count < statistics_minimum
        )
        if not requests:
            return ()
        try:
            rows = self._connection.execute(
                _UPSERT_HISTORY_BACKFILL_SQL,
                {"requests": json.dumps(requests, sort_keys=True)},
            ).fetchall()
            self._connection.commit()
        except Exception:
            self._connection.rollback()
            raise
        return tuple(str(row["status"]) for row in rows if row["feature_group"] == "results")


@dataclass(frozen=True)
class PredictionWriteStats:
    inserted: int = 0
    versioned: int = 0
    unchanged: int = 0
    stale: int = 0

    @property
    def evaluated(self) -> int:
        return self.inserted + self.versioned + self.unchanged + self.stale

    @property
    def written(self) -> int:
        return self.inserted + self.versioned

    def __add__(self, other: PredictionWriteStats) -> PredictionWriteStats:
        return PredictionWriteStats(
            inserted=self.inserted + other.inserted,
            versioned=self.versioned + other.versioned,
            unchanged=self.unchanged + other.unchanged,
            stale=self.stale + other.stale,
        )

    def __sub__(self, other: PredictionWriteStats) -> PredictionWriteStats:
        return PredictionWriteStats(
            inserted=self.inserted - other.inserted,
            versioned=self.versioned - other.versioned,
            unchanged=self.unchanged - other.unchanged,
            stale=self.stale - other.stale,
        )


class EnginePredictionRepository:
    def __init__(
        self,
        connection: ConnectionLike,
        jsonb_adapter: Callable[[dict[str, object]], object] | None = None,
        *,
        lifecycle_stability_tolerance: float = 0.20,
        lifecycle_direction_change_penalty: float = 0.50,
        lifecycle_neutral_score: float = 0.50,
    ) -> None:
        self._connection = connection
        self._jsonb_adapter = jsonb_adapter or (lambda value: value)
        self._lifecycle_stability_tolerance = lifecycle_stability_tolerance
        self._lifecycle_direction_change_penalty = lifecycle_direction_change_penalty
        self._lifecycle_neutral_score = lifecycle_neutral_score
        self.last_write_stats = PredictionWriteStats()
        self.total_write_stats = PredictionWriteStats()

    def save_predictions(self, predictions: list[EnginePrediction], *, commit: bool = True) -> PredictionWriteStats:
        stats = PredictionWriteStats()
        try:
            # Stable lock ordering prevents two overlapping multi-market writes
            # from deadlocking even if callers provide predictions differently.
            for prediction in sorted(predictions, key=lambda item: (item.fixture_id, item.market)):
                row = prediction.with_id()
                row.validate()
                params = row.to_db_row()
                params["features_used"] = self._jsonb_adapter(row.features_used)
                self._connection.execute(
                    _PREDICTION_WRITE_LOCK_SQL,
                    {"lock_key": f"{row.fixture_id}:{row.market}"},
                )
                current = self._connection.execute(_CURRENT_PREDICTION_SQL, params).fetchone()
                if current is not None and bool(current["content_matches"]):
                    self._connection.execute(
                        _TOUCH_LAST_EVALUATED_SQL,
                        {"id": current["id"], "last_evaluated_at": row.generated_at},
                    )
                    stats += PredictionWriteStats(unchanged=1)
                    continue
                if current is not None and row.generated_at < current["generated_at"]:  # type: ignore[operator]
                    stats += PredictionWriteStats(stale=1)
                    continue
                previous_probability = None
                previous_prediction = None
                previous_max_shift = None
                if current is not None and current.get("probability") is not None:
                    previous_probability = float(
                        current.get("calibrated_probability")
                        if current.get("calibrated_probability") is not None
                        else current["probability"]
                    )
                    previous_prediction = str(current["prediction"])
                    if current.get("max_lifecycle_probability_shift") is not None:
                        previous_max_shift = float(current["max_lifecycle_probability_shift"])
                stability = calculate_lifecycle_stability(
                    probability=row.probability,
                    prediction=row.prediction,
                    previous_probability=previous_probability,
                    previous_prediction=previous_prediction,
                    previous_max_shift=previous_max_shift,
                    tolerance=self._lifecycle_stability_tolerance,
                    direction_change_penalty=self._lifecycle_direction_change_penalty,
                    neutral_score=self._lifecycle_neutral_score,
                )
                row = replace(
                    row,
                    previous_probability=stability.previous_probability,
                    probability_delta=stability.probability_delta,
                    max_lifecycle_probability_shift=stability.max_lifecycle_probability_shift,
                    prediction_direction_changed=stability.prediction_direction_changed,
                    lifecycle_stability_score=stability.lifecycle_stability_score,
                )
                params.update(row.to_db_row())
                params["features_used"] = self._jsonb_adapter(row.features_used)
                if current is not None:
                    self._connection.execute(
                        _SUPERSEDE_SQL,
                        {"id": current["id"], "superseded_at": row.generated_at},
                    )
                    stats += PredictionWriteStats(versioned=1)
                else:
                    stats += PredictionWriteStats(inserted=1)
                self._connection.execute(_INSERT_SQL, params)
            if commit:
                self._connection.commit()
        except Exception:
            self._connection.rollback()
            raise
        if commit:
            self.last_write_stats = stats
            self.total_write_stats += stats
        return stats

    def replace_in_play_predictions(
        self, fixture_id: int, predictions: list[EnginePrediction], *, generated_at: datetime
    ) -> PredictionWriteStats:
        """Atomically publish one complete model read for a live fixture.

        The per-market writer retains historical versions, but by itself it
        cannot identify a ladder line which disappeared after a goal. The
        membership list records exactly which keys are public in this run.
        Absent rows remain active for settlement, whose candidate query reads
        unsuperseded predictions, but the API excludes them from the board.
        A valid empty set also advances the watermark against delayed jobs.
        """
        generated_at = _validated_as_of_timestamp(generated_at)
        keys: list[str] = []
        for row in predictions:
            if row.fixture_id != fixture_id or row.generated_at != generated_at:
                raise ValueError("in-play publication rows must share fixture and generation time")
            if row.lifecycle_stage not in IN_PLAY_LIFECYCLE_STAGES:
                raise ValueError("in-play publication contains a non-in-play row")
            row.validate()
            keys.append(row.market)
        if len(keys) != len(set(keys)):
            raise ValueError("in-play publication contains duplicate market keys")

        try:
            # Serialise whole fixture publications, including an empty one.
            # The ordinary writer's per-market locks then keep their existing
            # lock order within this fixture-level critical section.
            self._connection.execute(_IN_PLAY_FIXTURE_WRITE_LOCK_SQL, {"fixture_id": fixture_id})
            previous = self._connection.execute(
                _IN_PLAY_PUBLICATION_WATERMARK_SQL, {"fixture_id": fixture_id}
            ).fetchone()
            if previous is not None and generated_at <= previous["generated_at"]:
                self._connection.commit()
                stats = PredictionWriteStats(stale=len(predictions))
                self.last_write_stats = stats
                self.total_write_stats += stats
                return stats

            stats = self.save_predictions(predictions, commit=False)
            self._connection.execute(_UPSERT_IN_PLAY_PUBLICATION_WATERMARK_SQL, {
                "fixture_id": fixture_id, "generated_at": generated_at,
                "current_markets": keys,
            })
            self._connection.commit()
        except Exception:
            self._connection.rollback()
            raise
        self.last_write_stats = stats
        self.total_write_stats += stats
        return stats

    def write_provisional_odds(self, snapshot: OddsSnapshot) -> None:
        """Persist synthesized 1x2 odds under a provisional bookmaker so the
        public odds surfaces have a value until real odds arrive. Real odds
        always take precedence because reads exclude or deprioritize this
        bookmaker."""
        try:
            self._connection.execute(
                _DELETE_PROVISIONAL_ODDS_SQL,
                {"fixture_id": snapshot.fixture_id},
            )
            for outcome, value in (
                ("Home", snapshot.home_win),
                ("Draw", snapshot.draw),
                ("Away", snapshot.away_win),
            ):
                self._connection.execute(
                    _INSERT_PROVISIONAL_ODDS_SQL,
                    {
                        "fixture_id": snapshot.fixture_id,
                        "outcome": outcome,
                        "value": value,
                        "implied_prob": round(1.0 / value, 6) if value > 0 else None,
                    },
                )
            self._connection.commit()
        except Exception:
            self._connection.rollback()
            raise


class PredictionSettlementRepository:
    def __init__(self, connection: ConnectionLike) -> None:
        self._connection = connection

    def preview_btts_corrections(self, fixture_id: int, *, limit: int = 500) -> dict[str, object]:
        """Read-only diagnosis of the former fixed-2.5 combined-market grader.

        Deliberately has no apply switch: result revisions and accuracy recounts
        alone do not correct immutable model_evaluation_facts. A historical
        repair must account for that derived evidence before writing anything.
        One SELECT keeps the official scores and previous grades in one snapshot.
        """
        if fixture_id <= 0 or not 1 <= limit <= 1000:
            raise ValueError("positive fixture_id and limit between 1 and 1000 required")
        try:
            candidates = self._connection.execute(
                _BTTS_CORRECTION_PREVIEW_SQL, {"fixture_id": fixture_id, "limit": limit + 1}
            ).fetchall()
            report_rows: list[dict[str, object]] = []
            skipped = 0
            for row in candidates[:limit]:
                home, away = row["home_goals_ft"], row["away_goals_ft"]
                if (row["market"] != "btts_total_goals" or row["settlement_status"] != "settled"
                        or str(row["status"]).lower() not in {"ft", "aet", "pen", "finished"}
                        or home is None or away is None or int(home) < 0 or int(away) < 0):
                    skipped += 1
                    continue
                recomputed = settle_prediction(
                    "btts_total_goals", str(row["prediction"]),
                    ActualFixtureResult(home_goals=int(home), away_goals=int(away)),
                )
                report_rows.append({
                    "prediction_id": str(row["id"]), "selection": str(row["prediction"]),
                    "source": str(row["source"]), "score": [int(home), int(away)],
                    "previous_actual": row["previous_actual"],
                    "previous_is_correct": row["previous_is_correct"],
                    "proposed_actual": recomputed.actual, "proposed_is_correct": recomputed.is_correct,
                    "changed": (row["previous_actual"] != recomputed.actual
                                or row["previous_is_correct"] != recomputed.is_correct),
                })
            return {
                "dry_run": True, "fixture_id": fixture_id,
                "examined": len(candidates[:limit]), "skipped": skipped,
                "changed": sum(bool(row["changed"]) for row in report_rows),
                "truncated": len(candidates) > limit, "rows": report_rows,
                "application_blocker": "Historical application requires audited correction of model_evaluation_facts and accuracy rollups.",
            }
        finally:
            # The command owns its connection. Release the read snapshot without
            # committing anything, including on malformed legacy selections.
            self._connection.rollback()

    def list_settlement_fixture_ids(self, *, scope: str, limit: int, now: datetime) -> list[int]:
        if limit <= 0:
            raise ValueError("limit must be positive")
        normalized_scope = scope.strip().lower().replace("-", "_")
        if normalized_scope not in {"finished", "historical", "replay"}:
            raise ValueError(f"unsupported settlement scope: {scope}")
        rows = self._connection.execute(
            _SETTLEMENT_FIXTURE_IDS_SQL,
            {"limit": limit, "now": now},
        ).fetchall()
        return [int(row["fixture_id"]) for row in rows]

    def settle_fixture(self, fixture_id: int, *, settled_at: datetime) -> list[SettlementResult]:
        try:
            fixture_row = self._connection.execute(_SETTLEMENT_FIXTURE_SQL, {"fixture_id": fixture_id}).fetchone()
            if fixture_row is None:
                raise LookupError(f"finished fixture {fixture_id} not found")
            actual = _map_actual_fixture_result(fixture_row, self._load_fixture_stat_totals(fixture_id, fixture_row))
            prediction_rows = self._connection.execute(
                _CURRENT_UNSETTLED_PREDICTIONS_SQL,
                {"fixture_id": fixture_id},
            ).fetchall()

            # A prediction with no model_registry_id cannot be settled: the
            # evaluation fact it would write has nothing to attribute the
            # outcome to, and an unattributable row corrupts the accuracy record
            # this engine exists to keep honest.
            #
            # It is not a reason to abandon the fixture, though, and it used to
            # be: this loop raised, so one unattributable prediction discarded
            # settlement for every other prediction on the same match. In
            # production 2,357 of 126,728 current predictions were missing the
            # id -- under 2% -- and 1,070 finished fixtures from the previous
            # seven days had predictions still unsettled behind them. Nothing
            # settled meant no result reached a reader and the accuracy record
            # simply stopped.
            #
            # So the unattributable ones are skipped and reported, and the rest
            # of the fixture settles. They stay unsettled and can be settled
            # later once the production model is linked, which is exactly the
            # state they were already in.
            settleable = [row for row in prediction_rows if row.get("model_registry_id") is not None]
            if len(settleable) != len(prediction_rows):
                skipped = [str(row["id"]) for row in prediction_rows if row.get("model_registry_id") is None]
                print(
                    f"settlement skipped {len(skipped)} prediction(s) on fixture {fixture_id} "
                    f"with no model_registry_id: {', '.join(skipped[:10])}"
                    f"{' ...' if len(skipped) > 10 else ''}; "
                    "bootstrap and link the production model to settle them",
                    flush=True,
                )

            # The goal timeline is loaded once per fixture, not once per row,
            # and only when a row actually needs it -- a fixture whose markets
            # all grade on the score must not pay for a query it cannot use.
            evidence = self._load_goal_evidence(fixture_id, fixture_row, actual, settleable)
            actual = evidence.actual

            results: list[SettlementResult] = []
            for row in settleable:
                anchor = self._row_anchor(row, evidence)
                result = settle_prediction(str(row["market"]), str(row["prediction"]), actual, anchor)
                self._connection.execute(
                    _UPSERT_PREDICTION_RESULT_SQL, _settlement_params(row, result, settled_at, anchor)
                )
                # The initial settlement is recorded in the history too, so a
                # later reversal has something to point back at. A history that
                # only starts when something goes wrong cannot show what was
                # originally said.
                self._connection.execute(
                    _INSERT_PREDICTION_RESULT_REVISION_SQL,
                    _revision_params(row, result, settled_at, REVISION_INITIAL, None, anchor),
                )
                self._connection.execute(
                    _INSERT_MODEL_EVALUATION_FACT_SQL,
                    _evaluation_fact_params(row, result, settled_at),
                )
                results.append(result)
            self._connection.commit()
            return results
        except Exception:
            self._connection.rollback()
            raise

    def void_expired_predictions(
        self,
        *,
        now: datetime,
        limit: int,
        max_pending_age_hours: int = DEFAULT_MAX_PENDING_AGE_HOURS,
        not_played_grace_hours: int = DEFAULT_NOT_PLAYED_GRACE_HOURS,
    ) -> ExpirySweepSummary:
        """Give every prediction that will never get a result a terminal state.

        This is the backstop behind results reconciliation, not a replacement
        for it. Reconciliation fixes the cause -- a result that was never
        fetched -- and it is the better outcome every time it works. But any
        fetch can fail permanently: a fixture the provider drops, a match
        abandoned at 70 minutes, a postponement never replayed. Without a
        bound those predictions stay `pending` forever, which is what a user
        reported on fixture 3735781.

        The bound is `max_pending_age_hours` after kickoff, full stop. Nothing
        can stay pending past it.
        """
        if limit <= 0:
            raise ValueError("limit must be positive")
        if max_pending_age_hours <= 0 or not_played_grace_hours <= 0:
            raise ValueError("expiry thresholds must be positive")
        try:
            rows = self._connection.execute(
                _EXPIRED_PREDICTIONS_SQL,
                {
                    "limit": limit,
                    "stale_cutoff": now - timedelta(hours=max_pending_age_hours),
                    "not_played_cutoff": now - timedelta(hours=not_played_grace_hours),
                },
            ).fetchall()

            voided = 0
            facts_written = 0
            unevaluable: list[str] = []
            reasons: dict[str, int] = {}
            fixtures: set[int] = set()
            for row in rows:
                reason = str(row["void_reason"])
                result = expire_prediction(str(row["market"]), str(row["prediction"]), reason)
                self._connection.execute(_UPSERT_PREDICTION_RESULT_SQL, _settlement_params(row, result, now))
                voided += 1
                reasons[reason] = reasons.get(reason, 0) + 1
                fixtures.add(int(row["fixture_id"]))
                if row.get("model_registry_id") is None:
                    # The prediction still gets its terminal state -- the bound
                    # is not negotiable. Only the evaluation fact is skipped,
                    # because a prediction with no model linked has nothing to
                    # evaluate. It is counted so it cannot pass unnoticed.
                    unevaluable.append(str(row["id"]))
                    continue
                self._connection.execute(
                    _INSERT_MODEL_EVALUATION_FACT_SQL,
                    _evaluation_fact_params(row, result, now),
                )
                facts_written += 1
            self._connection.commit()
            return ExpirySweepSummary(
                examined=len(rows),
                voided=voided,
                facts_written=facts_written,
                fixtures_touched=len(fixtures),
                unevaluable_prediction_ids=tuple(unevaluable),
                reasons=reasons,
            )
        except Exception:
            self._connection.rollback()
            raise

    def list_retracted_settlement_fixture_ids(self, *, limit: int) -> list[int]:
        """Fixtures whose settled predictions rest on withdrawn evidence."""
        if limit <= 0:
            raise ValueError("limit must be positive")
        rows = self._connection.execute(_RETRACTED_SETTLEMENT_FIXTURES_SQL, {"limit": limit}).fetchall()
        return [int(row["fixture_id"]) for row in rows]

    def resettle_fixture(self, fixture_id: int, *, recorded_at: datetime) -> list[Resettlement]:
        """Re-grade every settled prediction on a fixture and apply what changed.

        Called when the provider has withdrawn a goal. Every prediction on the
        fixture is re-graded, not only the ones whose recorded evidence names the
        retracted event: a withdrawn goal also changes the full-time score, and
        therefore the 1X2, both totals and BTTS markets that were graded on the
        score rather than on named events.

        Only changed gradings are written. Most predictions on a fixture are
        unaffected by any one retraction, and a revision per row would bury the
        ones that matter in an audit trail meant to be read.

        Returns every Resettlement, changed or not, so a caller can report how
        much was examined against how much moved.
        """
        try:
            fixture_row = self._connection.execute(_SETTLEMENT_FIXTURE_SQL, {"fixture_id": fixture_id}).fetchone()
            if fixture_row is None:
                raise LookupError(f"finished fixture {fixture_id} not found")
            actual = _map_actual_fixture_result(fixture_row, self._load_fixture_stat_totals(fixture_id, fixture_row))
            rows = self._connection.execute(
                _SETTLED_PREDICTIONS_FOR_FIXTURE_SQL, {"fixture_id": fixture_id}
            ).fetchall()

            evidence = self._load_goal_evidence(fixture_id, fixture_row, actual, rows)
            actual = evidence.actual

            retracted_key = self._newest_retracted_goal_key(fixture_id)

            outcomes: list[Resettlement] = []
            for row in rows:
                anchor = self._row_anchor(row, evidence)
                recomputed = settle_prediction(
                    str(row["market"]), str(row["prediction"]), actual, anchor
                )
                outcome = resettle_prediction(
                    previous_status=str(row["settlement_status"]),
                    previous_actual=None if row["previous_actual"] is None else str(row["previous_actual"]),
                    previous_is_correct=row["previous_is_correct"],
                    recomputed=recomputed,
                    triggered_by_retraction=True,
                )
                outcomes.append(outcome)
                if not outcome.changed:
                    continue
                self._connection.execute(
                    _UPSERT_PREDICTION_RESULT_SQL,
                    _settlement_params(row, outcome.result, recorded_at, anchor),
                )
                self._connection.execute(
                    _INSERT_PREDICTION_RESULT_REVISION_SQL,
                    _revision_params(
                        row,
                        outcome.result,
                        recorded_at,
                        outcome.revision_reason or REVISION_RESCORED,
                        retracted_key,
                        anchor,
                    ),
                )
                # Accuracy must forget a result it has already counted. A no-op
                # when the prediction was never rolled up, which is why it runs
                # unconditionally rather than behind a lookup.
                self._connection.execute(_MARK_ROLLUP_NEEDS_RECOUNT_SQL, {"prediction_id": row["id"]})
            self._connection.commit()
            return outcomes
        except Exception:
            self._connection.rollback()
            raise

    def _newest_retracted_goal_key(self, fixture_id: int) -> str | None:
        """The most recently withdrawn goal on this fixture, for the audit trail.

        Best effort and deliberately not load-bearing: it names the likely cause
        in the revision row. The re-grade itself compares recomputed against
        stored and does not depend on knowing which event moved.
        """
        row = self._connection.execute(
            _NEWEST_RETRACTED_GOAL_SQL, {"fixture_id": fixture_id}
        ).fetchone()
        if row is None:
            return None
        return None if row["identity_key"] is None else str(row["identity_key"])

    def _load_inplay_anchors(
        self,
        fixture_id: int,
        fixture_row: dict[str, object],
        actual: ActualFixtureResult,
    ) -> list[tuple[datetime, int, int, str | None, str]] | None:
        """The goal timeline as (observed_at, home_so_far, away_so_far, team, key).

        Returns None when the timeline cannot be trusted, which is the same test
        `ReconcileScoringEvents` applies before it withdraws anything: the
        per-team counts must equal the provider's authoritative full-time score.
        An incomplete timeline is indistinguishable from a goalless first half,
        and anchoring on it would grade `total_goals_remaining` against goals we
        simply failed to record. Better to close those rows as
        `unsettled_data_unavailable` than to invent a number.
        """
        home_team_id = fixture_row.get("home_team_id")
        away_team_id = fixture_row.get("away_team_id")
        if home_team_id is None or away_team_id is None:
            return None
        rows = self._connection.execute(
            _SETTLEMENT_GOAL_TIMELINE_SQL,
            {"fixture_id": fixture_id},
        ).fetchall()
        return _reconcile_goal_timeline(rows, fixture_row, actual)

    def _load_goal_evidence(
        self,
        fixture_id: int,
        fixture_row: dict[str, object],
        actual: ActualFixtureResult,
        prediction_rows: list[dict[str, object]],
    ) -> "_GoalEvidence":
        """What the event timeline can say about this fixture's predictions.

        Runs the one query both uses share, and only if some row needs it. A
        goalless match needs nothing for `team_to_score_first`: the score alone
        grades it as `no_goals`.
        """
        needs_anchors = any(str(row["lifecycle_stage"]) in _IN_PLAY_STAGE_VALUES for row in prediction_rows)
        needs_first_goal = actual.home_goals + actual.away_goals > 0 and any(
            str(row["market"]) == _FIRST_GOAL_MARKET for row in prediction_rows
        )
        if not needs_anchors and not needs_first_goal:
            return _GoalEvidence(actual=actual)
        if fixture_row.get("home_team_id") is None or fixture_row.get("away_team_id") is None:
            return _GoalEvidence(actual=actual)
        goal_rows = self._connection.execute(
            _SETTLEMENT_GOAL_TIMELINE_SQL,
            {"fixture_id": fixture_id},
        ).fetchall()
        timeline = _reconcile_goal_timeline(goal_rows, fixture_row, actual)
        first_goal = _first_goal_anchor(goal_rows, fixture_row) if needs_first_goal and timeline is not None else None
        if first_goal is None:
            return _GoalEvidence(actual=actual, timeline=timeline)
        return _GoalEvidence(
            actual=replace(actual, first_goal_team=first_goal.next_goal_team),
            timeline=timeline,
            first_goal=first_goal,
        )

    def _row_anchor(self, row: dict[str, object], evidence: "_GoalEvidence") -> InPlayAnchor | None:
        if str(row["market"]) == _FIRST_GOAL_MARKET:
            return evidence.first_goal
        if evidence.timeline is not None and str(row["lifecycle_stage"]) in _IN_PLAY_STAGE_VALUES:
            return self._anchor_at(evidence.timeline, row.get("data_cutoff_at"))
        return None

    @staticmethod
    def _anchor_at(
        timeline: list[tuple[datetime, int, int, str | None, str]],
        cutoff: datetime | None,
    ) -> InPlayAnchor | None:
        """The score as at `cutoff`, which side scored next, and on what evidence.

        `deciding_event_keys` is every goal after the cutoff, not just the next
        one, because `total_goals_remaining` depends on all of them while
        `next_goal` depends only on the first. Recording the union keeps one
        anchor usable for both, and a retraction of any of them is a reason to
        re-grade.
        """
        if cutoff is None:
            return None
        home_goals, away_goals = 0, 0
        next_goal_team: str | None = None
        deciding: list[str] = []
        for observed_at, home_so_far, away_so_far, side, key in timeline:
            if observed_at <= cutoff:
                home_goals, away_goals = home_so_far, away_so_far
                continue
            if next_goal_team is None:
                next_goal_team = side
            deciding.append(key)
        return InPlayAnchor(
            home_goals=home_goals,
            away_goals=away_goals,
            next_goal_team=next_goal_team,
            deciding_event_keys=tuple(deciding),
        )

    def _load_fixture_stat_totals(self, fixture_id: int, fixture_row: dict[str, object]) -> dict[str, int | float | None]:
        rows = self._connection.execute(
            _SETTLEMENT_STATISTICS_SQL,
            {
                "fixture_id": fixture_id,
                "home_team_id": fixture_row["home_team_id"],
                "away_team_id": fixture_row["away_team_id"],
            },
        ).fetchall()
        totals: dict[str, int | float | None] = {
            "home_corners": None,
            "away_corners": None,
            "home_card_points": None,
            "away_card_points": None,
            "home_fouls": None,
            "away_fouls": None,
            "home_shots_total": None,
            "away_shots_total": None,
            "home_shots_on_target": None,
            "away_shots_on_target": None,
            "home_possession": None,
            "away_possession": None,
        }
        for row in rows:
            team_id = int(row["team_id"])
            prefix = "home" if team_id == int(fixture_row["home_team_id"]) else "away"
            totals[f"{prefix}_corners"] = _optional_int(row.get("corners"))
            totals[f"{prefix}_card_points"] = _optional_int(row.get("card_points"))
            totals[f"{prefix}_fouls"] = _optional_int(row.get("fouls"))
            totals[f"{prefix}_shots_total"] = _optional_int(row.get("shots_total"))
            totals[f"{prefix}_shots_on_target"] = _optional_int(row.get("shots_on_target"))
            totals[f"{prefix}_possession"] = _optional_float(row.get("possession_pct"))
        return totals


class AccuracySnapshotRepository:
    def __init__(self, connection: ConnectionLike) -> None:
        self._connection = connection

    def save_snapshot(self, snapshot: AccuracySnapshot) -> None:
        try:
            self._connection.execute(
                _UPSERT_ACCURACY_SNAPSHOT_SQL,
                {
                    "snapshot_date": snapshot.snapshot_date,
                    "scope": snapshot.scope,
                    "scope_id": snapshot.scope_id,
                    "total_predictions": snapshot.total_predictions,
                    "correct": snapshot.correct,
                    "accuracy": snapshot.accuracy,
                },
            )
            self._connection.commit()
        except Exception:
            self._connection.rollback()
            raise

    def compute_and_save_snapshots(self, *, snapshot_date: date) -> list[AccuracySnapshot]:
        try:
            rows = self._connection.execute(_SETTLED_ACCURACY_ROWS_SQL, {}).fetchall()
            snapshots = _accuracy_snapshots_from_rows(rows, snapshot_date=snapshot_date)
            for snapshot in snapshots:
                self._connection.execute(_UPSERT_ACCURACY_SNAPSHOT_SQL, _accuracy_snapshot_params(snapshot))
            self._connection.commit()
            return snapshots
        except Exception:
            self._connection.rollback()
            raise


@dataclass(frozen=True)
class PredictionRetentionStats:
    results_examined: int = 0
    snapshots_written: int = 0
    results_pruned: int = 0

    def __add__(self, other: PredictionRetentionStats) -> PredictionRetentionStats:
        return PredictionRetentionStats(
            results_examined=self.results_examined + other.results_examined,
            snapshots_written=self.snapshots_written + other.snapshots_written,
            results_pruned=self.results_pruned + other.results_pruned,
        )


@dataclass(frozen=True)
class InPlaySnapshotRef:
    """Where a written snapshot landed in the stream."""

    id: int
    sequence: int


class InPlaySnapshotRepository:
    """Writes the in-play model time series (000079).

    Separate from `EnginePredictionRepository` because the two records answer
    different questions: that one writes the settleable published prediction, this
    one writes the series a client graphs. Sharing a repository would invite
    sharing a grain, and the grain is the whole point of keeping them apart.
    """

    # A "before" snapshot older than this does not describe the state
    # immediately preceding the event. Matches the 60-second timer with headroom
    # for a late poll.
    DEFAULT_BEFORE_MAX_AGE_SECONDS = 90

    def __init__(self, connection: ConnectionLike, *, jsonb_adapter: Callable[[object], object] | None = None) -> None:
        self._connection = connection
        self._jsonb = jsonb_adapter or (lambda value: json.dumps(value, sort_keys=True))

    def write_snapshot(
        self,
        *,
        fixture_id: int,
        captured_at: datetime,
        trigger: str,
        status: str,
        model_health: str,
        model_version: str,
        markets: dict[str, dict[str, int]] | None = None,
        data_cutoff_at: datetime | None = None,
        triggered_by_event_key: str | None = None,
        period: str | None = None,
        elapsed_minute: int | None = None,
        elapsed_extra: int | None = None,
        home_goals: int | None = None,
        away_goals: int | None = None,
        home_red_cards: int | None = None,
        away_red_cards: int | None = None,
        suppression_reason: str | None = None,
        feature_version: str | None = None,
        fixture_live_snapshot_id: int | None = None,
    ) -> InPlaySnapshotRef:
        """Append one snapshot and return its stream position.

        `trigger = 'final'` also writes the permanent copy, in the same
        transaction: the frozen full-time record and the series row must not be
        able to disagree, and a separate pass could crash between them.
        """
        if fixture_id <= 0:
            raise ValueError("fixture_id must be positive")
        params = {
            "fixture_id": fixture_id,
            "captured_at": captured_at,
            "data_cutoff_at": data_cutoff_at,
            "trigger": trigger,
            "triggered_by_event_key": triggered_by_event_key,
            "status": status,
            "period": period,
            "elapsed_minute": elapsed_minute,
            "elapsed_extra": elapsed_extra,
            "home_goals": home_goals,
            "away_goals": away_goals,
            "home_red_cards": home_red_cards,
            "away_red_cards": away_red_cards,
            "model_health": model_health,
            "suppression_reason": suppression_reason,
            "markets": self._jsonb(markets or {}),
            "model_version": model_version,
            "feature_version": feature_version,
            "fixture_live_snapshot_id": fixture_live_snapshot_id,
        }
        try:
            row = self._connection.execute(_INSERT_INPLAY_SNAPSHOT_SQL, params).fetchone()
            if row is None:
                raise RuntimeError(f"in-play snapshot insert returned no row for fixture {fixture_id}")
            ref = InPlaySnapshotRef(id=int(row["id"]), sequence=int(row["sequence"]))
            if trigger == "final":
                final_params = {key: value for key, value in params.items() if key not in {"trigger", "triggered_by_event_key"}}
                final_params["sequence"] = ref.sequence
                self._connection.execute(_INSERT_INPLAY_FINAL_SNAPSHOT_SQL, final_params)
            self._connection.commit()
            return ref
        except Exception:
            self._connection.rollback()
            raise

    def finalise_snapshots(self, *, limit: int) -> tuple[int, int]:
        """Freeze the full-time snapshot for finished fixtures.

        Returns (fixtures_examined, snapshots_frozen). Needs its own sweep because
        the live poll drops a fixture the moment it ends, so no live pass ever sees
        a match at the final whistle.
        """
        if limit <= 0:
            raise ValueError("limit must be positive")
        try:
            row = self._connection.execute(_FINALISE_INPLAY_SNAPSHOTS_SQL, {"limit": limit}).fetchone()
            if row is None:
                raise RuntimeError("snapshot finalisation returned no summary")
            result = (int(row["fixtures_examined"]), int(row["snapshots_frozen"]))
            self._connection.commit()
            return result
        except Exception:
            self._connection.rollback()
            raise

    def snapshot_before_event(
        self,
        *,
        fixture_id: int,
        event_at: datetime,
        max_age_seconds: int | None = None,
    ) -> InPlaySnapshotRef | None:
        """The snapshot describing the state immediately before an event.

        None means no snapshot is recent enough, and the caller must write a
        `pre_*` row rather than link to a stale one.
        """
        row = self._connection.execute(
            _INPLAY_SNAPSHOT_BEFORE_EVENT_SQL,
            {
                "fixture_id": fixture_id,
                "event_at": event_at,
                "max_age_seconds": max_age_seconds or self.DEFAULT_BEFORE_MAX_AGE_SECONDS,
            },
        ).fetchone()
        if row is None:
            return None
        return InPlaySnapshotRef(id=int(row["id"]), sequence=int(row["sequence"]))


class TrackerStateRepository:
    """Maintains the tracker-state projection (client contract §8).

    Its own class rather than a method on the input repository, which reads, or
    the settlement repository, which grades finished fixtures. This reads
    pre-match picks and writes a current-state projection over live ones -- a
    distinct concern, and hanging a writer off something named "Input" is how
    layers stop meaning anything.
    """

    def __init__(self, connection: ConnectionLike) -> None:
        self._connection = connection

    def refresh_tracker_state(
        self,
        fixture_id: int,
        *,
        state: MatchState,
        match_minute: int | None,
        computed_at: datetime,
    ) -> dict[str, int]:
        """Recompute every pre-match pick's tracker state for one fixture.

        Returns a count per state, so a run can report what it judged rather than
        just how many rows it touched -- "forty picks examined" says nothing,
        "thirty-one on track, six won, three unknown" says whether the feed is
        working.

        Picks are read with the kickoff guard, so an in-play row can never be
        served as a pre-match pick. A tracker is public and in-play predictions
        are not.
        """
        try:
            rows = self._connection.execute(
                _TRACKABLE_PREMATCH_PICKS_SQL, {"fixture_id": fixture_id}
            ).fetchall()
            counts: dict[str, int] = {}
            for row in rows:
                judged = tracker_state(
                    market=str(row["market"]),
                    prediction=str(row["prediction"]),
                    state=state,
                    settlement_status=(
                        None if row["settlement_status"] is None else str(row["settlement_status"])
                    ),
                    settled_is_correct=row["is_correct"],
                )
                self._connection.execute(
                    _UPSERT_TRACKER_STATE_SQL,
                    {
                        "prediction_id": row["id"],
                        "fixture_id": int(row["fixture_id"]),
                        "state": judged.value,
                        "home_goals": state.home_goals,
                        "away_goals": state.away_goals,
                        "match_minute": match_minute,
                        "computed_at": computed_at,
                    },
                )
                counts[judged.value] = counts.get(judged.value, 0) + 1
            self._connection.commit()
            return counts
        except Exception:
            self._connection.rollback()
            raise



@dataclass(frozen=True)
class AccuracyRecountStats:
    flagged_examined: int = 0
    predictions_recounted: int = 0
    buckets_adjusted: int = 0
    flags_cleared: int = 0
    # Flagged rows the rollup counted but whose bucket it never recorded. These
    # predate 000078 and stay flagged, because guessing a bucket would corrupt a
    # real one. A non-zero count is a visible debt, not a failure.
    unrecountable: int = 0

    def __add__(self, other: AccuracyRecountStats) -> AccuracyRecountStats:
        return AccuracyRecountStats(
            flagged_examined=self.flagged_examined + other.flagged_examined,
            predictions_recounted=self.predictions_recounted + other.predictions_recounted,
            buckets_adjusted=self.buckets_adjusted + other.buckets_adjusted,
            flags_cleared=self.flags_cleared + other.flags_cleared,
            unrecountable=self.unrecountable + other.unrecountable,
        )


class PredictionRetentionRepository:
    def __init__(self, connection: ConnectionLike) -> None:
        self._connection = connection

    def recount_reversed_results(self, *, limit: int) -> AccuracyRecountStats:
        """Move reversed settlements between accuracy counters in their bucket.

        The other half of reversible settlement. `resettle_fixture` marks
        `needs_recount`; this applies it, so a withdrawn result stops counting as
        correct in the published figure.

        One prediction moves between counters inside the bucket the rollup
        recorded for it -- `total_predictions` does not change, because the
        prediction still happened and was still counted. Only its classification
        moved.
        """
        if limit <= 0:
            raise ValueError("limit must be positive")
        try:
            row = self._connection.execute(_RECOUNT_REVERSED_RESULTS_SQL, {"limit": limit}).fetchone()
            if row is None:
                raise RuntimeError("accuracy recount query returned no summary")
            stats = AccuracyRecountStats(
                flagged_examined=int(row["flagged_examined"]),
                predictions_recounted=int(row["predictions_recounted"]),
                buckets_adjusted=int(row["buckets_adjusted"]),
                flags_cleared=int(row["flags_cleared"]),
                unrecountable=int(row["unrecountable"]),
            )
            self._connection.commit()
            return stats
        except Exception:
            self._connection.rollback()
            raise

    def aggregate_and_prune(self, *, raw_cutoff: datetime, batch_limit: int) -> PredictionRetentionStats:
        if batch_limit <= 0:
            raise ValueError("batch_limit must be positive")
        try:
            row = self._connection.execute(
                _AGGREGATE_AND_PRUNE_RESULTS_SQL,
                {"raw_cutoff": raw_cutoff, "batch_limit": batch_limit},
            ).fetchone()
            if row is None:
                raise RuntimeError("prediction retention query returned no summary")
            stats = PredictionRetentionStats(
                results_examined=int(row["results_examined"]),
                snapshots_written=int(row["snapshots_written"]),
                results_pruned=int(row["results_pruned"]),
            )
            self._connection.commit()
            return stats
        except Exception:
            self._connection.rollback()
            raise


@dataclass(frozen=True)
class InPlayFixtureState:
    """Everything the in-play runner needs that the pre-match path does not."""

    fixture_id: int
    home_team_id: int
    away_team_id: int
    kickoff_at: datetime
    snapshot: LiveSnapshot
    events: tuple[LiveEvent, ...]


def _map_live_snapshot(fixture_id: int, row: dict[str, object]) -> LiveSnapshot:
    return LiveSnapshot(
        fixture_id=fixture_id,
        captured_at=row["captured_at"],  # type: ignore[arg-type]
        status=str(row.get("status") or ""),
        period=(str(row["period"]) if row.get("period") is not None else None),
        elapsed_minute=_optional_int(row.get("elapsed_minute")),
        elapsed_extra=_optional_int(row.get("elapsed_extra")),
        home_goals=_optional_int(row.get("home_goals")),
        away_goals=_optional_int(row.get("away_goals")),
        home_red_cards=_optional_int(row.get("home_red_cards")),
        away_red_cards=_optional_int(row.get("away_red_cards")),
        provider=str(row.get("provider") or ""),
        provider_timestamp=row.get("provider_timestamp"),  # type: ignore[arg-type]
        snapshot_id=_optional_int(row.get("snapshot_id")),
    )


def _map_live_event(fixture_id: int, row: dict[str, object]) -> LiveEvent:
    """One event row, with the provider team id already resolved to canonical.

    `fixture_events.team_id` is the PROVIDER's team id while
    `fixtures.home_team_id` is ours, so the resolution happens in the query
    through provider_entity_maps. An event whose team does not resolve keeps a
    null team id and is counted against neither side, which is the same choice
    the timeline read path makes: a card with no side is better than a card
    charged to the wrong one.
    """
    return LiveEvent(
        fixture_id=fixture_id,
        observed_at=row["observed_at"],  # type: ignore[arg-type]
        elapsed_minute=int(row.get("elapsed_minute") or 0),
        event_type=str(row.get("event_type") or ""),
        event_detail=str(row.get("event_detail") or ""),
        team_id=_optional_int(row.get("canonical_team_id")),
        elapsed_extra=_optional_int(row.get("elapsed_extra")),
        player_id=_optional_int(row.get("player_id")),
        identity_key=(str(row["identity_key"]) if row.get("identity_key") is not None else None),
        is_retracted=_bool(row.get("is_retracted")),
    )


def map_fixture_record(row: dict[str, object]) -> FixtureRecord:
    return FixtureRecord(
        id=int(row["id"]),
        kickoff_at=row["kickoff_at"],  # type: ignore[arg-type]
        league_id=int(row["league_id"]),
        league_name=str(row.get("league_name") or ""),
        season=int(row["season_id"]),
        home_team_id=int(row["home_team_id"]),
        away_team_id=int(row["away_team_id"]),
        home_goals=_optional_int(row.get("home_goals_ft")),
        away_goals=_optional_int(row.get("away_goals_ft")),
        status=str(row["status"]),
        source=str(row.get("source") or ""),
    )


def map_team_fixture_stats(row: dict[str, object]) -> TeamFixtureStats:
    return TeamFixtureStats(
        fixture_id=int(row["fixture_id"]),
        team_id=int(row["team_id"]),
        shots_total=_optional_int(row.get("shots_total")),
        shots_on_target=_optional_int(row.get("shots_on_target")),
        corners=_optional_int(row.get("corners")),
        fouls=_optional_int(row.get("fouls")),
        card_points=_optional_int(row.get("card_points")),
        possession=_optional_float(row.get("possession_pct")),
        xg=_optional_float(row.get("xg")),
    )


def map_odds_snapshot(fixture_id: int, rows: list[dict[str, object]]) -> OddsSnapshot:
    odds: dict[str, float] = {}
    for row in rows:
        outcome = _normalize_one_x_two_outcome(str(row["outcome"]))
        odds[outcome] = float(row["value"])
    missing = [outcome for outcome in ("home", "draw", "away") if outcome not in odds]
    if missing:
        raise ValueError(f"missing 1X2 odds outcome: {', '.join(missing)}")
    return OddsSnapshot(
        fixture_id=fixture_id,
        home_win=odds["home"],
        draw=odds["draw"],
        away_win=odds["away"],
    )


def _map_optional_odds_snapshot(fixture_id: int, rows: list[dict[str, object]]) -> OddsSnapshot | None:
    if not rows:
        return None
    try:
        return map_odds_snapshot(fixture_id, rows)
    except (KeyError, TypeError, ValueError):
        return None


def _optional_int(value: object) -> int | None:
    return None if value is None else int(value)


def _optional_float(value: object) -> float | None:
    return None if value is None else float(value)


def _normalize_one_x_two_outcome(outcome: str) -> str:
    normalized = outcome.strip().lower().replace("_", " ").replace("-", " ")
    if normalized in {"home", "home win", "1"}:
        return "home"
    if normalized in {"draw", "x"}:
        return "draw"
    if normalized in {"away", "away win", "2"}:
        return "away"
    raise ValueError(f"unsupported 1X2 odds outcome: {outcome}")


def _map_coverage(row: dict[str, object] | None) -> dict[str, bool]:
    if row is None:
        return {}
    return {
        key: bool(row.get(key))
        for key in ("has_statistics", "has_xg", "has_injuries", "has_odds", "has_lineups")
    }


def _map_market_accuracy(rows: list[dict[str, object]]) -> dict[str, float]:
    return {
        str(row["scope_id"]): float(row["accuracy"])
        for row in rows
        if str(row.get("scope")) == "market" and row.get("scope_id") is not None
    }


def _map_league_accuracy(rows: list[dict[str, object]]) -> float | None:
    for row in rows:
        if str(row.get("scope")) == "league":
            return float(row["accuracy"])
    return None


_FIRST_GOAL_MARKET = "team_to_score_first"


@dataclass(frozen=True)
class _GoalEvidence:
    """A fixture's result plus what its goal timeline can add to it.

    `timeline` is None when the timeline was not needed or cannot be trusted.
    `first_goal` is the anchor at kickoff: nothing scored yet, and the side that
    scored next. It also names the goals the grade rests on, so a settlement
    records them and a later retraction can be traced to it.
    """

    actual: ActualFixtureResult
    timeline: list[tuple[datetime, int, int, str | None, str]] | None = None
    first_goal: InPlayAnchor | None = None


def _reconcile_goal_timeline(
    rows: list[dict[str, object]],
    fixture_row: dict[str, object],
    actual: ActualFixtureResult,
) -> list[tuple[datetime, int, int, str | None, str]] | None:
    home_team_id = fixture_row["home_team_id"]
    away_team_id = fixture_row["away_team_id"]
    timeline: list[tuple[datetime, int, int, str | None, str]] = []
    home_so_far, away_so_far = 0, 0
    for row in rows:
        team_id = row["team_id"]
        if team_id is None:
            # Unattributable goal: the same bail-out ReconcileScoringEvents
            # takes rather than guessing a side.
            return None
        if int(team_id) == int(home_team_id):
            home_so_far += 1
            side = "home"
        elif int(team_id) == int(away_team_id):
            away_so_far += 1
            side = "away"
        else:
            return None
        timeline.append((row["observed_at"], home_so_far, away_so_far, side, str(row["identity_key"])))
    if home_so_far != actual.home_goals or away_so_far != actual.away_goals:
        return None
    return timeline


def _first_goal_anchor(rows: list[dict[str, object]], fixture_row: dict[str, object]) -> InPlayAnchor | None:
    """Which side scored first, from a timeline that has already reconciled.

    Ordered by the match clock, not by `observed_at`: that is when a poll first
    saw a goal, and a backfill or a batched response sees several at once or out
    of order. 45+3 sorts before 46 because the clock is (minute, added minute).

    Goals for both sides in the same minute are not separated by anything the
    provider sends, so the first scorer is left unset and the market closes as
    `unsettled_data_unavailable` rather than guessing.
    """
    if not rows:
        return None

    def clock(row: dict[str, object]) -> tuple[int, int]:
        return int(row["elapsed_minute"]), int(row.get("elapsed_extra") or 0)

    earliest = min(clock(row) for row in rows)
    opening = [row for row in rows if clock(row) == earliest]
    team_ids = {int(row["team_id"]) for row in opening}
    if len(team_ids) != 1:
        return None
    side = "home" if team_ids == {int(fixture_row["home_team_id"])} else "away"
    return InPlayAnchor(
        home_goals=0,
        away_goals=0,
        next_goal_team=side,
        deciding_event_keys=tuple(str(row["identity_key"]) for row in opening),
    )


def _map_actual_fixture_result(row: dict[str, object], stat_totals: dict[str, int | float | None]) -> ActualFixtureResult:
    return ActualFixtureResult(
        home_goals=int(row["home_goals_ft"]),
        away_goals=int(row["away_goals_ft"]),
        home_goals_ht=_optional_int(row.get("home_goals_ht")),
        away_goals_ht=_optional_int(row.get("away_goals_ht")),
        home_corners=stat_totals.get("home_corners"),
        away_corners=stat_totals.get("away_corners"),
        home_card_points=stat_totals.get("home_card_points"),
        away_card_points=stat_totals.get("away_card_points"),
        home_fouls=stat_totals.get("home_fouls"),
        away_fouls=stat_totals.get("away_fouls"),
        home_shots_total=stat_totals.get("home_shots_total"),
        away_shots_total=stat_totals.get("away_shots_total"),
        home_shots_on_target=stat_totals.get("home_shots_on_target"),
        away_shots_on_target=stat_totals.get("away_shots_on_target"),
        home_possession=stat_totals.get("home_possession"),
        away_possession=stat_totals.get("away_possession"),
    )


def _settlement_params(
    row: dict[str, object],
    result: SettlementResult,
    settled_at: datetime,
    anchor: InPlayAnchor | None = None,
) -> dict[str, object]:
    return {
        # NULL rather than an empty array when nothing named decided the grade:
        # the column's comment reads NULL as "decided by the full-time score",
        # and an empty array would instead claim "decided by no events at all".
        "settled_by_event_keys": list(anchor.deciding_event_keys)
        if anchor is not None and anchor.deciding_event_keys
        else None,
        "prediction_id": row["id"],
        "fixture_id": int(row["fixture_id"]),
        "market": str(row["market"]),
        "predicted": str(row["prediction"]),
        "actual": result.actual,
        "is_correct": result.is_correct,
        "settlement_status": result.status,
        "void_reason": result.void_reason,
        "probability": float(row["probability"]),
        "confidence": float(row["confidence"]),
        "confidence_tier": str(row["confidence_tier"]),
        "lifecycle_stage_at_settlement": str(row["lifecycle_stage"]),
        "league_id": int(row["league_id"]),
        "settled_at": settled_at,
    }


def _revision_params(
    row: dict[str, object],
    result: SettlementResult,
    recorded_at: datetime,
    revision_reason: str,
    triggered_by_event_key: str | None,
    anchor: InPlayAnchor | None = None,
) -> dict[str, object]:
    return {
        "prediction_id": row["id"],
        "fixture_id": int(row["fixture_id"]),
        "settlement_status": result.status,
        "actual": result.actual,
        "is_correct": result.is_correct,
        "void_reason": result.void_reason,
        "settled_by_event_keys": list(anchor.deciding_event_keys)
        if anchor is not None and anchor.deciding_event_keys
        else None,
        "revision_reason": revision_reason,
        "triggered_by_event_key": triggered_by_event_key,
        "recorded_at": recorded_at,
    }


def _evaluation_fact_params(
    row: dict[str, object],
    result: SettlementResult,
    settled_at: datetime,
) -> dict[str, object]:
    if result.status == "settled" and result.is_correct is not None:
        classification = "correct" if result.is_correct else "incorrect"
        eligible = True
        exclusion_reason = None
    elif result.status in {"void", "push"}:
        classification = result.status
        eligible = False
        exclusion_reason = f"settlement_{result.status}"
        if result.void_reason:
            # A void the market rule produced and a void that means "no result
            # ever arrived" must not be indistinguishable in the evaluation
            # ledger -- the first is evidence about the model, the second is
            # evidence about the feed.
            exclusion_reason = f"{exclusion_reason}:{result.void_reason}"
    else:
        classification = "excluded"
        eligible = False
        exclusion_reason = result.status

    raw_probability = row.get("raw_probability")
    if raw_probability is None:
        # For rows written by python-engine-v1 the stored probability is
        # the uncalibrated model output. Historical uncertainty remains
        # explicit in registry metadata; other model versions must populate
        # raw_probability before they can be settled through this writer.
        if str(row.get("model_version")) != "python-engine-v1":
            raise ValueError(
                f"prediction {row['id']} has no raw_probability for model "
                f"{row.get('model_version')}"
            )
        raw_probability = row["probability"]

    return {
        "prediction_id": row["id"],
        "model_registry_id": row["model_registry_id"],
        "fixture_id": int(row["fixture_id"]),
        "market": str(row["market"]),
        "selection": str(row["prediction"]),
        "lifecycle_stage": str(row["lifecycle_stage"]),
        "prediction_timestamp": row["generated_at"],
        "data_cutoff_at": row.get("data_cutoff_at"),
        "feature_version": row.get("feature_version"),
        "raw_probability": float(raw_probability),
        "calibrated_probability": row.get("calibrated_probability"),
        "confidence": float(row["confidence"]),
        "final_outcome": result.actual or "data_unavailable",
        "settlement_classification": classification,
        "settled_at": settled_at,
        "odds_snapshot_id": row.get("odds_snapshot_id"),
        "evaluation_eligible": eligible,
        "exclusion_reason": exclusion_reason,
        "evaluation_contract_version": "model-evaluation-v1",
    }


def _accuracy_snapshot_params(snapshot: AccuracySnapshot) -> dict[str, object]:
    return {
        "snapshot_date": snapshot.snapshot_date,
        "scope": snapshot.scope,
        "scope_id": snapshot.scope_id,
        "total_predictions": snapshot.total_predictions,
        "correct": snapshot.correct,
        "accuracy": snapshot.accuracy,
    }


def _accuracy_snapshots_from_rows(rows: list[dict[str, object]], *, snapshot_date: date) -> list[AccuracySnapshot]:
    if not rows:
        return [_accuracy_snapshot_from_counts(snapshot_date, "overall", None, total=0, correct=0)]
    return [
        _accuracy_snapshot_from_counts(
            snapshot_date,
            str(row["scope"]),
            None if row.get("scope_id") is None else str(row["scope_id"]),
            total=int(row["total_predictions"]),
            correct=int(row["correct"]),
        )
        for row in rows
    ]


def _accuracy_snapshot_from_values(
    snapshot_date: date,
    scope: str,
    scope_id: str | None,
    values: list[bool],
) -> AccuracySnapshot:
    correct = sum(1 for value in values if value)
    total = len(values)
    return AccuracySnapshot(
        snapshot_date=snapshot_date,
        scope=scope,
        scope_id=scope_id,
        total_predictions=total,
        correct=correct,
        accuracy=(correct / total) if total else 0.0,
    )


def _accuracy_snapshot_from_counts(
    snapshot_date: date,
    scope: str,
    scope_id: str | None,
    *,
    total: int,
    correct: int,
) -> AccuracySnapshot:
    return AccuracySnapshot(
        snapshot_date=snapshot_date,
        scope=scope,
        scope_id=scope_id,
        total_predictions=total,
        correct=correct,
        accuracy=(correct / total) if total else 0.0,
    )


def _bool(value: object) -> bool:
    return bool(value)


def _validated_as_of_timestamp(value: datetime) -> datetime:
    if value.tzinfo is None or value.utcoffset() is None:
        raise ValueError("as_of_timestamp must be timezone-aware")
    return value.astimezone(UTC)


_TARGET_FIXTURE_SQL = """
SELECT
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
FROM fixtures AS f
LEFT JOIN leagues AS l ON l.id = f.league_id
WHERE f.id = %(fixture_id)s
"""


_HISTORY_FIXTURES_SQL = """
SELECT
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
FROM fixtures AS f
LEFT JOIN leagues AS l ON l.id = f.league_id
WHERE f.id <> %(fixture_id)s
  AND f.season_id IN (%(season_id)s, %(season_id)s - 1)
  AND f.kickoff_at < %(target_kickoff_at)s
  AND f.kickoff_at < %(as_of_timestamp)s
  AND f.updated_at <= %(target_kickoff_at)s
  AND f.updated_at <= %(as_of_timestamp)s
  AND f.home_goals_ft IS NOT NULL
  AND f.away_goals_ft IS NOT NULL
  AND (
    f.home_team_id IN (%(home_team_id)s, %(away_team_id)s)
    OR f.away_team_id IN (%(home_team_id)s, %(away_team_id)s)
  )
ORDER BY f.kickoff_at DESC
LIMIT 80
"""


# Same point-in-time rules as the history above, across whole leagues. The limit
# only bounds a corrupt league: two seasons of the largest real one fit well inside.
_LEAGUE_HISTORY_FIXTURES_SQL = """
SELECT
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
FROM fixtures AS f
LEFT JOIN leagues AS l ON l.id = f.league_id
WHERE f.league_id = ANY(%(league_ids)s)
  AND f.season_id IN (%(season_id)s, %(season_id)s - 1)
  AND f.kickoff_at < %(target_kickoff_at)s
  AND f.kickoff_at < %(as_of_timestamp)s
  AND f.updated_at <= %(target_kickoff_at)s
  AND f.updated_at <= %(as_of_timestamp)s
  AND f.home_goals_ft IS NOT NULL
  AND f.away_goals_ft IS NOT NULL
ORDER BY f.kickoff_at DESC
LIMIT 5000
"""


# Cards shown to one team in one fixture: yellow + red.
#
# Read from the raw counts, never from the stored card_points column. That
# column was written in two scales -- the football-data importer stored
# yellow + 2*red, the live statistics job yellow*10 + red*25 -- so a
# total_cards line of 2.5-5.5 meant cards on one team's history and booking
# points on the next, and "over 5.5" settled as won from a single yellow on
# any fixture the live job had written. Deriving the count here gives
# forecasting and settlement one unit whichever writer stored the row.
#
# The statistics provider reports a zero as null. Both counts null is therefore a clean
# match when the row was otherwise captured (fouls, shots or corners present)
# and a missing record only when nothing was. The old path skipped every such
# clean match, which biased team averages upward.
_CARD_COUNT_SQL = """
  CASE
    WHEN yellow_cards IS NULL AND red_cards IS NULL THEN
      CASE WHEN fouls IS NOT NULL OR shots_total IS NOT NULL OR corners IS NOT NULL THEN 0 END
    ELSE COALESCE(yellow_cards, 0) + COALESCE(red_cards, 0)
  END AS card_points"""


_FIXTURE_STATISTICS_SQL = """
SELECT
  fixture_id,
  team_id,
  shots_total,
  shots_on_target,
  corners,
  fouls,""" + _CARD_COUNT_SQL + """,
  possession_pct,
  xg
FROM fixture_statistics
WHERE fixture_id = ANY(%(fixture_ids)s)
  AND updated_at <= %(as_of_timestamp)s
ORDER BY fixture_id DESC, team_id
"""


_LATEST_ONE_X_TWO_ODDS_SQL = """
WITH latest_snapshot AS (
  SELECT fetched_at
  FROM odds
  WHERE fixture_id = %(fixture_id)s
    AND lower(market) IN ('1x2', 'match winner', 'match_winner')
    AND lower(COALESCE(bookmaker, '')) <> 'sokalens-provisional'
    AND fetched_at <= %(as_of_timestamp)s
  ORDER BY fetched_at DESC
  LIMIT 1
)
SELECT o.outcome, o.value
FROM odds AS o
JOIN latest_snapshot AS latest ON latest.fetched_at = o.fetched_at
WHERE o.fixture_id = %(fixture_id)s
  AND lower(o.market) IN ('1x2', 'match winner', 'match_winner')
  AND lower(COALESCE(o.bookmaker, '')) <> 'sokalens-provisional'
ORDER BY o.outcome
"""

_DELETE_PROVISIONAL_ODDS_SQL = """
DELETE FROM odds
WHERE fixture_id = %(fixture_id)s
  AND lower(COALESCE(bookmaker, '')) = 'sokalens-provisional'
"""

_INSERT_PROVISIONAL_ODDS_SQL = """
INSERT INTO odds (fixture_id, bookmaker, market, outcome, value, implied_prob, fetched_at, source_snapshot_id)
VALUES (%(fixture_id)s, 'sokalens-provisional', '1x2', %(outcome)s, %(value)s, %(implied_prob)s, now(), NULL)
"""


_LEAGUE_COVERAGE_SQL = """
SELECT has_statistics, has_xg, has_injuries, has_odds, has_lineups
FROM league_coverage
WHERE league_id = %(league_id)s
  AND season_id = %(season_id)s
  AND updated_at <= %(as_of_timestamp)s
"""


_UPSERT_HISTORY_BACKFILL_SQL = """
WITH requested AS (
  SELECT *
  FROM jsonb_to_recordset(%(requests)s::jsonb) AS request(
    target_fixture_id BIGINT,
    team_id BIGINT,
    competition_id BIGINT,
    feature_group TEXT,
    matches_needed INTEGER
  )
), upserted AS (
  INSERT INTO data_backfill_requests (
    target_fixture_id,
    team_id,
    competition_id,
    feature_group,
    matches_needed,
    status
  )
  SELECT target_fixture_id, team_id, competition_id, feature_group, matches_needed, 'pending'
  FROM requested
  ON CONFLICT (target_fixture_id, team_id, competition_id, feature_group)
  DO UPDATE SET
    matches_needed = GREATEST(data_backfill_requests.matches_needed, EXCLUDED.matches_needed),
    status = CASE
      WHEN data_backfill_requests.status IN ('pending', 'in_progress', 'expired')
       AND data_backfill_requests.expires_at <= now()
       AND EXISTS (SELECT 1 FROM fixtures AS target
                   WHERE target.id = EXCLUDED.target_fixture_id
                     AND target.kickoff_at > now()) THEN 'pending'
      ELSE data_backfill_requests.status
    END,
    next_attempt_at = CASE
      WHEN data_backfill_requests.status IN ('pending', 'in_progress', 'expired')
       AND data_backfill_requests.expires_at <= now()
       AND EXISTS (SELECT 1 FROM fixtures AS target
                   WHERE target.id = EXCLUDED.target_fixture_id
                     AND target.kickoff_at > now()) THEN now()
      ELSE data_backfill_requests.next_attempt_at
    END,
    expires_at = CASE
      WHEN data_backfill_requests.status IN ('pending', 'in_progress', 'expired')
       AND data_backfill_requests.expires_at <= now()
       AND EXISTS (SELECT 1 FROM fixtures AS target
                   WHERE target.id = EXCLUDED.target_fixture_id
                     AND target.kickoff_at > now()) THEN now() + interval '24 hours'
      ELSE data_backfill_requests.expires_at
    END,
    completed_at = CASE
      WHEN data_backfill_requests.status IN ('pending', 'in_progress', 'expired')
       AND data_backfill_requests.expires_at <= now()
       AND EXISTS (SELECT 1 FROM fixtures AS target
                   WHERE target.id = EXCLUDED.target_fixture_id
                     AND target.kickoff_at > now()) THEN NULL
      ELSE data_backfill_requests.completed_at
    END,
    attempts = CASE
      WHEN data_backfill_requests.status IN ('pending', 'in_progress', 'expired')
       AND data_backfill_requests.expires_at <= now()
       AND EXISTS (SELECT 1 FROM fixtures AS target
                   WHERE target.id = EXCLUDED.target_fixture_id
                     AND target.kickoff_at > now()) THEN 0
      ELSE data_backfill_requests.attempts
    END,
    updated_at = now()
  RETURNING id, target_fixture_id, team_id, competition_id, feature_group, matches_needed, status
), enqueued AS (
  INSERT INTO jobs (
    job_key, status, payload, queue, priority, run_after, idempotency_key, max_attempts
  )
  SELECT
    'backfill.team_history',
    'queued',
    jsonb_build_object(
      'request_id', id,
      'team_id', team_id,
      'competition_id', competition_id,
      'matches_needed', matches_needed,
      'feature_group', feature_group
    ),
    'data-backfill',
    CASE WHEN feature_group = 'results' THEN 5 ELSE 35 END,
    now(),
    'backfill.team_history:' || id::text,
    6
  FROM upserted
  WHERE status IN ('pending', 'in_progress')
  ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL AND status IN ('queued', 'running')
  DO UPDATE SET updated_at = jobs.updated_at
  RETURNING id
)
SELECT feature_group, status
FROM upserted
ORDER BY team_id
"""


_LATEST_ACCURACY_SNAPSHOTS_SQL = """
WITH latest AS (
  SELECT max(snapshot_date) AS snapshot_date
  FROM accuracy_snapshots
  WHERE snapshot_date <= %(as_of_date)s
    AND computed_at <= %(as_of_timestamp)s
)
SELECT scope, scope_id, accuracy
FROM accuracy_snapshots
WHERE snapshot_date = (SELECT snapshot_date FROM latest)
  AND computed_at <= %(as_of_timestamp)s
  AND (
    scope = 'market'
    OR (scope = 'league' AND scope_id = %(league_id)s)
  )
"""


_BTTS_CORRECTION_PREVIEW_SQL = """
SELECT ep.id, ep.fixture_id, ep.market, ep.prediction,
  f.status, f.home_goals_ft, f.away_goals_ft,
  CASE WHEN pr.prediction_id IS NOT NULL THEN pr.settlement_status ELSE newest.settlement_status END AS settlement_status,
  CASE WHEN pr.prediction_id IS NOT NULL THEN pr.actual ELSE newest.actual END AS previous_actual,
  CASE WHEN pr.prediction_id IS NOT NULL THEN pr.is_correct ELSE newest.is_correct END AS previous_is_correct,
  CASE WHEN pr.prediction_id IS NOT NULL THEN 'prediction_results' ELSE 'prediction_result_revisions' END AS source
FROM engine_predictions AS ep
JOIN fixtures AS f ON f.id = ep.fixture_id
LEFT JOIN prediction_results AS pr ON pr.prediction_id = ep.id
LEFT JOIN LATERAL (
  SELECT rev.settlement_status, rev.actual, rev.is_correct
  FROM prediction_result_revisions AS rev
  WHERE rev.prediction_id = ep.id
  ORDER BY rev.recorded_at DESC, rev.id DESC
  LIMIT 1
) AS newest ON TRUE
WHERE ep.fixture_id = %(fixture_id)s
  AND ep.market = 'btts_total_goals'
  AND lower(f.status) IN ('ft', 'aet', 'pen', 'finished')
  AND f.home_goals_ft >= 0 AND f.away_goals_ft >= 0
  AND CASE WHEN pr.prediction_id IS NOT NULL THEN pr.settlement_status ELSE newest.settlement_status END = 'settled'
ORDER BY ep.id
LIMIT %(limit)s
"""


_SETTLEMENT_FIXTURE_SQL = """
SELECT
  f.id,
  f.league_id,
  f.home_team_id,
  f.away_team_id,
  f.home_goals_ft,
  f.away_goals_ft,
  f.home_goals_ht,
  f.away_goals_ht
FROM fixtures AS f
WHERE f.id = %(fixture_id)s
  AND lower(f.status) IN ('ft', 'aet', 'pen', 'finished')
  AND f.home_goals_ft IS NOT NULL
  AND f.away_goals_ft IS NOT NULL
"""


_SETTLEMENT_FIXTURE_IDS_SQL = """
SELECT DISTINCT ep.fixture_id
FROM engine_predictions AS ep
JOIN fixtures AS f ON f.id = ep.fixture_id
LEFT JOIN prediction_results AS pr ON pr.prediction_id = ep.id
LEFT JOIN prediction_result_rollup_ledger AS rollup ON rollup.prediction_id = ep.id
WHERE ep.superseded_at IS NULL
  AND ep.model_registry_id IS NOT NULL
  AND pr.prediction_id IS NULL
  AND rollup.prediction_id IS NULL
  AND lower(f.status) IN ('ft', 'aet', 'pen', 'finished')
  AND f.home_goals_ft IS NOT NULL
  AND f.away_goals_ft IS NOT NULL
ORDER BY ep.fixture_id ASC
LIMIT %(limit)s
"""


_SETTLEMENT_STATISTICS_SQL = """
SELECT
  team_id,
  corners,""" + _CARD_COUNT_SQL + """,
  fouls,
  shots_total,
  shots_on_target,
  possession_pct
FROM fixture_statistics
WHERE fixture_id = %(fixture_id)s
  AND team_id IN (%(home_team_id)s, %(away_team_id)s)
"""


# Predictions that have run out of time.
#
# The settleable set is narrow on purpose: a full-time status *and* both
# full-time goals. Everything else produced no `prediction_results` row at
# all, and the API renders a missing row as `pending` -- which is why a match
# that finished yesterday can still show twenty pending cards.
#
# Two disjoint reasons, and they get different clocks:
#
#   fixture_not_played  The fixture reached a terminal state that is not a
#                       played-out result. Nothing is coming, ever, so the
#                       only wait is for a correction to a mis-reported
#                       postponement -- hours, not days.
#   result_never_arrived Past the hard age bound with no usable result:
#                       `unknown` where the reaper parked it, `upcoming` where
#                       the provider never spoke again, `finished` with no
#                       goals. The bound must sit well beyond the reach of
#                       hourly results reconciliation, so reconciliation
#                       always wins the race and a real result is preferred
#                       to a void.
#   settlement_unavailable Past the bound *with* a confirmed result. Not an
#                       absence -- a defect. A market missing from
#                       `_settlement_outcome` raises and rolls back the whole
#                       fixture, so one unmappable market strands every
#                       prediction on it. Voided anyway, because the bound is
#                       the point, but under a reason that says "fix the
#                       mapping" rather than blaming the feed.
#
# There is deliberately no "settleable fixtures are exempt" clause. An
# exemption is how a prediction stays pending forever, which is the one
# outcome this must not permit.
_EXPIRED_PREDICTIONS_SQL = """
SELECT
  ep.id,
  ep.fixture_id,
  ep.market,
  ep.prediction,
  ep.probability,
  ep.confidence,
  ep.confidence_tier,
  ep.lifecycle_stage,
  ep.model_version,
  ep.model_registry_id,
  ep.generated_at,
  ep.data_cutoff_at,
  ep.feature_version,
  ep.raw_probability,
  ep.calibrated_probability,
  ep.odds_snapshot_id,
  f.league_id,
  CASE
    WHEN lower(f.status) IN ('ft', 'aet', 'pen', 'finished')
      AND f.home_goals_ft IS NOT NULL
      AND f.away_goals_ft IS NOT NULL
      THEN 'settlement_unavailable'
    WHEN lower(f.status) IN (
      'postponed', 'cancelled', 'canceled', 'abandoned', 'awarded', 'walkover'
    ) THEN 'fixture_not_played'
    ELSE 'result_never_arrived'
  END AS void_reason
FROM engine_predictions AS ep
JOIN fixtures AS f ON f.id = ep.fixture_id
LEFT JOIN prediction_results AS pr ON pr.prediction_id = ep.id
LEFT JOIN prediction_result_rollup_ledger AS rollup ON rollup.prediction_id = ep.id
WHERE ep.superseded_at IS NULL
  AND pr.prediction_id IS NULL
  AND rollup.prediction_id IS NULL
  AND (
    (
      lower(f.status) IN (
        'postponed', 'cancelled', 'canceled', 'abandoned', 'awarded', 'walkover'
      )
      AND f.kickoff_at < %(not_played_cutoff)s
    )
    OR f.kickoff_at < %(stale_cutoff)s
  )
ORDER BY f.kickoff_at ASC, ep.id ASC
LIMIT %(limit)s
"""


# The confirmed goal timeline for one fixture, used to anchor in-play markets
# that are relative to when they were published (7.4).
#
# Ordered by observed_at, not by match minute, and that is deliberate: the
# question an anchor answers is "did this prediction know about this goal", and
# what the prediction knew is bounded by when we saw the goal, not by the clock
# the goal is stamped with. A 20th-minute goal first observed at 20:31 is a goal
# a prediction cut off at 20:30 did not have.
#
# is_retracted is excluded because the provider is the source of truth: a goal
# the provider has withdrawn never happened, and grading against it would score
# a prediction on an event that has been un-said.
# Lifecycle stage values that mark a row as in-play, as plain strings for
# membership tests against the database's TEXT column.
_IN_PLAY_STAGE_VALUES = frozenset(stage.value for stage in IN_PLAY_LIFECYCLE_STAGES)


_SETTLEMENT_GOAL_TIMELINE_SQL = """
SELECT observed_at, elapsed_minute, elapsed_extra, team_id, identity_key
FROM fixture_events
WHERE fixture_id = %(fixture_id)s
  AND lower(event_type) = 'goal'
  AND lower(event_detail) NOT LIKE '%%missed penalty%%'
  AND is_retracted = false
ORDER BY observed_at ASC, id ASC
"""

_CURRENT_UNSETTLED_PREDICTIONS_SQL = """
SELECT
  ep.id,
  ep.fixture_id,
  ep.market,
  ep.prediction,
  ep.probability,
  ep.confidence,
  ep.confidence_tier,
  ep.lifecycle_stage,
  ep.model_version,
  ep.model_registry_id,
  ep.generated_at,
  ep.data_cutoff_at,
  ep.feature_version,
  ep.raw_probability,
  ep.calibrated_probability,
  ep.odds_snapshot_id,
  f.league_id
FROM engine_predictions AS ep
JOIN fixtures AS f ON f.id = ep.fixture_id
LEFT JOIN prediction_results AS pr ON pr.prediction_id = ep.id
LEFT JOIN prediction_result_rollup_ledger AS rollup ON rollup.prediction_id = ep.id
WHERE ep.fixture_id = %(fixture_id)s
  AND ep.superseded_at IS NULL
  AND pr.prediction_id IS NULL
  AND rollup.prediction_id IS NULL
ORDER BY ep.generated_at DESC, ep.market ASC, ep.id ASC
"""


# The canonical set of statuses that mean "this match will not change again
# through normal play". It mirrors the `terminal` set in
# services/worker/internal/matchstate/state.go and the Go-side
# fixturepolicy.ResolvedStatusSQLList, which must stay in step with it.
#
# `awarded` and `walkover` are decided results, just not played ones. `unknown`
# is where the reaper parks a match that is past every plausible duration and
# that reconciliation could not resolve; matchstate declares it terminal on
# purpose, because nothing further is coming for that row. All three were
# missing here, which is why a single un-reconciled match suppressed both its
# teams' later fixtures indefinitely.
RESOLVED_FIXTURE_STATUSES = (
    "'ft', 'aet', 'pen', 'finished', 'awarded', 'walkover', 'unknown', "
    "'cancelled', 'canceled', 'postponed', 'abandoned'"
)

# Statuses that mean a result was actually played out, used to decide when a
# fixture's existing prediction has been overtaken by a newer outcome.
PLAYED_RESULT_STATUSES = "'ft', 'aet', 'pen', 'finished', 'awarded', 'walkover'"

# Statuses that disqualify a fixture from being predicted at all.
#
# This is deliberately RESOLVED_FIXTURE_STATUSES minus `unknown`. The two sets
# differ for one good reason: matchstate.Normalize falls back to `unknown` for
# any provider status string it does not recognise, so a perfectly ordinary
# fixture that has not kicked off yet can be sitting at `unknown` simply
# because its provider label was unfamiliar. Refusing to predict those would
# re-create the outage this change fixes -- Manchester City vs Sunderland was
# one of them. For an earlier match that has already kicked off, `unknown`
# does carry the reaper's terminal meaning, which is why the blocker subquery
# treats it as resolved and this filter does not.
NON_PREDICTABLE_FIXTURE_STATUSES = (
    "'ft', 'aet', 'pen', 'finished', 'awarded', 'walkover', "
    "'cancelled', 'canceled', 'postponed', 'abandoned'"
)

# How long an already-started match may suppress its teams' later fixtures
# while its result is awaited.
#
# matchstate.HardBound says 210 minutes is the longest a match can plausibly
# still be in play; six hours is that bound plus room for a results feed that
# reports late. Past it, a non-terminal status is a data defect in that row,
# not a result still on its way -- and a data defect in one row must not keep
# deleting predictions for every later fixture those two teams play.
UNRESOLVED_GRACE_HOURS = 6

# Suppress a fixture only while either team has an earlier match that has
# already kicked off, has no result recorded, and is still inside the window in
# which a result can plausibly arrive. Each condition is load-bearing:
#
# - Already kicked off (`< as_of_timestamp`). The previous version matched any
#   earlier fixture, future ones included. A fixture that has not been played
#   cannot be awaiting its result, so a team playing Wednesday and Sunday had
#   its Sunday fixture suppressed by its own Wednesday fixture -- for the whole
#   horizon, because Wednesday stays `upcoming` until Wednesday.
# - No result recorded. A row carrying a full-time score is resolved whatever
#   its status text says.
# - Inside the grace window. The previous version looked back 14 days with no
#   upper bound on the wait, so any row whose status never advanced past
#   `upcoming` or `unknown` suppressed both its teams for a fortnight. That was
#   the dominant cause of the missing-predictions reports.
_EARLIER_UNRESOLVED_CLAUSE = f"""
  AND NOT EXISTS (
    SELECT 1
    FROM fixtures AS earlier_unresolved
    WHERE earlier_unresolved.id <> f.id
      AND earlier_unresolved.kickoff_at < f.kickoff_at
      AND earlier_unresolved.kickoff_at < %(as_of_timestamp)s
      AND earlier_unresolved.kickoff_at
        >= %(as_of_timestamp)s - interval '{UNRESOLVED_GRACE_HOURS} hours'
      AND (
        earlier_unresolved.home_team_id IN (f.home_team_id, f.away_team_id)
        OR earlier_unresolved.away_team_id IN (f.home_team_id, f.away_team_id)
      )
      AND earlier_unresolved.home_goals_ft IS NULL
      AND earlier_unresolved.away_goals_ft IS NULL
      AND lower(COALESCE(earlier_unresolved.status, 'scheduled')) NOT IN
        ({RESOLVED_FIXTURE_STATUSES})
  )"""


_UPCOMING_FIXTURE_IDS_SQL = f"""
SELECT f.id
FROM fixtures AS f
WHERE f.kickoff_at >= %(as_of_timestamp)s
  AND f.kickoff_at < (%(as_of_timestamp)s + interval '7 days')
  AND f.updated_at <= %(as_of_timestamp)s
  AND lower(f.status) NOT IN ({NON_PREDICTABLE_FIXTURE_STATUSES})
{_EARLIER_UNRESOLVED_CLAUSE}
  AND NOT EXISTS (
    SELECT 1
    FROM engine_predictions AS predicted
    WHERE predicted.fixture_id = f.id
      AND predicted.superseded_at IS NULL
      AND NOT EXISTS (
        SELECT 1
        FROM fixtures AS newly_resolved
        WHERE newly_resolved.id <> f.id
          AND newly_resolved.kickoff_at < f.kickoff_at
          AND newly_resolved.kickoff_at >= f.kickoff_at - interval '14 days'
          AND (
            newly_resolved.home_team_id IN (f.home_team_id, f.away_team_id)
            OR newly_resolved.away_team_id IN (f.home_team_id, f.away_team_id)
          )
          AND lower(COALESCE(newly_resolved.status, 'scheduled')) IN ({PLAYED_RESULT_STATUSES})
          AND newly_resolved.updated_at > predicted.generated_at
      )
      -- A Model B or C forecast made after this row was last evaluated. Forecasts
      -- target fixtures that already have a prediction, so without this a fixture
      -- would keep the champion's numbers until kick-off. Unchanged rows still move
      -- last_evaluated_at, so each forecast causes one re-run.
      AND NOT EXISTS (
        SELECT 1
        FROM challenger_market_predictions AS forecast
        JOIN model_registry AS registry ON registry.id = forecast.model_registry_id
        WHERE forecast.fixture_id = f.id
          AND forecast.origin = 'live'
          AND registry.model_key IN ('elo-markets', 'model-c-markets')
          AND forecast.as_of_timestamp > predicted.last_evaluated_at
          AND forecast.as_of_timestamp <= %(as_of_timestamp)s
      )
  )
ORDER BY f.kickoff_at ASC, f.id ASC
LIMIT %(limit)s
"""


# --- In-play candidate selection (architecture v2 §7.5) ---------------------
#
# "The two guards -- extend, don't delete." The pre-match guard above
# (`f.kickoff_at >= %(as_of_timestamp)s`) is untouched and stays untouched:
# deleting it would let post-kickoff predictions leak into pre-match surfaces,
# and both rows would look valid, so no test would catch it. What follows is
# the *parallel* in-play branch, reached only through an explicit scope, with
# the mirrored condition `f.kickoff_at < %(as_of_timestamp)s`.
#
# The Go half of the same pair -- `ep.generated_at <= f.kickoff_at` in
# services/api/internal/{matches,predictions}/source.go -- is likewise left
# alone here; its in-play read path belongs to the API surface, not to this
# repository.
IN_PLAY_SCOPES = frozenset({"in_play", "inplay"})

# The lifecycle stages an in-play run writes, as a SQL literal list. Kept
# beside the scope so the two definitions of "in-play" cannot drift.
IN_PLAY_LIFECYCLE_STAGES_SQL = ", ".join(
    f"'{stage.value}'" for stage in sorted(IN_PLAY_LIFECYCLE_STAGES, key=lambda item: item.value)
)

# A fixture whose newest snapshot is older than this is not something to
# generate a prediction from (§6.4 first tier, §14). Matches
# `prediction_engine.inplay_features.STALE_AFTER_SECONDS`; the candidate query
# declines to hand the engine work it would only suppress.
IN_PLAY_SNAPSHOT_FRESHNESS_SECONDS = 90

# matchstate.HardBound: past this a "live" fixture is a data defect the reaper
# owns, not a match still being played.
IN_PLAY_HARD_BOUND_MINUTES = 210


_IN_PLAY_FIXTURE_IDS_SQL = f"""
SELECT f.id
FROM fixtures AS f
WHERE f.kickoff_at < %(as_of_timestamp)s
  AND f.kickoff_at >= %(as_of_timestamp)s - interval '{IN_PLAY_HARD_BOUND_MINUTES} minutes'
  AND lower(COALESCE(f.status, 'scheduled')) NOT IN ({NON_PREDICTABLE_FIXTURE_STATUSES})
  AND EXISTS (
    SELECT 1
    FROM fixture_live_snapshots AS snapshot
    WHERE snapshot.fixture_id = f.id
      AND snapshot.captured_at
        >= %(as_of_timestamp)s - interval '{IN_PLAY_SNAPSHOT_FRESHNESS_SECONDS} seconds'
      AND snapshot.captured_at <= %(as_of_timestamp)s
  )
ORDER BY f.kickoff_at ASC, f.id ASC
LIMIT %(limit)s
"""


# The newest snapshot for a fixture, with the canonical team ids beside it so
# the caller does not need a second round trip to count cards by side.
#
# `captured_at <= as_of_timestamp` matters as much as the lower bound: a replay
# at a past instant must see what was known then, not what is known now, and
# without it the engine would quietly read the future when re-run.
_IN_PLAY_LATEST_SNAPSHOT_SQL = """
SELECT
  snapshot.id AS snapshot_id,
  snapshot.captured_at,
  snapshot.provider,
  snapshot.provider_timestamp,
  snapshot.status,
  snapshot.period,
  snapshot.elapsed_minute,
  snapshot.elapsed_extra,
  snapshot.home_goals,
  snapshot.away_goals,
  snapshot.home_red_cards,
  snapshot.away_red_cards,
  f.home_team_id,
  f.away_team_id,
  f.kickoff_at
FROM fixtures AS f
JOIN fixture_live_snapshots AS snapshot
  ON snapshot.fixture_id = f.id
WHERE f.id = %(fixture_id)s
  AND snapshot.captured_at <= %(as_of_timestamp)s
ORDER BY snapshot.captured_at DESC, snapshot.id DESC
LIMIT 1
"""


# The card timeline, deduplicated to the newest observation of each event and
# with the provider's team id resolved to ours.
#
# DISTINCT ON (identity_key) with the newest observation winning is what makes
# a retraction stick: `Var` withdraws an earlier event by re-filing it with
# is_retracted set, and taking the first observation would keep counting a
# cancelled dismissal for the rest of the match.
_IN_PLAY_EVENTS_SQL = """
SELECT DISTINCT ON (event.identity_key)
  event.identity_key,
  event.observed_at,
  event.elapsed_minute,
  event.elapsed_extra,
  event.event_type,
  event.event_detail,
  event.player_id,
  event.is_retracted,
  CASE
    WHEN pem.canonical_entity_id IS NULL THEN NULL
    ELSE pem.canonical_entity_id::BIGINT
  END AS canonical_team_id
FROM fixture_events AS event
LEFT JOIN provider_entity_maps AS pem
  ON pem.entity_type = 'team'
  AND pem.provider = event.provider
  AND pem.retired_at IS NULL
  AND pem.canonical_entity_id IS NOT NULL
  AND pem.canonical_entity_id ~ '^[0-9]+$'
  AND pem.provider_entity_id = event.team_id::TEXT
WHERE event.fixture_id = %(fixture_id)s
  AND event.observed_at <= %(as_of_timestamp)s
ORDER BY event.identity_key, event.observed_at DESC
"""


# The current in-play prediction for a fixture, whichever market it is for.
# Every market in one run shares a generated_at and a match state, so any one
# of them answers "when did we last publish, and what did we see?".
_IN_PLAY_LAST_PREDICTION_SQL = f"""
SELECT generated_at, features_used
FROM engine_predictions
WHERE fixture_id = %(fixture_id)s
  AND lifecycle_stage IN ({IN_PLAY_LIFECYCLE_STAGES_SQL})
ORDER BY generated_at DESC, public_id DESC
LIMIT 1
"""


_HISTORICAL_FIXTURE_IDS_SQL = """
SELECT f.id
FROM fixtures AS f
WHERE f.kickoff_at < %(as_of_timestamp)s
  AND f.updated_at <= %(as_of_timestamp)s
  AND lower(f.status) IN ('ft', 'aet', 'pen', 'finished')
  AND f.home_goals_ft IS NOT NULL
  AND f.away_goals_ft IS NOT NULL
  AND (
    SELECT COUNT(*)
    FROM fixtures AS prior_home
    WHERE prior_home.league_id = f.league_id
      AND prior_home.season_id = f.season_id
      AND prior_home.kickoff_at < f.kickoff_at
      AND prior_home.updated_at <= f.kickoff_at
      AND prior_home.updated_at <= %(as_of_timestamp)s
      AND prior_home.home_goals_ft IS NOT NULL
      AND prior_home.away_goals_ft IS NOT NULL
      AND (prior_home.home_team_id = f.home_team_id OR prior_home.away_team_id = f.home_team_id)
  ) >= 10
  AND (
    SELECT COUNT(*)
    FROM fixtures AS prior_away
    WHERE prior_away.league_id = f.league_id
      AND prior_away.season_id = f.season_id
      AND prior_away.kickoff_at < f.kickoff_at
      AND prior_away.updated_at <= f.kickoff_at
      AND prior_away.updated_at <= %(as_of_timestamp)s
      AND prior_away.home_goals_ft IS NOT NULL
      AND prior_away.away_goals_ft IS NOT NULL
      AND (prior_away.home_team_id = f.away_team_id OR prior_away.away_team_id = f.away_team_id)
  ) >= 10
ORDER BY f.kickoff_at DESC, f.id DESC
LIMIT %(limit)s
"""


_PREDICTION_WRITE_LOCK_SQL = """
SELECT pg_advisory_xact_lock(hashtextextended(%(lock_key)s, 0))
"""


_CURRENT_PREDICTION_SQL = """
SELECT
  id,
  generated_at,
  prediction,
  probability,
  calibrated_probability,
  max_lifecycle_probability_shift,
  engine_prediction_content_hash(
    fixture_id,
    market,
    prediction,
    probability,
    confidence,
    confidence_tier,
    is_best_bet,
    lifecycle_stage,
    data_completeness,
    features_used - 'as_of_timestamp',
    explanation,
    gate_tier,
    horizon_eligible,
    embargo_active,
    model_version
  ) = engine_prediction_content_hash(
    %(fixture_id)s::BIGINT,
    %(market)s::TEXT,
    %(prediction)s::TEXT,
    %(probability)s::NUMERIC(8,6),
    %(confidence)s::NUMERIC(8,6),
    %(confidence_tier)s::TEXT,
    %(is_best_bet)s::BOOLEAN,
    %(lifecycle_stage)s::TEXT,
    %(data_completeness)s::NUMERIC(8,6),
    %(features_used)s::JSONB - 'as_of_timestamp',
    %(explanation)s::TEXT,
    %(gate_tier)s::TEXT,
    %(horizon_eligible)s::BOOLEAN,
    %(embargo_active)s::BOOLEAN,
    %(model_version)s::TEXT
  ) AS content_matches
FROM engine_predictions
WHERE fixture_id = %(fixture_id)s
  AND market = %(market)s
  AND superseded_at IS NULL
FOR UPDATE
"""


_MODEL_FORECASTS_SQL = """
SELECT DISTINCT ON (registry.model_key)
  registry.model_key,
  registry.model_version,
  forecast.markets,
  forecast.as_of_timestamp
FROM challenger_market_predictions AS forecast
JOIN model_registry AS registry ON registry.id = forecast.model_registry_id
WHERE forecast.fixture_id = %(fixture_id)s
  AND forecast.origin = 'live'
  AND forecast.as_of_timestamp <= %(as_of_timestamp)s
  AND registry.model_key IN ('elo-markets', 'model-c-markets')
ORDER BY registry.model_key, forecast.as_of_timestamp DESC, forecast.created_at DESC
"""


_TOUCH_LAST_EVALUATED_SQL = """
UPDATE engine_predictions
SET
  last_evaluated_at = GREATEST(last_evaluated_at, %(last_evaluated_at)s),
  content_hash = engine_prediction_content_hash(
    fixture_id,
    market,
    prediction,
    probability,
    confidence,
    confidence_tier,
    is_best_bet,
    lifecycle_stage,
    data_completeness,
    features_used - 'as_of_timestamp',
    explanation,
    gate_tier,
    horizon_eligible,
    embargo_active,
    model_version
  )
WHERE id = %(id)s
"""


_SUPERSEDE_SQL = """
UPDATE engine_predictions
SET
  superseded_at = %(superseded_at)s,
  updated_at = %(superseded_at)s
WHERE id = %(id)s
  AND superseded_at IS NULL
"""


_INSERT_SQL = """
INSERT INTO engine_predictions (
  id,
  fixture_id,
  market,
  prediction,
  probability,
  confidence,
  confidence_tier,
  is_best_bet,
  lifecycle_stage,
  data_completeness,
  features_used,
  explanation,
  generated_at,
  superseded_at,
  gate_tier,
  horizon_eligible,
  embargo_active,
  model_version,
  model_registry_id,
  as_of_timestamp,
  feature_version,
  raw_probability,
  previous_probability,
  probability_delta,
  max_lifecycle_probability_shift,
  prediction_direction_changed,
  lifecycle_stability_score,
  fixture_live_snapshot_id,
  data_cutoff_at,
  match_minute,
  period,
  content_hash,
  last_evaluated_at,
  updated_at
) VALUES (
  %(id)s,
  %(fixture_id)s,
  %(market)s,
  %(prediction)s,
  %(probability)s,
  %(confidence)s,
  %(confidence_tier)s,
  %(is_best_bet)s,
  %(lifecycle_stage)s,
  %(data_completeness)s,
  %(features_used)s,
  %(explanation)s,
  %(generated_at)s,
  %(superseded_at)s,
  %(gate_tier)s,
  %(horizon_eligible)s,
  %(embargo_active)s,
  %(model_version)s,
  (
    SELECT registry.id
    FROM model_registry AS registry
    WHERE registry.model_version = %(model_version)s
      AND registry.market_family = engine_prediction_market_family(%(market)s)
      AND registry.status = 'production'
  ),
  %(generated_at)s,
  %(feature_version)s,
  %(probability)s,
  %(previous_probability)s,
  %(probability_delta)s,
  %(max_lifecycle_probability_shift)s,
  %(prediction_direction_changed)s,
  %(lifecycle_stability_score)s,
  %(fixture_live_snapshot_id)s,
  %(data_cutoff_at)s,
  %(match_minute)s,
  %(period)s,
  engine_prediction_content_hash(
    %(fixture_id)s::BIGINT,
    %(market)s::TEXT,
    %(prediction)s::TEXT,
    %(probability)s::NUMERIC(8,6),
    %(confidence)s::NUMERIC(8,6),
    %(confidence_tier)s::TEXT,
    %(is_best_bet)s::BOOLEAN,
    %(lifecycle_stage)s::TEXT,
    %(data_completeness)s::NUMERIC(8,6),
    %(features_used)s::JSONB - 'as_of_timestamp',
    %(explanation)s::TEXT,
    %(gate_tier)s::TEXT,
    %(horizon_eligible)s::BOOLEAN,
    %(embargo_active)s::BOOLEAN,
    %(model_version)s::TEXT
  ),
  %(generated_at)s,
  %(generated_at)s
)
"""


_UPSERT_PREDICTION_RESULT_SQL = """
INSERT INTO prediction_results (
  prediction_id,
  fixture_id,
  market,
  predicted,
  actual,
  is_correct,
  settlement_status,
  void_reason,
  probability,
  confidence,
  confidence_tier,
  lifecycle_stage_at_settlement,
  league_id,
  settled_at,
  settled_by_event_keys
) VALUES (
  %(prediction_id)s,
  %(fixture_id)s,
  %(market)s,
  %(predicted)s,
  %(actual)s,
  %(is_correct)s,
  %(settlement_status)s,
  %(void_reason)s,
  %(probability)s,
  %(confidence)s,
  %(confidence_tier)s,
  %(lifecycle_stage_at_settlement)s,
  %(league_id)s,
  %(settled_at)s,
  %(settled_by_event_keys)s
)
ON CONFLICT (prediction_id) DO UPDATE SET
  actual = EXCLUDED.actual,
  is_correct = EXCLUDED.is_correct,
  settlement_status = EXCLUDED.settlement_status,
  void_reason = EXCLUDED.void_reason,
  settled_at = EXCLUDED.settled_at,
  settled_by_event_keys = EXCLUDED.settled_by_event_keys
"""


# Append-only settlement history. Every state a prediction's settlement has held,
# including the one a reversal replaced -- `prediction_results` keeps only the
# current state, so without this a reversal would erase what it reversed.
# The in-play model time series (000079, client data contract).
#
# `sequence` is deliberately not supplied: the column defaults to
# nextval('inplay_stream_sequence'), so no writer can forget it and two
# concurrent writers cannot collide on a value. It is returned because the caller
# needs it -- the stream cursor a client resumes from is this number.
# The last graph point written for a fixture, for the snapshot cadence decision.
#
# Reads the state the snapshot recorded rather than recomputing it from the live
# feed: the question is "has anything changed since we last drew a point", and the
# only authority on what that point said is the point itself.
# Freeze the full-time snapshot for fixtures that have finished.
#
# WHY THIS NEEDS ITS OWN SWEEP. The in-play candidate scope only contains live
# fixtures, and `fixtures?live=all` drops a match the moment it ends. So nothing
# on the live path is ever looking at a fixture at the instant it finishes, and a
# `final` snapshot would never be written by the runner -- the permanent record
# the clients need would simply never exist.
#
# Copies the newest series row rather than recomputing: the model cannot be re-run
# for a finished match, because the live state it conditioned on is gone. What the
# model last said IS the full-time record.
#
# Idempotent by ON CONFLICT DO NOTHING, so the sweep can run on any cadence and a
# repeat is free.
_FINALISE_INPLAY_SNAPSHOTS_SQL = """
WITH finished AS (
  SELECT f.id AS fixture_id
  FROM fixtures AS f
  WHERE lower(f.status) IN ('ft', 'aet', 'pen', 'finished')
    AND EXISTS (SELECT 1 FROM inplay_snapshots AS s WHERE s.fixture_id = f.id)
    AND NOT EXISTS (SELECT 1 FROM inplay_final_snapshots AS fs WHERE fs.fixture_id = f.id)
  ORDER BY f.kickoff_at DESC
  LIMIT %(limit)s
), newest AS (
  SELECT DISTINCT ON (s.fixture_id)
    s.fixture_id, s.sequence, s.captured_at, s.data_cutoff_at, s.status, s.period,
    s.elapsed_minute, s.elapsed_extra, s.home_goals, s.away_goals,
    s.home_red_cards, s.away_red_cards, s.model_health, s.suppression_reason,
    s.markets, s.model_version, s.feature_version, s.fixture_live_snapshot_id
  FROM inplay_snapshots AS s
  JOIN finished ON finished.fixture_id = s.fixture_id
  ORDER BY s.fixture_id, s.captured_at DESC, s.id DESC
), frozen AS (
  INSERT INTO inplay_final_snapshots (
    fixture_id, sequence, captured_at, data_cutoff_at, status, period,
    elapsed_minute, elapsed_extra, home_goals, away_goals,
    home_red_cards, away_red_cards, model_health, suppression_reason,
    markets, model_version, feature_version, fixture_live_snapshot_id
  )
  SELECT
    fixture_id, sequence, captured_at, data_cutoff_at, status, period,
    elapsed_minute, elapsed_extra, home_goals, away_goals,
    home_red_cards, away_red_cards, model_health, suppression_reason,
    markets, model_version, feature_version, fixture_live_snapshot_id
  FROM newest
  ON CONFLICT (fixture_id) DO NOTHING
  RETURNING fixture_id
)
SELECT
  (SELECT count(*) FROM finished)::BIGINT AS fixtures_examined,
  (SELECT count(*) FROM frozen)::BIGINT AS snapshots_frozen
"""


# One publication is the complete current set for a fixture. Keep this lock
# distinct from the per-market locks used by save_predictions: a valid empty
# set still needs a lock and a durable generation timestamp.
_IN_PLAY_FIXTURE_WRITE_LOCK_SQL = """
SELECT pg_advisory_xact_lock(hashtextextended('inplay-fixture:' || %(fixture_id)s::text, 0))
"""

_IN_PLAY_PUBLICATION_WATERMARK_SQL = """
SELECT generated_at
FROM inplay_publication_state
WHERE fixture_id = %(fixture_id)s
"""

_UPSERT_IN_PLAY_PUBLICATION_WATERMARK_SQL = """
INSERT INTO inplay_publication_state (fixture_id, generated_at, current_markets)
VALUES (%(fixture_id)s, %(generated_at)s, %(current_markets)s::text[])
ON CONFLICT (fixture_id) DO UPDATE SET
  generated_at = EXCLUDED.generated_at,
  current_markets = EXCLUDED.current_markets
"""


# Pre-match picks whose tracker state is worth computing for a live fixture.
#
# LOCKED AT KICKOFF, and that guard is not cosmetic. `generated_at <= kickoff_at`
# is the same predicate that closed the leak through guest-readable
# `/v1/analysis/{id}`: without it the newest current prediction for a fixture is
# an IN-PLAY row, and serving that as a pre-match pick hands the paid asset to
# anonymous readers. A tracker is a public surface, so it must only ever read
# what was published before the whistle.
#
# Restricted to pre-match lifecycle stages for the same reason, belt and braces:
# the stage filter and the timestamp guard would each have been enough, and the
# cost of both is nothing.
_TRACKABLE_PREMATCH_PICKS_SQL = """
SELECT
  ep.id,
  ep.fixture_id,
  ep.market,
  ep.prediction,
  pr.settlement_status,
  pr.is_correct
FROM engine_predictions AS ep
JOIN fixtures AS f ON f.id = ep.fixture_id
LEFT JOIN prediction_results AS pr ON pr.prediction_id = ep.id
WHERE ep.fixture_id = %(fixture_id)s
  AND ep.superseded_at IS NULL
  AND ep.generated_at <= f.kickoff_at
  AND ep.lifecycle_stage NOT IN ({stages})
ORDER BY ep.market, ep.id
""".format(stages=IN_PLAY_LIFECYCLE_STAGES_SQL)


# Replace a pick's tracker state in place. A projection, not a history: the
# minute-by-minute record is inplay_snapshots, and a second copy here would only
# be a worse one.
_UPSERT_TRACKER_STATE_SQL = """
INSERT INTO prediction_tracker_state (
  prediction_id, fixture_id, state, home_goals, away_goals, match_minute, computed_at
) VALUES (
  %(prediction_id)s, %(fixture_id)s, %(state)s, %(home_goals)s, %(away_goals)s,
  %(match_minute)s, %(computed_at)s
)
ON CONFLICT (prediction_id) DO UPDATE SET
  state = EXCLUDED.state,
  home_goals = EXCLUDED.home_goals,
  away_goals = EXCLUDED.away_goals,
  match_minute = EXCLUDED.match_minute,
  computed_at = EXCLUDED.computed_at
"""


# The newest statistics capture for a fixture, both teams summed.
#
# ONE CAPTURE, NOT THE LATEST VALUE PER TEAM. The writer stamps both teams of a
# batch with the same captured_at precisely so they can be read as one
# observation; taking each team's newest row independently would combine home
# corners at minute 63 with away corners at minute 61 and present the total as
# a single moment.
#
# A statistic is NULL unless both teams reported it. Half a total is worse than
# none: it reads as a quiet match rather than an unreported one, and the ladder
# would publish lines around a count that is systematically low.
_LATEST_INPLAY_STATISTICS_SQL = """
WITH newest AS (
    SELECT max(captured_at) AS captured_at
    FROM fixture_statistics_snapshots
    WHERE fixture_id = %(fixture_id)s
      AND captured_at <= %(as_of_timestamp)s
)
SELECT
    s.captured_at,
    count(*) AS team_rows,
    count(s.yellow_cards) + count(s.red_cards) AS card_rows,
    sum(s.card_points)      AS card_points,
    count(s.card_points)    AS card_points_rows,
    sum(s.corners)          AS corners,
    count(s.corners)        AS corners_rows,
    sum(s.shots_total)      AS shots_total,
    count(s.shots_total)    AS shots_total_rows,
    sum(s.shots_on_target)  AS shots_on_target,
    count(s.shots_on_target) AS shots_on_target_rows,
    sum(s.fouls)            AS fouls,
    count(s.fouls)          AS fouls_rows
FROM fixture_statistics_snapshots s
JOIN newest ON newest.captured_at = s.captured_at
WHERE s.fixture_id = %(fixture_id)s
GROUP BY s.captured_at
"""


_LATEST_INPLAY_SNAPSHOT_STATE_SQL = """
SELECT captured_at, period, home_goals, away_goals, home_red_cards, away_red_cards
FROM inplay_snapshots
WHERE fixture_id = %(fixture_id)s
ORDER BY captured_at DESC, id DESC
LIMIT 1
"""


_INSERT_INPLAY_SNAPSHOT_SQL = """
INSERT INTO inplay_snapshots (
  fixture_id,
  captured_at,
  data_cutoff_at,
  trigger,
  triggered_by_event_key,
  status,
  period,
  elapsed_minute,
  elapsed_extra,
  home_goals,
  away_goals,
  home_red_cards,
  away_red_cards,
  model_health,
  suppression_reason,
  markets,
  model_version,
  feature_version,
  fixture_live_snapshot_id
) VALUES (
  %(fixture_id)s,
  %(captured_at)s,
  %(data_cutoff_at)s,
  %(trigger)s,
  %(triggered_by_event_key)s,
  %(status)s,
  %(period)s,
  %(elapsed_minute)s,
  %(elapsed_extra)s,
  %(home_goals)s,
  %(away_goals)s,
  %(home_red_cards)s,
  %(away_red_cards)s,
  %(model_health)s,
  %(suppression_reason)s,
  %(markets)s,
  %(model_version)s,
  %(feature_version)s,
  %(fixture_live_snapshot_id)s
)
RETURNING id, sequence
"""


# The frozen full-time snapshot, retained after the monthly partitions are
# dropped.
#
# ON CONFLICT DO NOTHING rather than DO UPDATE: the final whistle happens once,
# and a second write for the same fixture is a repeated job, not new information.
# Overwriting would let a later, worse observation replace the permanent record.
_INSERT_INPLAY_FINAL_SNAPSHOT_SQL = """
INSERT INTO inplay_final_snapshots (
  fixture_id, sequence, captured_at, data_cutoff_at, status, period,
  elapsed_minute, elapsed_extra, home_goals, away_goals,
  home_red_cards, away_red_cards, model_health, suppression_reason,
  markets, model_version, feature_version, fixture_live_snapshot_id
) VALUES (
  %(fixture_id)s, %(sequence)s, %(captured_at)s, %(data_cutoff_at)s, %(status)s, %(period)s,
  %(elapsed_minute)s, %(elapsed_extra)s, %(home_goals)s, %(away_goals)s,
  %(home_red_cards)s, %(away_red_cards)s, %(model_health)s, %(suppression_reason)s,
  %(markets)s, %(model_version)s, %(feature_version)s, %(fixture_live_snapshot_id)s
)
ON CONFLICT (fixture_id) DO NOTHING
"""


# The snapshot a "before" figure reads: the newest one strictly older than the
# event, and only if it is recent enough to describe the same passage of play.
#
# A LINK, NOT A SYNTHESIS. With a 60-second timer the newest prior snapshot is at
# most a minute stale, so it is the before-state. Writing a fresh pre_* row
# unconditionally would duplicate the timer row sitting beside it. The staleness
# bound is what makes the link honest: past it there is no snapshot that
# describes the state before this event, and the caller must write one.
_INPLAY_SNAPSHOT_BEFORE_EVENT_SQL = """
SELECT id, sequence, captured_at
FROM inplay_snapshots
WHERE fixture_id = %(fixture_id)s
  AND captured_at < %(event_at)s
  AND captured_at >= %(event_at)s::timestamptz - make_interval(secs => %(max_age_seconds)s)
ORDER BY captured_at DESC, id DESC
LIMIT 1
"""


_INSERT_PREDICTION_RESULT_REVISION_SQL = """
INSERT INTO prediction_result_revisions (
  prediction_id,
  fixture_id,
  settlement_status,
  actual,
  is_correct,
  void_reason,
  settled_by_event_keys,
  revision_reason,
  triggered_by_event_key,
  recorded_at
) VALUES (
  %(prediction_id)s,
  %(fixture_id)s,
  %(settlement_status)s,
  %(actual)s,
  %(is_correct)s,
  %(void_reason)s,
  %(settled_by_event_keys)s,
  %(revision_reason)s,
  %(triggered_by_event_key)s,
  %(recorded_at)s
)
"""


# A settlement already folded into published accuracy must be recounted when it
# is reversed or rescored, or the withdrawn result stays in the figure for ever.
# No-op when the prediction was never rolled up.
_MARK_ROLLUP_NEEDS_RECOUNT_SQL = """
UPDATE prediction_result_rollup_ledger
SET needs_recount = true
WHERE prediction_id = %(prediction_id)s
"""


# Fixtures whose settled predictions rest on evidence the provider has since
# withdrawn.
#
# `retracted_at > pr.settled_at` is the whole trigger: a retraction recorded
# before the settlement was already accounted for when the grade was computed,
# so re-grading on it would churn every fixture with any historical VAR decision
# on every sweep. No time bound in the other direction -- a reversal may arrive
# days after full time, which is the case that actually occurred (2026-09-23).
_NEWEST_RETRACTED_GOAL_SQL = """
SELECT identity_key
FROM fixture_events
WHERE fixture_id = %(fixture_id)s
  AND is_retracted = true
  AND lower(event_type) = 'goal'
ORDER BY retracted_at DESC NULLS LAST, id DESC
LIMIT 1
"""


_RETRACTED_SETTLEMENT_FIXTURES_SQL = """
WITH retractions AS (
  SELECT fixture_id, max(retracted_at) AS retracted_at
  FROM fixture_events
  WHERE is_retracted = true
    AND lower(event_type) = 'goal'
    AND retracted_at IS NOT NULL
  GROUP BY fixture_id
), from_results AS (
  -- Inside the raw retention window: the settlement row is still present.
  SELECT DISTINCT pr.fixture_id
  FROM prediction_results AS pr
  JOIN retractions AS r ON r.fixture_id = pr.fixture_id
  WHERE r.retracted_at > pr.settled_at
), from_revisions AS (
  -- Past the retention window: `aggregate_and_prune` has deleted the settlement
  -- row, so the only surviving record that this prediction was ever graded is
  -- its revision history. Without this arm a reversal arriving more than
  -- `--raw-retention-days` (default 2) after settlement is never noticed, which
  -- fails the rule outright: a withdrawn goal must be shown however long it
  -- takes, and the 2026-09-23 case was a retraction on an already-finished
  -- fixture.
  SELECT DISTINCT rev.fixture_id
  FROM prediction_result_revisions AS rev
  JOIN retractions AS r ON r.fixture_id = rev.fixture_id
  WHERE r.retracted_at > rev.recorded_at
)
SELECT fixture_id FROM (
  SELECT fixture_id FROM from_results
  UNION
  SELECT fixture_id FROM from_revisions
) AS candidates
ORDER BY fixture_id
LIMIT %(limit)s
"""


# The current settlement of every prediction on one fixture, for re-grading.
_SETTLED_PREDICTIONS_FOR_FIXTURE_SQL = """
SELECT
  ep.id,
  ep.fixture_id,
  ep.market,
  ep.prediction,
  ep.probability,
  ep.confidence,
  ep.confidence_tier,
  ep.lifecycle_stage,
  ep.model_version,
  ep.model_registry_id,
  ep.generated_at,
  ep.data_cutoff_at,
  ep.feature_version,
  ep.raw_probability,
  ep.calibrated_probability,
  ep.odds_snapshot_id,
  f.league_id,
  -- Previous state, preferring the live settlement row and falling back to the
  -- newest revision once that row has been pruned. The fallback is what makes a
  -- post-retention reversal possible at all: `engine_predictions` is never
  -- deleted, and the revision history outlives `prediction_results`.
  COALESCE(pr.settlement_status, newest.settlement_status) AS settlement_status,
  COALESCE(pr.actual, newest.actual) AS previous_actual,
  COALESCE(pr.is_correct, newest.is_correct) AS previous_is_correct
FROM engine_predictions AS ep
JOIN fixtures AS f ON f.id = ep.fixture_id
LEFT JOIN prediction_results AS pr ON pr.prediction_id = ep.id
LEFT JOIN LATERAL (
  SELECT rev.settlement_status, rev.actual, rev.is_correct
  FROM prediction_result_revisions AS rev
  WHERE rev.prediction_id = ep.id
  ORDER BY rev.recorded_at DESC, rev.id DESC
  LIMIT 1
) AS newest ON TRUE
WHERE ep.fixture_id = %(fixture_id)s
  -- Only predictions that were actually graded at some point. Without this the
  -- sweep would pull in never-settled rows and "re-settle" them, which is
  -- settling, not re-grading, and would bypass the ordinary settlement path.
  AND (pr.prediction_id IS NOT NULL OR newest.settlement_status IS NOT NULL)
ORDER BY ep.id
"""


_INSERT_MODEL_EVALUATION_FACT_SQL = """
INSERT INTO model_evaluation_facts (
  prediction_id,
  model_registry_id,
  fixture_id,
  market,
  selection,
  lifecycle_stage,
  prediction_timestamp,
  data_cutoff_at,
  feature_version,
  raw_probability,
  calibrated_probability,
  confidence,
  final_outcome,
  settlement_classification,
  settled_at,
  odds_snapshot_id,
  evaluation_eligible,
  exclusion_reason,
  evaluation_contract_version
) VALUES (
  %(prediction_id)s,
  %(model_registry_id)s,
  %(fixture_id)s,
  %(market)s,
  %(selection)s,
  %(lifecycle_stage)s,
  %(prediction_timestamp)s,
  %(data_cutoff_at)s,
  %(feature_version)s,
  %(raw_probability)s,
  %(calibrated_probability)s,
  %(confidence)s,
  %(final_outcome)s,
  %(settlement_classification)s,
  %(settled_at)s,
  %(odds_snapshot_id)s,
  %(evaluation_eligible)s,
  %(exclusion_reason)s,
  %(evaluation_contract_version)s
)
ON CONFLICT (prediction_id) DO NOTHING
"""


_SETTLED_ACCURACY_ROWS_SQL = """
WITH eligible_predictions AS MATERIALIZED (
  SELECT
    predictions.id,
    row_number() OVER (
      PARTITION BY predictions.fixture_id, predictions.market
      ORDER BY predictions.generated_at DESC, predictions.public_id DESC
    ) AS accuracy_rank
  FROM engine_predictions AS predictions
  JOIN fixtures ON fixtures.id = predictions.fixture_id
  WHERE predictions.generated_at <= fixtures.kickoff_at
), source_counts AS (
  SELECT
    market,
    confidence_tier,
    league_id::TEXT AS league_id,
    count(*)::BIGINT AS total_predictions,
    count(*) FILTER (WHERE is_correct)::BIGINT AS correct
  FROM (
    SELECT
      results.market,
      results.confidence_tier,
      results.league_id,
      results.is_correct,
      results.settlement_status,
      eligible_predictions.accuracy_rank
    FROM prediction_results AS results
    JOIN eligible_predictions
      ON eligible_predictions.id = results.prediction_id
  ) AS ranked_results
  WHERE accuracy_rank = 1
    AND settlement_status = 'settled'
    AND is_correct IS NOT NULL
  GROUP BY market, confidence_tier, league_id

  UNION ALL

  SELECT
    market,
    confidence_tier,
    league_id::TEXT AS league_id,
    sum(correct + incorrect)::BIGINT AS total_predictions,
    sum(correct)::BIGINT AS correct
  FROM prediction_accuracy_daily
  GROUP BY market, confidence_tier, league_id
), scoped AS (
  SELECT 'overall'::TEXT AS scope, NULL::TEXT AS scope_id, total_predictions, correct
  FROM source_counts
  UNION ALL
  SELECT 'league', league_id, total_predictions, correct
  FROM source_counts
  UNION ALL
  SELECT 'market', market, total_predictions, correct
  FROM source_counts
  UNION ALL
  SELECT 'market_tier', market || ':' || confidence_tier, total_predictions, correct
  FROM source_counts
)
SELECT
  scope,
  scope_id,
  sum(total_predictions)::BIGINT AS total_predictions,
  sum(correct)::BIGINT AS correct
FROM scoped
GROUP BY scope, scope_id
ORDER BY
  CASE scope
    WHEN 'overall' THEN 0
    WHEN 'league' THEN 1
    WHEN 'market' THEN 2
    ELSE 3
  END,
  scope_id NULLS FIRST
"""


# Un-count a reversed settlement from published accuracy (14, 000077, 000078).
#
# `needs_recount` marks a prediction whose settlement moved after it had already
# been folded into `prediction_accuracy_daily`. Without acting on it the
# withdrawn result stays in the published figure for ever -- the least obvious
# consequence of reversible settlement and the easiest to forget.
#
# The adjustment moves one prediction between counters inside its own bucket.
# `total_predictions` is unchanged, because the prediction still happened and was
# still counted; what changed is whether it reads as correct, incorrect, voided
# or pending. `prediction_accuracy_daily` carries
# `CHECK (total_predictions = correct + incorrect + voided + pending)`, so a
# decrement without its matching increment fails the constraint -- which is
# correct, and why both happen in one statement.
#
# Rows whose recorded bucket is NULL are left flagged and reported. Those predate
# 000078 and cannot be placed; guessing a bucket would corrupt a real one, and a
# row that stays flagged is a visible debt rather than silent corruption.
_RECOUNT_REVERSED_RESULTS_SQL = """
WITH flagged AS MATERIALIZED (
  SELECT
    ledger.prediction_id,
    ledger.aggregate_date,
    ledger.market,
    ledger.league_id,
    ledger.confidence_tier,
    ledger.model_version,
    ledger.counted_as,
    ledger.included_in_accuracy
  FROM prediction_result_rollup_ledger AS ledger
  WHERE ledger.needs_recount
  ORDER BY ledger.aggregated_at, ledger.prediction_id
  LIMIT %(limit)s
  FOR UPDATE SKIP LOCKED
), current_state AS (
  -- The newest revision is the settlement as it now stands. Read from the
  -- revisions table, not prediction_results: the raw row is pruned two days
  -- after settlement and a reversal can arrive long after that.
  SELECT DISTINCT ON (revisions.prediction_id)
    revisions.prediction_id,
    revisions.settlement_status,
    revisions.is_correct
  FROM prediction_result_revisions AS revisions
  WHERE revisions.prediction_id IN (SELECT prediction_id FROM flagged)
  ORDER BY revisions.prediction_id, revisions.recorded_at DESC, revisions.id DESC
), reclassified AS (
  SELECT
    flagged.prediction_id,
    flagged.aggregate_date,
    flagged.market,
    flagged.league_id,
    flagged.confidence_tier,
    flagged.model_version,
    flagged.counted_as AS old_counted_as,
    CASE
      WHEN current_state.settlement_status = 'settled' AND current_state.is_correct THEN 'correct'
      WHEN current_state.settlement_status = 'settled' AND current_state.is_correct IS NOT NULL THEN 'incorrect'
      WHEN current_state.settlement_status IN ('void', 'unsettled_data_unavailable') THEN 'voided'
      -- A reversed settlement counts as pending: the market had a grade, the
      -- grade was withdrawn, and it is not decided again yet. Counting it as
      -- voided would claim we know it can never be graded.
      ELSE 'pending'
    END AS new_counted_as
  FROM flagged
  JOIN current_state ON current_state.prediction_id = flagged.prediction_id
  WHERE flagged.included_in_accuracy
    AND flagged.aggregate_date IS NOT NULL
    AND flagged.counted_as IS NOT NULL
), deltas AS (
  SELECT
    aggregate_date,
    market,
    league_id,
    confidence_tier,
    model_version,
    count(*) FILTER (WHERE new_counted_as = 'correct')::INTEGER
      - count(*) FILTER (WHERE old_counted_as = 'correct')::INTEGER AS correct_delta,
    count(*) FILTER (WHERE new_counted_as = 'incorrect')::INTEGER
      - count(*) FILTER (WHERE old_counted_as = 'incorrect')::INTEGER AS incorrect_delta,
    count(*) FILTER (WHERE new_counted_as = 'voided')::INTEGER
      - count(*) FILTER (WHERE old_counted_as = 'voided')::INTEGER AS voided_delta,
    count(*) FILTER (WHERE new_counted_as = 'pending')::INTEGER
      - count(*) FILTER (WHERE old_counted_as = 'pending')::INTEGER AS pending_delta
  FROM reclassified
  WHERE new_counted_as <> old_counted_as
  GROUP BY aggregate_date, market, league_id, confidence_tier, model_version
), adjusted AS (
  UPDATE prediction_accuracy_daily AS daily
  SET
    correct = daily.correct + deltas.correct_delta,
    incorrect = daily.incorrect + deltas.incorrect_delta,
    voided = daily.voided + deltas.voided_delta,
    pending = daily.pending + deltas.pending_delta,
    aggregated_at = now()
  FROM deltas
  WHERE daily.aggregate_date = deltas.aggregate_date
    AND daily.market = deltas.market
    AND daily.league_id = deltas.league_id
    AND daily.confidence_tier = deltas.confidence_tier
    AND daily.model_version = deltas.model_version
  RETURNING daily.aggregate_date
), cleared AS (
  -- Clear the flag and record the new classification, so a second reversal on
  -- the same prediction decrements what this pass left behind rather than what
  -- the original rollup wrote.
  UPDATE prediction_result_rollup_ledger AS ledger
  SET needs_recount = false,
      counted_as = reclassified.new_counted_as
  FROM reclassified
  WHERE ledger.prediction_id = reclassified.prediction_id
  RETURNING ledger.prediction_id
), cleared_not_counted AS (
  -- Predictions the rollup never included in accuracy need no adjustment, but
  -- they must not stay flagged for ever either.
  UPDATE prediction_result_rollup_ledger AS ledger
  SET needs_recount = false
  FROM flagged
  WHERE ledger.prediction_id = flagged.prediction_id
    AND NOT flagged.included_in_accuracy
  RETURNING ledger.prediction_id
)
SELECT
  (SELECT count(*) FROM flagged)::BIGINT AS flagged_examined,
  (SELECT count(*) FROM reclassified WHERE new_counted_as <> old_counted_as)::BIGINT AS predictions_recounted,
  (SELECT count(*) FROM adjusted)::BIGINT AS buckets_adjusted,
  (SELECT count(*) FROM cleared)::BIGINT + (SELECT count(*) FROM cleared_not_counted)::BIGINT AS flags_cleared,
  (
    SELECT count(*) FROM flagged
    WHERE included_in_accuracy AND (aggregate_date IS NULL OR counted_as IS NULL)
  )::BIGINT AS unrecountable
"""


_AGGREGATE_AND_PRUNE_RESULTS_SQL = """
WITH candidate_ids AS MATERIALIZED (
  -- Lock and bound the results before looking up publication rank. Ranking
  -- every engine prediction first scans millions of historical versions even
  -- when this job can only prune one small batch.
  SELECT results.prediction_id
  FROM prediction_results AS results
  WHERE results.settled_at < %(raw_cutoff)s
  ORDER BY results.settled_at, results.prediction_id
  LIMIT %(batch_limit)s
  FOR UPDATE OF results SKIP LOCKED
), candidates AS MATERIALIZED (
  SELECT
    results.prediction_id,
    results.market,
    results.league_id,
    results.confidence_tier,
    predictions.model_version,
    results.is_correct,
    results.settlement_status,
    results.settled_at,
    CASE WHEN latest_eligible.id = predictions.id THEN 1 END AS accuracy_rank,
    (rollup.prediction_id IS NOT NULL) AS already_rolled_up,
    -- Which counter this row increments. Derived once here and used by both the
    -- aggregate and the ledger, so the counters and the record of what produced
    -- them cannot drift apart -- and so a recount can decrement exactly the
    -- column this row incremented.
    CASE
      WHEN results.settlement_status = 'settled' AND results.is_correct THEN 'correct'
      WHEN results.settlement_status = 'settled' AND results.is_correct IS NOT NULL THEN 'incorrect'
      WHEN results.settlement_status IN ('void', 'unsettled_data_unavailable') THEN 'voided'
      ELSE 'pending'
    END AS counted_as
  FROM candidate_ids
  JOIN prediction_results AS results ON results.prediction_id = candidate_ids.prediction_id
  JOIN engine_predictions AS predictions ON predictions.id = results.prediction_id
  JOIN fixtures ON fixtures.id = predictions.fixture_id
  LEFT JOIN LATERAL (
    SELECT eligible.id
    FROM engine_predictions AS eligible
    WHERE eligible.fixture_id = predictions.fixture_id
      AND eligible.market = predictions.market
      AND eligible.generated_at <= fixtures.kickoff_at
    ORDER BY eligible.generated_at DESC, eligible.public_id DESC
    LIMIT 1
  ) AS latest_eligible ON true
  LEFT JOIN prediction_result_rollup_ledger AS rollup ON rollup.prediction_id = results.prediction_id
), aggregated AS (
  INSERT INTO prediction_accuracy_daily (
    aggregate_date,
    market,
    league_id,
    confidence_tier,
    model_version,
    total_predictions,
    correct,
    incorrect,
    voided,
    pending,
    first_settled_at,
    last_settled_at
  )
  SELECT
    (settled_at AT TIME ZONE 'UTC')::DATE,
    market,
    league_id,
    confidence_tier,
    model_version,
    count(*)::INTEGER,
    count(*) FILTER (WHERE counted_as = 'correct')::INTEGER,
    count(*) FILTER (WHERE counted_as = 'incorrect')::INTEGER,
    count(*) FILTER (WHERE counted_as = 'voided')::INTEGER,
    count(*) FILTER (WHERE counted_as = 'pending')::INTEGER,
    min(settled_at),
    max(settled_at)
  FROM candidates
  WHERE accuracy_rank = 1
    AND NOT already_rolled_up
  GROUP BY (settled_at AT TIME ZONE 'UTC')::DATE, market, league_id, confidence_tier, model_version
  ON CONFLICT (aggregate_date, market, league_id, confidence_tier, model_version) DO UPDATE SET
    total_predictions = prediction_accuracy_daily.total_predictions + EXCLUDED.total_predictions,
    correct = prediction_accuracy_daily.correct + EXCLUDED.correct,
    incorrect = prediction_accuracy_daily.incorrect + EXCLUDED.incorrect,
    voided = prediction_accuracy_daily.voided + EXCLUDED.voided,
    pending = prediction_accuracy_daily.pending + EXCLUDED.pending,
    first_settled_at = LEAST(prediction_accuracy_daily.first_settled_at, EXCLUDED.first_settled_at),
    last_settled_at = GREATEST(prediction_accuracy_daily.last_settled_at, EXCLUDED.last_settled_at),
    aggregated_at = now()
  RETURNING 1
), ledgered AS (
  INSERT INTO prediction_result_rollup_ledger (
    prediction_id,
    included_in_accuracy,
    aggregated_at,
    aggregate_date,
    market,
    league_id,
    confidence_tier,
    model_version,
    counted_as
  )
  SELECT
    prediction_id,
    COALESCE(accuracy_rank = 1, FALSE),
    now(),
    -- The bucket, recorded now rather than derived later: prediction_results is
    -- deleted below, and settled_at moves when a settlement is revised, so a
    -- recount that re-derived this could decrement a bucket it never touched.
    CASE WHEN accuracy_rank = 1 THEN (settled_at AT TIME ZONE 'UTC')::DATE END,
    CASE WHEN accuracy_rank = 1 THEN market END,
    CASE WHEN accuracy_rank = 1 THEN league_id END,
    CASE WHEN accuracy_rank = 1 THEN confidence_tier END,
    CASE WHEN accuracy_rank = 1 THEN model_version END,
    CASE WHEN accuracy_rank = 1 THEN counted_as END
  FROM candidates
  ON CONFLICT (prediction_id) DO NOTHING
  RETURNING prediction_id
), deleted AS (
  DELETE FROM prediction_results AS results
  USING candidates
  WHERE results.prediction_id = candidates.prediction_id
    AND (SELECT count(*) FROM aggregated) >= 0
    AND (SELECT count(*) FROM ledgered) >= 0
  RETURNING results.prediction_id
)
SELECT
  (SELECT count(*) FROM candidates)::BIGINT AS results_examined,
  (SELECT count(*) FROM aggregated)::BIGINT AS snapshots_written,
  (SELECT count(*) FROM deleted)::BIGINT AS results_pruned
"""


_UPSERT_ACCURACY_SNAPSHOT_SQL = """
INSERT INTO accuracy_snapshots (
  snapshot_date,
  scope,
  scope_id,
  total_predictions,
  correct,
  accuracy
) VALUES (
  %(snapshot_date)s,
  %(scope)s,
  %(scope_id)s,
  %(total_predictions)s,
  %(correct)s,
  %(accuracy)s
)
ON CONFLICT (snapshot_date, scope, scope_id) DO UPDATE SET
  total_predictions = EXCLUDED.total_predictions,
  correct = EXCLUDED.correct,
  accuracy = EXCLUDED.accuracy,
  computed_at = now()
"""
