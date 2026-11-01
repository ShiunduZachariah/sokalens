"""The in-play counting markets: cards, corners, shots, fouls."""

from datetime import datetime, timezone
import unittest

from prediction_engine.inplay_counts import (
    CARDS,
    CORNERS,
    MIN_MINUTES_WITHOUT_PRIOR,
    CountObservation,
    CountPrior,
    over_probability,
    remaining_count_distribution,
)
from prediction_engine.inplay_features import (
    Freshness,
    InPlayFeatures,
    LiveEvent,
    MatchPeriod,
    count_cards,
    remaining_fraction,
)
from prediction_engine.inplay_model import (
    InPlayModelInputs,
    publishable_markets,
    run_in_play_model,
)
from prediction_engine.settlement import ActualFixtureResult, settle_prediction
from prediction_engine.tracker import MatchState, TrackerState, tracker_state


NOW = datetime(2026, 10, 1, 15, 0, tzinfo=timezone.utc)


def features_at(
    minute: int,
    *,
    home_cards: int = 0,
    away_cards: int = 0,
    cards_source: str = "events",
    home_goals: int = 1,
    away_goals: int = 0,
) -> InPlayFeatures:
    return InPlayFeatures(
        fixture_id=4411,
        data_cutoff_at=NOW,
        snapshot_captured_at=NOW,
        snapshot_id=1,
        snapshot_age_seconds=5.0,
        freshness=Freshness.FRESH,
        period=MatchPeriod.SECOND_HALF if minute > 45 else MatchPeriod.FIRST_HALF,
        raw_status="2H" if minute > 45 else "1H",
        elapsed_minute=minute,
        elapsed_extra=0,
        clock_minute=minute,
        model_minute=minute,
        remaining_fraction=remaining_fraction(minute),
        home_goals=home_goals,
        away_goals=away_goals,
        goal_difference=home_goals - away_goals,
        home_red_cards=0,
        away_red_cards=0,
        red_cards_source="events",
        fallback_flags=(),
        home_cards=home_cards,
        away_cards=away_cards,
        cards_source=cards_source,
    )


def card_event(minute: int, team_id: int, detail: str = "Yellow Card", player_id: int = 1) -> LiveEvent:
    return LiveEvent(
        fixture_id=4411,
        observed_at=NOW,
        elapsed_minute=minute,
        event_type="Card",
        event_detail=detail,
        team_id=team_id,
        player_id=player_id,
    )


