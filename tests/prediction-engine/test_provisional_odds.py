from __future__ import annotations

from datetime import UTC, datetime
import unittest

from prediction_engine.features import FixtureRecord
from prediction_engine.provisional_odds import PROVISIONAL_BOOKMAKER, synthesize_provisional_1x2


def fixture(fixture_id: int, home: int, away: int, home_goals: int, away_goals: int) -> FixtureRecord:
    return FixtureRecord(
        id=fixture_id,
        kickoff_at=datetime(2026, 5, 1, 12, 0, tzinfo=UTC),
        league_id=39,
        league_name="Premier League",
        season=2026,
        home_team_id=home,
        away_team_id=away,
        home_goals=home_goals,
        away_goals=away_goals,
        status="FT",
    )


class ProvisionalOddsTests(unittest.TestCase):
    def test_stronger_home_team_gets_shorter_home_odds(self) -> None:
        history = [
            fixture(1, 10, 20, 3, 0),
            fixture(2, 20, 10, 0, 2),
            fixture(3, 10, 30, 2, 0),
            fixture(4, 20, 40, 0, 1),
        ]
        odds = synthesize_provisional_1x2(history, 10, 20, 99)
        self.assertLess(odds.home_win, odds.away_win)

    def test_produces_sensible_positive_odds(self) -> None:
        history = [
            fixture(1, 10, 20, 2, 1),
            fixture(2, 20, 10, 1, 1),
        ]
        odds = synthesize_provisional_1x2(history, 10, 20, 99)
        self.assertGreater(odds.home_win, 1.0)
        self.assertGreater(odds.draw, 1.0)
        self.assertGreater(odds.away_win, 1.0)

    def test_empty_history_uses_neutral_fallback(self) -> None:
        odds = synthesize_provisional_1x2([], 10, 20, 99)
        self.assertGreater(odds.home_win, 1.0)
        self.assertGreater(odds.draw, 1.0)
        self.assertGreater(odds.away_win, 1.0)

    def test_extreme_history_never_rounds_odds_to_one(self) -> None:
        history = [fixture(i, 10, 20, 20, 0) for i in range(1, 11)]
        odds = synthesize_provisional_1x2(history, 10, 20, 99)
        self.assertGreaterEqual(odds.home_win, 1.01)
        self.assertLess(1.0 / odds.home_win, 1.0)

    def test_marker_is_stable(self) -> None:
        self.assertEqual(PROVISIONAL_BOOKMAKER, "sokalens-provisional")


if __name__ == "__main__":
    unittest.main()
