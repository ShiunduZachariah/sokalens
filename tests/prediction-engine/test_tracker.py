"""Server-side tracker state (client contract §8).

`on_track` means "would win if the match ended now" -- a report on the pick
already made, not a second, weaker prediction. The tracker grades through
`settle_prediction` so it cannot disagree with settlement: a pick that tracked
green all match and settled lost would be the defect that shape prevents.
"""

from __future__ import annotations

from datetime import UTC, datetime
import unittest

from prediction_engine.tracker import MatchState, TrackerState, tracker_state


def track(market: str, prediction: str, state: MatchState, **kwargs) -> TrackerState:
    return tracker_state(market=market, prediction=prediction, state=state, **kwargs)


class EarlySettlementTests(unittest.TestCase):
    """A decided pick is terminal, not "on track"."""

    def test_full_time_over_one_and_a_half_is_won_at_minute_ten(self) -> None:
        # The case this whole workstream started from. Two goals means over 1.5
        # is DECIDED; nothing in the remaining eighty minutes can take it away,
        # so calling it "on track" would understate it.
        self.assertEqual(track("total_goals", "over_1.5", MatchState(2, 0)), TrackerState.WON)

    def test_an_under_pick_is_lost_the_moment_the_line_is_passed(self) -> None:
        # A count rises and never falls, so the under side is permanently gone.
        self.assertEqual(track("total_goals", "under_2.5", MatchState(2, 1)), TrackerState.LOST)

    def test_a_line_not_yet_passed_is_still_live(self) -> None:
        self.assertEqual(track("total_goals", "over_2.5", MatchState(2, 0)), TrackerState.OFF_TRACK)
        self.assertEqual(track("total_goals", "under_2.5", MatchState(1, 0)), TrackerState.ON_TRACK)

    def test_a_count_exactly_at_a_whole_line_has_not_passed_it(self) -> None:
        # Over 2 at two goals is not yet over. Only a strictly greater count
        # decides it, and a client showing "won" here would be wrong.
        self.assertEqual(track("total_goals", "over_2", MatchState(1, 1)), TrackerState.OFF_TRACK)

    def test_btts_decides_both_sides_at_once(self) -> None:
        # Once each team has scored the fact cannot be undone, so "yes" is won
        # AND "no" is lost. Treating only "yes" as decided would leave a "no"
        # pick reading merely behind in a match it can never recover from.
        self.assertEqual(track("btts", "yes", MatchState(1, 1)), TrackerState.WON)
        self.assertEqual(track("btts", "no", MatchState(1, 1)), TrackerState.LOST)

    def test_btts_before_both_have_scored_is_live(self) -> None:
        self.assertEqual(track("btts", "yes", MatchState(1, 0)), TrackerState.OFF_TRACK)
        self.assertEqual(track("btts", "no", MatchState(1, 0)), TrackerState.ON_TRACK)


class LiveJudgementTests(unittest.TestCase):
    def test_a_lead_is_on_track_not_won(self) -> None:
        # A 1X2 lead can be equalised, so it is never decided before the whistle.
        self.assertEqual(track("1x2", "home", MatchState(1, 0)), TrackerState.ON_TRACK)

    def test_the_whistle_turns_a_lead_into_a_win(self) -> None:
        self.assertEqual(
            track("1x2", "home", MatchState(1, 0, finished=True)), TrackerState.WON
        )

    def test_a_goalless_draw_is_off_track_for_a_home_pick(self) -> None:
        # "Would win if it ended now" is the definition, and a draw would not.
        # Neutral would be a kinder reading and a less honest one.
        self.assertEqual(track("1x2", "home", MatchState(0, 0)), TrackerState.OFF_TRACK)

    def test_a_losing_pick_at_full_time_is_lost(self) -> None:
        self.assertEqual(
            track("1x2", "home", MatchState(0, 2, finished=True)), TrackerState.LOST
        )


