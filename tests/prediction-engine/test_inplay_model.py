"""Tests for the analytic in-play model (architecture v2 §7.1-§7.3, §7.6).

Every case here is driven from a feature vector shaped like the production
rows in `test_inplay_features.py`. There is no trained artefact to load and no
fixture history to sample -- that is the point of §7.1.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
import math
import unittest

from prediction_engine.inplay_features import (
    Freshness,
    InPlayFeatures,
    MatchPeriod,
)
from prediction_engine.inplay_model import (
    DEFAULT_IN_PLAY_CONFIG,
    IN_PLAY_MODEL_VERSION,
    INTENSITY_CALIBRATION_ENABLED,
    CadenceState,
    InPlayModelConfig,
    InPlayModelInputs,
    RegenerationTier,
    SuppressionReason,
    cadence_state_from,
    game_state_multiplier,
    intensity_multiplier,
    publishable_markets,
    red_card_multiplier,
    remaining_lambdas,
    run_in_play_model,
    should_regenerate,
)


CAPTURED_AT = datetime(2026, 9, 21, 7, 45, 33, tzinfo=UTC)

PRE_MATCH_HOME_LAMBDA = 1.55
PRE_MATCH_AWAY_LAMBDA = 1.10


def features(**overrides: object) -> InPlayFeatures:
    """A fresh minute-30 0-0, unless a test says otherwise."""
    minute = int(overrides.pop("model_minute", 30))  # type: ignore[arg-type]
    home_goals = int(overrides.pop("home_goals", 0))  # type: ignore[arg-type]
    away_goals = int(overrides.pop("away_goals", 0))  # type: ignore[arg-type]
    row: dict[str, object] = {
        "fixture_id": 3981689,
        "data_cutoff_at": CAPTURED_AT,
        "snapshot_captured_at": CAPTURED_AT,
        "snapshot_id": 194,
        "snapshot_age_seconds": 20.0,
        "freshness": Freshness.FRESH,
        "period": MatchPeriod.FIRST_HALF,
        "raw_status": "1H",
        "elapsed_minute": minute,
        "elapsed_extra": 0,
        "clock_minute": minute,
        "model_minute": minute,
        "remaining_fraction": max(0.0, (90 - minute) / 90),
        "home_goals": home_goals,
        "away_goals": away_goals,
        "goal_difference": home_goals - away_goals,
        "home_red_cards": 0,
        "away_red_cards": 0,
        "red_cards_source": "events",
        "fallback_flags": (),
    }
    row.update(overrides)
    return InPlayFeatures(**row)  # type: ignore[arg-type]


def run(**overrides: object):
    config = overrides.pop("config", DEFAULT_IN_PLAY_CONFIG)
    home_lambda = float(overrides.pop("pre_match_home_lambda", PRE_MATCH_HOME_LAMBDA))  # type: ignore[arg-type]
    away_lambda = float(overrides.pop("pre_match_away_lambda", PRE_MATCH_AWAY_LAMBDA))  # type: ignore[arg-type]
    return run_in_play_model(
        InPlayModelInputs(
            features=features(**overrides),
            pre_match_home_lambda=home_lambda,
            pre_match_away_lambda=away_lambda,
            config=config,  # type: ignore[arg-type]
        )
    )


class RedCardMultiplierTests(unittest.TestCase):
    def test_no_cards_leaves_the_rate_untouched(self) -> None:
        self.assertEqual(red_card_multiplier(0, 0, 0.5), 1.0)

    def test_own_dismissal_cuts_the_rate_within_the_spec_band(self) -> None:
        # At kickoff the full 25-30% reduction applies.
        multiplier = red_card_multiplier(1, 0, 1.0)
        self.assertLess(multiplier, 1.0)
        self.assertGreaterEqual(multiplier, 0.70)
        self.assertLessEqual(multiplier, 0.75)

    def test_opponent_dismissal_lifts_the_rate_within_the_spec_band(self) -> None:
        multiplier = red_card_multiplier(0, 1, 1.0)
        self.assertGreaterEqual(multiplier, 1.10)
        self.assertLessEqual(multiplier, 1.15)

    def test_a_late_red_matters_far_less_than_an_early_one(self) -> None:
        early = red_card_multiplier(1, 0, remaining_fraction_for(20))
        late = red_card_multiplier(1, 0, remaining_fraction_for(85))

        self.assertLess(early, late)
        self.assertAlmostEqual(late, 1.0, places=1)

    def test_effect_vanishes_entirely_when_no_time_remains(self) -> None:
        self.assertEqual(red_card_multiplier(2, 0, 0.0), 1.0)

    def test_two_dismissals_compound_but_stay_inside_the_bounds(self) -> None:
        multiplier = red_card_multiplier(2, 0, 1.0)

        self.assertLess(multiplier, red_card_multiplier(1, 0, 1.0))
        self.assertGreaterEqual(multiplier, DEFAULT_IN_PLAY_CONFIG.red_card_multiplier_floor)

    def test_a_whole_team_of_dismissals_cannot_drive_the_rate_below_the_floor(self) -> None:
        self.assertEqual(
            red_card_multiplier(11, 0, 1.0),
            DEFAULT_IN_PLAY_CONFIG.red_card_multiplier_floor,
        )

    def test_negative_counts_are_rejected(self) -> None:
        with self.assertRaises(ValueError):
            red_card_multiplier(-1, 0, 0.5)

    def test_out_of_range_fraction_is_rejected(self) -> None:
        with self.assertRaises(ValueError):
            red_card_multiplier(0, 0, 1.5)


class GameStateMultiplierTests(unittest.TestCase):
    def test_level_match_leaves_the_rate_untouched(self) -> None:
        self.assertEqual(game_state_multiplier(0, 0.5), 1.0)

    def test_trailing_team_pushes_and_leading_team_sits_deep(self) -> None:
        self.assertGreater(game_state_multiplier(-1, 0.25), 1.0)
        self.assertLess(game_state_multiplier(1, 0.25), 1.0)

    def test_the_effect_is_stronger_late_than_early(self) -> None:
        early = game_state_multiplier(-1, 1.0)
        late = game_state_multiplier(-1, 0.0)

        self.assertGreater(late, early)

    def test_the_effect_never_exceeds_the_fifteen_percent_ceiling(self) -> None:
        for goal_difference in range(-6, 7):
            for fraction in (0.0, 0.25, 0.5, 1.0):
                multiplier = game_state_multiplier(goal_difference, fraction)
                self.assertLessEqual(abs(multiplier - 1.0), DEFAULT_IN_PLAY_CONFIG.game_state_max_effect + 1e-12)

    def test_the_effect_saturates_at_a_two_goal_margin(self) -> None:
        self.assertEqual(game_state_multiplier(2, 0.3), game_state_multiplier(5, 0.3))

    def test_it_is_symmetric_in_sign(self) -> None:
        self.assertAlmostEqual(
            game_state_multiplier(-2, 0.4) - 1.0,
            1.0 - game_state_multiplier(2, 0.4),
        )


class IntensityStaysDisabledTests(unittest.TestCase):
    def test_rho_intensity_is_not_enabled(self) -> None:
        # Architecture v2 §15 decision 5. Turning this on needs a fitted
        # calibration table and the backtest behind it, not a flag flip.
        self.assertFalse(INTENSITY_CALIBRATION_ENABLED)

    def test_the_seam_is_inert(self) -> None:
        self.assertEqual(intensity_multiplier(), 1.0)

    def test_it_contributes_nothing_to_the_remaining_rates(self) -> None:
        _, _, multipliers = remaining_lambdas(features(), PRE_MATCH_HOME_LAMBDA, PRE_MATCH_AWAY_LAMBDA)

        self.assertEqual(multipliers["rho_intensity"], 1.0)


class RemainingLambdaTests(unittest.TestCase):
    def test_remaining_rates_are_the_spec_product(self) -> None:
        state = features(model_minute=45, home_goals=1, away_goals=0)
        home, away, multipliers = remaining_lambdas(state, PRE_MATCH_HOME_LAMBDA, PRE_MATCH_AWAY_LAMBDA)

        expected_home = (
            PRE_MATCH_HOME_LAMBDA
            * multipliers["remaining_fraction"]
            * multipliers["home_rho_red"]
            * multipliers["home_rho_state"]
        )
        expected_away = (
            PRE_MATCH_AWAY_LAMBDA
            * multipliers["remaining_fraction"]
            * multipliers["away_rho_red"]
            * multipliers["away_rho_state"]
        )

        self.assertAlmostEqual(home, expected_home)
        self.assertAlmostEqual(away, expected_away)
        self.assertAlmostEqual(multipliers["remaining_fraction"], 0.5)

    def test_a_goalless_first_minute_is_close_to_the_pre_match_rates(self) -> None:
        home, away, _ = remaining_lambdas(features(model_minute=0), PRE_MATCH_HOME_LAMBDA, PRE_MATCH_AWAY_LAMBDA)

        self.assertAlmostEqual(home, PRE_MATCH_HOME_LAMBDA)
        self.assertAlmostEqual(away, PRE_MATCH_AWAY_LAMBDA)

    def test_no_time_left_means_no_expected_goals_left(self) -> None:
        home, away, _ = remaining_lambdas(features(model_minute=90), PRE_MATCH_HOME_LAMBDA, PRE_MATCH_AWAY_LAMBDA)

        self.assertEqual((home, away), (0.0, 0.0))

    def test_negative_pre_match_rates_are_rejected(self) -> None:
        with self.assertRaises(ValueError):
            remaining_lambdas(features(), -1.0, 1.0)


class SuppressionTests(unittest.TestCase):
    def test_a_stale_snapshot_suppresses_the_prediction(self) -> None:
        result = run(snapshot_age_seconds=120.0, freshness=Freshness.STALE)

        self.assertTrue(result.suppressed)
        self.assertIs(result.suppression_reason, SuppressionReason.STALE_DATA)
        self.assertEqual(result.markets, {})
        self.assertEqual(publishable_markets(result), {})

    def test_a_suppressed_result_still_carries_its_audit_anchors(self) -> None:
        result = run(snapshot_age_seconds=1000.0, freshness=Freshness.EXPIRED)

        self.assertEqual(result.data_cutoff_at, CAPTURED_AT)
        self.assertEqual(result.fixture_live_snapshot_id, 194)
        self.assertIs(result.period, MatchPeriod.FIRST_HALF)

    def test_a_finished_match_gets_no_in_play_prediction(self) -> None:
        result = run(period=MatchPeriod.FINISHED, raw_status="FT")

        self.assertIs(result.suppression_reason, SuppressionReason.NOT_IN_PLAY)

    def test_extra_time_is_declared_out_of_scope_rather_than_guessed(self) -> None:
        result = run(period=MatchPeriod.EXTRA_TIME, raw_status="ET", model_minute=100)

        self.assertIs(result.suppression_reason, SuppressionReason.EXTRA_TIME_NOT_MODELLED)

    def test_a_missing_score_suppresses_rather_than_assuming_nil_nil(self) -> None:
        result = run(fallback_flags=("missing_score",))

        self.assertIs(result.suppression_reason, SuppressionReason.MISSING_SCORE)

    def test_absent_pre_match_rates_suppress(self) -> None:
        result = run(pre_match_home_lambda=0.0, pre_match_away_lambda=0.0)

        self.assertIs(result.suppression_reason, SuppressionReason.MISSING_PRE_MATCH_RATES)

    def test_half_time_is_a_live_state_and_is_predicted(self) -> None:
        result = run(period=MatchPeriod.HALF_TIME, raw_status="HT", model_minute=45)

        self.assertFalse(result.suppressed)


class LaunchMarketTests(unittest.TestCase):
    def test_the_four_launch_markets_are_produced(self) -> None:
        result = run()

        self.assertEqual(
            set(result.markets),
            {
                "match_result_in_play",
                "next_goal",
                "total_goals_remaining",
                "total_goals_match",
                "btts_in_play",
            },
        )

    def test_the_model_reports_its_version_and_audit_envelope(self) -> None:
        result = run()

        self.assertEqual(result.model_version, IN_PLAY_MODEL_VERSION)
        self.assertEqual(result.feature_version, "inplay-analytic-v1")
        self.assertEqual(result.data_cutoff_at, CAPTURED_AT)
        self.assertEqual(result.fixture_live_snapshot_id, 194)
        self.assertEqual(result.match_minute, 30)

    def test_match_result_probabilities_sum_to_one(self) -> None:
        selections = run().markets["match_result_in_play"].selections

        self.assertAlmostEqual(sum(selections.values()), 1.0)

    def test_a_two_goal_lead_at_eighty_minutes_is_near_certain(self) -> None:
        selections = run(model_minute=80, home_goals=2, away_goals=0).markets[
            "match_result_in_play"
        ].selections

        self.assertGreater(selections["home"], 0.95)
        self.assertLess(selections["away"], 0.01)

    def test_the_same_scoreline_early_is_far_less_certain(self) -> None:
        early = run(model_minute=20, home_goals=2, away_goals=0).markets["match_result_in_play"].selections
        late = run(model_minute=80, home_goals=2, away_goals=0).markets["match_result_in_play"].selections

        self.assertLess(early["home"], late["home"])

    def test_trailing_at_nil_nil_the_favourite_is_still_favoured(self) -> None:
        selections = run(model_minute=10).markets["match_result_in_play"].selections

        self.assertGreater(selections["home"], selections["away"])

    def test_next_goal_sums_to_one_and_favours_the_higher_rate(self) -> None:
        selections = run().markets["next_goal"].selections

        self.assertAlmostEqual(sum(selections.values()), 1.0)
        self.assertGreater(selections["home"], selections["away"])

    def test_next_goal_matches_the_competing_poisson_closed_form(self) -> None:
        result = run(model_minute=45)
        home_lambda = result.lambdas["remaining_home"]
        away_lambda = result.lambdas["remaining_away"]
        total = home_lambda + away_lambda
        selections = result.markets["next_goal"].selections

        self.assertAlmostEqual(selections["none"], math.exp(-total))
        self.assertAlmostEqual(selections["home"], (1 - math.exp(-total)) * home_lambda / total)

    def test_next_goal_none_probability_rises_as_the_match_runs_out(self) -> None:
        early = run(model_minute=10).markets["next_goal"].selections["none"]
        late = run(model_minute=85).markets["next_goal"].selections["none"]

        self.assertLess(early, late)

    def test_remaining_totals_are_monotone_in_the_line(self) -> None:
        selections = run().markets["total_goals_remaining"].selections

        self.assertGreater(selections["over_0.5"], selections["over_1.5"])
        self.assertGreater(selections["over_1.5"], selections["over_2.5"])
        for line in (0.5, 1.5, 2.5, 3.5):
            self.assertAlmostEqual(selections[f"over_{line}"] + selections[f"under_{line}"], 1.0)

    def test_match_totals_drop_lines_the_current_score_has_already_settled(self) -> None:
        selections = run(model_minute=60, home_goals=3, away_goals=1).markets[
            "total_goals_match"
        ].selections

        self.assertNotIn("over_0.5", selections)
        self.assertNotIn("over_3.5", selections)
        self.assertIn("over_4.5", selections)

    def test_match_totals_are_the_remaining_distribution_shifted_by_the_score(self) -> None:
        result = run(model_minute=60, home_goals=1, away_goals=1)
        match_selections = result.markets["total_goals_match"].selections
        remaining_selections = result.markets["total_goals_remaining"].selections

        # With two already scored, "over 3.5 in the match" is "over 1.5 left".
        self.assertAlmostEqual(match_selections["over_3.5"], remaining_selections["over_1.5"])

    def test_a_high_scoring_match_still_gets_a_line(self) -> None:
        # This asserted the opposite before the ladder: the fixed list stopped at
        # 5.5, so at seven goals every line was settled and the market went
        # blank. Generating from the count means a seven-goal match is offered
        # over 7.5 -- which is the only line anyone wants by then.
        result = run(model_minute=88, home_goals=4, away_goals=3)
        market = result.markets["total_goals_match"]

        self.assertFalse(market.is_determined)
        self.assertIn("over_7.5", market.selections)
        self.assertIn("total_goals_match", publishable_markets(result))

    def test_match_totals_market_is_determined_once_no_goals_can_remain(self) -> None:
        # At the whistle the remaining distribution collapses onto zero, so every
        # line is a statement of fact rather than a prediction. That is the real
        # determined case, and it is withheld.
        result = run(model_minute=90, home_goals=4, away_goals=3)

        self.assertTrue(result.markets["total_goals_match"].is_determined)
        self.assertNotIn("total_goals_match", publishable_markets(result))

    def test_btts_is_settled_and_withheld_once_both_teams_have_scored(self) -> None:
        result = run(model_minute=55, home_goals=1, away_goals=1)
        market = result.markets["btts_in_play"]

        self.assertTrue(market.is_determined)
        self.assertEqual(market.selections["yes"], 1.0)
        self.assertNotIn("btts_in_play", publishable_markets(result))

    def test_btts_with_one_side_still_to_score_is_that_side_scoring(self) -> None:
        result = run(model_minute=60, home_goals=1, away_goals=0)
        yes = result.markets["btts_in_play"].selections["yes"]
        no_away_goal = sum(row[0] for row in result.remaining_score_matrix)

        self.assertAlmostEqual(yes, 1 - no_away_goal)
        self.assertFalse(result.markets["btts_in_play"].is_determined)

    def test_btts_at_nil_nil_needs_both_sides_to_score(self) -> None:
        result = run(model_minute=10)
        yes = result.markets["btts_in_play"].selections["yes"]

        self.assertGreater(yes, 0.2)
        self.assertLess(yes, 0.6)

    def test_btts_falls_as_the_match_runs_out(self) -> None:
        early = run(model_minute=10).markets["btts_in_play"].selections["yes"]
        late = run(model_minute=80).markets["btts_in_play"].selections["yes"]

        self.assertLess(late, early)

    def test_every_published_probability_is_a_valid_probability(self) -> None:
        for minute in (0, 15, 44, 45, 60, 75, 89):
            for score in ((0, 0), (1, 0), (0, 2), (2, 2), (3, 1)):
                result = run(model_minute=minute, home_goals=score[0], away_goals=score[1])
                for market in result.markets.values():
                    for name, probability in market.selections.items():
                        self.assertGreaterEqual(probability, -1e-12, f"{market.market_key}.{name}")
                        self.assertLessEqual(probability, 1.0 + 1e-12, f"{market.market_key}.{name}")

    def test_final_score_distribution_is_anchored_on_the_current_score(self) -> None:
        result = run(model_minute=70, home_goals=2, away_goals=1)

        self.assertAlmostEqual(sum(result.final_score_distribution.values()), 1.0)
        self.assertNotIn("1-1", result.final_score_distribution)
        self.assertIn("2-1", result.final_score_distribution)

    def test_the_final_whistle_edge_case_collapses_onto_the_current_score(self) -> None:
        # f = 0 at minute 90. Every market becomes a statement of fact, and
        # `publishable_markets` withholds the ones that have.
        result = run(model_minute=90, home_goals=1, away_goals=0)

        self.assertAlmostEqual(result.markets["match_result_in_play"].selections["home"], 1.0)
        self.assertTrue(result.markets["match_result_in_play"].is_determined)
        self.assertEqual(result.markets["next_goal"].selections["none"], 1.0)
        self.assertEqual(publishable_markets(result), {})

    def test_a_red_card_moves_the_result_market_the_way_it_should(self) -> None:
        level = run(model_minute=30).markets["match_result_in_play"].selections
        home_down_to_ten = run(model_minute=30, home_red_cards=1).markets[
            "match_result_in_play"
        ].selections

        self.assertLess(home_down_to_ten["home"], level["home"])
        self.assertGreater(home_down_to_ten["away"], level["away"])


class ConfigValidationTests(unittest.TestCase):
    def test_an_impossible_game_state_band_is_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "game_state effects"):
            InPlayModelConfig(game_state_min_effect=0.4, game_state_max_effect=0.1)

    def test_a_non_positive_freshness_limit_is_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "freshness_limit_seconds"):
            InPlayModelConfig(freshness_limit_seconds=0.0)

    def test_the_default_freshness_limit_matches_the_staleness_guard(self) -> None:
        self.assertEqual(DEFAULT_IN_PLAY_CONFIG.freshness_limit_seconds, 90.0)


class CadenceTests(unittest.TestCase):
    def _state(self, **overrides: object) -> CadenceState:
        row: dict[str, object] = {
            "generated_at": CAPTURED_AT,
            "home_goals": 0,
            "away_goals": 0,
            "home_red_cards": 0,
            "away_red_cards": 0,
            "period": MatchPeriod.FIRST_HALF,
        }
        row.update(overrides)
        return CadenceState(**row)  # type: ignore[arg-type]

    def test_the_first_prediction_always_runs(self) -> None:
        decision = should_regenerate(features=features(), previous=None, now=CAPTURED_AT)

        self.assertTrue(decision.should_regenerate)
        self.assertEqual(decision.reason, "first_prediction")

    def test_a_goal_regenerates_immediately(self) -> None:
        decision = should_regenerate(
            features=features(home_goals=1),
            previous=self._state(),
            now=CAPTURED_AT + timedelta(seconds=5),
        )

        self.assertTrue(decision.should_regenerate)
        self.assertEqual(decision.reason, "goal")

    def test_a_red_card_regenerates_immediately(self) -> None:
        decision = should_regenerate(
            features=features(away_red_cards=1),
            previous=self._state(),
            now=CAPTURED_AT + timedelta(seconds=5),
        )

        self.assertEqual(decision.reason, "red_card")

    def test_half_time_regenerates(self) -> None:
        decision = should_regenerate(
            features=features(period=MatchPeriod.HALF_TIME, model_minute=45),
            previous=self._state(),
            now=CAPTURED_AT + timedelta(seconds=5),
        )

        self.assertEqual(decision.reason, "half_time")

    def test_an_unchanged_match_waits_for_the_tier_a_timer(self) -> None:
        quiet = should_regenerate(
            features=features(),
            previous=self._state(),
            now=CAPTURED_AT + timedelta(seconds=90),
        )
        due = should_regenerate(
            features=features(),
            previous=self._state(),
            now=CAPTURED_AT + timedelta(seconds=120),
        )

        self.assertFalse(quiet.should_regenerate)
        self.assertEqual(quiet.reason, "unchanged")
        self.assertTrue(due.should_regenerate)
        self.assertEqual(due.reason, "timer")

    def test_tier_b_waits_five_minutes(self) -> None:
        at_three = should_regenerate(
            features=features(),
            previous=self._state(),
            now=CAPTURED_AT + timedelta(minutes=3),
            tier=RegenerationTier.B,
        )
        at_five = should_regenerate(
            features=features(),
            previous=self._state(),
            now=CAPTURED_AT + timedelta(minutes=5),
            tier=RegenerationTier.B,
        )

        self.assertFalse(at_three.should_regenerate)
        self.assertTrue(at_five.should_regenerate)

    def test_cadence_state_round_trips_from_the_features(self) -> None:
        state = cadence_state_from(features(home_goals=2, away_red_cards=1), CAPTURED_AT)

        self.assertEqual(state.home_goals, 2)
        self.assertEqual(state.away_red_cards, 1)
        self.assertIs(state.period, MatchPeriod.FIRST_HALF)


def remaining_fraction_for(minute: int) -> float:
    return max(0.0, (90 - minute) / 90)


if __name__ == "__main__":
    unittest.main()
