"""The dynamic line ladder.

Lines are generated from the live count, not taken from a fixed list. The fixed
list stopped at 5.5, so a nine-goal match had nothing to offer at exactly the
moment the over was the clearest read of the game.
"""

from __future__ import annotations

import unittest

from prediction_engine.inplay_model import (
    DEFAULT_IN_PLAY_CONFIG,
    InPlayModelConfig,
    basis_points,
    generate_ladder,
    is_material_change,
    settled_lines,
)
from prediction_engine.inplay_runner import _rows_for
from prediction_engine.settlement import ActualFixtureResult, settle_prediction
from test_inplay_model import run


def poisson(mean: float, cap: int = 12) -> dict[int, float]:
    """A tidy remaining-count distribution to generate a ladder over."""
    import math

    return {k: math.exp(-mean) * mean**k / math.factorial(k) for k in range(cap + 1)}


class LadderGenerationTests(unittest.TestCase):
    def test_the_band_starts_above_the_observed_count(self) -> None:
        # Lines below the count are facts, not predictions, so the ladder does
        # not spend its slots on them.
        ladder = generate_ladder(observed=9, remaining=poisson(1.2))
        self.assertTrue(all(line > 9 for line in ladder), ladder)
        self.assertIn(9.5, ladder)

    def test_a_nine_goal_match_is_offered_over_nine_and_a_half(self) -> None:
        # The case a fixed list capped at 5.5 could not express at all.
        ladder = generate_ladder(observed=9, remaining=poisson(1.5))
        self.assertIn(9.5, ladder)
        self.assertGreater(ladder[9.5], 0.0)

    def test_everything_below_the_count_reads_as_won(self) -> None:
        self.assertEqual(
            settled_lines(9),
            (0.5, 1.5, 2.5, 3.5, 4.5, 5.5, 6.5, 7.5, 8.5),
        )
        self.assertEqual(settled_lines(0), ())
        self.assertEqual(settled_lines(1), (0.5,))

    def test_the_settled_set_and_the_published_ladder_never_overlap(self) -> None:
        # A line cannot be both a fact and a prediction. If these ever met, a
        # client would show the same line as won and as pending at once.
        for observed in range(0, 11):
            with self.subTest(observed=observed):
                ladder = generate_ladder(observed=observed, remaining=poisson(1.4))
                self.assertFalse(set(ladder) & set(settled_lines(observed)))

    def test_the_band_excludes_lines_that_are_effectively_decided(self) -> None:
        ladder = generate_ladder(observed=0, remaining=poisson(1.0))
        for line, over in ladder.items():
            with self.subTest(line=line):
                self.assertGreaterEqual(over, DEFAULT_IN_PLAY_CONFIG.ladder_band_epsilon)
                self.assertLessEqual(over, 1.0 - DEFAULT_IN_PLAY_CONFIG.ladder_band_epsilon)

    def test_the_ladder_is_capped_so_a_freak_scoreline_cannot_run_away(self) -> None:
        # Without a cap, a high-scoring match with plenty of time left would
        # generate a row per reachable line on every regeneration.
        ladder = generate_ladder(observed=0, remaining=poisson(6.0))
        self.assertLessEqual(len(ladder), DEFAULT_IN_PLAY_CONFIG.ladder_max_lines)

    def test_the_cap_keeps_the_lines_closest_to_even_money(self) -> None:
        # Those carry the most information. Keeping the first N by value would
        # fill the ladder with near-certain overs.
        config = InPlayModelConfig(ladder_max_lines=2)
        ladder = generate_ladder(observed=0, remaining=poisson(3.0), config=config)
        self.assertEqual(len(ladder), 2)
        for over in ladder.values():
            self.assertGreater(over, 0.2)
            self.assertLess(over, 0.8)

    def test_lines_are_returned_in_ascending_order(self) -> None:
        ladder = generate_ladder(observed=2, remaining=poisson(2.0))
        self.assertEqual(list(ladder), sorted(ladder))

    def test_a_narrow_band_still_publishes_something(self) -> None:
        # Late on, every line can fall outside the band -- the next goal is
        # near-certain or near-impossible. Publishing nothing would blank the
        # market, leaving a client with no line to draw at all.
        ladder = generate_ladder(observed=3, remaining=poisson(0.02))
        self.assertGreaterEqual(len(ladder), 1)
        self.assertLessEqual(len(ladder), DEFAULT_IN_PLAY_CONFIG.ladder_min_lines)

    def test_an_empty_distribution_yields_no_ladder(self) -> None:
        self.assertEqual(generate_ladder(observed=2, remaining={}), {})


