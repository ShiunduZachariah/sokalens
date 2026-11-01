"""The wiring that makes in-play predictions exist (architecture v2 §7.4-§7.6).

Everything asserted here was individually true before this module existed --
the model was pure and tested, the scope was supported, the columns were
migrated -- and production still had zero in-play rows, because nothing joined
them up. So these tests are mostly about the joins:

* a published row carries the §7.4 audit envelope, not just a probability;
* §7.5 holds structurally: the in-play path is reached only by naming the
  scope, and the pre-match path is byte-for-byte unaffected by its existence;
* §14 holds: a stale snapshot produces a recorded reason and no row;
* §7.6 is delegated to `should_regenerate` rather than re-derived here.
"""

from __future__ import annotations

from dataclasses import replace
from datetime import UTC, datetime, timedelta
from types import SimpleNamespace
import unittest

from prediction_engine.db_repository import InPlayFixtureState
from prediction_engine.inplay_features import LiveEvent, LiveSnapshot, MatchPeriod
from prediction_engine.inplay_model import (
    CadenceState,
    RegenerationTier,
    SnapshotState,
    SuppressionReason,
)
from prediction_engine.inplay_runner import (
    CADENCE_UNCHANGED,
    NO_PUBLISHABLE_MARKETS,
    NO_SNAPSHOT,
    PRE_MATCH_RATES_UNAVAILABLE,
    InPlayRunStats,
    lifecycle_stage_for,
    run_and_save_in_play_fixture,
    run_in_play_fixture,
)
from prediction_engine.persistence import (
    IN_PLAY_LIFECYCLE_STAGES,
    PredictionLifecycleStage,
)
from prediction_engine.pipeline import PreMatchRates
import prediction_engine.inplay_runner as inplay_runner


NOW = datetime(2026, 9, 21, 9, 52, 30, tzinfo=UTC)
FIXTURE_ID = 3956996
HOME_TEAM_ID = 771
AWAY_TEAM_ID = 772


def snapshot(**overrides: object) -> LiveSnapshot:
    defaults = {
        "fixture_id": FIXTURE_ID,
        "captured_at": NOW - timedelta(seconds=12),
        "status": "2H",
        "period": "2H",
        "elapsed_minute": 63,
        "elapsed_extra": 0,
        "home_goals": 1,
        "away_goals": 0,
        "provider": "api-football",
        "snapshot_id": 4821,
    }
    defaults.update(overrides)
    return LiveSnapshot(**defaults)  # type: ignore[arg-type]


def red_card_event(team_id: int, minute: int) -> LiveEvent:
    return LiveEvent(
        fixture_id=FIXTURE_ID,
        observed_at=NOW - timedelta(seconds=30),
        elapsed_minute=minute,
        event_type="Card",
        event_detail="Red Card",
        team_id=team_id,
        identity_key=f"card-{team_id}-{minute}",
    )


class FakeRepository:
    """Stands in for PredictionInputRepository's in-play reads."""

    def __init__(
        self,
        *,
        state: InPlayFixtureState | None = None,
        cadence: CadenceState | None = None,
        rates: PreMatchRates | None = None,
        fixture_missing: bool = False,
        snapshot_state: object | None = None,
        snapshots_supported: bool = True,
    ) -> None:
        self._state = state
        self._cadence = cadence
        self._snapshot_state = snapshot_state
        if not snapshots_supported:
            # A repository predating snapshots: the runner must still work, and
            # simply record no graph point. Shadowing the method with None is what
            # the runner's getattr lookup sees as "not available".
            self.load_in_play_snapshot_state = None  # type: ignore[assignment]
        self._rates = rates or PreMatchRates(1.62, 1.05, 0.88, ())
        self._fixture_missing = fixture_missing
        self.fixture_input_calls = 0

    def load_in_play_state(self, fixture_id: int, *, as_of_timestamp: datetime):
        return self._state

    def load_in_play_cadence_state(self, fixture_id: int):
        return self._cadence

    def load_in_play_snapshot_state(self, fixture_id: int):
        return self._snapshot_state

    def load_fixture_inputs(self, fixture_id: int, *, as_of_timestamp: datetime):
        self.fixture_input_calls += 1
        if self._fixture_missing:
            raise LookupError(f"fixture {fixture_id} not found")
        return object()