class RemainingCountDistributionTests(unittest.TestCase):
    def test_the_posterior_mean_blends_the_prior_with_the_observed_rate(self):
        # Six corners by minute 63 against a pre-match expectation of ten.
        # The conjugate update weights the observation by elapsed minutes over
        # (prior strength + elapsed), so with a 30-minute prior the match's own
        # rate already carries two thirds of the answer.
        distribution = remaining_count_distribution(
            observed=6,
            elapsed_minutes=63,
            remaining_minutes=27,
            prior=CountPrior(statistic=CORNERS, full_match_mean=10.0),
        )
        self.assertAlmostEqual(sum(distribution.values()), 1.0, places=9)
        mean = sum(count * probability for count, probability in distribution.items())
        # posterior rate = (10/90*30 + 6) / (30 + 63) per minute, over 27 minutes
        expected = ((10.0 / 90.0 * 30.0) + 6) / (30.0 + 63.0) * 27.0
        self.assertAlmostEqual(mean, expected, places=3)

    def test_a_busy_match_outruns_its_prior(self):
        """The whole point of reading the match rather than the expectation.

        Nine corners by the half hour against an expectation of ten is a match
        that will not finish on ten, and a model that still says so is useless
        at exactly the moment a reader looks.
        """
        quiet = remaining_count_distribution(
            observed=2,
            elapsed_minutes=30,
            remaining_minutes=60,
            prior=CountPrior(statistic=CORNERS, full_match_mean=10.0),
        )
        busy = remaining_count_distribution(
            observed=9,
            elapsed_minutes=30,
            remaining_minutes=60,
            prior=CountPrior(statistic=CORNERS, full_match_mean=10.0),
        )
        quiet_mean = sum(c * p for c, p in quiet.items())
        busy_mean = sum(c * p for c, p in busy.items())
        self.assertGreater(busy_mean, quiet_mean * 2)

    def test_it_is_over_dispersed_relative_to_a_poisson(self):
        """Why the predictive is Negative Binomial and not a Poisson at the mean.

        The ladder keeps a line only while its probability sits inside the band,
        so understating the tail does not merely shade a number -- it deletes
        lines that are live. A Poisson at the same mean puts less mass far out;
        this asserts the extra mass is actually there.
        """
        import math

        prior = CountPrior(statistic=CORNERS, full_match_mean=10.0)
        distribution = remaining_count_distribution(
            observed=4, elapsed_minutes=45, remaining_minutes=45, prior=prior
        )
        mean = sum(count * probability for count, probability in distribution.items())
        variance = sum(
            probability * (count - mean) ** 2 for count, probability in distribution.items()
        )
        self.assertGreater(variance, mean)
        far = mean + 3.0 * math.sqrt(mean)
        negbin_tail = sum(p for c, p in distribution.items() if c > far)
        poisson_tail = sum(
            math.exp(-mean) * mean**c / math.factorial(c)
            for c in range(int(far) + 1, 80)
        )
        self.assertGreater(negbin_tail, poisson_tail)

    def test_without_a_prior_it_declines_until_there_is_enough_play(self):
        """One corner in the fourth minute does not imply twenty-two."""
        early = remaining_count_distribution(
            observed=1, elapsed_minutes=4, remaining_minutes=86
        )
        self.assertEqual(early, {})
        later = remaining_count_distribution(
            observed=4,
            elapsed_minutes=MIN_MINUTES_WITHOUT_PRIOR + 1,
            remaining_minutes=60,
        )
        self.assertNotEqual(later, {})

    def test_a_statistic_stuck_at_zero_is_not_treated_as_a_quiet_match(self):
        """Zero by the half hour is indistinguishable from an unreported feed.

        Both readings are plausible and they imply opposite markets, so neither
        is published. This is the same judgement the contract makes about a
        league with no corner data: show no market rather than a wrong one.
        """
        self.assertEqual(
            remaining_count_distribution(
                observed=0, elapsed_minutes=40, remaining_minutes=50
            ),
            {},
        )

    def test_no_time_left_means_no_distribution(self):
        self.assertEqual(
            remaining_count_distribution(
                observed=9,
                elapsed_minutes=90,
                remaining_minutes=0,
                prior=CountPrior(statistic=CORNERS, full_match_mean=10.0),
            ),
            {},
        )

    def test_over_probability_counts_the_events_still_needed(self):
        """The off-by-one that would quietly shift every line.

        A line of 8.5 with six observed needs THREE more, not two.
        """
        remaining = {0: 0.1, 1: 0.2, 2: 0.3, 3: 0.4}
        self.assertAlmostEqual(
            over_probability(line=8.5, observed=6, remaining=remaining), 0.4
        )
        self.assertAlmostEqual(
            over_probability(line=7.5, observed=6, remaining=remaining), 0.7
        )


