from __future__ import annotations

from datetime import UTC, datetime
import json
import math
from pathlib import Path
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "services" / "prediction-engine"))

from prediction_engine import challenger_markets as markets
from prediction_engine.elo import EloPrediction
from prediction_engine.features import FixtureRecord
from prediction_engine.form_features import FEATURE_NAMES, FormFeatureBook
from prediction_engine.gbdt import TreeEnsemble
from prediction_engine.model_a import ModelAInputs, run_model_a
from prediction_engine.settlement import ActualFixtureResult
from prediction_engine.stat_features import STAT_FEATURE_NAMES, StatFeatureBook


KICKOFF = datetime(2030, 3, 1, 15, 0, tzinfo=UTC)


def _record(fixture_id: int = 1, *, home: int | None = 2, away: int | None = 1, league: str = "Premier League") -> FixtureRecord:
    return FixtureRecord(
        id=fixture_id,
        kickoff_at=KICKOFF,
        league_id=39,
        league_name=league,
        season=2030,
        home_team_id=10,
        away_team_id=20,
        home_goals=home,
        away_goals=away,
        status="FT",
        source="test",
    )


def _document() -> dict[str, dict[str, float]]:
    result = run_model_a(ModelAInputs(home_lambda=1.5, away_lambda=1.1))
    return markets.goal_document(result.markets, result.score_matrix)


class UnderProbabilityTests(unittest.TestCase):
    def test_zero_dispersion_is_poisson(self) -> None:
        expected = sum(math.exp(-4.0) * 4.0**k / math.factorial(k) for k in range(6))
        self.assertAlmostEqual(markets.under_probability(4.0, 0.0, 5.5), expected, places=12)

    def test_negative_binomial_keeps_the_mean_and_widens_the_tails(self) -> None:
        mean, dispersion = 10.0, 0.05
        size = 1 / dispersion
        probabilities = []
        previous = 0.0
        for count in range(200):
            current = markets.under_probability(mean, dispersion, count + 0.5)
            probabilities.append(current - previous)
            previous = current
        self.assertAlmostEqual(sum(probabilities), 1.0, places=9)
        self.assertAlmostEqual(sum(k * p for k, p in enumerate(probabilities)), mean, places=6)
        self.assertAlmostEqual(
            sum((k - mean) ** 2 * p for k, p in enumerate(probabilities)), mean + mean**2 / size, places=4
        )
        self.assertGreater(markets.under_probability(mean, dispersion, 4.5), markets.under_probability(mean, 0.0, 4.5))


