from __future__ import annotations

from dataclasses import asdict
from datetime import UTC, date, datetime, timedelta
import hashlib
import json
import unittest

from prediction_engine.db_repository import PredictionInputRepository
from prediction_engine.features import FeatureAssembler
from prediction_engine.pipeline import run_fixture


AS_OF = datetime(2026, 7, 15, 12, 0, tzinfo=UTC)
TARGET_ID = 900


class TemporalLeakageHarnessTests(unittest.TestCase):
    def test_future_observations_cannot_change_an_earlier_as_of_replay(self) -> None:
        database = PointInTimeFixtureDatabase()

        baseline_features, baseline_predictions = replay_fingerprints(database)
        database.add_future_observations()
        replay_features, replay_predictions = replay_fingerprints(database)

        self.assertEqual(replay_features, baseline_features)
        self.assertEqual(replay_predictions, baseline_predictions)
        self.assertGreater(len(baseline_predictions), 0)

    def test_identical_historical_replays_are_bit_for_bit_stable(self) -> None:
        database = PointInTimeFixtureDatabase()
        database.add_future_observations()

        first = replay_fingerprints(database)
        second = replay_fingerprints(database)
        third = replay_fingerprints(database)

        self.assertEqual(first, second)
        self.assertEqual(second, third)


def replay_fingerprints(database: "PointInTimeFixtureDatabase") -> tuple[str, str]:
    repository = PredictionInputRepository(database)
    inputs = repository.load_fixture_inputs(TARGET_ID, as_of_timestamp=AS_OF)
    features = FeatureAssembler().assemble(
        inputs.target_fixture,
        inputs.history,
        inputs.statistics_by_fixture,
        as_of_timestamp=inputs.as_of_timestamp,
        odds=inputs.odds,
    )
    predictions = run_fixture(
        TARGET_ID,
        as_of_timestamp=AS_OF,
        input_repository=repository,
    )
    return stable_hash(asdict(features)), stable_hash(
        [
            {
                "fixture_id": row.fixture_id,
                "market": row.market,
                "prediction": row.prediction,
                "probability": row.probability,
                "confidence": row.confidence,
                "confidence_tier": row.confidence_tier.value,
                "is_best_bet": row.is_best_bet,
                "lifecycle_stage": row.lifecycle_stage.value,
                "data_completeness": row.data_completeness,
                "features_used": row.features_used,
                "explanation": row.explanation,
                "generated_at": row.generated_at,
                "gate_tier": row.gate_tier.value,
                "model_version": row.model_version,
            }
            for row in predictions
        ]
    )


def stable_hash(value: object) -> str:
    payload = json.dumps(value, sort_keys=True, separators=(",", ":"), default=encode_json)
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()


def encode_json(value: object) -> str:
    if isinstance(value, (date, datetime)):
        return value.isoformat()
    raise TypeError(f"unsupported deterministic fixture value: {type(value).__name__}")


