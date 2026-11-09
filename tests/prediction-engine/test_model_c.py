"""Model C: its features, the stdlib tree evaluator and the shipped artifact.

The artifact is the trained model itself, so these tests are also the
serving-side half of the train/serve contract: the feature names it was
trained on must be exactly the ones `form_features` produces, in order.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
import json
import math
import random
import unittest

from prediction_engine.features import FixtureRecord
from prediction_engine.form_features import FEATURE_NAMES, FEATURE_VERSION, FormFeatureBook
from prediction_engine.gbdt import ARTIFACT_FORMAT, TreeEnsemble
from prediction_engine.model_c import MARKET_FAMILY, MODEL_KEY, load_model, run_model_c


START = datetime(2024, 8, 1, 15, 0, tzinfo=UTC)


def fixture(fixture_id: int, home: int, away: int, *, days: float, home_goals=None, away_goals=None,
            status: str = "FT", league: int = 1, league_name: str = "Testland Premier", season: int = 2024) -> FixtureRecord:
    return FixtureRecord(
        id=fixture_id,
        kickoff_at=START + timedelta(days=days),
        league_id=league,
        league_name=league_name,
        season=season,
        home_team_id=home,
        away_team_id=away,
        home_goals=home_goals,
        away_goals=away_goals,
        status=status,
    )


class FormFeatureTests(unittest.TestCase):
    def test_a_team_with_no_history_is_missing_not_zero(self):
        row = FormFeatureBook().features(fixture(1, 10, 20, days=0, status="NS"))
        self.assertEqual(list(row), list(FEATURE_NAMES))
        for name in ("home_ppg_5", "home_rest_days", "away_season_ppg", "ppg_5_difference"):
            self.assertIsNone(row[name], name)
        self.assertEqual(row["home_season_played"], 0.0)
        self.assertEqual(row["home_known_matches"], 0.0)

    def test_recent_form_counts_only_the_last_five(self):
        book = FormFeatureBook()
        # Team 10: four losses, then six wins at home against team 30.
        for index in range(4):
            book.record(fixture(index + 1, 10, 30, days=index * 7, home_goals=0, away_goals=1))
        for index in range(4, 10):
            book.record(fixture(index + 1, 10, 30, days=index * 7, home_goals=2, away_goals=0))
        row = book.features(fixture(99, 10, 20, days=70, status="NS"))
        self.assertEqual(row["home_ppg_5"], 3.0)
        self.assertAlmostEqual(row["home_ppg_10"], 18 / 10)
        self.assertAlmostEqual(row["home_goals_for_5"], 2.0)
        self.assertEqual(row["home_season_played"], 10.0)
        self.assertEqual(row["home_rest_days"], 7.0)

    def test_rest_days_are_capped_and_season_resets(self):
        book = FormFeatureBook()
        book.record(fixture(1, 10, 30, days=0, home_goals=1, away_goals=1, season=2024))
        row = book.features(fixture(2, 10, 20, days=90, status="NS", season=2025))
        self.assertEqual(row["home_rest_days"], 30.0)
        self.assertIsNone(row["home_season_ppg"])
        self.assertEqual(row["home_ppg_5"], 1.0)

    def test_unfinished_and_friendly_fixtures_are_ignored(self):
        book = FormFeatureBook()
        book.record(fixture(1, 10, 30, days=0, status="NS"))
        book.record(fixture(2, 10, 30, days=1, home_goals=5, away_goals=0, league_name="Club Friendlies"))
        row = book.features(fixture(3, 10, 20, days=2, status="NS"))
        self.assertIsNone(row["home_ppg_5"])


def _tiny_artifact() -> str:
    # One iteration of a two-class model: tree 0 is class "a", tree 1 class "b".
    return json.dumps(
        {
            "format": ARTIFACT_FORMAT,
            "feature_names": ["x", "y"],
            "classes": ["a", "b"],
            "model_version": "tiny",
            "trees": [
                # x <= 1.0 ? (NaN goes left) 1.0 : 3.0 ; root value 2.0
                [[0, 1.0, 1, 2, 1, 2, 2.0], [1.0], [3.0]],
                # y <= 0.0 ? (missing type None: NaN becomes 0) -1.0 : 0.5
                [[1, 0.0, 1, 2, 0, 0, 0.0], [-1.0], [0.5]],
            ],
        }
    )


class TreeEvaluatorTests(unittest.TestCase):
    def test_routing_follows_lightgbm_missing_value_rules(self):
        model = TreeEnsemble.from_json(_tiny_artifact())
        self.assertEqual(model.raw_scores([0.5, 1.0]), [1.0, 0.5])
        self.assertEqual(model.raw_scores([2.0, -1.0]), [3.0, -1.0])
        # NaN with missing type NaN takes the default (left) branch; with type
        # None it is compared as zero, and 0 <= 0 goes left.
        self.assertEqual(model.raw_scores([None, None]), [1.0, -1.0])
        self.assertEqual(model.raw_scores([math.nan, math.nan]), [1.0, -1.0])

    def test_probabilities_are_a_softmax_of_the_class_scores(self):
        model = TreeEnsemble.from_json(_tiny_artifact())
        probabilities = model.probabilities([0.5, 1.0])
        self.assertAlmostEqual(sum(probabilities.values()), 1.0)
        self.assertAlmostEqual(probabilities["a"], 1 / (1 + math.exp(-0.5)))

    def test_contributions_and_base_sum_to_the_raw_score(self):
        model = TreeEnsemble.from_json(_tiny_artifact())
        base, credit = model.contributions([2.0, 1.0], "a")
        self.assertEqual(base, 2.0)
        self.assertEqual(credit, {"x": 1.0})
        self.assertAlmostEqual(base + sum(credit.values()), model.raw_scores([2.0, 1.0])[0])

    def test_malformed_artifacts_are_refused(self):
        payload = json.loads(_tiny_artifact())
        with self.assertRaises(ValueError):
            TreeEnsemble.from_json(json.dumps({**payload, "format": "other"}))
        broken = json.loads(_tiny_artifact())
        broken["trees"][0][0][0] = 7
        with self.assertRaises(ValueError):
            TreeEnsemble.from_json(json.dumps(broken))
        with self.assertRaises(ValueError):
            TreeEnsemble.from_json(json.dumps({**payload, "trees": payload["trees"][:1]}))

    def test_a_vector_must_carry_every_feature(self):
        model = TreeEnsemble.from_json(_tiny_artifact())
        with self.assertRaises(ValueError):
            model.vector({"x": 1.0})


def _row(**overrides: float | None) -> dict[str, float | None]:
    row: dict[str, float | None] = {name: None for name in FEATURE_NAMES}
    row.update(
        league_home_win_rate=0.45, league_draw_rate=0.26, league_goals_per_match=2.7,
        home_season_played=15.0, away_season_played=15.0, home_known_matches=50.0, away_known_matches=50.0,
        home_rest_days=7.0, away_rest_days=7.0,
    )
    row.update(overrides)
    return row


def _form(side: str, ppg: float, goals_for: float, goals_against: float) -> dict[str, float]:
    return {
        f"{side}_ppg_5": ppg, f"{side}_ppg_10": ppg, f"{side}_season_ppg": ppg, f"{side}_venue_ppg_10": ppg,
        f"{side}_goals_for_5": goals_for, f"{side}_goals_against_5": goals_against,
        f"{side}_season_goals_for": goals_for, f"{side}_season_goals_against": goals_against,
        f"{side}_goal_difference_10": goals_for - goals_against,
    }


def _mismatch(strong: str) -> dict[str, float | None]:
    weak = "away" if strong == "home" else "home"
    row = _row(**_form(strong, 2.4, 2.3, 0.7), **_form(weak, 0.6, 0.8, 2.0))
    sign = 1 if strong == "home" else -1
    for name in ("ppg_5", "ppg_10", "season_ppg"):
        row[f"{name}_difference"] = sign * 1.8
    row["goal_difference_10_difference"] = sign * 2.8
    return row


class ShippedArtifactTests(unittest.TestCase):
    def setUp(self):
        self.model = load_model()

    def test_the_artifact_matches_the_serving_features(self):
        self.assertEqual(self.model.feature_names, FEATURE_NAMES)
        self.assertEqual(self.model.classes, ("home", "draw", "away"))
        self.assertEqual(self.model.metadata["model_key"], MODEL_KEY)
        self.assertEqual(self.model.metadata["market_family"], MARKET_FAMILY)
        self.assertEqual(self.model.metadata["feature_version"], FEATURE_VERSION)
        self.assertTrue(str(self.model.metadata["model_version"]).startswith("model-c-"))

    def test_an_obvious_mismatch_points_the_right_way(self):
        home_strong = run_model_c(_mismatch("home"), self.model).match_result
        away_strong = run_model_c(_mismatch("away"), self.model).match_result
        self.assertGreater(home_strong["home"], 0.6)
        self.assertGreater(away_strong["away"], 0.45)
        self.assertGreater(home_strong["home"], away_strong["home"])

    def test_an_empty_history_still_yields_a_valid_distribution(self):
        result = run_model_c({name: None for name in FEATURE_NAMES}, self.model)
        self.assertAlmostEqual(sum(result.match_result.values()), 1.0)
        self.assertTrue(all(0.0 < value < 1.0 for value in result.match_result.values()))
        self.assertAlmostEqual(sum(result.draw_no_bet.values()), 1.0)

    def test_attribution_is_exact_on_the_shipped_trees(self):
        generator = random.Random(7)
        for _ in range(20):
            row = _row(**_form("home", generator.uniform(0, 3), generator.uniform(0, 3), generator.uniform(0, 3)),
                       **_form("away", generator.uniform(0, 3), generator.uniform(0, 3), generator.uniform(0, 3)))
            vector = self.model.vector(row)
            for index, outcome in enumerate(self.model.classes):
                base, credit = self.model.contributions(vector, outcome)
                self.assertAlmostEqual(base + sum(credit.values()), self.model.raw_scores(vector)[index], places=9)

    def test_top_factors_move_the_prediction_more_than_bottom_factors(self):
        # Phase 6 gate: perturbing a top contributor should move the forecast;
        # perturbing the weakest contributor should barely move it.
        for strong in ("home", "away"):
            row = _mismatch(strong)
            result = run_model_c(row, self.model)
            pick = result.explanation_factors["explained_outcome"]
            _, credit = self.model.contributions(self.model.vector(row), pick)
            known = {name: value for name, value in credit.items() if row[name] is not None}
            top = max(known, key=lambda name: abs(known[name]))
            bottom = min(known, key=lambda name: abs(known[name]))
            baseline = result.match_result[pick]

            def shift(name: str) -> float:
                return abs(run_model_c(dict(row, **{name: None}), self.model).match_result[pick] - baseline)

            self.assertEqual(result.explanation_factors["top_positive"][0]["feature"] if known[top] > 0 else top, top)
            self.assertGreater(shift(top), 0.005)
            self.assertLess(shift(bottom), shift(top) / 3)


if __name__ == "__main__":
    unittest.main()
