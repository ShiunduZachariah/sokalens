from __future__ import annotations

from datetime import datetime
import json
from pathlib import Path
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "services" / "model-lab"))
sys.path.insert(0, str(ROOT / "services" / "prediction-engine"))

from model_lab.all_market_eval import _closing_totals, outcome_probabilities, run_champion
from model_lab.all_market_stats import team_statistics
from model_lab.elo_walk_forward import ChampionReplay, corpus_fixtures
from model_lab.football_data import Match, load_matches
from prediction_engine import challenger_markets as markets
from prediction_engine.model_a import ModelAInputs, run_model_a
from prediction_engine.settlement import ActualFixtureResult


def _match(home: int, away: int, half_home: int | None, half_away: int | None, **fields: object) -> Match:
    return Match(
        source_file="main/England/2025-2026/E0.csv",
        row_number=1,
        country="England",
        league="E0",
        season="2025-2026",
        kickoff=datetime(2026, 1, 1),
        home_team="A",
        away_team="B",
        home_goals=home,
        away_goals=away,
        result="H" if home > away else "A" if away > home else "D",
        features={},
        half_time_home_goals=half_home,
        half_time_away_goals=half_away,
        **fields,
    )


class CorpusScoringMatchesProductionGradingTests(unittest.TestCase):
    """The holdout evidence is only worth anything if live shadow grades the same way."""

    def test_every_shared_market_gives_the_same_probability(self) -> None:
        result = run_model_a(ModelAInputs(home_lambda=1.6, away_lambda=1.05))
        document = markets.goal_document(result.markets, result.score_matrix)
        for home, away, half_home, half_away in ((2, 1, 1, 1), (0, 0, 0, 0), (1, 1, 0, 1), (0, 3, 0, 2), (4, 2, 3, 0), (1, 0, 1, 0)):
            corpus = outcome_probabilities(result.markets, result.score_matrix, _match(home, away, half_home, half_away))
            live = markets.grade(
                document, ActualFixtureResult(home_goals=home, away_goals=away, home_goals_ht=half_home, away_goals_ht=half_away)
            )
            shared = set(corpus) & set(live)
            self.assertGreaterEqual(len(shared), 15)
            self.assertEqual(set(corpus) - set(live), set())
            for market in shared:
                self.assertAlmostEqual(float(corpus[market][1]), float(live[market][1]), places=3, msg=f"{market} at {home}-{away}")

    def test_a_drawn_draw_no_bet_is_left_out_of_both(self) -> None:
        result = run_model_a(ModelAInputs(home_lambda=1.2, away_lambda=1.2))
        self.assertNotIn("draw_no_bet", outcome_probabilities(result.markets, result.score_matrix, _match(1, 1, 0, 0)))


class ChampionReplayTests(unittest.TestCase):
    def test_the_champion_row_scores_the_markets_production_publishes(self) -> None:
        """Scoring the blend here once made the champion look worse than Elo on double chance."""
        # A short-priced home favourite that the goals alone do not rate, so the anchor shows.
        rows =["Div,Date,Time,HomeTeam,AwayTeam,FTHG,FTAG,FTR,AvgCH,AvgCD,AvgCA"]
        for month, (home, away, home_goals, away_goals) in enumerate(
            (("A", "B", 0, 2), ("B", "A", 3, 0), ("A", "B", 1, 1), ("B", "A", 2, 1), ("A", "B", 2, 1)), start=1
        ):
            rows.append(f"E0,01/{month:02d}/2023,15:00,{home},{away},{home_goals},{away_goals},{'H' if home_goals > away_goals else 'A' if away_goals > home_goals else 'D'},1.25,6.50,11.00")
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "E0.csv"
            path.write_text("\n".join(rows) + "\n", encoding="utf-8")
            out = Path(directory) / "champion.jsonl"
            run_champion([path], out)
            last = [json.loads(line) for line in out.read_text(encoding="utf-8").splitlines()][-1]["scored"]
        # The home side won, so these are the probabilities given to home, and the
        # draw-or-away selection missed: every number is the published 1X2 restated.
        home = last["1x2"][1]
        self.assertEqual(last["1x2"][0], "home")
        self.assertAlmostEqual(last["double_chance:draw_or_away"][1], home)
        # De-vigged, 1.25 implies 0.766, and the anchor takes half of it at the least.
        self.assertGreater(home, 0.38, "the favourite's price has reached the published 1X2")
        away = home / last["draw_no_bet"][1] - home
        self.assertAlmostEqual(last["double_chance:home_or_draw"][1], 1 - away)