class PointInTimeFixtureDatabase:
    """Small deterministic database double that applies the production query cutoffs."""

    def __init__(self) -> None:
        self.fixtures = [target_fixture(), *historical_fixtures()]
        self.statistics = historical_statistics(self.fixtures[1:])
        self.odds = odds_snapshot(AS_OF - timedelta(hours=1), 1.85, 3.60, 4.10)
        self.coverage = [
            {
                "league_id": 39,
                "season_id": 2026,
                "updated_at": AS_OF - timedelta(days=20),
                "has_statistics": True,
                "has_xg": True,
                "has_injuries": False,
                "has_odds": True,
                "has_lineups": False,
            }
        ]
        self.accuracy = [
            accuracy_row(date(2026, 7, 14), "market", "1x2", 0.58, AS_OF - timedelta(hours=4)),
            accuracy_row(date(2026, 7, 14), "market", "total_goals", 0.62, AS_OF - timedelta(hours=4)),
            accuracy_row(date(2026, 7, 14), "league", "39", 0.60, AS_OF - timedelta(hours=4)),
        ]

    def add_future_observations(self) -> None:
        # A fixture whose kickoff and result both occur after the replay cutoff.
        future = fixture_row(
            950,
            AS_OF + timedelta(days=1),
            10,
            20,
            8,
            7,
            AS_OF + timedelta(days=1, hours=2),
        )
        # A match kicked off before the cutoff but its result arrived afterwards.
        late_result = fixture_row(
            951,
            AS_OF - timedelta(days=1),
            10,
            20,
            9,
            8,
            AS_OF + timedelta(minutes=30),
        )
        self.fixtures.extend((future, late_result))
        self.statistics.extend(
            historical_statistics((future, late_result), updated_at=AS_OF + timedelta(hours=1))
        )

        # A later target-fixture price must not replace the price knowable at AS_OF.
        self.odds.extend(odds_snapshot(AS_OF + timedelta(minutes=1), 1.20, 8.00, 15.00))

        # Later coverage and accuracy observations must not change the replay gate
        # or confidence inputs selected at AS_OF.
        self.coverage.append(
            {
                "league_id": 39,
                "season_id": 2026,
                "updated_at": AS_OF + timedelta(minutes=1),
                "has_statistics": False,
                "has_xg": False,
                "has_injuries": False,
                "has_odds": False,
                "has_lineups": False,
            }
        )
        computed_later = AS_OF + timedelta(minutes=1)
        self.accuracy.extend(
            (
                accuracy_row(date(2026, 7, 16), "market", "1x2", 0.01, computed_later),
                accuracy_row(date(2026, 7, 16), "market", "total_goals", 0.99, computed_later),
                accuracy_row(date(2026, 7, 16), "league", "39", 0.02, computed_later),
            )
        )

    def execute(self, sql: str, params: dict[str, object]) -> "Rows":
        as_of = params.get("as_of_timestamp")
        if "f.id <> %(fixture_id)s" in sql:
            assert isinstance(as_of, datetime)
            rows = [
                row
                for row in self.fixtures
                if row["id"] != params["fixture_id"]
                and row["league_id"] == params["league_id"]
                and row["season_id"] == params["season_id"]
                and row["kickoff_at"] < params["target_kickoff_at"]
                and row["kickoff_at"] < as_of
                and row["updated_at"] <= params["target_kickoff_at"]
                and row["updated_at"] <= as_of
                and row["home_goals_ft"] is not None
                and row["away_goals_ft"] is not None
                and teams_overlap(row, int(params["home_team_id"]), int(params["away_team_id"]))
            ]
            return Rows(sorted(rows, key=lambda row: row["kickoff_at"], reverse=True)[:80])
        if "f.league_id = ANY(%(league_ids)s)" in sql:
            assert isinstance(as_of, datetime)
            rows = [
                row
                for row in self.fixtures
                if row["league_id"] in params["league_ids"]
                and row["season_id"] == params["season_id"]
                and row["kickoff_at"] < params["target_kickoff_at"]
                and row["kickoff_at"] < as_of
                and row["updated_at"] <= params["target_kickoff_at"]
                and row["updated_at"] <= as_of
                and row["home_goals_ft"] is not None
                and row["away_goals_ft"] is not None
            ]
            return Rows(sorted(rows, key=lambda row: row["kickoff_at"], reverse=True))
        if "FROM fixtures AS f" in sql and "WHERE f.id = %(fixture_id)s" in sql:
            return Rows([next(row for row in self.fixtures if row["id"] == params["fixture_id"])])
        if "FROM fixture_statistics" in sql:
            assert isinstance(as_of, datetime)
            fixture_ids = set(params["fixture_ids"])
            return Rows(
                [
                    row
                    for row in self.statistics
                    if row["fixture_id"] in fixture_ids and row["updated_at"] <= as_of
                ]
            )
        if "FROM odds AS o" in sql:
            assert isinstance(as_of, datetime)
            visible = [row for row in self.odds if row["fetched_at"] <= as_of]
            if not visible:
                return Rows([])
            latest = max(row["fetched_at"] for row in visible)
            return Rows([row for row in visible if row["fetched_at"] == latest])
        if "FROM league_coverage" in sql:
            assert isinstance(as_of, datetime)
            visible = [
                row
                for row in self.coverage
                if row["league_id"] == params["league_id"]
                and row["season_id"] == params["season_id"]
                and row["updated_at"] <= as_of
            ]
            return Rows([max(visible, key=lambda row: row["updated_at"])]) if visible else Rows([])
        if "FROM accuracy_snapshots" in sql:
            assert isinstance(as_of, datetime)
            visible = [
                row
                for row in self.accuracy
                if row["snapshot_date"] <= params["as_of_date"] and row["computed_at"] <= as_of
            ]
            if not visible:
                return Rows([])
            latest_date = max(row["snapshot_date"] for row in visible)
            return Rows(
                [
                    row
                    for row in visible
                    if row["snapshot_date"] == latest_date
                    and (row["scope"] == "market" or row["scope_id"] == params["league_id"])
                ]
            )
        if "FROM challenger_market_predictions" in sql:
            # The cut-off itself is checked against real rows in test_challengers_sql.
            assert "forecast.as_of_timestamp <= %(as_of_timestamp)s" in sql
            assert "forecast.origin = 'live'" in sql
            return Rows([])
        raise AssertionError(f"temporal fixture did not recognize SQL: {sql}")


