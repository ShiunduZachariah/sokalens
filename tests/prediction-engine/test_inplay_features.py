"""Feature tests built on rows shaped like the ones production actually writes.

The snapshots and events below are copied from `fixture_live_snapshots` and
`fixture_events` on the Contabo host on 2026-09-21 -- the first day either
table carried real traffic. The null red-card columns and the raw provider
casing in `event_type` are not hypotheticals; they are what is in the table.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
import unittest

from prediction_engine.inplay_features import (
    EXPIRED_AFTER_SECONDS,
    IN_PLAY_FEATURE_VERSION,
    STALE_AFTER_SECONDS,
    Freshness,
    LiveEvent,
    LiveSnapshot,
    MatchPeriod,
    assemble_in_play_features,
    classify_freshness,
    count_red_cards,
    latest_snapshot,
    normalize_period,
    remaining_fraction,
)


HOME_TEAM_ID = 1723
AWAY_TEAM_ID = 21343
CAPTURED_AT = datetime(2026, 9, 21, 7, 45, 33, tzinfo=UTC)


def production_snapshot(**overrides: object) -> LiveSnapshot:
    """Fixture 3981689 at 07:45:33 -- 2-0, 44 minutes, red cards null."""
    row: dict[str, object] = {
        "fixture_id": 3981689,
        "captured_at": CAPTURED_AT,
        "provider_timestamp": datetime(2026, 9, 21, 7, 0, tzinfo=UTC),
        "status": "1H",
        "period": "1H",
        "elapsed_minute": 44,
        "elapsed_extra": None,
        "home_goals": 2,
        "away_goals": 0,
        "home_red_cards": None,
        "away_red_cards": None,
        "snapshot_id": 194,
    }
    row.update(overrides)
    return LiveSnapshot(**row)  # type: ignore[arg-type]


class PeriodNormalisationTests(unittest.TestCase):
    def test_production_status_strings_map_onto_periods(self) -> None:
        self.assertIs(normalize_period("1H", "1H"), MatchPeriod.FIRST_HALF)
        self.assertIs(normalize_period("HT", "HT"), MatchPeriod.HALF_TIME)
        self.assertIs(normalize_period("2H", "2H"), MatchPeriod.SECOND_HALF)

    def test_period_column_wins_over_a_lagging_status(self) -> None:
        self.assertIs(normalize_period("1H", "HT"), MatchPeriod.HALF_TIME)

    def test_status_is_used_when_the_period_column_is_null(self) -> None:
        self.assertIs(normalize_period("2H", None), MatchPeriod.SECOND_HALF)

    def test_unrecognised_status_is_unknown_rather_than_guessed(self) -> None:
        self.assertIs(normalize_period("SOMETHING_NEW", None), MatchPeriod.UNKNOWN)

    def test_matching_is_case_and_separator_insensitive(self) -> None:
        self.assertIs(normalize_period("second-half", None), MatchPeriod.SECOND_HALF)
        self.assertIs(normalize_period(" ft ", None), MatchPeriod.FINISHED)


class RemainingFractionTests(unittest.TestCase):
    def test_fraction_is_the_spec_formula(self) -> None:
        self.assertAlmostEqual(remaining_fraction(0), 1.0)
        self.assertAlmostEqual(remaining_fraction(45), 0.5)
        self.assertAlmostEqual(remaining_fraction(90), 0.0)

    def test_fraction_never_goes_negative_past_ninety(self) -> None:
        self.assertEqual(remaining_fraction(105), 0.0)

    def test_negative_minute_is_rejected(self) -> None:
        with self.assertRaises(ValueError):
            remaining_fraction(-1)


class RedCardCountingTests(unittest.TestCase):
    def _card(self, **overrides: object) -> LiveEvent:
        row: dict[str, object] = {
            "fixture_id": 3981689,
            "observed_at": CAPTURED_AT,
            "elapsed_minute": 30,
            "event_type": "Card",
            "event_detail": "Red Card",
            "team_id": HOME_TEAM_ID,
            "player_id": 900,
        }
        row.update(overrides)
        return LiveEvent(**row)  # type: ignore[arg-type]

    def test_counts_reds_from_events_when_snapshot_columns_are_null(self) -> None:
        counted = count_red_cards(
            [self._card(), self._card(team_id=AWAY_TEAM_ID, player_id=901, elapsed_minute=62)],
            home_team_id=HOME_TEAM_ID,
            away_team_id=AWAY_TEAM_ID,
        )

        self.assertEqual((counted.home, counted.away), (1, 1))
        self.assertEqual(counted.source, "events")

    def test_second_yellow_counts_as_a_dismissal(self) -> None:
        counted = count_red_cards(
            [self._card(event_detail="Second Yellow card")],
            home_team_id=HOME_TEAM_ID,
            away_team_id=AWAY_TEAM_ID,
        )

        self.assertEqual(counted.home, 1)

    def test_yellow_cards_and_other_event_types_are_ignored(self) -> None:
        counted = count_red_cards(
            [
                self._card(event_detail="Yellow Card"),
                LiveEvent(
                    fixture_id=3981689,
                    observed_at=CAPTURED_AT,
                    elapsed_minute=20,
                    event_type="Goal",
                    event_detail="Normal Goal",
                    team_id=HOME_TEAM_ID,
                ),
                LiveEvent(
                    fixture_id=3981689,
                    observed_at=CAPTURED_AT,
                    elapsed_minute=19,
                    event_type="subst",
                    event_detail="Substitution 1",
                    team_id=HOME_TEAM_ID,
                ),
            ],
            home_team_id=HOME_TEAM_ID,
            away_team_id=AWAY_TEAM_ID,
        )

        self.assertEqual((counted.home, counted.away), (0, 0))

    def test_retracted_dismissal_is_not_counted(self) -> None:
        counted = count_red_cards(
            [self._card(is_retracted=True)],
            home_team_id=HOME_TEAM_ID,
            away_team_id=AWAY_TEAM_ID,
        )

        self.assertEqual(counted.home, 0)

    def test_same_dismissal_under_two_identity_keys_counts_once(self) -> None:
        # Exactly the duplication production showed on fixture 3981689's
        # 20th-minute goal: one real event, two rows, two identity keys.
        counted = count_red_cards(
            [
                self._card(identity_key="a", observed_at=CAPTURED_AT - timedelta(minutes=1)),
                self._card(identity_key="b"),
            ],
            home_team_id=HOME_TEAM_ID,
            away_team_id=AWAY_TEAM_ID,
        )

        self.assertEqual(counted.home, 1)

    def test_snapshot_columns_act_as_a_floor_not_a_replacement(self) -> None:
        counted = count_red_cards(
            [self._card()],
            home_team_id=HOME_TEAM_ID,
            away_team_id=AWAY_TEAM_ID,
            snapshot_home_red_cards=0,
            snapshot_away_red_cards=2,
        )

        self.assertEqual((counted.home, counted.away), (1, 2))
        self.assertEqual(counted.source, "events+snapshot")

    def test_no_events_and_no_snapshot_columns_reports_unknown_source(self) -> None:
        counted = count_red_cards(
            None,
            home_team_id=HOME_TEAM_ID,
            away_team_id=AWAY_TEAM_ID,
        )

        self.assertEqual(counted.source, "none")


class FreshnessTests(unittest.TestCase):
    def test_tiers_match_the_staleness_guard(self) -> None:
        self.assertIs(classify_freshness(0.0), Freshness.FRESH)
        self.assertIs(classify_freshness(STALE_AFTER_SECONDS - 0.1), Freshness.FRESH)
        self.assertIs(classify_freshness(STALE_AFTER_SECONDS), Freshness.STALE)
        self.assertIs(classify_freshness(EXPIRED_AFTER_SECONDS), Freshness.EXPIRED)


class AssembleFeatureTests(unittest.TestCase):
    def test_assembles_the_production_row_into_the_model_inputs(self) -> None:
        features = assemble_in_play_features(
            snapshot=production_snapshot(),
            now=CAPTURED_AT + timedelta(seconds=20),
            home_team_id=HOME_TEAM_ID,
            away_team_id=AWAY_TEAM_ID,
            events=[],
        )

        self.assertEqual(features.fixture_id, 3981689)
        self.assertIs(features.period, MatchPeriod.FIRST_HALF)
        self.assertEqual(features.model_minute, 44)
        self.assertAlmostEqual(features.remaining_fraction, 46 / 90)
        self.assertEqual((features.home_goals, features.away_goals), (2, 0))
        self.assertEqual(features.goal_difference, 2)
        self.assertEqual((features.home_red_cards, features.away_red_cards), (0, 0))
        self.assertIs(features.freshness, Freshness.FRESH)
        self.assertEqual(features.snapshot_age_seconds, 20.0)
        self.assertEqual(features.data_cutoff_at, CAPTURED_AT)
        self.assertEqual(features.snapshot_id, 194)
        self.assertEqual(features.feature_version, IN_PLAY_FEATURE_VERSION)
        self.assertTrue(features.is_in_play)

    def test_null_red_card_columns_do_not_become_a_fallback_flag_when_events_are_present(self) -> None:
        features = assemble_in_play_features(
            snapshot=production_snapshot(),
            now=CAPTURED_AT,
            home_team_id=HOME_TEAM_ID,
            away_team_id=AWAY_TEAM_ID,
            events=[],
        )

        self.assertEqual(features.red_cards_source, "events")
        self.assertNotIn("red_cards_unknown", features.fallback_flags)

    def test_no_event_feed_at_all_is_flagged_as_unknown_red_cards(self) -> None:
        features = assemble_in_play_features(
            snapshot=production_snapshot(),
            now=CAPTURED_AT,
            home_team_id=HOME_TEAM_ID,
            away_team_id=AWAY_TEAM_ID,
            events=None,
        )

        self.assertIn("red_cards_unknown", features.fallback_flags)

    def test_first_half_stoppage_time_does_not_advance_the_modelling_minute(self) -> None:
        # 45+2 is added time inside the first half, not minute 47 of the match.
        features = assemble_in_play_features(
            snapshot=production_snapshot(elapsed_minute=45, elapsed_extra=2),
            now=CAPTURED_AT,
            home_team_id=HOME_TEAM_ID,
            away_team_id=AWAY_TEAM_ID,
            events=[],
        )

        self.assertEqual(features.model_minute, 45)
        self.assertEqual(features.clock_minute, 47)
        self.assertAlmostEqual(features.remaining_fraction, 0.5)

    def test_half_time_is_pinned_to_minute_forty_five(self) -> None:
        features = assemble_in_play_features(
            snapshot=production_snapshot(status="HT", period="HT", elapsed_minute=45),
            now=CAPTURED_AT,
            home_team_id=HOME_TEAM_ID,
            away_team_id=AWAY_TEAM_ID,
            events=[],
        )

        self.assertIs(features.period, MatchPeriod.HALF_TIME)
        self.assertEqual(features.model_minute, 45)
        self.assertAlmostEqual(features.remaining_fraction, 0.5)

    def test_second_half_minute_is_capped_at_ninety(self) -> None:
        features = assemble_in_play_features(
            snapshot=production_snapshot(status="2H", period="2H", elapsed_minute=90, elapsed_extra=4),
            now=CAPTURED_AT,
            home_team_id=HOME_TEAM_ID,
            away_team_id=AWAY_TEAM_ID,
            events=[],
        )

        self.assertEqual(features.model_minute, 90)
        self.assertEqual(features.clock_minute, 94)
        self.assertEqual(features.remaining_fraction, 0.0)

    def test_a_stale_snapshot_is_reported_rather_than_extrapolated(self) -> None:
        features = assemble_in_play_features(
            snapshot=production_snapshot(),
            now=CAPTURED_AT + timedelta(seconds=300),
            home_team_id=HOME_TEAM_ID,
            away_team_id=AWAY_TEAM_ID,
            events=[],
        )

        # The minute is still the one the provider reported, not 44 + 5.
        self.assertEqual(features.model_minute, 44)
        self.assertIs(features.freshness, Freshness.STALE)

    def test_events_newer_than_the_snapshot_move_the_data_cutoff(self) -> None:
        later = CAPTURED_AT + timedelta(seconds=30)
        features = assemble_in_play_features(
            snapshot=production_snapshot(),
            now=later + timedelta(seconds=5),
            home_team_id=HOME_TEAM_ID,
            away_team_id=AWAY_TEAM_ID,
            events=[
                LiveEvent(
                    fixture_id=3981689,
                    observed_at=later,
                    elapsed_minute=44,
                    event_type="Goal",
                    event_detail="Normal Goal",
                    team_id=HOME_TEAM_ID,
                )
            ],
        )

        self.assertEqual(features.data_cutoff_at, later)

    def test_missing_score_is_flagged_rather_than_assumed_goalless(self) -> None:
        features = assemble_in_play_features(
            snapshot=production_snapshot(home_goals=None, away_goals=None),
            now=CAPTURED_AT,
            home_team_id=HOME_TEAM_ID,
            away_team_id=AWAY_TEAM_ID,
            events=[],
        )

        self.assertIn("missing_score", features.fallback_flags)

    def test_naive_timestamps_are_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "timezone-aware"):
            assemble_in_play_features(
                snapshot=production_snapshot(),
                now=datetime(2026, 9, 21, 7, 45, 40),
                home_team_id=HOME_TEAM_ID,
                away_team_id=AWAY_TEAM_ID,
                events=[],
            )

    def test_latest_snapshot_picks_the_newest_capture(self) -> None:
        older = production_snapshot(captured_at=CAPTURED_AT - timedelta(minutes=1), elapsed_minute=43)
        self.assertEqual(latest_snapshot([older, production_snapshot()]).elapsed_minute, 44)

    def test_latest_snapshot_rejects_an_empty_sequence(self) -> None:
        with self.assertRaises(ValueError):
            latest_snapshot([])


if __name__ == "__main__":
    unittest.main()