class FakeOutput:
    def __init__(self) -> None:
        self.saved: list[object] = []
        self.publications: list[tuple[int, list[object], datetime]] = []

    def replace_in_play_predictions(self, fixture_id: int, predictions: list[object], *, generated_at: datetime) -> None:
        self.publications.append((fixture_id, predictions, generated_at))
        self.saved.extend(predictions)


def state(**overrides: object) -> InPlayFixtureState:
    defaults = {
        "fixture_id": FIXTURE_ID,
        "home_team_id": HOME_TEAM_ID,
        "away_team_id": AWAY_TEAM_ID,
        "kickoff_at": NOW - timedelta(minutes=70),
        "snapshot": snapshot(),
        "events": (),
    }
    defaults.update(overrides)
    return InPlayFixtureState(**defaults)  # type: ignore[arg-type]


class InPlayRunnerTests(unittest.TestCase):
    def setUp(self) -> None:
        self._real_rates = inplay_runner.fixture_expected_goals
        inplay_runner.fixture_expected_goals = lambda _inputs: PreMatchRates(1.62, 1.05, 0.88, ())

    def tearDown(self) -> None:
        inplay_runner.fixture_expected_goals = self._real_rates

    def test_a_live_fixture_produces_publishable_markets(self) -> None:
        outcome = run_in_play_fixture(
            FIXTURE_ID,
            as_of_timestamp=NOW,
            input_repository=FakeRepository(state=state()),
        )

        self.assertFalse(outcome.suppressed)
        self.assertEqual(outcome.reason, SuppressionReason.NONE.value)
        markets = {prediction.market for prediction in outcome.published}
        self.assertIn("match_result_in_play", markets)
        self.assertIn("next_goal", markets)
        self.assertIn("btts_in_play", markets)
        # Over/under is one bet per line, not one row with the whole ladder.
        self.assertTrue(any(market.startswith("total_goals_remaining_") for market in markets))

    def test_every_published_row_carries_the_7_4_audit_envelope(self) -> None:
        outcome = run_in_play_fixture(
            FIXTURE_ID,
            as_of_timestamp=NOW,
            input_repository=FakeRepository(state=state()),
        )

        self.assertTrue(outcome.published)
        for prediction in outcome.published:
            prediction.validate()
            self.assertEqual(prediction.lifecycle_stage, PredictionLifecycleStage.IN_PLAY_2H)
            self.assertEqual(prediction.fixture_live_snapshot_id, 4821)
            self.assertEqual(prediction.match_minute, 63)
            self.assertEqual(prediction.period, MatchPeriod.SECOND_HALF.value)
            self.assertEqual(prediction.data_cutoff_at, NOW - timedelta(seconds=12))
            self.assertEqual(prediction.feature_version, "inplay-analytic-v1")
            row = prediction.to_db_row()
            self.assertEqual(row["fixture_live_snapshot_id"], 4821)
            self.assertEqual(row["feature_version"], "inplay-analytic-v1")

    def test_the_lifecycle_stage_follows_the_match_period(self) -> None:
        for period, expected in (
            (MatchPeriod.FIRST_HALF, PredictionLifecycleStage.IN_PLAY_1H),
            (MatchPeriod.HALF_TIME, PredictionLifecycleStage.HALF_TIME),
            (MatchPeriod.SECOND_HALF, PredictionLifecycleStage.IN_PLAY_2H),
        ):
            with self.subTest(period=period):
                self.assertEqual(lifecycle_stage_for(period), expected)

    def test_a_pre_match_period_has_no_in_play_stage_rather_than_a_default(self) -> None:
        # Defaulting to PRE_MATCH here is exactly the §7.5 leak: the row would
        # be valid on both surfaces and no test downstream would catch it.
        with self.assertRaises(ValueError):
            lifecycle_stage_for(MatchPeriod.PRE_MATCH)

    def test_a_stale_snapshot_is_suppressed_with_a_recorded_reason(self) -> None:
        stale = state(snapshot=snapshot(captured_at=NOW - timedelta(seconds=95)))

        outcome = run_in_play_fixture(
            FIXTURE_ID, as_of_timestamp=NOW, input_repository=FakeRepository(state=stale)
        )

        self.assertTrue(outcome.suppressed)
        self.assertEqual(outcome.reason, SuppressionReason.STALE_DATA.value)
        self.assertEqual(outcome.published, ())
        # §14 wants the reason recorded, not just the absence.
        self.assertGreater(outcome.summary()["snapshot_age_seconds"], 90)

    def test_extra_time_is_suppressed_rather_than_modelled(self) -> None:
        extra = state(snapshot=snapshot(status="ET", period="ET", elapsed_minute=98))

        outcome = run_in_play_fixture(
            FIXTURE_ID, as_of_timestamp=NOW, input_repository=FakeRepository(state=extra)
        )

        self.assertEqual(outcome.reason, SuppressionReason.EXTRA_TIME_NOT_MODELLED.value)

    def test_a_fixture_with_no_snapshot_is_an_outcome_not_an_error(self) -> None:
        outcome = run_in_play_fixture(
            FIXTURE_ID, as_of_timestamp=NOW, input_repository=FakeRepository(state=None)
        )

        self.assertEqual(outcome.reason, NO_SNAPSHOT)
        self.assertEqual(outcome.published, ())

    def test_a_fixture_the_pre_match_path_cannot_rate_publishes_nothing(self) -> None:
        # No pre-match lambdas means nothing to condition on. Substituting a
        # league average would be the extrapolation §14 forbids.
        inplay_runner.fixture_expected_goals = self._real_rates
        repository = FakeRepository(state=state(), fixture_missing=True)

        outcome = run_in_play_fixture(FIXTURE_ID, as_of_timestamp=NOW, input_repository=repository)

        self.assertEqual(outcome.reason, PRE_MATCH_RATES_UNAVAILABLE)

    def test_btts_settled_by_the_current_score_is_withheld(self) -> None:
        both_scored = state(snapshot=snapshot(home_goals=1, away_goals=1))

        outcome = run_in_play_fixture(
            FIXTURE_ID, as_of_timestamp=NOW, input_repository=FakeRepository(state=both_scored)
        )

        markets = {prediction.market for prediction in outcome.published}
        self.assertNotIn("btts_in_play", markets)
        self.assertIn("match_result_in_play", markets)

    def test_a_red_card_from_the_event_timeline_moves_the_rates(self) -> None:
        with_card = state(events=(red_card_event(AWAY_TEAM_ID, 40),))

        plain = run_in_play_fixture(
            FIXTURE_ID, as_of_timestamp=NOW, input_repository=FakeRepository(state=state())
        )
        carded = run_in_play_fixture(
            FIXTURE_ID, as_of_timestamp=NOW, input_repository=FakeRepository(state=with_card)
        )

        self.assertGreater(
            carded.model_result.lambdas["remaining_home"],
            plain.model_result.lambdas["remaining_home"],
        )
        self.assertLess(
            carded.model_result.lambdas["remaining_away"],
            plain.model_result.lambdas["remaining_away"],
        )