class LadderThroughTheModelTests(unittest.TestCase):
    def ladder_lines(self, **kwargs) -> list[str]:
        selections = run(**kwargs).markets["total_goals_match"].selections
        return sorted({name.split("_", 1)[1] for name in selections}, key=float)

    def test_the_band_slides_up_with_the_score(self) -> None:
        early = self.ladder_lines(model_minute=1, home_goals=0, away_goals=0)
        late = self.ladder_lines(model_minute=70, home_goals=9, away_goals=0)
        self.assertEqual(early[0], "0.5")
        self.assertEqual(late[0], "9.5")
        # Nothing the earlier ladder offered is still a prediction at 9-0.
        self.assertFalse(set(early) & set(late))

    def test_less_time_left_narrows_the_ladder(self) -> None:
        # Both nine goals; one has twenty minutes to add more and one has five.
        mid = self.ladder_lines(model_minute=70, home_goals=9, away_goals=0)
        late = self.ladder_lines(model_minute=85, home_goals=5, away_goals=4)
        self.assertGreater(len(mid), len(late))


class LadderSettlementTests(unittest.TestCase):
    """Every generated line must grade, or the ladder publishes dead rows."""

    def test_a_generated_line_settles_against_the_final_score(self) -> None:
        result = run(model_minute=70, home_goals=9, away_goals=0)
        actual = ActualFixtureResult(home_goals=10, away_goals=0)
        for selection in result.markets["total_goals_match"].selections:
            with self.subTest(selection=selection):
                settled = settle_prediction("total_goals_match", selection, actual)
                self.assertEqual(settled.status, "settled")
                self.assertIsNotNone(settled.is_correct)

    def test_over_nine_and_a_half_grades_as_won_on_ten_goals(self) -> None:
        settled = settle_prediction(
            "total_goals_match", "over_9.5", ActualFixtureResult(home_goals=10, away_goals=0)
        )
        self.assertTrue(settled.is_correct)
        self.assertEqual(settled.actual, "over_9.5")

    def test_the_line_label_round_trips_through_settlement(self) -> None:
        # The label the model writes and the outcome settlement rebuilds must
        # match exactly, or a correct prediction grades as wrong. A line rendered
        # "10.50" or "10" would not compare equal to "10.5".
        for line in ("0.5", "2.5", "9.5", "10.5", "12.5"):
            with self.subTest(line=line):
                settled = settle_prediction(
                    "total_goals_match",
                    f"over_{line}",
                    ActualFixtureResult(home_goals=20, away_goals=0),
                )
                self.assertEqual(settled.actual, f"over_{line}")
                self.assertTrue(settled.is_correct)


class LadderBasisPointTests(unittest.TestCase):
    def test_each_line_pair_is_apportioned_on_its_own(self) -> None:
        # The regression: apportioning the whole ladder together divided every
        # number by the line count, sending an 81.65% over as 16.33%.
        result = run(model_minute=30, home_goals=1, away_goals=0)
        selections = result.markets["total_goals_match"].selections
        points = basis_points(selections)
        for name, value in selections.items():
            _, _, line = name.rpartition("_")
            with self.subTest(name=name):
                pair = points[f"over_{line}"] + points[f"under_{line}"]
                self.assertEqual(pair, 10000)
                self.assertLessEqual(abs(points[name] - round(value * 10000)), 1)


if __name__ == "__main__":
    unittest.main()


