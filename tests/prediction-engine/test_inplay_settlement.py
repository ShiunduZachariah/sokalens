"""Settlement of in-play markets (architecture v2 7.4).

Before this, all five published in-play market keys raised
`ValueError: unsupported settlement market` from `_settlement_outcome`. Because
`settle_fixture` rolls back and re-raises, and `settle-scope` had no per-fixture
guard, a single in-play row on a finished fixture aborted the whole settlement
batch and stranded the pre-match predictions queued behind it.

These tests cover both halves of the fix: the arms themselves, and the anchor
that the two publication-relative markets need.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
import unittest

from prediction_engine.db_repository import (
    AccuracyRecountStats,
    PredictionRetentionRepository,
    PredictionSettlementRepository,
)
from prediction_engine.persistence import (
    IN_PLAY_LIFECYCLE_STAGES,
    ConfidenceTier,
    EnginePrediction,
    PredictionLifecycleStage,
)
from prediction_engine.settlement import (
    REVISION_EVENT_RETRACTED,
    REVISION_RESCORED,
    STATUS_REVERSED,
    ActualFixtureResult,
    InPlayAnchor,
    resettle_prediction,
    settle_prediction,
)
from test_db_repository import FakeConnection, FakeCursor

IN_PLAY_MARKETS = (
    "match_result_in_play",
    "next_goal",
    "total_goals_remaining",
    "total_goals_match",
    "btts_in_play",
)


KICKOFF = datetime(2026, 9, 29, 15, 0, tzinfo=UTC)


def at(minute: int) -> datetime:
    """Wall-clock instant `minute` minutes after kickoff."""
    return KICKOFF + timedelta(minutes=minute)


class InPlayMarketSettlementTests(unittest.TestCase):
    def test_no_in_play_market_raises(self) -> None:
        # The regression that matters. Every published in-play key must reach a
        # terminal state rather than raising, because a raise here discards the
        # whole fixture.
        actual = ActualFixtureResult(home_goals=3, away_goals=1, home_goals_ht=1, away_goals_ht=1)
        anchor = InPlayAnchor(home_goals=1, away_goals=1, next_goal_team="home")
        for market in IN_PLAY_MARKETS:
            with self.subTest(market=market):
                prediction = "over_1.5" if market.startswith("total_goals") else "home"
                result = settle_prediction(market, prediction, actual, anchor)
                self.assertIn(result.status, {"settled", "void", "unsettled_data_unavailable"})

    def test_full_time_equivalent_markets_grade_on_the_final_result(self) -> None:
        # A 1X2 called at 67 minutes is still a claim about the full-time
        # result, so it settles exactly as the pre-match market does.
        actual = ActualFixtureResult(home_goals=2, away_goals=0)
        self.assertTrue(settle_prediction("match_result_in_play", "home", actual).is_correct)
        self.assertFalse(settle_prediction("match_result_in_play", "draw", actual).is_correct)
        self.assertFalse(settle_prediction("btts_in_play", "yes", actual).is_correct)
        self.assertTrue(settle_prediction("btts_in_play", "no", actual).is_correct)
        self.assertTrue(settle_prediction("total_goals_match", "over_1.5", actual).is_correct)
        self.assertFalse(settle_prediction("total_goals_match", "over_2.5", actual).is_correct)

    def test_arbitrary_lines_settle_without_a_catalogue(self) -> None:
        # The dynamic ladder depends on this: the line is parsed off the
        # prediction string, so a ten-goal match settles over_9.5 with no schema
        # change and no registered slot.
        actual = ActualFixtureResult(home_goals=7, away_goals=3)
        self.assertTrue(settle_prediction("total_goals_match", "over_9.5", actual).is_correct)
        self.assertFalse(settle_prediction("total_goals_match", "over_10.5", actual).is_correct)
        self.assertEqual(settle_prediction("total_goals_match", "over_9.5", actual).actual, "over_9.5")

    def test_remaining_goals_count_only_goals_after_the_cutoff(self) -> None:
        # Anchored at 1-1 with a 3-1 final: two goals remained.
        actual = ActualFixtureResult(home_goals=3, away_goals=1)
        anchor = InPlayAnchor(home_goals=1, away_goals=1)
        self.assertTrue(settle_prediction("total_goals_remaining", "over_1.5", actual, anchor).is_correct)
        self.assertFalse(settle_prediction("total_goals_remaining", "over_2.5", actual, anchor).is_correct)

    def test_remaining_goals_clamp_at_zero_when_a_goal_was_retracted(self) -> None:
        # A withdrawn goal can leave the anchor above the final score. Negative
        # remaining goals is not a number this market can be graded on.
        actual = ActualFixtureResult(home_goals=1, away_goals=0)
        anchor = InPlayAnchor(home_goals=2, away_goals=0)
        result = settle_prediction("total_goals_remaining", "under_0.5", actual, anchor)
        self.assertTrue(result.is_correct)
        self.assertEqual(result.actual, "under_0.5")

    def test_next_goal_reads_the_side_that_scored_after_publication(self) -> None:
        actual = ActualFixtureResult(home_goals=2, away_goals=1)
        self.assertTrue(
            settle_prediction(
                "next_goal", "away", actual, InPlayAnchor(home_goals=1, away_goals=0, next_goal_team="away")
            ).is_correct
        )
        # No further goal is a real outcome for this market, not missing data.
        settled = settle_prediction(
            "next_goal", "none", actual, InPlayAnchor(home_goals=2, away_goals=1, next_goal_team=None)
        )
        self.assertTrue(settled.is_correct)
        self.assertEqual(settled.actual, "none")

    def test_anchored_markets_close_rather_than_guess_without_an_anchor(self) -> None:
        # Full time closes everything. A market that cannot be graded is closed
        # as unsettled_data_unavailable -- excluded from accuracy, never pending.
        actual = ActualFixtureResult(home_goals=2, away_goals=1)
        for market, prediction in (("next_goal", "home"), ("total_goals_remaining", "over_0.5")):
            with self.subTest(market=market):
                result = settle_prediction(market, prediction, actual, None)
                self.assertEqual(result.status, "unsettled_data_unavailable")
                self.assertFalse(result.settled)
                self.assertIsNone(result.is_correct)

    def test_markets_without_provider_data_close_at_full_time(self) -> None:
        # The operator decision: publish corners and fouls regardless of league
        # coverage, and close them honestly when the statistic never arrived.
        actual = ActualFixtureResult(home_goals=1, away_goals=1, home_corners=None, away_corners=None)
        result = settle_prediction("corners_total", "over_8.5", actual)
        self.assertEqual(result.status, "unsettled_data_unavailable")
        self.assertIsNone(result.is_correct)

    def test_an_unknown_market_still_raises(self) -> None:
        # The raise was never the defect; the blast radius was. An unknown
        # market must not be silently guessed at.
        with self.assertRaises(ValueError):
            settle_prediction("not_a_market", "home", ActualFixtureResult(home_goals=0, away_goals=0))


class AnchorFromTimelineTests(unittest.TestCase):
    """`_anchor_at` over a loaded goal timeline."""

    # (observed_at, home_so_far, away_so_far, side, identity_key)
    TIMELINE = [
        (at(10), 1, 0, "home", "g1"),
        (at(30), 1, 1, "away", "g2"),
        (at(70), 2, 1, "home", "g3"),
    ]

    def anchor(self, cutoff: datetime | None) -> InPlayAnchor | None:  # noqa: D102
        return PredictionSettlementRepository._anchor_at(self.TIMELINE, cutoff)

    def test_score_at_cutoff_and_the_next_goal(self) -> None:
        early = self.anchor(at(20))
        self.assertEqual((early.home_goals, early.away_goals), (1, 0))
        self.assertEqual(early.next_goal_team, "away")

        late = self.anchor(at(50))
        self.assertEqual((late.home_goals, late.away_goals), (1, 1))
        self.assertEqual(late.next_goal_team, "home")

    def test_before_kickoff_anchors_at_nil_nil(self) -> None:
        anchor = self.anchor(at(5))
        self.assertEqual((anchor.home_goals, anchor.away_goals), (0, 0))
        self.assertEqual(anchor.next_goal_team, "home")

    def test_after_the_last_goal_there_is_no_next_goal(self) -> None:
        anchor = self.anchor(at(80))
        self.assertEqual((anchor.home_goals, anchor.away_goals), (2, 1))
        self.assertIsNone(anchor.next_goal_team)

    def test_a_goal_observed_exactly_at_the_cutoff_is_known(self) -> None:
        # The cutoff is what the prediction knew, inclusive: a goal observed at
        # the same instant as the snapshot it was built from was in that
        # snapshot.
        anchor = self.anchor(at(30))
        self.assertEqual((anchor.home_goals, anchor.away_goals), (1, 1))
        self.assertEqual(anchor.next_goal_team, "home")

    def test_no_cutoff_yields_no_anchor(self) -> None:
        self.assertIsNone(self.anchor(None))

    def test_deciding_keys_are_every_goal_after_the_cutoff(self) -> None:
        # total_goals_remaining depends on all of them; next_goal only on the
        # first. Recording the union keeps one anchor usable for both, and a
        # retraction of any of them is a reason to re-grade.
        self.assertEqual(self.anchor(at(20)).deciding_event_keys, ("g2", "g3"))
        self.assertEqual(self.anchor(at(50)).deciding_event_keys, ("g3",))
        self.assertEqual(self.anchor(at(80)).deciding_event_keys, ())
        self.assertEqual(self.anchor(at(5)).deciding_event_keys, ("g1", "g2", "g3"))


class GoalTimelineLoadTests(unittest.TestCase):
    """`_load_inplay_anchors` and the guard that stops it trusting a bad feed."""

    FIXTURE_ROW = {"home_team_id": 10, "away_team_id": 20}

    def load(
        self,
        goals: list[dict[str, object]],
        actual: ActualFixtureResult,
        fixture_row: dict[str, object] | None = None,
    ) -> list[tuple[datetime, int, int, str | None]] | None:
        connection = FakeConnection(results=[FakeCursor(many=goals)])
        repository = PredictionSettlementRepository(connection)
        self.connection = connection
        return repository._load_inplay_anchors(42, fixture_row or self.FIXTURE_ROW, actual)

    def test_a_complete_timeline_loads_with_running_scores(self) -> None:
        timeline = self.load(
            [
                {"observed_at": at(10), "team_id": 10, "identity_key": "g1"},
                {"observed_at": at(30), "team_id": 20, "identity_key": "g2"},
                {"observed_at": at(70), "team_id": 10, "identity_key": "g3"},
            ],
            ActualFixtureResult(home_goals=2, away_goals=1),
        )
        self.assertEqual(
            timeline,
            [
                (at(10), 1, 0, "home", "g1"),
                (at(30), 1, 1, "away", "g2"),
                (at(70), 2, 1, "home", "g3"),
            ],
        )

    def test_the_query_excludes_retracted_goals_and_missed_penalties(self) -> None:
        # Provider truth: a goal the provider withdrew never happened, and
        # grading against it would score a prediction on an un-said event.
        self.load([], ActualFixtureResult(home_goals=0, away_goals=0))
        sql = self.connection.executed[0][0]
        self.assertIn("is_retracted = false", sql)
        self.assertIn("missed penalty", sql)
        self.assertIn("ORDER BY observed_at ASC", sql)

    def test_an_incomplete_timeline_is_refused(self) -> None:
        # Two goals recorded against a 3-1 final. An incomplete timeline is
        # indistinguishable from a goalless spell, and anchoring on it would
        # grade remaining-goals against goals we simply failed to record.
        self.assertIsNone(
            self.load(
                [
                    {"observed_at": at(10), "team_id": 10, "identity_key": "g10"},
                    {"observed_at": at(30), "team_id": 20, "identity_key": "g30"},
                ],
                ActualFixtureResult(home_goals=3, away_goals=1),
            )
        )

    def test_a_timeline_with_the_right_total_but_wrong_split_is_refused(self) -> None:
        # Three goals for a 3-1 final, but all attributed to the home side. The
        # guard is per team, not on the total, exactly as
        # ReconcileScoringEvents applies it.
        self.assertIsNone(
            self.load(
                [
                    {"observed_at": at(10), "team_id": 10, "identity_key": "g10"},
                    {"observed_at": at(30), "team_id": 10, "identity_key": "g30"},
                    {"observed_at": at(70), "team_id": 10, "identity_key": "g70"},
                ],
                ActualFixtureResult(home_goals=3, away_goals=1),
            )
        )

    def test_an_unattributable_goal_is_refused(self) -> None:
        self.assertIsNone(
            self.load(
                [{"observed_at": at(10), "team_id": None, "identity_key": "g10"}],
                ActualFixtureResult(home_goals=1, away_goals=0),
            )
        )

    def test_a_goal_for_neither_side_is_refused(self) -> None:
        self.assertIsNone(
            self.load(
                [{"observed_at": at(10), "team_id": 99, "identity_key": "g10"}],
                ActualFixtureResult(home_goals=1, away_goals=0),
            )
        )

    def test_a_fixture_with_no_team_ids_is_refused_without_querying(self) -> None:
        connection = FakeConnection(results=[FakeCursor(many=[])])
        repository = PredictionSettlementRepository(connection)
        self.assertIsNone(
            repository._load_inplay_anchors(
                42, {"home_team_id": None, "away_team_id": 20}, ActualFixtureResult(home_goals=0, away_goals=0)
            )
        )
        self.assertEqual(connection.executed, [])

    def test_a_goalless_match_loads_an_empty_timeline(self) -> None:
        self.assertEqual(self.load([], ActualFixtureResult(home_goals=0, away_goals=0)), [])


class ResettlementTests(unittest.TestCase):
    """Reversible settlement: the provider withdrew the evidence."""

    def test_an_untouched_market_writes_nothing(self) -> None:
        # Re-settlement runs for every prediction on a fixture when any goal is
        # retracted. Most are unaffected, and writing a revision for each would
        # bury the ones that matter.
        recomputed = settle_prediction(
            "total_goals_match", "over_1.5", ActualFixtureResult(home_goals=2, away_goals=1)
        )
        outcome = resettle_prediction(
            previous_status="settled",
            previous_actual="over_1.5",
            previous_is_correct=True,
            recomputed=recomputed,
            triggered_by_retraction=True,
        )
        self.assertFalse(outcome.changed)
        self.assertIsNone(outcome.revision_reason)

    def test_a_withdrawn_goal_that_flips_the_answer_is_rescored(self) -> None:
        # 2-1 became 1-1, so the total crossed the line and over_2.5 goes from
        # won to lost. The market still settles; it settles differently.
        recomputed = settle_prediction(
            "total_goals_match", "over_2.5", ActualFixtureResult(home_goals=1, away_goals=1)
        )
        outcome = resettle_prediction(
            previous_status="settled",
            previous_actual="over_2.5",
            previous_is_correct=True,
            recomputed=recomputed,
            triggered_by_retraction=True,
        )
        self.assertTrue(outcome.changed)
        self.assertEqual(outcome.revision_reason, REVISION_RESCORED)
        self.assertEqual(outcome.result.status, "settled")
        self.assertFalse(outcome.result.is_correct)

    def test_a_market_that_can_no_longer_be_graded_is_reversed(self) -> None:
        # The anchor is gone, so next_goal cannot be graded. A user saw "won";
        # that is no longer true and the correction has to be shown, not hidden
        # by the market quietly going blank.
        recomputed = settle_prediction(
            "next_goal", "home", ActualFixtureResult(home_goals=1, away_goals=1), None
        )
        outcome = resettle_prediction(
            previous_status="settled",
            previous_actual="home",
            previous_is_correct=True,
            recomputed=recomputed,
            triggered_by_retraction=True,
        )
        self.assertTrue(outcome.changed)
        self.assertEqual(outcome.revision_reason, REVISION_EVENT_RETRACTED)
        self.assertEqual(outcome.result.status, STATUS_REVERSED)
        self.assertIsNone(outcome.result.actual)
        self.assertIsNone(outcome.result.is_correct)
        self.assertFalse(outcome.result.settled)

    def test_a_row_that_becomes_gradable_is_rescored_not_reversed(self) -> None:
        # The retraction made the timeline consistent again. That is an
        # improvement, and calling it a reversal would tell the user we withdrew
        # something when we in fact supplied it.
        recomputed = settle_prediction(
            "total_goals_match", "over_1.5", ActualFixtureResult(home_goals=2, away_goals=1)
        )
        outcome = resettle_prediction(
            previous_status="unsettled_data_unavailable",
            previous_actual=None,
            previous_is_correct=None,
            recomputed=recomputed,
            triggered_by_retraction=True,
        )
        self.assertTrue(outcome.changed)
        self.assertEqual(outcome.revision_reason, REVISION_RESCORED)
        self.assertEqual(outcome.result.status, "settled")

    def test_a_reversal_outside_a_retraction_is_recorded_as_rescored(self) -> None:
        # Reason tracks cause. A grade lost for some reason other than a
        # withdrawn event must not claim the provider retracted anything.
        recomputed = settle_prediction(
            "next_goal", "home", ActualFixtureResult(home_goals=1, away_goals=1), None
        )
        outcome = resettle_prediction(
            previous_status="settled",
            previous_actual="home",
            previous_is_correct=True,
            recomputed=recomputed,
            triggered_by_retraction=False,
        )
        self.assertEqual(outcome.revision_reason, REVISION_RESCORED)
        self.assertEqual(outcome.result.status, STATUS_REVERSED)


class ResettleFixtureWriteTests(unittest.TestCase):
    """The reversal write path: detect, re-grade, record, mark for recount."""

    def fixture_row(self) -> dict[str, object]:
        return {
            "fixture_id": 42,
            "home_team_id": 10,
            "away_team_id": 20,
            "home_goals_ft": 1,
            "away_goals_ft": 1,
            "home_goals_ht": 0,
            "away_goals_ht": 0,
            "status": "ft",
            "league_id": 7,
            "first_goal_team": None,
        }

    def prediction_row(self, market: str, prediction: str, previous_actual: str, stage: str = "IN_PLAY_2H"):
        return {
            "id": "uuid-1",
            "fixture_id": 42,
            "market": market,
            "prediction": prediction,
            "probability": 0.61,
            "confidence": 0.7,
            "confidence_tier": "medium",
            "lifecycle_stage": stage,
            "model_version": "inplay-analytic-v1",
            "model_registry_id": "model-1",
            "generated_at": at(20),
            "data_cutoff_at": at(20),
            "feature_version": "v1",
            "raw_probability": 0.6,
            "calibrated_probability": 0.61,
            "odds_snapshot_id": None,
            "league_id": 7,
            "settlement_status": "settled",
            "previous_actual": previous_actual,
            "previous_is_correct": True,
        }

    def run_resettle(self, prediction_row, goals):
        connection = FakeConnection(
            results=[
                FakeCursor(one=self.fixture_row()),   # _SETTLEMENT_FIXTURE_SQL
                FakeCursor(many=[]),                  # statistics totals
                FakeCursor(many=[prediction_row]),    # settled predictions
                FakeCursor(many=goals),               # goal timeline
                FakeCursor(one={"identity_key": "retracted-goal"}),  # newest retraction
            ]
        )
        repository = PredictionSettlementRepository(connection)
        outcomes = repository.resettle_fixture(42, recorded_at=at(120))
        return connection, outcomes

    def test_a_reversal_is_recorded_and_marked_for_recount(self) -> None:
        # The retraction left the surviving timeline inconsistent with the stored
        # full-time score -- one goal against a 1-1 result -- so the timeline is
        # refused, the anchor is unavailable, and next_goal can no longer be
        # graded at all. A user saw "won"; that is the case the reversal exists
        # to make visible.
        goals = [{"observed_at": at(10), "team_id": 10, "identity_key": "g1"}]
        connection, outcomes = self.run_resettle(
            self.prediction_row("next_goal", "home", "home"), goals
        )
        self.assertEqual(len(outcomes), 1)
        self.assertTrue(outcomes[0].changed)
        self.assertEqual(outcomes[0].result.status, STATUS_REVERSED)

        statements = [sql for sql, _ in connection.executed]
        self.assertTrue(any("INSERT INTO prediction_results" in sql for sql in statements))
        self.assertTrue(any("INSERT INTO prediction_result_revisions" in sql for sql in statements))
        self.assertTrue(any("needs_recount = true" in sql for sql in statements))
        self.assertTrue(connection.committed)

        revision = next(params for sql, params in connection.executed if "INSERT INTO prediction_result_revisions" in sql)
        self.assertEqual(revision["revision_reason"], REVISION_EVENT_RETRACTED)
        self.assertEqual(revision["settlement_status"], STATUS_REVERSED)
        # The audit trail names the withdrawn event.
        self.assertEqual(revision["triggered_by_event_key"], "retracted-goal")

    def test_a_flipped_grade_is_rescored_not_reversed(self) -> None:
        # Both surviving goals now precede the cutoff, so nobody scored "next".
        # "none" is a real outcome, so the market still settles -- it settles
        # against the prediction instead of for it. Calling that a reversal would
        # tell the user we withdrew a grade when we in fact changed it.
        goals = [
            {"observed_at": at(10), "team_id": 10, "identity_key": "g1"},
            {"observed_at": at(15), "team_id": 20, "identity_key": "g2"},
        ]
        connection, outcomes = self.run_resettle(
            self.prediction_row("next_goal", "home", "home"), goals
        )
        self.assertTrue(outcomes[0].changed)
        self.assertEqual(outcomes[0].result.status, "settled")
        self.assertEqual(outcomes[0].result.actual, "none")
        self.assertFalse(outcomes[0].result.is_correct)
        revision = next(params for sql, params in connection.executed if "INSERT INTO prediction_result_revisions" in sql)
        self.assertEqual(revision["revision_reason"], REVISION_RESCORED)
        self.assertTrue(any("needs_recount = true" in sql for sql, _ in connection.executed))

    def test_an_unaffected_prediction_writes_nothing(self) -> None:
        # Graded on the full-time score, which the retraction did not change:
        # 1-1 is still under 2.5. Nothing is written, and nothing is marked for
        # recount -- a revision here would bury the reversals that matter.
        goals = [
            {"observed_at": at(10), "team_id": 10, "identity_key": "g1"},
            {"observed_at": at(15), "team_id": 20, "identity_key": "g2"},
        ]
        connection, outcomes = self.run_resettle(
            self.prediction_row("total_goals_match", "under_2.5", "under_2.5", stage="PRE_MATCH"),
            goals,
        )
        self.assertFalse(outcomes[0].changed)
        statements = [sql for sql, _ in connection.executed]
        self.assertFalse(any("INSERT INTO prediction_results" in sql for sql in statements))
        self.assertFalse(any("needs_recount" in sql for sql in statements))
        self.assertTrue(connection.committed)

    def test_the_regrade_reads_previous_state_from_the_revision_when_pruned(self) -> None:
        # engine_predictions is never deleted, so after the prune the prediction
        # is still there while its settlement row is not. Previous state then
        # comes from the newest revision.
        connection = FakeConnection(
            results=[
                FakeCursor(one=self.fixture_row()),
                FakeCursor(many=[]),
                FakeCursor(many=[]),
            ]
        )
        PredictionSettlementRepository(connection).resettle_fixture(42, recorded_at=at(120))
        sql = connection.executed[2][0]
        self.assertIn("FROM engine_predictions AS ep", sql)
        self.assertIn("LEFT JOIN prediction_results", sql)
        self.assertIn("COALESCE(pr.settlement_status, newest.settlement_status)", sql)
        # Never-graded predictions stay out: re-settling one would be settling it,
        # bypassing the ordinary settlement path.
        self.assertIn("pr.prediction_id IS NOT NULL OR newest.settlement_status IS NOT NULL", sql)

    def test_a_failure_rolls_back(self) -> None:
        # A partial re-settlement must not be committed: half a fixture re-graded
        # is worse than none, because the untouched half still claims a grade
        # computed against evidence that has changed.
        connection = FakeConnection(fail_on_call=1)
        with self.assertRaises(RuntimeError):
            PredictionSettlementRepository(connection).resettle_fixture(42, recorded_at=at(120))
        self.assertTrue(connection.rolled_back)
        self.assertFalse(connection.committed)


class ProbabilityBandTests(unittest.TestCase):
    """The 0.01-0.99 band is a pre-match invariant, not a universal one.

    A flat band silently rejected the calls the in-play model is most confident
    about -- a 1X2 on a three-goal lead at 85 minutes both exceeds 0.99 and is
    the clearest read of the match. `engine_predictions.probability` carried the
    same CHECK, so those rows could not even be written.
    """

    def prediction(self, *, probability: float, stage: PredictionLifecycleStage) -> EnginePrediction:
        in_play = stage in IN_PLAY_LIFECYCLE_STAGES
        return EnginePrediction(
            fixture_id=42,
            market="match_result_in_play" if in_play else "1x2",
            prediction="home",
            probability=probability,
            confidence=0.9,
            confidence_tier=ConfidenceTier.STRONG,
            lifecycle_stage=stage,
            data_completeness=1.0,
            gate_tier="FULL",
            model_version="inplay-analytic-v1" if in_play else "v1",
            is_best_bet=False,
            features_used={},
            explanation="",
            horizon_eligible=True,
            embargo_active=False,
            generated_at=at(80),
            data_cutoff_at=at(80) if in_play else None,
            match_minute=85 if in_play else None,
            period="2H" if in_play else None,
            fixture_live_snapshot_id=1 if in_play else None,
        )

    def test_an_in_play_probability_above_the_pre_match_ceiling_is_valid(self) -> None:
        self.prediction(probability=0.995, stage=PredictionLifecycleStage.IN_PLAY_2H).validate()
        self.prediction(probability=0.002, stage=PredictionLifecycleStage.IN_PLAY_1H).validate()

    def test_in_play_certainty_is_still_refused(self) -> None:
        # publishable_markets drops collapsed selections, so a published in-play
        # probability is never exactly 0 or 1 and a row claiming otherwise is a bug.
        for probability in (0.0, 1.0):
            with self.subTest(probability=probability):
                with self.assertRaises(ValueError):
                    self.prediction(
                        probability=probability, stage=PredictionLifecycleStage.IN_PLAY_2H
                    ).validate()

    def test_the_pre_match_band_is_unchanged(self) -> None:
        self.prediction(probability=0.99, stage=PredictionLifecycleStage.PRE_MATCH).validate()
        with self.assertRaises(ValueError):
            self.prediction(probability=0.995, stage=PredictionLifecycleStage.PRE_MATCH).validate()
        with self.assertRaises(ValueError):
            self.prediction(probability=0.005, stage=PredictionLifecycleStage.PRE_MATCH).validate()


class RetractedFixtureDetectionTests(unittest.TestCase):
    def test_the_trigger_only_fires_for_retractions_newer_than_the_settlement(self) -> None:
        # Otherwise every fixture with any historical VAR decision would be
        # re-graded on every sweep.
        connection = FakeConnection(results=[FakeCursor(many=[{"fixture_id": 42}, {"fixture_id": 43}])])
        ids = PredictionSettlementRepository(connection).list_retracted_settlement_fixture_ids(limit=10)
        self.assertEqual(ids, [42, 43])
        sql = connection.executed[0][0]
        self.assertIn("is_retracted = true", sql)
        self.assertIn("lower(event_type) = 'goal'", sql)
        self.assertIn("r.retracted_at > pr.settled_at", sql)

    def test_detection_survives_the_raw_retention_prune(self) -> None:
        # prediction_results is deleted two days after settlement, so a reversal
        # arriving later has no settlement row. The revision history outlives the
        # prune and is the second arm of the trigger -- without it a late VAR
        # decision is never noticed, which fails the rule outright.
        connection = FakeConnection(results=[FakeCursor(many=[])])
        PredictionSettlementRepository(connection).list_retracted_settlement_fixture_ids(limit=10)
        sql = connection.executed[0][0]
        self.assertIn("prediction_result_revisions", sql)
        self.assertIn("r.retracted_at > rev.recorded_at", sql)
        # Both arms, unioned, so a fixture found twice is reported once.
        self.assertIn("from_results", sql)
        self.assertIn("from_revisions", sql)
        self.assertIn("UNION", sql)

    def test_a_non_positive_limit_is_refused(self) -> None:
        connection = FakeConnection()
        with self.assertRaises(ValueError):
            PredictionSettlementRepository(connection).list_retracted_settlement_fixture_ids(limit=0)


class AccuracyRecountTests(unittest.TestCase):
    """`needs_recount` applied: a withdrawn result stops counting as correct.

    The published accuracy figure is the half of reversible settlement that is
    easiest to forget. `prediction_results` is pruned two days after settlement,
    so a late reversal has no raw row to read -- the bucket the rollup recorded on
    the ledger is what makes the adjustment possible at all.
    """

    def recount(self, row: dict[str, object] | None):
        connection = FakeConnection(results=[FakeCursor(one=row)])
        stats = PredictionRetentionRepository(connection).recount_reversed_results(limit=100)
        return connection, stats

    def summary(self, **overrides: int) -> dict[str, object]:
        base = {
            "flagged_examined": 3,
            "predictions_recounted": 2,
            "buckets_adjusted": 1,
            "flags_cleared": 3,
            "unrecountable": 0,
        }
        base.update(overrides)
        return base

    def test_the_summary_is_reported_and_committed(self) -> None:
        connection, stats = self.recount(self.summary())
        self.assertEqual(stats.flagged_examined, 3)
        self.assertEqual(stats.predictions_recounted, 2)
        self.assertEqual(stats.buckets_adjusted, 1)
        self.assertEqual(stats.flags_cleared, 3)
        self.assertEqual(stats.unrecountable, 0)
        self.assertTrue(connection.committed)

    def test_the_query_reads_the_revision_history_not_the_pruned_raw_row(self) -> None:
        connection, _ = self.recount(self.summary())
        sql = connection.executed[0][0]
        self.assertIn("prediction_result_revisions", sql)
        self.assertIn("needs_recount", sql)
        # total_predictions is never touched: the prediction still happened and was
        # still counted, only its classification moved. The aggregate's CHECK
        # requires the counters to keep summing to it.
        self.assertNotIn("total_predictions =", sql)
        # Concurrent-safe: two sweeps must not both adjust the same bucket.
        self.assertIn("FOR UPDATE SKIP LOCKED", sql)

    def test_a_reversal_is_counted_as_pending_not_voided(self) -> None:
        # The grade was withdrawn, not proven ungradable. Counting it as voided
        # would claim we know it can never be graded.
        connection, _ = self.recount(self.summary())
        sql = connection.executed[0][0]
        reversed_branch = sql[sql.index("new_counted_as") - 900 : sql.index("AS new_counted_as")]
        self.assertIn("ELSE 'pending'", reversed_branch)

    def test_unrecountable_rows_are_reported_rather_than_guessed(self) -> None:
        # Rows written before the ledger recorded a bucket cannot be placed.
        # Guessing one would corrupt a real bucket, so they stay flagged and are
        # surfaced as a debt.
        _, stats = self.recount(self.summary(unrecountable=4))
        self.assertEqual(stats.unrecountable, 4)

    def test_a_missing_summary_is_an_error_not_a_silent_zero(self) -> None:
        connection = FakeConnection(results=[FakeCursor(one=None)])
        with self.assertRaises(RuntimeError):
            PredictionRetentionRepository(connection).recount_reversed_results(limit=10)
        self.assertTrue(connection.rolled_back)

    def test_a_non_positive_limit_is_refused(self) -> None:
        with self.assertRaises(ValueError):
            PredictionRetentionRepository(FakeConnection()).recount_reversed_results(limit=0)

    def test_stats_add_so_batches_can_be_accumulated(self) -> None:
        total = AccuracyRecountStats(flagged_examined=2, predictions_recounted=1) + AccuracyRecountStats(
            flagged_examined=3, predictions_recounted=2, unrecountable=1
        )
        self.assertEqual(total.flagged_examined, 5)
        self.assertEqual(total.predictions_recounted, 3)
        self.assertEqual(total.unrecountable, 1)


if __name__ == "__main__":
    unittest.main()