class CadenceTests(unittest.TestCase):
    """§7.6 is the model's decision; the runner only supplies the inputs."""

    def setUp(self) -> None:
        self._real_rates = inplay_runner.fixture_expected_goals
        inplay_runner.fixture_expected_goals = lambda _inputs: PreMatchRates(1.62, 1.05, 0.88, ())

    def tearDown(self) -> None:
        inplay_runner.fixture_expected_goals = self._real_rates

    def _cadence(self, **overrides: object) -> CadenceState:
        defaults = {
            "generated_at": NOW - timedelta(seconds=30),
            "home_goals": 1,
            "away_goals": 0,
            "home_red_cards": 0,
            "away_red_cards": 0,
            "period": MatchPeriod.SECOND_HALF,
        }
        defaults.update(overrides)
        return CadenceState(**defaults)  # type: ignore[arg-type]

    def test_an_unchanged_match_inside_the_interval_is_not_regenerated(self) -> None:
        outcome = run_in_play_fixture(
            FIXTURE_ID,
            as_of_timestamp=NOW,
            input_repository=FakeRepository(state=state(), cadence=self._cadence()),
        )

        self.assertEqual(outcome.reason, CADENCE_UNCHANGED)
        self.assertEqual(outcome.published, ())

    def test_a_goal_regenerates_immediately(self) -> None:
        outcome = run_in_play_fixture(
            FIXTURE_ID,
            as_of_timestamp=NOW,
            input_repository=FakeRepository(state=state(), cadence=self._cadence(home_goals=0)),
        )

        self.assertEqual(outcome.regeneration_reason, "goal")
        self.assertTrue(outcome.published)

    def test_a_red_card_regenerates_immediately(self) -> None:
        outcome = run_in_play_fixture(
            FIXTURE_ID,
            as_of_timestamp=NOW,
            input_repository=FakeRepository(
                state=state(events=(red_card_event(AWAY_TEAM_ID, 40),)),
                cadence=self._cadence(),
            ),
        )

        self.assertEqual(outcome.regeneration_reason, "red_card")

    def test_half_time_regenerates_immediately(self) -> None:
        outcome = run_in_play_fixture(
            FIXTURE_ID,
            as_of_timestamp=NOW,
            input_repository=FakeRepository(
                state=state(snapshot=snapshot(status="HT", period="HT", elapsed_minute=45)),
                cadence=self._cadence(period=MatchPeriod.FIRST_HALF),
            ),
        )

        self.assertIn(outcome.regeneration_reason, {"half_time", "period_change"})
        self.assertTrue(outcome.published)

    def test_the_tier_b_timer_is_slower_than_the_tier_a_timer(self) -> None:
        three_minutes_ago = self._cadence(generated_at=NOW - timedelta(minutes=3))

        tier_a = run_in_play_fixture(
            FIXTURE_ID,
            as_of_timestamp=NOW,
            input_repository=FakeRepository(state=state(), cadence=three_minutes_ago),
            tier=RegenerationTier.A,
        )
        tier_b = run_in_play_fixture(
            FIXTURE_ID,
            as_of_timestamp=NOW,
            input_repository=FakeRepository(state=state(), cadence=three_minutes_ago),
            tier=RegenerationTier.B,
        )

        self.assertEqual(tier_a.regeneration_reason, "timer")
        self.assertEqual(tier_b.reason, CADENCE_UNCHANGED)

    def test_the_published_row_carries_the_state_the_next_cadence_check_needs(self) -> None:
        # The cadence survives a worker restart only because it is read back
        # out of the row rather than held in memory.
        outcome = run_in_play_fixture(
            FIXTURE_ID,
            as_of_timestamp=NOW,
            input_repository=FakeRepository(state=state(events=(red_card_event(HOME_TEAM_ID, 30),))),
        )

        recorded = outcome.published[0].features_used["in_play"]
        self.assertEqual(recorded["home_goals"], 1)
        self.assertEqual(recorded["away_goals"], 0)
        self.assertEqual(recorded["home_red_cards"], 1)
        self.assertEqual(recorded["away_red_cards"], 0)
        self.assertEqual(recorded["period"], MatchPeriod.SECOND_HALF.value)
        self.assertEqual(recorded["snapshot_id"], 4821)