class LadderTrackingTests(unittest.TestCase):
    """The in-play ladder carries its line in the MARKET key, not the prediction."""

    def test_a_ladder_pick_tracks_against_the_line_in_its_market_key(self) -> None:
        self.assertEqual(
            track("total_goals_match_9.5", "over", MatchState(9, 0)), TrackerState.OFF_TRACK
        )
        self.assertEqual(
            track("total_goals_match_9.5", "over", MatchState(10, 0)), TrackerState.WON
        )
        self.assertEqual(
            track("total_goals_match_9.5", "under", MatchState(10, 0)), TrackerState.LOST
        )

    def test_a_remaining_goals_pick_cannot_be_tracked_without_its_anchor(self) -> None:
        # It is relative to when it was published, and the tracker holds no
        # anchor. Guessing one would grade against goals the pick never claimed.
        self.assertEqual(
            track("total_goals_remaining_1.5", "over", MatchState(3, 0)), TrackerState.UNKNOWN
        )


class SettlementOverrideTests(unittest.TestCase):
    def test_a_real_settlement_wins_over_the_live_feed(self) -> None:
        # Once graded for real the tracker reports that and does not second-guess
        # it from a feed that may have moved on.
        self.assertEqual(
            track("1x2", "home", MatchState(0, 3), settlement_status="settled", settled_is_correct=True),
            TrackerState.WON,
        )
        self.assertEqual(
            track("1x2", "home", MatchState(3, 0), settlement_status="settled", settled_is_correct=False),
            TrackerState.LOST,
        )

    def test_a_void_settlement_is_void(self) -> None:
        for status in ("void", "unsettled_data_unavailable"):
            with self.subTest(status=status):
                self.assertEqual(
                    track("1x2", "home", MatchState(1, 0), settlement_status=status),
                    TrackerState.VOID,
                )

    def test_a_reversal_reads_unknown_rather_than_a_grade(self) -> None:
        # The evidence was withdrawn and the answer is not back yet. Showing a
        # colour would claim more than we know.
        self.assertEqual(
            track("1x2", "home", MatchState(1, 0), settlement_status="settlement_reversed"),
            TrackerState.UNKNOWN,
        )


class UnknownStateTests(unittest.TestCase):
    def test_a_market_needing_absent_statistics_is_unknown(self) -> None:
        # A corners pick before any corner data arrives is not a judgement.
        self.assertEqual(
            track("corners_total", "over_8.5", MatchState(1, 0)), TrackerState.UNKNOWN
        )

    def test_a_market_with_no_settlement_arm_is_unknown_not_guessed(self) -> None:
        self.assertEqual(track("not_a_market", "home", MatchState(1, 0)), TrackerState.UNKNOWN)

    def test_statistics_markets_track_once_the_data_is_there(self) -> None:
        state = MatchState(1, 0, home_corners=6, away_corners=4)
        self.assertEqual(track("corners_total", "over_8.5", state), TrackerState.WON)
        self.assertEqual(track("corners_total", "under_8.5", state), TrackerState.LOST)

    def test_every_state_emitted_is_in_the_contract(self) -> None:
        # A client renders exactly these. A sixth value would be an unhandled
        # case in two apps at once.
        self.assertEqual(
            {state.value for state in TrackerState},
            {"on_track", "off_track", "won", "lost", "void", "unknown"},
        )


class AgreementWithSettlementTests(unittest.TestCase):
    """The tracker must never contradict the grade the same state settles to."""

    def test_a_finished_match_tracks_exactly_as_it_settles(self) -> None:
        from prediction_engine.settlement import settle_prediction

        state = MatchState(2, 1, home_goals_ht=1, away_goals_ht=1, finished=True)
        for market, prediction in (
            ("1x2", "home"),
            ("1x2", "away"),
            ("btts", "yes"),
            ("btts", "no"),
            ("total_goals", "over_2.5"),
            ("total_goals", "under_2.5"),
            ("half_time_result", "draw"),
            ("odd_even_total_goals", "odd"),
        ):
            with self.subTest(market=market, prediction=prediction):
                settled = settle_prediction(market, prediction, state.as_result())
                expected = TrackerState.WON if settled.is_correct else TrackerState.LOST
                self.assertEqual(track(market, prediction, state), expected)


if __name__ == "__main__":
    unittest.main()