class Rows:
    def __init__(self, rows: list[dict[str, object]]) -> None:
        self.rows = rows

    def fetchone(self) -> dict[str, object] | None:
        return self.rows[0] if self.rows else None

    def fetchall(self) -> list[dict[str, object]]:
        return self.rows


def target_fixture() -> dict[str, object]:
    return fixture_row(TARGET_ID, AS_OF + timedelta(hours=4), 10, 20, None, None, AS_OF - timedelta(days=10))


def historical_fixtures() -> list[dict[str, object]]:
    fixtures: list[dict[str, object]] = []
    for index in range(10):
        fixtures.append(
            fixture_row(
                100 + index,
                AS_OF - timedelta(days=30 - index),
                10 if index % 2 == 0 else 30 + index,
                30 + index if index % 2 == 0 else 10,
                2 if index % 3 else 1,
                0 if index % 2 == 0 else 1,
                AS_OF - timedelta(days=29 - index),
            )
        )
        fixtures.append(
            fixture_row(
                200 + index,
                AS_OF - timedelta(days=30 - index),
                20 if index % 2 == 0 else 50 + index,
                50 + index if index % 2 == 0 else 20,
                1 if index % 2 == 0 else 2,
                1 if index % 3 else 0,
                AS_OF - timedelta(days=29 - index),
            )
        )
    return fixtures


def fixture_row(
    fixture_id: int,
    kickoff_at: datetime,
    home_team_id: int,
    away_team_id: int,
    home_goals: int | None,
    away_goals: int | None,
    updated_at: datetime,
) -> dict[str, object]:
    return {
        "id": fixture_id,
        "kickoff_at": kickoff_at,
        "league_id": 39,
        "league_name": "Premier League",
        "season_id": 2026,
        "home_team_id": home_team_id,
        "away_team_id": away_team_id,
        "home_goals_ft": home_goals,
        "away_goals_ft": away_goals,
        "status": "FT" if home_goals is not None else "NS",
        "updated_at": updated_at,
    }


def historical_statistics(
    fixtures: object,
    *,
    updated_at: datetime | None = None,
) -> list[dict[str, object]]:
    rows: list[dict[str, object]] = []
    for record in fixtures:  # type: ignore[union-attr]
        observed_at = updated_at or record["updated_at"]
        rows.extend(
            (
                statistic_row(record["id"], record["home_team_id"], observed_at, 12, 5, 54.0),
                statistic_row(record["id"], record["away_team_id"], observed_at, 9, 3, 46.0),
            )
        )
    return rows


def statistic_row(
    fixture_id: int,
    team_id: int,
    updated_at: datetime,
    shots_total: int,
    shots_on_target: int,
    possession_pct: float,
) -> dict[str, object]:
    return {
        "fixture_id": fixture_id,
        "team_id": team_id,
        "shots_total": shots_total,
        "shots_on_target": shots_on_target,
        "corners": 5,
        "fouls": 10,
        "card_points": 2,
        "possession_pct": possession_pct,
        "xg": 1.4,
        "updated_at": updated_at,
    }


def odds_snapshot(fetched_at: datetime, home: float, draw: float, away: float) -> list[dict[str, object]]:
    return [
        {"outcome": "Home", "value": home, "fetched_at": fetched_at},
        {"outcome": "Draw", "value": draw, "fetched_at": fetched_at},
        {"outcome": "Away", "value": away, "fetched_at": fetched_at},
    ]


def accuracy_row(
    snapshot_date: date,
    scope: str,
    scope_id: str,
    accuracy: float,
    computed_at: datetime,
) -> dict[str, object]:
    return {
        "snapshot_date": snapshot_date,
        "scope": scope,
        "scope_id": scope_id,
        "accuracy": accuracy,
        "computed_at": computed_at,
    }


def teams_overlap(row: dict[str, object], home_team_id: int, away_team_id: int) -> bool:
    teams = {row["home_team_id"], row["away_team_id"]}
    return bool(teams.intersection((home_team_id, away_team_id)))


if __name__ == "__main__":
    unittest.main()