class SaveTests(unittest.TestCase):
    def setUp(self) -> None:
        self._real_rates = inplay_runner.fixture_expected_goals
        inplay_runner.fixture_expected_goals = lambda _inputs: PreMatchRates(1.62, 1.05, 0.88, ())

    def tearDown(self) -> None:
        inplay_runner.fixture_expected_goals = self._real_rates

    def test_nothing_is_written_when_the_run_is_suppressed(self) -> None:
        output = FakeOutput()
        stale = state(snapshot=snapshot(captured_at=NOW - timedelta(seconds=300)))

        run_and_save_in_play_fixture(
            FIXTURE_ID,
            as_of_timestamp=NOW,
            input_repository=FakeRepository(state=stale),
            output_repository=output,
        )

        self.assertEqual(output.saved, [])
        self.assertEqual(output.publications, [], "suppression must not replace the current set")

    def test_healthy_empty_market_set_is_published_as_empty(self) -> None:
        output = FakeOutput()
        original = inplay_runner.publishable_markets
        inplay_runner.publishable_markets = lambda _result: {}
        try:
            outcome = run_and_save_in_play_fixture(
                FIXTURE_ID,
                as_of_timestamp=NOW,
                input_repository=FakeRepository(state=state()),
                output_repository=output,
            )
        finally:
            inplay_runner.publishable_markets = original

        self.assertEqual(outcome.reason, NO_PUBLISHABLE_MARKETS)
        self.assertEqual(output.publications, [(FIXTURE_ID, [], NOW)])

    def test_publishable_markets_are_written(self) -> None:
        output = FakeOutput()

        outcome = run_and_save_in_play_fixture(
            FIXTURE_ID,
            as_of_timestamp=NOW,
            input_repository=FakeRepository(state=state()),
            output_repository=output,
        )

        self.assertEqual(len(output.saved), len(outcome.published))
        self.assertTrue(output.saved)