class TrackerWriterTests(unittest.TestCase):
    """`refresh_tracker_state`: the projection the API serves."""

    def repository(self, picks):
        from prediction_engine.db_repository import TrackerStateRepository
        from test_db_repository import FakeConnection, FakeCursor

        connection = FakeConnection(results=[FakeCursor(many=picks)])
        return connection, TrackerStateRepository(connection)

    def pick(self, market: str, prediction: str, **overrides):
        row = {
            "id": f"uuid-{market}",
            "fixture_id": 42,
            "market": market,
            "prediction": prediction,
            "settlement_status": None,
            "is_correct": None,
        }
        row.update(overrides)
        return row

    def test_it_reports_a_count_per_state(self) -> None:
        # "Forty picks examined" says nothing; "thirty-one on track, six won"
        # says whether the feed is working.
        connection, repository = self.repository(
            [
                self.pick("total_goals", "over_1.5"),   # 2-0 -> decided, won
                self.pick("1x2", "home"),               # leading -> on_track
                self.pick("1x2", "away"),               # behind  -> off_track
                self.pick("corners_total", "over_8.5"), # no data -> unknown
            ]
        )
        counts = repository.refresh_tracker_state(
            42,
            state=MatchState(2, 0),
            match_minute=25,
            computed_at=datetime(2026, 9, 30, 15, 25, tzinfo=UTC),
        )
        self.assertEqual(counts, {"won": 1, "on_track": 1, "off_track": 1, "unknown": 1})
        self.assertTrue(connection.committed)

    def test_the_query_carries_the_kickoff_guard(self) -> None:
        # Without it the newest current prediction for a fixture is an IN-PLAY
        # row, and serving that as a pre-match pick on a public tracker hands the
        # paid asset to anonymous readers -- the leak that went through
        # guest-readable /v1/analysis/{id}.
        connection, repository = self.repository([])
        repository.refresh_tracker_state(
            42, state=MatchState(0, 0), match_minute=1,
            computed_at=datetime(2026, 9, 30, tzinfo=UTC),
        )
        sql = connection.executed[0][0]
        self.assertIn("ep.generated_at <= f.kickoff_at", sql)
        self.assertIn("lifecycle_stage NOT IN", sql)
        self.assertIn("ep.superseded_at IS NULL", sql)

    def test_it_replaces_in_place_rather_than_appending(self) -> None:
        # A projection, not a history. The minute-by-minute record is
        # inplay_snapshots; a second copy here would only be a worse one.
        connection, repository = self.repository([self.pick("1x2", "home")])
        repository.refresh_tracker_state(
            42, state=MatchState(1, 0), match_minute=30,
            computed_at=datetime(2026, 9, 30, tzinfo=UTC),
        )
        upsert = connection.executed[1]
        self.assertIn("ON CONFLICT (prediction_id) DO UPDATE", upsert[0])
        self.assertEqual(upsert[1]["state"], "on_track")
        # The state judged against is stored, so a surprising tracker can be
        # explained without re-running anything.
        self.assertEqual((upsert[1]["home_goals"], upsert[1]["away_goals"]), (1, 0))
        self.assertEqual(upsert[1]["match_minute"], 30)

    def test_a_settled_pick_takes_its_state_from_settlement(self) -> None:
        connection, repository = self.repository(
            [self.pick("1x2", "home", settlement_status="settled", is_correct=True)]
        )
        counts = repository.refresh_tracker_state(
            42, state=MatchState(0, 3), match_minute=90,
            computed_at=datetime(2026, 9, 30, tzinfo=UTC),
        )
        # The live score says losing; settlement says won. Settlement wins.
        self.assertEqual(counts, {"won": 1})

    def test_a_failure_rolls_back(self) -> None:
        from prediction_engine.db_repository import TrackerStateRepository
        from test_db_repository import FakeConnection

        connection = FakeConnection(fail_on_call=1)
        with self.assertRaises(RuntimeError):
            TrackerStateRepository(connection).refresh_tracker_state(
                42, state=MatchState(0, 0), match_minute=1,
                computed_at=datetime(2026, 9, 30, tzinfo=UTC),
            )
        self.assertTrue(connection.rolled_back)
        self.assertFalse(connection.committed)
