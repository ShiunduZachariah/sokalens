"""Shadow challengers: point-in-time forecasting, evaluation and the commands."""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
import unittest

from prediction_engine import challengers
from prediction_engine.challengers import (
    ELO_MODEL_KEY,
    RESULT_LAG,
    Target,
    _renormalise_rounded,
    decision,
    evaluate,
    forecast,
    registrations,
    run_forecasts,
)
from prediction_engine.elo import EloConfig
from prediction_engine.features import FixtureRecord
from prediction_engine.model_c import MODEL_KEY as MODEL_C_KEY, load_model


START = datetime(2025, 8, 1, 15, 0, tzinfo=UTC)
TEAMS = (1, 2, 3, 4)


def record(fixture_id: int, home: int, away: int, kickoff: datetime, *, goals=(1, 0), status="FT") -> FixtureRecord:
    return FixtureRecord(
        id=fixture_id,
        kickoff_at=kickoff,
        league_id=7,
        league_name="Testland Premier",
        season=2025,
        home_team_id=home,
        away_team_id=away,
        home_goals=goals[0] if status == "FT" else None,
        away_goals=goals[1] if status == "FT" else None,
        status=status,
    )


def season(start: datetime = START) -> list[FixtureRecord]:
    """A round robin in which team 1 always wins and team 4 always loses."""
    rows = []
    fixture_id = 1
    day = 0
    for home in TEAMS:
        for away in TEAMS:
            if home == away:
                continue
            goals = (2, 0) if home < away else (0, 2)
            rows.append(record(fixture_id, home, away, start + timedelta(days=day), goals=goals))
            fixture_id += 1
            day += 3
    return rows


class ForecastTests(unittest.TestCase):
    def setUp(self):
        self.model = load_model()

    def test_every_target_gets_both_challengers(self):
        history = season()
        target = Target(record(100, 1, 4, START + timedelta(days=60), status="NS"), START + timedelta(days=59), "EARLY")
        forecasts = forecast(history, [target], model_c=self.model)
        self.assertEqual({item.model_key for item in forecasts}, {ELO_MODEL_KEY, MODEL_C_KEY})
        for item in forecasts:
            self.assertAlmostEqual(sum(item.probabilities.values()), 1.0)
            self.assertGreater(item.probabilities["home"], item.probabilities["away"])

    def test_a_result_inside_the_lag_is_never_seen(self):
        history = season()
        last = history[-1]
        # As of just inside the lag after the last fixture's kick-off, its
        # result may not exist yet; three hours later it does.
        team = last.home_team_id
        opponent = next(other for other in TEAMS if other not in (last.home_team_id, last.away_team_id))
        kickoff = last.kickoff_at + timedelta(days=2)
        early = Target(record(100, team, opponent, kickoff, status="NS"), last.kickoff_at + RESULT_LAG - timedelta(minutes=1), "EARLY")
        late = Target(record(101, team, opponent, kickoff, status="NS"), last.kickoff_at + RESULT_LAG, "EARLY")
        by_target = {}
        for item in forecast(history, [early, late], model_c=self.model):
            if item.model_key == ELO_MODEL_KEY:
                by_target[item.target.fixture.id] = item.explanation_factors
        seen = sum(1 for row in history if team in (row.home_team_id, row.away_team_id))
        self.assertEqual(by_target[100]["home_sample"], seen - 1)
        self.assertEqual(by_target[101]["home_sample"], seen)

    def test_a_replayed_fixture_never_sees_its_own_result(self):
        history = season()
        own = history[5]
        target = Target(own, own.kickoff_at - timedelta(hours=1), "PRE_MATCH")
        with_own = forecast(history, [target], model_c=self.model)
        without_own = forecast([row for row in history if row.id != own.id], [target], model_c=self.model)
        self.assertEqual(
            [item.probabilities for item in with_own], [item.probabilities for item in without_own]
        )

    def test_history_out_of_order_is_refused(self):
        history = season()
        target = Target(record(100, 1, 2, START + timedelta(days=90), status="NS"), START + timedelta(days=89), "EARLY")
        with self.assertRaises(ValueError):
            forecast(list(reversed(history)), [target], model_c=self.model)


class EvaluationTests(unittest.TestCase):
    def _row(self, challenger, champion, outcome):
        row = {f"{name}_probability": value for name, value in zip(("home", "draw", "away"), challenger)}
        row.update(
            {f"champion_{name}_probability": value for name, value in zip(("home", "draw", "away"), champion or (None,) * 3)}
        )
        row["final_outcome"] = outcome
        return row

    def test_paired_difference_is_negative_when_the_challenger_is_better(self):
        rows = [
            self._row((0.7, 0.2, 0.1), (0.5, 0.3, 0.2), "home"),
            self._row((0.6, 0.25, 0.15), (0.4, 0.3, 0.3), "home"),
            self._row((0.2, 0.3, 0.5), (0.3, 0.3, 0.4), "away"),
            self._row((0.2, 0.3, 0.5), None, "away"),
        ]
        summary = evaluate(rows)
        self.assertEqual(summary["sample_count"], 4)
        self.assertEqual(summary["paired_with_champion"], 3)
        self.assertLess(summary["mean_log_loss_difference"], 0.0)
        self.assertEqual(summary["accuracy"], 1.0)

    def test_the_report_never_promotes(self):
        self.assertEqual(decision({"paired_with_champion": 10}, minimum_samples=500)[0], "extend")
        self.assertEqual(decision({"paired_with_champion": 10_000}, minimum_samples=500)[0], "pending")

    def test_rounded_probabilities_still_sum_to_one(self):
        params = {"home_probability": 0.333333, "draw_probability": 0.333333, "away_probability": 0.333333}
        _renormalise_rounded(params, "")
        self.assertAlmostEqual(sum(params.values()), 1.0, places=9)