class LifecycleVocabularyTests(unittest.TestCase):
    """§7.5 applied to the vocabulary: widened, never narrowed."""

    def test_the_pre_match_stages_are_unchanged(self) -> None:
        self.assertEqual(PredictionLifecycleStage.PRE_MATCH.value, "PRE_MATCH")
        self.assertEqual(PredictionLifecycleStage.EARLY.value, "EARLY")
        self.assertEqual(PredictionLifecycleStage.LINEUP_CONFIRMED.value, "LINEUP_CONFIRMED")

    def test_no_pre_match_stage_counts_as_in_play(self) -> None:
        for stage in (
            PredictionLifecycleStage.EARLY,
            PredictionLifecycleStage.PRE_MATCH,
            PredictionLifecycleStage.LINEUP_CONFIRMED,
        ):
            self.assertNotIn(stage, IN_PLAY_LIFECYCLE_STAGES)

    def test_the_migrated_vocabulary_is_fully_representable(self) -> None:
        # Migration 000071 widened the CHECK constraint to these eight. A
        # value the database accepts but the enum cannot express is a column
        # that can never be written, which is how this shipped unused.
        self.assertEqual(
            {stage.value for stage in PredictionLifecycleStage},
            {
                "EARLY",
                "PRE_MATCH",
                "LINEUP_CONFIRMED",
                "IN_PLAY_1H",
                "HALF_TIME",
                "IN_PLAY_2H",
                "EXTRA_TIME",
                "FINAL",
            },
        )


