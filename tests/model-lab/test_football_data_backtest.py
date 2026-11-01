import csv
import json
import sys
import unittest
from pathlib import Path
from tempfile import TemporaryDirectory


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "services" / "model-lab"))

from model_lab.football_data import load_matches, run_backtest


class FootballDataBacktestTests(unittest.TestCase):
    def write_csv(self, directory, name, rows, fieldnames):
        path = Path(directory) / name
        with path.open("w", newline="", encoding="utf-8") as handle:
            writer = csv.DictWriter(handle, fieldnames=fieldnames)
            writer.writeheader()
            writer.writerows(rows)
        return path

    def test_normalizes_main_and_extra_csv_fields_without_post_match_features(self):
        with TemporaryDirectory() as directory:
            main = self.write_csv(
                directory,
                "main.csv",
                [
                    {
                        "Div": "B1",
                        "Date": "04/08/95",
                        "HomeTeam": "Aalst",
                        "AwayTeam": "Anderlecht",
                        "FTHG": "3",
                        "FTAG": "1",
                        "FTR": "H",
                        "PSCH": "2.10",
                        "PSCD": "3.10",
                        "PSCA": "3.80",
                        "HS": "12",
                        "AS": "4",
                        "HY": "1",
                    }
                ],
                ["Div", "Date", "HomeTeam", "AwayTeam", "FTHG", "FTAG", "FTR", "PSCH", "PSCD", "PSCA", "HS", "AS", "HY"],
            )
            extra = self.write_csv(
                directory,
                "extra.csv",
                [
                    {
                        "Country": "Argentina",
                        "League": "Liga Profesional",
                        "Season": "2012/2013",
                        "Date": "03/08/2012",
                        "Time": "23:00",
                        "Home": "Arsenal Sarandi",
                        "Away": "Union de Santa Fe",
                        "HG": "1",
                        "AG": "0",
                        "Res": "H",
                    }
                ],
                ["Country", "League", "Season", "Date", "Time", "Home", "Away", "HG", "AG", "Res"],
            )

            result = load_matches([main, extra])

        self.assertEqual(len(result.matches), 2)
        first = result.matches[0]
        self.assertEqual(first.home_team, "Aalst")
        self.assertEqual(first.away_team, "Anderlecht")
        self.assertEqual(first.home_goals, 3)
        self.assertEqual(first.away_goals, 1)
        self.assertEqual(first.result, "H")
        self.assertEqual(first.odds_home, 2.1)
        self.assertEqual(first.odds_draw, 3.1)
        self.assertEqual(first.odds_away, 3.8)
        self.assertEqual(first.features["home_team"], "Aalst")
        self.assertNotIn("HS", first.features)
        self.assertNotIn("AS", first.features)
        self.assertNotIn("HY", first.features)
        self.assertEqual(result.matches[1].country, "Argentina")
        self.assertEqual(result.matches[1].season, "2012/2013")

    def test_skips_missing_results_and_duplicate_rows_with_counts(self):
        with TemporaryDirectory() as directory:
            path = self.write_csv(
                directory,
                "fixtures.csv",
                [
                    {"Div": "B1", "Date": "04/08/95", "HomeTeam": "Aalst", "AwayTeam": "Anderlecht", "FTHG": "3", "FTAG": "1", "FTR": "H"},
                    {"Div": "B1", "Date": "04/08/95", "HomeTeam": "Aalst", "AwayTeam": "Anderlecht", "FTHG": "3", "FTAG": "1", "FTR": "H"},
                    {"Div": "B1", "Date": "05/08/95", "HomeTeam": "Cercle Brugge", "AwayTeam": "Antwerp", "FTHG": "", "FTAG": "", "FTR": ""},
                ],
                ["Div", "Date", "HomeTeam", "AwayTeam", "FTHG", "FTAG", "FTR"],
            )

            result = load_matches([path])

        self.assertEqual(len(result.matches), 1)
        self.assertEqual(result.duplicate_rows, 1)
        self.assertEqual(result.missing_result_rows, 1)

    def test_runs_deterministic_baseline_over_chronological_split(self):
        with TemporaryDirectory() as directory:
            path = self.write_csv(
                directory,
                "fixtures.csv",
                [
                    {"Div": "T1", "Date": "01/01/20", "HomeTeam": "Alpha", "AwayTeam": "Beta", "FTHG": "2", "FTAG": "0", "FTR": "H"},
                    {"Div": "T1", "Date": "02/01/20", "HomeTeam": "Beta", "AwayTeam": "Alpha", "FTHG": "0", "FTAG": "2", "FTR": "A"},
                    {"Div": "T1", "Date": "03/01/20", "HomeTeam": "Alpha", "AwayTeam": "Beta", "FTHG": "1", "FTAG": "0", "FTR": "H"},
                    {"Div": "T1", "Date": "04/01/20", "HomeTeam": "Beta", "AwayTeam": "Alpha", "FTHG": "0", "FTAG": "1", "FTR": "A"},
                ],
                ["Div", "Date", "HomeTeam", "AwayTeam", "FTHG", "FTAG", "FTR"],
            )
            matches = load_matches([path]).matches

        first = run_backtest(matches, train_fraction=0.5)
        second = run_backtest(list(reversed(matches)), train_fraction=0.5)

        self.assertEqual([p.predicted_result for p in first.predictions], ["H", "A"])
        self.assertEqual(first.total_tested, 2)
        self.assertEqual(first.correct, 2)
        self.assertEqual(first.accuracy, 1.0)
        self.assertEqual(
            [(p.match.home_team, p.match.away_team, p.predicted_result) for p in first.predictions],
            [(p.match.home_team, p.match.away_team, p.predicted_result) for p in second.predictions],
        )

    def test_parser_handles_windows_1252_encoded_files(self):
        with TemporaryDirectory() as directory:
            path = Path(directory) / "encoded.csv"
            path.write_bytes(
                (
                    "Div,Date,HomeTeam,AwayTeam,FTHG,FTAG,FTR\n"
                    "D1,12/08/00,Köln,Bayern\xa0Munich,1,2,A\n"
                ).encode("cp1252")
            )

            result = load_matches([path])

        self.assertEqual(len(result.matches), 1)
        self.assertEqual(result.matches[0].home_team, "Köln")
        self.assertEqual(result.matches[0].away_team, "Bayern Munich")
        self.assertEqual(result.report.parsed_files, 1)
        self.assertEqual(result.report.failed_files, [])

    def test_parser_ignores_malformed_extra_columns_and_counts_bad_rows(self):
        with TemporaryDirectory() as directory:
            path = Path(directory) / "ragged.csv"
            path.write_text(
                "\n".join(
                    [
                        "Div,Date,HomeTeam,AwayTeam,FTHG,FTAG,FTR",
                        "B1,04/08/95,Aalst,Anderlecht,3,1,H,unexpected,trailing",
                        "B1,bad-date,Bad Date,Antwerp,1,0,H",
                    ]
                ),
                encoding="utf-8",
            )

            result = load_matches([path])

        self.assertEqual(len(result.matches), 1)
        self.assertEqual(result.matches[0].home_team, "Aalst")
        self.assertEqual(result.report.parsed_files, 1)
        self.assertEqual(result.report.failed_files, [])
        self.assertEqual(result.report.row_error_count, 1)

    def test_parser_report_records_failed_files_without_crashing(self):
        with TemporaryDirectory() as directory:
            valid = self.write_csv(
                directory,
                "valid.csv",
                [
                    {
                        "Div": "T1",
                        "Date": "01/01/20",
                        "HomeTeam": "Alpha",
                        "AwayTeam": "Beta",
                        "FTHG": "2",
                        "FTAG": "0",
                        "FTR": "H",
                    }
                ],
                ["Div", "Date", "HomeTeam", "AwayTeam", "FTHG", "FTAG", "FTR"],
            )
            missing = Path(directory) / "missing.csv"

            result = load_matches([valid, missing])

        self.assertEqual(len(result.matches), 1)
        self.assertEqual(result.report.parsed_files, 1)
        self.assertEqual(len(result.report.failed_files), 1)
        self.assertEqual(result.report.failed_files[0].path, str(missing))

    def test_odds_fallback_ignores_invalid_primary_cells(self):
        with TemporaryDirectory() as directory:
            path = self.write_csv(
                directory,
                "odds-fallback.csv",
                [
                    {
                        "Div": "T1",
                        "Date": "01/01/20",
                        "HomeTeam": "Alpha",
                        "AwayTeam": "Beta",
                        "FTHG": "2",
                        "FTAG": "1",
                        "FTR": "H",
                        "PSCH": "#",
                        "PSCD": "#",
                        "PSCA": "#",
                        "AvgCH": "2.40",
                        "AvgCD": "3.20",
                        "AvgCA": "3.10",
                    }
                ],
                ["Div", "Date", "HomeTeam", "AwayTeam", "FTHG", "FTAG", "FTR", "PSCH", "PSCD", "PSCA", "AvgCH", "AvgCD", "AvgCA"],
            )

            result = load_matches([path])

        self.assertEqual(len(result.matches), 1)
        self.assertEqual(result.report.row_error_count, 0)
        self.assertEqual(result.matches[0].odds_home, 2.4)
        self.assertEqual(result.matches[0].odds_draw, 3.2)
        self.assertEqual(result.matches[0].odds_away, 3.1)

    def test_cli_prints_parser_report_fields(self):
        with TemporaryDirectory() as directory:
            path = self.write_csv(
                directory,
                "fixtures.csv",
                [
                    {
                        "Div": "T1",
                        "Date": "01/01/20",
                        "HomeTeam": "Alpha",
                        "AwayTeam": "Beta",
                        "FTHG": "2",
                        "FTAG": "0",
                        "FTR": "H",
                    },
                    {
                        "Div": "T1",
                        "Date": "bad-date",
                        "HomeTeam": "Broken",
                        "AwayTeam": "Beta",
                        "FTHG": "1",
                        "FTAG": "0",
                        "FTR": "H",
                    },
                ],
                ["Div", "Date", "HomeTeam", "AwayTeam", "FTHG", "FTAG", "FTR"],
            )

            completed = __import__("subprocess").run(
                [
                    sys.executable,
                    "-m",
                    "model_lab.football_data",
                    str(path),
                ],
                check=True,
                capture_output=True,
                cwd=ROOT,
                env={**__import__("os").environ, "PYTHONPATH": str(ROOT / "services" / "model-lab")},
                text=True,
            )

        payload = json.loads(completed.stdout)
        self.assertIn("tested_matches", payload)
        self.assertIn("successful_predictions", payload)
        self.assertIn("failed_predictions", payload)
        self.assertIn("hit_rate", payload)
        self.assertEqual(payload["tested_matches"], payload["total_tested"])
        self.assertEqual(payload["successful_predictions"], payload["correct"])
        self.assertEqual(payload["failed_predictions"], payload["total_tested"] - payload["correct"])
        self.assertEqual(payload["hit_rate"], payload["accuracy"])
        self.assertEqual(payload["parser_report"]["parsed_files"], 1)
        self.assertEqual(payload["parser_report"]["failed_files"], [])
        self.assertEqual(payload["parser_report"]["row_error_count"], 1)
        self.assertIn("one_x_two_benchmarks", payload)
        self.assertIn("odds_favorite_accuracy", payload["one_x_two_benchmarks"])
        self.assertIn("logistic_regression_accuracy", payload["one_x_two_benchmarks"])
        self.assertIn("logistic_regression_total", payload["one_x_two_benchmarks"])
        self.assertIn("logistic_regression_coverage", payload["one_x_two_benchmarks"])


if __name__ == "__main__":
    unittest.main()
