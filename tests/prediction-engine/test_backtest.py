from __future__ import annotations

import unittest

from prediction_engine.backtest import BacktestPrediction, run_backtest
from prediction_engine.persistence import ConfidenceTier


class BacktestTests(unittest.TestCase):
    def test_backtest_settles_predictions_and_summarizes_accuracy_by_confidence_tier(self) -> None:
        predictions = [
            BacktestPrediction("1", "1x2", "home", ConfidenceTier.STRONG),
            BacktestPrediction("2", "1x2", "home", ConfidenceTier.STRONG),
            BacktestPrediction("3", "1x2", "home", ConfidenceTier.MEDIUM),
            BacktestPrediction("4", "1x2", "home", ConfidenceTier.LOW),
        ]

        result = run_backtest(predictions, actual_outcome=lambda row: row.prediction if row.fixture_id != "4" else "away")

        self.assertEqual(result.total_predictions, 4)
        self.assertAlmostEqual(result.overall_accuracy, 0.75)
        self.assertAlmostEqual(result.accuracy_by_tier[ConfidenceTier.STRONG], 1.0)
        self.assertAlmostEqual(result.accuracy_by_tier[ConfidenceTier.MEDIUM], 1.0)
        self.assertAlmostEqual(result.accuracy_by_tier[ConfidenceTier.LOW], 0.0)

    def test_confidence_ordering_check_flags_inverted_tiers(self) -> None:
        predictions = [
            BacktestPrediction("1", "1x2", "home", ConfidenceTier.STRONG),
            BacktestPrediction("2", "1x2", "home", ConfidenceTier.LOW),
        ]

        result = run_backtest(predictions, actual_outcome=lambda row: "away" if row.confidence_tier is ConfidenceTier.STRONG else "home")

        self.assertFalse(result.confidence_ordering_holds)


if __name__ == "__main__":
    unittest.main()