class RunStatsTests(unittest.TestCase):
    def test_suppressions_are_counted_by_reason(self) -> None:
        stats = InPlayRunStats()
        stats.record(inplay_runner.InPlayRunOutcome(fixture_id=1, reason=NO_SNAPSHOT))
        stats.record(inplay_runner.InPlayRunOutcome(fixture_id=2, reason=NO_SNAPSHOT))
        stats.record(
            inplay_runner.InPlayRunOutcome(fixture_id=3, reason=SuppressionReason.STALE_DATA.value)
        )

        self.assertEqual(stats.fixtures_processed, 3)
        self.assertEqual(stats.fixtures_published, 0)
        self.assertEqual(stats.suppressed_by_reason, {NO_SNAPSHOT: 2, "STALE_DATA": 1})


if __name__ == "__main__":
    unittest.main()


class SnapshotWritingTests(unittest.TestCase):
    """The runner's two writes are independent: publish, and record a graph point.

    Before this the runner returned before the model ran whenever regeneration was
    unchanged, so a quiet tick had no result to snapshot and the graph went blank
    through every flat passage of play.
    """

    def setUp(self) -> None:
        # The pre-match rates come from the pipeline, which these fakes do not
        # feed; the other suites in this file stub it the same way.
        self._real_rates = inplay_runner.fixture_expected_goals
        inplay_runner.fixture_expected_goals = lambda _inputs: PreMatchRates(1.62, 1.05, 0.88, ())

    def tearDown(self) -> None:
        inplay_runner.fixture_expected_goals = self._real_rates

    class RecordingSnapshots:
        def __init__(self) -> None:
            self.calls: list[dict[str, object]] = []

        def write_snapshot(self, **kwargs: object) -> object:
            self.calls.append(kwargs)
            return SimpleNamespace(id=len(self.calls), sequence=len(self.calls))

    @staticmethod
    def cadence_at(moment: datetime) -> CadenceState:
        return CadenceState(
            generated_at=moment,
            home_goals=1,
            away_goals=0,
            home_red_cards=0,
            away_red_cards=0,
            period=MatchPeriod.SECOND_HALF,
        )

    def run_once(self, *, cadence, snapshot_state, now_offset_seconds: int = 0):
        snapshots = self.RecordingSnapshots()
        repository = FakeRepository(
            state=state(),
            cadence=cadence,
            snapshot_state=snapshot_state,
        )
        outcome = run_and_save_in_play_fixture(
            FIXTURE_ID,
            as_of_timestamp=NOW + timedelta(seconds=now_offset_seconds),
            input_repository=repository,
            output_repository=SimpleNamespace(replace_in_play_predictions=lambda fixture_id, rows, **kwargs: None),
            snapshot_repository=snapshots,
        )
        return outcome, snapshots

    def test_a_quiet_tick_publishes_nothing_but_still_records_a_point(self) -> None:
        # The behaviour that was missing. Regeneration is unchanged, so nothing
        # publishes -- and the graph still gets its point.
        cadence = self.cadence_at(NOW - timedelta(seconds=5))
        outcome, snapshots = self.run_once(cadence=cadence, snapshot_state=None)
        self.assertEqual(outcome.published, ())
        self.assertEqual(outcome.reason, CADENCE_UNCHANGED)
        self.assertEqual(len(snapshots.calls), 1)
        self.assertEqual(snapshots.calls[0]["trigger"], "timer")

    def test_nothing_is_written_when_neither_is_due(self) -> None:
        # A snapshot written moments ago and an unchanged match: the fixture is
        # skipped entirely, which is what keeps a 30-second tick cheap.
        cadence = self.cadence_at(NOW - timedelta(seconds=5))
        snapshot_state = SnapshotState(
            captured_at=NOW - timedelta(seconds=5),
            home_goals=1,
            away_goals=0,
            home_red_cards=0,
            away_red_cards=0,
            period=MatchPeriod.SECOND_HALF,
        )
        outcome, snapshots = self.run_once(cadence=cadence, snapshot_state=snapshot_state)
        self.assertEqual(outcome.reason, CADENCE_UNCHANGED)
        self.assertEqual(snapshots.calls, [])

    def test_the_snapshot_carries_the_match_state_and_basis_points(self) -> None:
        outcome, snapshots = self.run_once(cadence=None, snapshot_state=None)
        call = snapshots.calls[0]
        self.assertEqual(call["fixture_id"], FIXTURE_ID)
        self.assertEqual(call["model_health"], "ok")
        self.assertIsNone(call["suppression_reason"])
        # Basis points sum to the total per MUTUALLY EXCLUSIVE GROUP, not per
        # market. A line ladder is many over/under pairs, so a three-line market
        # sums to 30,000 -- and the earlier version of this assertion, which
        # required 10,000 for the whole market, was the shape of a real defect:
        # apportioning the pairs together sent an 81.65% over as 16.33%.
        for market, selections in call["markets"].items():
            lines: dict[str, int] = {}
            plain = 0
            for name, value in selections.items():
                prefix, _, suffix = name.rpartition("_")
                if prefix in {"over", "under"}:
                    lines[suffix] = lines.get(suffix, 0) + value
                else:
                    plain += value
            with self.subTest(market=market):
                if plain:
                    self.assertEqual(plain, 10000)
                for line, total in lines.items():
                    with self.subTest(line=line):
                        self.assertEqual(total, 10000)
        # Match state comes from the features, not from the model result.
        self.assertIsNotNone(call["home_goals"])
        self.assertIsNotNone(call["away_goals"])

    def test_a_repository_without_snapshot_support_still_runs(self) -> None:
        # The loader is optional so pre-snapshot call sites keep working; they
        # simply record no graph point.
        snapshots = self.RecordingSnapshots()
        repository = FakeRepository(state=state(), cadence=None, snapshots_supported=False)
        outcome = run_and_save_in_play_fixture(
            FIXTURE_ID,
            as_of_timestamp=NOW,
            input_repository=repository,
            output_repository=SimpleNamespace(replace_in_play_predictions=lambda fixture_id, rows, **kwargs: None),
            snapshot_repository=snapshots,
        )
        self.assertTrue(outcome.published)
        self.assertEqual(snapshots.calls, [])

    def test_repeated_healthy_generation_still_offers_a_complete_set(self) -> None:
        # Publication membership is a whole-set claim. Even an unchanged
        # rerun must pass every current line to the atomic writer; its content
        # hash and watermark decide whether any row needs versioning.
        snapshots = self.RecordingSnapshots()
        first = FakeRepository(state=state(), cadence=None, snapshot_state=None)
        published = run_and_save_in_play_fixture(
            FIXTURE_ID,
            as_of_timestamp=NOW,
            input_repository=first,
            output_repository=SimpleNamespace(replace_in_play_predictions=lambda fixture_id, rows, **kwargs: None),
            snapshot_repository=snapshots,
        )
        self.assertTrue(published.published)

        # Replay the same model inputs. The writer compares its rows with the
        # database; the runner must still offer a complete membership set.
        repeat = FakeRepository(state=state(), cadence=None, snapshot_state=None)
        again = run_and_save_in_play_fixture(
            FIXTURE_ID,
            as_of_timestamp=NOW,
            input_repository=repeat,
            output_repository=SimpleNamespace(replace_in_play_predictions=lambda fixture_id, rows, **kwargs: None),
            snapshot_repository=snapshots,
        )
        self.assertEqual(
            {row.market for row in again.published},
            {row.market for row in published.published},
        )
        self.assertEqual(len(snapshots.calls), 2)

    def test_no_snapshot_repository_means_no_write(self) -> None:
        repository = FakeRepository(state=state(), cadence=None, snapshot_state=None)
        outcome = run_and_save_in_play_fixture(
            FIXTURE_ID,
            as_of_timestamp=NOW,
            input_repository=repository,
            output_repository=SimpleNamespace(replace_in_play_predictions=lambda fixture_id, rows, **kwargs: None),
        )
        self.assertTrue(outcome.published)