class GradeTests(unittest.TestCase):
    def test_every_exclusive_goal_market_sums_to_one(self) -> None:
        for key, selections in _document().items():
            if key not in markets.INDEPENDENT_SELECTIONS:
                self.assertAlmostEqual(sum(selections.values()), 1.0, places=3, msg=key)

    def test_grading_names_what_settlement_says_happened(self) -> None:
        document = _document()
        scored = markets.grade(document, ActualFixtureResult(home_goals=2, away_goals=1, home_goals_ht=1, away_goals_ht=1))
        self.assertEqual(scored["1x2"][0], "home")
        self.assertEqual(scored["total_goals:2.5"][0], "over_2.5")
        self.assertEqual(scored["total_goals:3.5"][0], "under_3.5")
        self.assertEqual(scored["btts"][0], "yes")
        self.assertEqual(scored["odd_even_total_goals"][0], "odd")
        self.assertEqual(scored["btts_total_goals:2.5"][0], "yes_over_2.5")
        self.assertEqual(scored["btts_match_result"][0], "home_yes")
        self.assertEqual(scored["result_total_goals:2.5"][0], "home_over_2.5")
        self.assertEqual(scored["correct_score"][0], "2-1")
        self.assertEqual(scored["half_time_result"][0], "draw")
        self.assertEqual(scored["highest_scoring_half"][0], "first_half")
        self.assertEqual(scored["double_chance:home_or_draw"][0], "yes")
        self.assertEqual(scored["double_chance:home_or_away"][0], "yes")
        self.assertEqual(scored["double_chance:draw_or_away"][0], "no")
        self.assertEqual(scored["draw_no_bet"][0], "home")

    def test_the_score_is_the_probability_given_to_the_outcome(self) -> None:
        document = _document()
        scored = markets.grade(document, ActualFixtureResult(home_goals=0, away_goals=1))
        total = sum(document["1x2"].values())
        self.assertAlmostEqual(scored["1x2"][1], document["1x2"]["away"] / total, places=5)
        self.assertAlmostEqual(
            scored["double_chance:home_or_draw"][1], 1 - document["double_chance"]["home_or_draw"], places=5
        )

    def test_a_drawn_draw_no_bet_stays_out_as_it_does_from_published_accuracy(self) -> None:
        scored = markets.grade(_document(), ActualFixtureResult(home_goals=1, away_goals=1))
        self.assertNotIn("draw_no_bet", scored)

    def test_a_market_without_its_data_stays_out(self) -> None:
        document = {**_document(), **markets.count_document("total_corners", 10.0, 0.0)}
        scored = markets.grade(document, ActualFixtureResult(home_goals=1, away_goals=0))
        self.assertNotIn("total_corners:9.5", scored)
        self.assertNotIn("half_time_result", scored)

    def test_counts_grade_against_the_statistic(self) -> None:
        document = markets.count_document("total_corners", 10.0, 0.0)
        document.update(markets.count_document("team_corners_home", 5.0, 0.0))
        document.update(markets.count_document("total_cards", 4.0, 0.0))
        scored = markets.grade(
            document,
            ActualFixtureResult(
                home_goals=1, away_goals=0, home_corners=6, away_corners=5, home_card_points=1, away_card_points=1
            ),
        )
        self.assertEqual(scored["total_corners:9.5"][0], "over_9.5")
        self.assertEqual(scored["total_corners:10.5"][0], "over_10.5")
        self.assertEqual(scored["team_corners_home:5.5"][0], "over_5.5")
        self.assertEqual(scored["total_cards:2.5"][0], "under_2.5")

    def test_a_score_beyond_the_grid_lands_on_other(self) -> None:
        scored = markets.grade(_document(), ActualFixtureResult(home_goals=7, away_goals=0))
        self.assertEqual(scored["correct_score"][0], "other")
        self.assertGreater(scored["correct_score"][1], 0.0)

    def test_first_goal_is_not_scored_because_production_cannot_grade_it(self) -> None:
        self.assertNotIn("team_to_score_first", _document())


class CompareTests(unittest.TestCase):
    def test_a_better_forecast_has_a_negative_difference(self) -> None:
        challenger = {fixture: {"btts": 0.6 + fixture * 1e-4} for fixture in range(10)}
        champion = {fixture: {"btts": 0.5} for fixture in range(10)}
        summary = markets.compare(challenger, champion)["btts"]
        self.assertEqual(summary["paired_with_champion"], 10)
        self.assertLess(summary["mean_log_loss_difference"], 0)
        self.assertLess(summary["log_loss"], summary["champion_log_loss"])

    def test_only_fixtures_both_models_scored_are_paired(self) -> None:
        summary = markets.compare({1: {"btts": 0.6}, 2: {"btts": 0.6}}, {2: {"btts": 0.5}})["btts"]
        self.assertEqual(summary["paired_with_champion"], 1)

    def test_double_chance_counts_each_fixture_once(self) -> None:
        scores = {f"double_chance:{name}": 0.6 for name in ("home_or_draw", "draw_or_away", "home_or_away")}
        reference = {key: 0.5 for key in scores}
        summary = markets.compare({1: scores, 2: scores}, {1: reference, 2: reference})
        self.assertEqual(set(summary), {"double_chance"})
        self.assertEqual(summary["double_chance"]["paired_with_champion"], 2)

    def test_the_report_never_promotes(self) -> None:
        self.assertEqual(markets.decision(10, minimum_samples=500)[0], "extend")
        self.assertEqual(markets.decision(10_000, minimum_samples=500)[0], "pending")