class ChampionReplayOrderTests(unittest.TestCase):
    def test_a_forecast_does_not_depend_on_which_fixture_ran_before_it(self) -> None:
        # Two matches in one league at one kick-off, between teams with different goal
        # histories. The pipeline builds a new assembler per fixture, so neither may
        # inherit the other's league baseline.
        rows = [
            "Div,Date,Time,HomeTeam,AwayTeam,FTHG,FTAG,FTR,AvgCH,AvgCD,AvgCA",
            "E0,01/09/2023,15:00,A,B,5,4,H,2.00,3.40,3.60",
            "E0,08/09/2023,15:00,B,A,4,3,H,2.00,3.40,3.60",
            "E0,01/09/2023,17:00,C,D,0,0,D,2.00,3.40,3.60",
            "E0,08/09/2023,17:00,D,C,1,0,H,2.00,3.40,3.60",
            "E0,15/09/2023,15:00,A,B,1,1,D,2.00,3.40,3.60",
            "E0,15/09/2023,15:00,C,D,1,1,D,2.00,3.40,3.60",
        ]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "E0.csv"
            path.write_text("\n".join(rows) + "\n", encoding="utf-8")
            fixtures = corpus_fixtures(load_matches([path]).matches)
        first, second = fixtures[-2:]

        alone = ChampionReplay(fixtures).predict(second)
        shared = ChampionReplay(fixtures)
        shared.predict(first)

        self.assertEqual(shared.predict(second), alone)


class CountsTests(unittest.TestCase):
    def test_cards_count_yellow_plus_red_as_settlement_does(self) -> None:
        stats = team_statistics(_match(1, 0, 0, 0, home_cards=2, away_cards=1, home_red_cards=1, away_red_cards=None))
        self.assertEqual(stats["cards"], (3, 1))

    def test_a_missing_statistic_stays_missing(self) -> None:
        stats = team_statistics(_match(1, 0, 0, 0))
        self.assertEqual(stats["cards"], (None, None))
        self.assertEqual(stats["corners"], (None, None))


class LoaderTests(unittest.TestCase):
    def test_the_loader_reads_shots_reds_and_total_goal_odds(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "E0.csv"
            path.write_text(
                "Div,Date,Time,HomeTeam,AwayTeam,FTHG,FTAG,FTR,HS,AS,HST,AST,HR,AR,Avg>2.5,Avg<2.5,AvgC>2.5,AvgC<2.5\n"
                "E0,01/01/2026,15:00,A,B,2,1,H,12,9,5,3,1,0,1.90,1.95,1.80,2.05\n",
                encoding="utf-8",
            )
            match = load_matches([path]).matches[0]
        self.assertEqual((match.home_shots, match.away_shots), (12, 9))
        self.assertEqual((match.home_shots_on_target, match.away_shots_on_target), (5, 3))
        self.assertEqual((match.home_red_cards, match.away_red_cards), (1, 0))
        self.assertEqual((match.odds_over_2_5, match.odds_under_2_5), (1.80, 2.05), "closing prices come first")
        odds = _closing_totals(match)
        self.assertAlmostEqual(odds["over"] + odds["under"], 1.0)
        self.assertGreater(odds["over"], odds["under"])


if __name__ == "__main__":
    unittest.main()