class MaterialChangeTests(unittest.TestCase):
    """Regeneration is decided per fixture; what to WRITE is decided per selection.

    A ladder market can carry twelve selections, and on a quiet timer tick most
    have not moved enough for a reader to notice. Writing them all produces rows
    that say the same thing as the rows they supersede.
    """

    def test_nothing_published_yet_always_publishes(self) -> None:
        # Silence is not "we still think this" when there is no standing claim.
        self.assertTrue(is_material_change(probability=0.5, previous=None))

    def test_a_move_below_the_threshold_is_skipped(self) -> None:
        self.assertFalse(is_material_change(probability=0.500, previous=0.495))
        self.assertFalse(is_material_change(probability=0.500, previous=0.500))

    def test_a_move_at_or_above_the_threshold_publishes(self) -> None:
        self.assertTrue(is_material_change(probability=0.52, previous=0.50))
        self.assertTrue(is_material_change(probability=0.48, previous=0.50))

    def test_the_threshold_is_symmetric(self) -> None:
        # A number falling is as material as one rising; an asymmetric filter
        # would let confidence drift down unrecorded.
        self.assertEqual(
            is_material_change(probability=0.55, previous=0.50),
            is_material_change(probability=0.45, previous=0.50),
        )

    def test_the_threshold_is_configurable(self) -> None:
        loose = InPlayModelConfig(material_change_threshold=0.20)
        self.assertFalse(is_material_change(probability=0.60, previous=0.50, config=loose))
        strict = InPlayModelConfig(material_change_threshold=0.0)
        self.assertTrue(is_material_change(probability=0.50, previous=0.50, config=strict))

    def test_the_default_threshold_is_below_visible_precision(self) -> None:
        # Basis points render to 0.01%, so a threshold at or below that would
        # filter nothing. Two percentage points is well above it and well below
        # what a reader would act on.
        self.assertGreater(DEFAULT_IN_PLAY_CONFIG.material_change_threshold, 0.0001)
        self.assertLess(DEFAULT_IN_PLAY_CONFIG.material_change_threshold, 0.10)


class MaterialChangeThroughTheRunnerTests(unittest.TestCase):
    """The filter keyed on (market, selection), as the runner applies it."""

    def rows(self, published: dict[tuple[str, str], float]) -> list[tuple[str, str]]:
        result = run(model_minute=30, home_goals=1, away_goals=0)
        markets = {key: market for key, market in result.markets.items() if not market.is_determined}
        kept: list[tuple[str, str]] = []
        for market in markets.values():
            for market_key, selection, probability in _rows_for(market):
                if is_material_change(probability=probability, previous=published.get((market_key, selection))):
                    kept.append((market_key, selection))
        return kept

    def current_rows(self) -> dict[tuple[str, str], float]:
        result = run(model_minute=30, home_goals=1, away_goals=0)
        markets = {key: market for key, market in result.markets.items() if not market.is_determined}
        return {
            (market_key, selection): probability
            for market in markets.values()
            for market_key, selection, probability in _rows_for(market)
        }

    def test_a_tick_that_changed_nothing_writes_nothing(self) -> None:
        # The reduction the filter exists for: an identical pass is entirely
        # suppressed rather than superseding every row with a copy of itself.
        self.assertEqual(self.rows(self.current_rows()), [])

    def test_a_first_pass_writes_everything(self) -> None:
        self.assertGreater(len(self.rows({})), 0)

    def test_a_moved_selection_is_written_and_its_neighbours_are_not(self) -> None:
        published = self.current_rows()
        target = next(iter(published))
        published[target] = published[target] - 0.10
        self.assertEqual(self.rows(published), [target])

    def test_a_flipped_side_publishes_because_the_key_carries_the_selection(self) -> None:
        # 50.5% over becoming 49.5% under is the most material thing a line can
        # do, and the number moved only one point. Keying on the selection makes
        # the new side read as "no previous", so it always publishes.
        published = self.current_rows()
        flipped = {}
        for (market_key, selection), probability in published.items():
            other = "under" if selection == "over" else "over" if selection == "under" else selection
            flipped[(market_key, other)] = probability
        written = self.rows(flipped)
        line_rows = [row for row in written if row[0].startswith("total_goals")]
        self.assertGreater(len(line_rows), 0)

    def test_a_new_ladder_line_publishes(self) -> None:
        # The band slides as the match moves, so a line can appear that has no
        # standing row. It must not be suppressed for want of a predecessor.
        published = self.current_rows()
        new_line = ("total_goals_match_9.5", "over")
        self.assertNotIn(new_line, published)
        self.assertTrue(is_material_change(probability=0.4, previous=published.get(new_line)))