class ChampionCountTests(unittest.TestCase):
    def test_a_market_needs_five_samples_a_side(self) -> None:
        statistics = {"home_corners_for": 5.0, "away_corners_for": 4.0}
        document = markets.champion_count_document(statistics, {"home_corners_for": 5, "away_corners_for": 5})
        self.assertIn("total_corners:9.5", document)
        self.assertIn("team_corners_home:4.5", document)
        thin = markets.champion_count_document(statistics, {"home_corners_for": 4, "away_corners_for": 5})
        self.assertNotIn("total_corners:9.5", thin)
        self.assertNotIn("team_corners_home:4.5", thin)
        self.assertIn("team_corners_away:4.5", thin)

    def test_it_is_poisson_on_the_sum_of_the_averages(self) -> None:
        document = markets.count_document("total_corners", 9.0, 0.0)
        self.assertAlmostEqual(
            document["total_corners:9.5"]["under_9.5"], markets.under_probability(9.0, 0.0, 9.5), places=5
        )


class StatFeatureBookTests(unittest.TestCase):
    def test_a_fixture_never_sees_its_own_statistics(self) -> None:
        book = StatFeatureBook()
        record = _record()
        before = book.features(record)
        self.assertEqual(set(before), set(STAT_FEATURE_NAMES))
        self.assertTrue(all(value is None for value in before.values()))
        book.record(record, {"corners": (7, 3)})
        after = book.features(record)
        self.assertEqual(after["home_corners_for_10"], 7)
        self.assertEqual(after["home_corners_against_10"], 3)
        self.assertEqual(after["away_corners_for_10"], 3)
        self.assertEqual(after["league_corners_per_team"], 5)

    def test_a_missing_statistic_is_not_a_zero(self) -> None:
        book = StatFeatureBook()
        book.record(_record(), {"corners": (None, 3), "fouls": (10, 12)})
        features = book.features(_record())
        self.assertIsNone(features["home_corners_for_10"])
        self.assertEqual(features["home_fouls_for_10"], 10)

    def test_friendlies_and_unfinished_fixtures_are_ignored_like_form(self) -> None:
        book = StatFeatureBook()
        book.record(_record(league="Club Friendlies"), {"corners": (9, 9)})
        book.record(_record(home=None, away=None), {"corners": (9, 9)})
        self.assertIsNone(book.features(_record())["home_corners_for_10"])


def _single_leaf_artifact(names: tuple[str, ...], leaf: float) -> str:
    return json.dumps(
        {"format": "sokalens-gbdt-v1", "feature_names": list(names), "classes": ["count"], "trees": [[[leaf]]]}
    )


