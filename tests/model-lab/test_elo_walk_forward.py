"""The Elo walk-forward harness: corpus mapping, scoring and the full report.

The harness imports the engine's own Model A path, so these tests also fail if
that path is renamed or reshaped, rather than the comparison silently scoring
a different champion.
"""

import csv
from datetime import datetime, timezone
import json
import math
import os
from pathlib import Path
import random
import subprocess
import sys
from tempfile import TemporaryDirectory
import unittest


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "services" / "model-lab"))
sys.path.insert(0, str(ROOT / "services" / "prediction-engine"))

from model_lab.elo_walk_forward import (  # noqa: E402
    _csv_paths,
    corpus_fixtures,
    paired_log_loss,
    run_walk_forward,
    score,
)
from model_lab.football_data import load_matches  # noqa: E402


FIELDS = ["Div", "Date", "Time", "HomeTeam", "AwayTeam", "FTHG", "FTAG", "FTR", "PSCH", "PSCD", "PSCA"]
TEAMS = ["Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot"]
STRENGTH = {"Alpha": 1.2, "Bravo": 0.6, "Charlie": 0.0, "Delta": 0.0, "Echo": -0.6, "Foxtrot": -1.2}


def write_season(root: Path, start_year: int, seed: int) -> Path:
    """One double round robin, written where the corpus keeps main leagues."""
    generator = random.Random(seed)
    folder = root / "raw" / "main" / "Testland" / f"{start_year}-{start_year + 1}"
    folder.mkdir(parents=True)
    path = folder / "T1.csv"
    rows = []
    day = 0
    for home in TEAMS:
        for away in TEAMS:
            if home == away:
                continue
            edge = STRENGTH[home] - STRENGTH[away] + 0.3
            home_goals = max(0, round(1.4 + edge + generator.uniform(-1.3, 1.3)))
            away_goals = max(0, round(1.1 - edge + generator.uniform(-1.3, 1.3)))
            result = "H" if home_goals > away_goals else "A" if away_goals > home_goals else "D"
            kickoff = datetime(start_year, 8, 10).toordinal() + day
            date = datetime.fromordinal(kickoff).strftime("%d/%m/%Y")
            rows.append(
                {
                    "Div": "T1",
                    "Date": date,
                    "Time": "15:00",
                    "HomeTeam": home,
                    "AwayTeam": away,
                    "FTHG": str(home_goals),
                    "FTAG": str(away_goals),
                    "FTR": result,
                    "PSCH": f"{1.5 + max(0.0, -edge) * 2:.2f}",
                    "PSCD": "3.40",
                    "PSCA": f"{1.5 + max(0.0, edge) * 2:.2f}",
                }
            )
            day += 7
    with path.open("w", newline="", encoding="utf-8") as handle:
        writer = csv.DictWriter(handle, fieldnames=FIELDS)
        writer.writeheader()
        writer.writerows(rows)
    return path


def write_corpus(root: Path) -> None:
    for offset, year in enumerate((2019, 2020, 2021, 2022)):
        write_season(root, year, seed=offset)
    # The corpus ships combined copies of every row; they must not be read.
    combined = root / "raw" / "main" / "_combined_main"
    combined.mkdir(parents=True)
    (combined / "all.csv").write_text(
        (root / "raw" / "main" / "Testland" / "2019-2020" / "T1.csv").read_text(encoding="utf-8"),
        encoding="utf-8",
    )


class CorpusMappingTests(unittest.TestCase):
    def test_country_and_season_come_from_the_path_and_combined_copies_are_skipped(self):
        with TemporaryDirectory() as directory:
            root = Path(directory)
            write_corpus(root)
            files = _csv_paths([root / "raw"])
            fixtures = corpus_fixtures(load_matches(files).matches)

        self.assertEqual(len(files), 4)
        self.assertFalse(any("_combined" in str(path) for path in files))
        self.assertEqual(len(fixtures), 4 * 30)
        self.assertEqual({fixture.league_key for fixture in fixtures}, {"Testland T1"})
        self.assertEqual({fixture.record.season for fixture in fixtures}, {2019, 2020, 2021, 2022})
        self.assertEqual(len({fixture.record.home_team_id for fixture in fixtures}), len(TEAMS))
        self.assertTrue(all(fixture.record.kickoff_at.tzinfo is not None for fixture in fixtures))
        self.assertTrue(all(fixture.odds is not None for fixture in fixtures))