class CardCountingTests(unittest.TestCase):
    def test_every_colour_counts_and_an_unknown_detail_is_not_dropped(self):
        """Keyed on the event type, not a whitelist of details.

        A detail this code has never seen -- a competition's own wording -- would
        be dropped by a whitelist, and a dropped card understates the market.
        """
        cards = count_cards(
            [
                card_event(12, 10, "Yellow Card", player_id=1),
                card_event(31, 10, "Second Yellow card", player_id=2),
                card_event(55, 11, "Red Card", player_id=3),
                card_event(70, 11, "Sin Bin", player_id=4),
            ],
            home_team_id=10,
            away_team_id=11,
        )
        self.assertEqual((cards.home, cards.away, cards.total), (2, 2, 4))
        self.assertEqual(cards.source, "events")

    def test_the_same_booking_under_two_identities_counts_once(self):
        """The provider revises a minute or a player and resends.

        Counting it twice would push the market over a line nobody crossed.
        """
        duplicate = card_event(31, 10, "Yellow Card", player_id=7)
        cards = count_cards([duplicate, duplicate], home_team_id=10, away_team_id=11)
        self.assertEqual(cards.total, 1)

    def test_a_retracted_card_is_not_counted(self):
        cards = count_cards(
            [card_event(31, 10, "Yellow Card")],
            home_team_id=10,
            away_team_id=11,
        )
        retracted = LiveEvent(
            fixture_id=4411,
            observed_at=NOW,
            elapsed_minute=31,
            event_type="Card",
            event_detail="Yellow Card",
            team_id=10,
            player_id=1,
            is_retracted=True,
        )
        self.assertEqual(cards.total, 1)
        self.assertEqual(
            count_cards([retracted], home_team_id=10, away_team_id=11).total, 0
        )

    def test_the_statistics_feed_is_a_floor_and_never_loses_a_card(self):
        """Both feeds carry cards and either can lag. A card is only ever added.

        Taking the larger reading is monotone, so neither feed being behind can
        lose one -- the same trick count_red_cards uses for dismissals.
        """
        cards = count_cards(
            [card_event(31, 10, "Yellow Card")],
            home_team_id=10,
            away_team_id=11,
            snapshot_home_cards=3,
            snapshot_away_cards=1,
        )
        self.assertEqual((cards.home, cards.away), (3, 1))
        self.assertEqual(cards.source, "events+snapshot")


class CountingMarketTests(unittest.TestCase):
    def test_the_card_market_needs_no_statistics_poll(self):
        """Cards come from the live event list, so this market works today.

        This is the one counting market that is not waiting on the batched
        statistics poll, and the test exists to keep it that way: if it ever
        starts depending on an observation being handed in, it breaks here.
        """
        result = run_in_play_model(
            InPlayModelInputs(
                features=features_at(63, home_cards=3, away_cards=2),
                pre_match_home_lambda=1.6,
                pre_match_away_lambda=1.1,
            )
        )
        self.assertIn(CARDS, result.markets)
        market = result.markets[CARDS]
        self.assertTrue(market.selections)
        self.assertTrue(market.method.endswith(":events"))
        # The ladder starts above the count: five cards shown means no line
        # below 5.5 is still a prediction.
        lines = {float(key.split("_")[-1]) for key in market.selections}
        self.assertTrue(all(line > 5 for line in lines), lines)

    def test_no_card_feed_means_no_card_market(self):
        """Absent, not zero. A market with no data is not a market at 0-0."""
        result = run_in_play_model(
            InPlayModelInputs(
                features=features_at(63, cards_source="none"),
                pre_match_home_lambda=1.6,
                pre_match_away_lambda=1.1,
            )
        )
        self.assertNotIn(CARDS, result.markets)

    def test_statistics_markets_appear_only_when_observed(self):
        """Today's state: the statistics poll is off, so no corner market.

        An absent market is the contract's own rule for a fixture with no
        corner data, and this asserts the model degrades to that rather than
        publishing a corner ladder off nothing.
        """
        base = InPlayModelInputs(
            features=features_at(63, home_cards=3, away_cards=2),
            pre_match_home_lambda=1.6,
            pre_match_away_lambda=1.1,
        )
        self.assertNotIn(CORNERS, run_in_play_model(base).markets)

        with_corners = InPlayModelInputs(
            features=base.features,
            pre_match_home_lambda=1.6,
            pre_match_away_lambda=1.1,
            count_observations=(
                CountObservation(statistic=CORNERS, observed=6, source="statistics_snapshot"),
            ),
            count_priors=(CountPrior(statistic=CORNERS, full_match_mean=10.0),),
        )
        result = run_in_play_model(with_corners)
        self.assertIn(CORNERS, result.markets)
        self.assertIn(CORNERS, publishable_markets(result))
        self.assertTrue(result.markets[CORNERS].method.endswith(":statistics_snapshot"))

    def test_each_line_is_a_coherent_over_under_pair(self):
        result = run_in_play_model(
            InPlayModelInputs(
                features=features_at(55, home_cards=2, away_cards=2),
                pre_match_home_lambda=1.4,
                pre_match_away_lambda=1.2,
            )
        )
        selections = result.markets[CARDS].selections
        lines = {key.split("_")[-1] for key in selections}
        for line in lines:
            over = selections[f"over_{line}"]
            under = selections[f"under_{line}"]
            self.assertAlmostEqual(over + under, 1.0, places=9)


