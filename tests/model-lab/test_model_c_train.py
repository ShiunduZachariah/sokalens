"""Model C training pipeline: time-ordered windows and the tree export.

LightGBM is a training-only dependency, so these tests cover everything that
does not need it: the windows that enforce section 24.1's ban on random
splits, the single pass that builds feature rows, and the conversion from
LightGBM's dump to the format the serving engine reads. The parity of a real
export is checked by the training script itself before it writes an artifact.
"""

from datetime import datetime, timedelta, timezone
from pathlib import Path
import sys
import unittest


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "services" / "model-lab"))
sys.path.insert(0, str(ROOT / "services" / "prediction-engine"))

from model_lab.elo_walk_forward import CorpusFixture  # noqa: E402
from model_lab.model_c_train import FeatureRow, _flatten, feature_rows, split_windows  # noqa: E402
from prediction_engine.features import FixtureRecord  # noqa: E402
from prediction_engine.form_features import FEATURE_NAMES  # noqa: E402
from prediction_engine.gbdt import TreeEnsemble  # noqa: E402


START = datetime(2020, 8, 1, 15, 0, tzinfo=timezone.utc)


def corpus(days: int = 40) -> list[CorpusFixture]:
    fixtures = []
    for index in range(days):
        kickoff = START + timedelta(days=index)
        home, away = (1, 2) if index % 2 == 0 else (2, 1)
        # Two simultaneous fixtures per day: the second must not see the first.
        for offset, (h, a) in enumerate(((home, away), (home + 10, away + 10))):
            fixture_id = index * 2 + offset + 1
            record = FixtureRecord(
                id=fixture_id, kickoff_at=kickoff, league_id=1, league_name="Testland Premier", season=2020,
                home_team_id=h, away_team_id=a, home_goals=2, away_goals=1, status="FT",
            )
            fixtures.append(CorpusFixture(record=record, league_key="Testland T1", odds=None, result="home"))
    return fixtures


class WindowTests(unittest.TestCase):
    def test_windows_split_by_kickoff_and_never_overlap(self):
        rows = feature_rows(corpus())
        training, validation, holdout = split_windows(
            rows, validation_from=START + timedelta(days=20), holdout_from=START + timedelta(days=30)
        )
        self.assertEqual(len(training) + len(validation) + len(holdout), len(rows))
        self.assertLess(max(row.kickoff_at for row in training), min(row.kickoff_at for row in validation))
        self.assertLess(max(row.kickoff_at for row in validation), min(row.kickoff_at for row in holdout))

    def test_unordered_boundaries_are_refused(self):
        with self.assertRaises(ValueError):
            split_windows([], validation_from=START, holdout_from=START)

    def test_feature_rows_never_see_their_own_slot(self):
        rows = {row.fixture_id: row for row in feature_rows(corpus())}
        # Day 0: nobody has played yet, including the simultaneous fixture.
        self.assertIsNone(rows[1].features["home_ppg_5"])
        self.assertIsNone(rows[2].features["home_ppg_5"])
        # Day 1: team 2 (home) lost its only previous match, at the slot before.
        self.assertEqual(rows[3].features["home_ppg_5"], 0.0)
        self.assertEqual(list(rows[3].features), list(FEATURE_NAMES))
        self.assertIsInstance(rows[3], FeatureRow)


class ExportTests(unittest.TestCase):
    def test_a_lightgbm_dump_flattens_to_the_serving_format(self):
        dumped = {
            "split_feature": 0, "threshold": 1.5, "decision_type": "<=", "default_left": True,
            "missing_type": "NaN", "internal_value": 0.2,
            "left_child": {"leaf_value": -0.1},
            "right_child": {
                "split_feature": 1, "threshold": 0.0, "decision_type": "<=", "default_left": False,
                "missing_type": "None", "internal_value": 0.4,
                "left_child": {"leaf_value": 0.3}, "right_child": {"leaf_value": 0.5},
            },
        }
        nodes = _flatten(dumped)
        self.assertEqual(nodes[0], [0.0, 1.5, 1.0, 2.0, 1.0, 2.0, 0.2])
        self.assertEqual(nodes[1], [-0.1])
        model = TreeEnsemble(
            feature_names=("x", "y"), classes=("a",), trees=(tuple(tuple(node) for node in nodes),),
            metadata={}, artifact_hash="",
        )
        self.assertEqual(model.raw_scores([1.0, 5.0]), [-0.1])
        self.assertEqual(model.raw_scores([None, 5.0]), [-0.1])
        self.assertEqual(model.raw_scores([2.0, None]), [0.3])
        self.assertEqual(model.raw_scores([2.0, 1.0]), [0.5])

    def test_categorical_splits_are_refused(self):
        with self.assertRaises(ValueError):
            _flatten({"split_feature": 0, "threshold": "1||2", "decision_type": "==",
                      "left_child": {"leaf_value": 0}, "right_child": {"leaf_value": 1}})


if __name__ == "__main__":
    unittest.main()