class ScoringTests(unittest.TestCase):
    def test_a_certain_correct_forecast_scores_zero_loss(self):
        labels = {1: "home", 2: "away"}
        predictions = {
            1: {"home": 1.0, "draw": 0.0, "away": 0.0},
            2: {"home": 0.0, "draw": 0.0, "away": 1.0},
        }
        result = score(predictions, labels)

        self.assertEqual(result["fixtures"], 2)
        self.assertAlmostEqual(result["log_loss"], 0.0)
        self.assertAlmostEqual(result["brier"], 0.0)
        self.assertEqual(result["accuracy"], 1.0)

    def test_paired_difference_is_negative_when_the_challenger_is_better(self):
        labels = {index: "home" for index in range(10)}
        sharp = {index: {"home": 0.7, "draw": 0.2, "away": 0.1} for index in range(10)}
        flat = {index: {"home": 1 / 3, "draw": 1 / 3, "away": 1 / 3} for index in range(10)}
        sharp[0] = {"home": 0.6, "draw": 0.2, "away": 0.2}
        comparison = paired_log_loss(sharp, flat, labels)

        self.assertEqual(comparison["fixtures"], 10)
        self.assertLess(comparison["mean_difference"], 0.0)
        self.assertLess(comparison["z"], 0.0)


class WalkForwardReportTests(unittest.TestCase):
    def test_every_model_is_scored_on_the_same_holdout(self):
        with TemporaryDirectory() as directory:
            root = Path(directory)
            write_corpus(root)
            fixtures = corpus_fixtures(load_matches(_csv_paths([root / "raw"])).matches)
        holdout_from = datetime(2022, 7, 1, tzinfo=timezone.utc)
        report = run_walk_forward(fixtures, holdout_from=holdout_from, tuning_years=2, tune=False)

        self.assertEqual(report["holdout_fixtures"], 30)
        models = report["all_holdout"]
        expected = {
            "league_frequency", "elo", "elo_default", "model_a", "v1_blend",
            "champion", "market", "model_a_elo_mix",
        }
        self.assertEqual(set(models), expected)
        self.assertEqual({entry["fixtures"] for entry in models.values()}, {30})
        for entry in models.values():
            self.assertTrue(math.isfinite(entry["log_loss"]))
            self.assertAlmostEqual(sum(entry["mean_probability"].values()), 1.0, places=3)
        self.assertIn("elo_vs_model_a", report["paired_log_loss"])
        self.assertEqual(report["model_a_expected_goals_check"]["fixtures"], 30)
        self.assertEqual(report["by_league"]["Testland T1"]["fixtures"], 30)

    def test_tuning_never_sees_the_holdout(self):
        with TemporaryDirectory() as directory:
            root = Path(directory)
            write_corpus(root)
            fixtures = corpus_fixtures(load_matches(_csv_paths([root / "raw"])).matches)
        holdout_from = datetime(2022, 7, 1, tzinfo=timezone.utc)
        report = run_walk_forward(fixtures, holdout_from=holdout_from, tuning_years=2, champion=False)

        tuning_fixtures = {trial["fixtures"] for trial in report["tuning_trials"]}
        # Seasons 2020 and 2021 fall in the tuning window; 2022 is the holdout.
        self.assertEqual(tuning_fixtures, {60})

    def test_cli_writes_a_json_report(self):
        with TemporaryDirectory() as directory:
            root = Path(directory)
            write_corpus(root)
            output = root / "report.json"
            completed = subprocess.run(
                [
                    sys.executable,
                    "-m",
                    "model_lab.elo_walk_forward",
                    str(root / "raw"),
                    "--holdout-from",
                    "2022-07-01",
                    "--no-tune",
                    "--output",
                    str(output),
                ],
                check=True,
                capture_output=True,
                cwd=ROOT,
                env={
                    **os.environ,
                    "PYTHONPATH": os.pathsep.join(
                        [str(ROOT / "services" / "model-lab"), str(ROOT / "services" / "prediction-engine")]
                    ),
                },
                text=True,
            )
            written = json.loads(output.read_text(encoding="utf-8"))

        self.assertEqual(json.loads(completed.stdout), written)
        self.assertEqual(written["files"], 4)
        self.assertIn("elo", written["all_holdout"])


if __name__ == "__main__":
    unittest.main()