class CountingMarketSettlementTests(unittest.TestCase):
    """The ladder's keys must grade, or they abort the settlement batch.

    This is the Phase 0 defect in miniature: in-play rows with no settlement arm
    did not fail quietly, they raised and took the whole batch with them. Every
    market shape the model can publish needs a case here.
    """

    def actual(self, **overrides) -> ActualFixtureResult:
        import inspect

        required = {
            name: 0
            for name, parameter in inspect.signature(ActualFixtureResult).parameters.items()
            if parameter.default is inspect.Parameter.empty
        }
        required.update(overrides)
        return ActualFixtureResult(**required)

    def test_a_card_ladder_line_settles_on_cards_shown(self):
        actual = self.actual(
            home_goals=2, away_goals=1, home_card_points=4, away_card_points=3
        )
        won = settle_prediction(market="total_cards_6.5", prediction="over", actual=actual)
        self.assertEqual((won.actual, won.is_correct, won.status), ("over", True, "settled"))
        lost = settle_prediction(market="total_cards_7.5", prediction="over", actual=actual)
        self.assertEqual((lost.actual, lost.is_correct, lost.status), ("under", False, "settled"))

    def test_a_corner_ladder_line_settles(self):
        actual = self.actual(home_goals=0, away_goals=0, home_corners=6, away_corners=5)
        result = settle_prediction(market="total_corners_9.5", prediction="over", actual=actual)
        self.assertEqual((result.is_correct, result.status), (True, "settled"))

    def test_shots_on_target_is_not_read_as_total_shots(self):
        """Two prefixes that must not be confused, with different numbers."""
        actual = self.actual(
            home_goals=0,
            away_goals=0,
            home_shots_total=14,
            away_shots_total=11,
            home_shots_on_target=4,
            away_shots_on_target=3,
        )
        total = settle_prediction(market="total_shots_20.5", prediction="over", actual=actual)
        self.assertEqual((total.is_correct, total.status), (True, "settled"))
        on_target = settle_prediction(
            market="shots_on_target_7.5", prediction="over", actual=actual
        )
        self.assertEqual((on_target.is_correct, on_target.status), (False, "settled"))

    def test_a_statistic_the_provider_never_sent_is_voided_not_guessed(self):
        """Full time closes every market; one it cannot grade is voided.

        This is the answer for a league with no corner data: the market was
        published honestly and closes honestly.
        """
        actual = self.actual(home_goals=1, away_goals=1)
        result = settle_prediction(market="total_fouls_22.5", prediction="over", actual=actual)
        self.assertEqual(result.status, "unsettled_data_unavailable")
        self.assertFalse(result.settled)
        self.assertIsNone(result.is_correct)


class CountingMarketTrackerTests(unittest.TestCase):
    def test_a_passed_card_line_is_won_immediately(self):
        """A count never falls, so an over that is reached is decided.

        Without the suffixed key being understood, the tracker read None for
        the count and the pick sat on_track for the rest of a match it had
        already won -- the same failure shape as the BTTS asymmetry.
        """
        state = MatchState(
            home_goals=1, away_goals=1, home_card_points=4, away_card_points=3
        )
        self.assertEqual(
            tracker_state(market="total_cards_6.5", prediction="over", state=state),
            TrackerState.WON,
        )

    def test_a_line_not_yet_reached_is_still_live(self):
        state = MatchState(
            home_goals=1, away_goals=1, home_card_points=2, away_card_points=1
        )
        self.assertEqual(
            tracker_state(market="total_cards_6.5", prediction="over", state=state),
            TrackerState.OFF_TRACK,
        )

    def test_no_data_for_the_statistic_is_unknown_not_void(self):
        state = MatchState(home_goals=1, away_goals=1)
        self.assertEqual(
            tracker_state(market="total_shots_20.5", prediction="over", state=state),
            TrackerState.UNKNOWN,
        )


if __name__ == "__main__":
    unittest.main()