class ForecastTests(unittest.TestCase):
    def setUp(self) -> None:
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        path = Path(directory.name)
        all_names = (*FEATURE_NAMES, *STAT_FEATURE_NAMES)
        for name, leaf in {"home": 1.5, "away": 1.1, "half_time_home": 0.7, "half_time_away": 0.5}.items():
            (path / f"goals_{name}.json").write_text(_single_leaf_artifact(FEATURE_NAMES, math.log(leaf)), encoding="utf-8")
        targets = ("home_corners", "away_corners", "total_cards", "total_fouls", "total_shots", "shots_on_target")
        for name in targets:
            (path / f"count_{name}.json").write_text(_single_leaf_artifact(all_names, math.log(5.0)), encoding="utf-8")
        (path / "manifest.json").write_text(
            json.dumps(
                {
                    "count_targets": list(targets),
                    "dispersions": {name: 0.02 for name in targets},
                    "elo_goal_map": {
                        "home": [0.3, 0.9, 0.0, 0.0],
                        "away": [0.1, -0.9, 0.0, 0.0],
                        "half_time_home_share": 0.44,
                        "half_time_away_share": 0.44,
                    },
                    "feature_version": "test",
                    "hyperparameters": {},
                }
            ),
            encoding="utf-8",
        )
        self.models = markets.load_market_models(path)

    def test_expected_counts_invert_the_log_link(self) -> None:
        ensemble = TreeEnsemble.from_json(_single_leaf_artifact(("a",), math.log(3.0)))
        self.assertAlmostEqual(ensemble.expected_count([1.0]), 3.0, places=9)
        classifier = TreeEnsemble.from_json(
            json.dumps({"format": "sokalens-gbdt-v1", "feature_names": ["a"], "classes": ["x", "y"], "trees": [[[0.0]], [[0.0]]]})
        )
        with self.assertRaises(ValueError):
            classifier.expected_count([1.0])

    def test_model_c_speaks_to_goal_and_count_markets(self) -> None:
        form, stats = FormFeatureBook(), StatFeatureBook()
        target = _record(home=None, away=None)
        document = markets.model_c_document({**form.features(target), **stats.features(target)}, self.models)
        self.assertIn("total_goals:2.5", document)
        self.assertIn("total_corners:9.5", document)
        self.assertIn("shots_on_target:7.5", document)
        self.assertNotIn("possession_winner", document)

    def test_model_b_keeps_elo_1x2_and_adds_goals_but_no_counts(self) -> None:
        prediction = EloPrediction(
            model_id="elo",
            home_rating=1600.0,
            away_rating=1500.0,
            rating_differential=150.0,
            match_result={"home": 0.55, "draw": 0.25, "away": 0.20},
            double_chance={"home_or_draw": 0.80, "draw_or_away": 0.45, "home_or_away": 0.75},
            draw_no_bet={"home": 0.733, "away": 0.267},
            home_sample=20,
            away_sample=20,
            draw_rate=0.26,
        )
        document = markets.model_b_document(prediction, 2.7, self.models.elo_goal_map)
        self.assertEqual(document["1x2"], {"home": 0.55, "draw": 0.25, "away": 0.2})
        self.assertIn("btts", document)
        self.assertFalse([key for key in document if key.startswith(("total_corners", "total_cards", "total_shots"))])

    def test_a_stronger_home_side_is_expected_to_score_more(self) -> None:
        goal_map = self.models.elo_goal_map
        strong = markets.elo_expected_goals(300.0, 2.7, goal_map)
        even = markets.elo_expected_goals(0.0, 2.7, goal_map)
        self.assertGreater(strong[0], even[0])
        self.assertLess(strong[1], even[1])

    def test_the_forecast_pass_only_sees_results_known_before_the_target(self) -> None:
        def history():
            for fixture_id, day in ((1, 1), (2, 2)):
                yield (
                    FixtureRecord(
                        id=fixture_id,
                        kickoff_at=datetime(2030, 3, day, 15, 0, tzinfo=UTC),
                        league_id=39,
                        league_name="Premier League",
                        season=2030,
                        home_team_id=10,
                        away_team_id=20,
                        home_goals=9,
                        away_goals=0,
                        status="FT",
                        source="test",
                    ),
                    {"corners": (9, 1)},
                )

        target = markets.MarketTarget(_record(5, home=None, away=None), datetime(2030, 3, 1, 12, 0, tzinfo=UTC), "PRE_MATCH")
        seen: list[dict[str, float | None]] = []
        real = markets.model_c_document

        def capture(features, models):
            seen.append(dict(features))
            return real(features, models)

        markets.model_c_document = capture
        self.addCleanup(setattr, markets, "model_c_document", real)
        forecasts = markets.forecast_documents(history(), [target], self.models)
        self.assertEqual(len(forecasts), 1)
        self.assertIsNone(seen[0]["home_corners_for_10"], "a fixture after the as-of time must not be recorded")

    def test_history_must_be_ordered(self) -> None:
        def history():
            yield _record(2), {}
            yield _record(1), {}

        with self.assertRaises(ValueError):
            markets.forecast_documents(history(), [], self.models)

    def test_a_model_change_changes_its_identity(self) -> None:
        self.assertTrue(self.models.model_c_version.startswith(markets.MODEL_C_KEY))
        self.assertTrue(self.models.model_b_version.startswith(markets.MODEL_B_KEY))


if __name__ == "__main__":
    unittest.main()