class RegistrationTests(unittest.TestCase):
    def test_versions_follow_configuration_and_artifact(self):
        model = load_model()
        first = {entry.model_key: entry for entry in registrations(model)}
        again = {entry.model_key: entry for entry in registrations(model)}
        changed = {entry.model_key: entry for entry in registrations(model, config=EloConfig(k_factor=30.0))}
        self.assertEqual(first[ELO_MODEL_KEY].model_version, again[ELO_MODEL_KEY].model_version)
        self.assertNotEqual(first[ELO_MODEL_KEY].model_version, changed[ELO_MODEL_KEY].model_version)
        self.assertEqual(first[MODEL_C_KEY].model_version, model.metadata["model_version"])
        self.assertEqual(first[MODEL_C_KEY].artifact_hash, model.artifact_hash)


class _FakeRepository:
    def __init__(self, *, last=None, live=(), history=()):
        self.last = last
        self.live = list(live)
        self.rows = list(history)
        self.history_reads = 0
        self.inserted = []
        self.events = []

    def ensure_registered(self, entries):
        self.events.append("register")
        return {entry.model_key: f"id-{entry.model_key}" for entry in entries}

    def commit(self):
        self.events.append("commit")

    def last_live_forecast_at(self):
        return self.last

    def live_targets(self, *, as_of, limit, model_ids):
        return self.live[:limit]

    def replay_targets(self, *, as_of, limit, model_ids):
        return [row for row in self.rows if row.kickoff_at < as_of - RESULT_LAG][-limit:]

    def history(self, *, since, until):
        self.history_reads += 1
        return iter([row for row in self.rows if since <= row.kickoff_at < until])

    def insert_forecasts(self, forecasts, *, model_ids, origin):
        self.inserted.extend((origin, item) for item in forecasts)
        return len(forecasts)


class RunForecastsTests(unittest.TestCase):
    def setUp(self):
        self.model = load_model()
        self.as_of = START + timedelta(days=60)

    def _run(self, repository, *, origin="live", champion=lambda fixture, as_of: None, min_interval=timedelta(minutes=30)):
        return run_forecasts(
            repository,
            as_of=self.as_of,
            origin=origin,
            limit=10,
            history_days=400,
            min_interval=min_interval,
            champion=champion,
            model_c=self.model,
        )

    def test_nothing_to_forecast_reads_no_history(self):
        repository = _FakeRepository(history=season())
        summary = self._run(repository)
        self.assertEqual(summary["targets"], 0)
        self.assertEqual(repository.history_reads, 0)

    def test_a_recent_run_throttles_the_replay(self):
        upcoming = record(100, 1, 4, self.as_of + timedelta(days=1), status="NS")
        repository = _FakeRepository(last=self.as_of - timedelta(minutes=5), live=[(upcoming, "EARLY")], history=season())
        summary = self._run(repository)
        self.assertEqual(summary["skipped"], "min_interval")
        self.assertEqual(repository.inserted, [])

    def test_live_targets_are_forecast_with_the_champion_beside_them(self):
        upcoming = record(100, 1, 4, self.as_of + timedelta(days=1), status="NS")
        repository = _FakeRepository(live=[(upcoming, "PRE_MATCH")], history=season())
        champion = {"home": 0.5, "draw": 0.3, "away": 0.2}
        summary = self._run(repository, champion=lambda fixture, as_of: champion)
        self.assertEqual(summary["challenger_forecasts"], 2)
        self.assertEqual(summary["champion_paired"], 1)
        # The registry is committed before the champion read, which may roll back.
        self.assertEqual(repository.events[:2], ["register", "commit"])
        for origin, item in repository.inserted:
            self.assertEqual(origin, "live")
            self.assertEqual(item.target.champion, champion)
            self.assertEqual(item.target.lifecycle_stage, "PRE_MATCH")

    def test_replay_forecasts_an_hour_before_kickoff(self):
        repository = _FakeRepository(history=season())
        summary = self._run(repository, origin="replay")
        self.assertGreater(summary["targets"], 0)
        for _, item in repository.inserted:
            self.assertEqual(item.target.as_of, item.target.fixture.kickoff_at - challengers.REPLAY_LEAD)

    def test_an_unknown_origin_is_refused(self):
        with self.assertRaises(ValueError):
            self._run(_FakeRepository(), origin="backfill")


if __name__ == "__main__":
    unittest.main()
