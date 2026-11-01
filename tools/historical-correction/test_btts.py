"""Classification tests for the bounded historical BTTS correction."""

import importlib.util
import sys
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("historical_btts", Path(__file__).with_name("btts.py"))
btts = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = btts
spec.loader.exec_module(btts)


def row(**overrides):
    value = {
        "prediction_id": "00000000-0000-0000-0000-000000000001",
        "fixture_id": 1, "selection": "yes_over_0.5",
        "fact_actual": "yes_under_0.5", "fact_classification": "incorrect",
        "fixture_status": "FT", "home_goals_ft": 6, "away_goals_ft": 2,
        "parent_present": True, "result_status": "settled",
        "parent_market": "btts_total_goals", "parent_selection": "yes_over_0.5",
        "result_actual": "yes_under_0.5", "result_is_correct": False,
        "result_market": "btts_total_goals", "result_selection": "yes_over_0.5",
        "result_void_reason": None,
        "revision_id": None, "revision_status": None,
        "revision_actual": None, "revision_is_correct": None,
        "revision_void_reason": None, "ledger_present": False,
        "included_in_accuracy": None, "aggregate_date": None,
        "ledger_market": None, "league_id": None,
        "confidence_tier": None, "model_version": None,
        "counted_as": None, "needs_recount": None,
    }
    value.update(overrides)
    return value


class CorrectionTests(unittest.TestCase):
    def test_each_supported_half_line_is_graded_against_own_line(self):
        for line in range(6):
            with self.subTest(line=line):
                actual, correct = btts.grade(f"yes_over_{line}.5", 6, 2)
                self.assertEqual(actual, f"yes_over_{line}.5")
                self.assertTrue(correct)
                self.assertEqual(btts.grade(f"no_under_{line}.5", 0, 0)[0],
                                 f"no_under_{line}.5")

    def test_unaggregated_survivor_and_exact_bucket_survivor(self):
        self.assertEqual(btts.classify(row())["classification"],
                         "eligible_unaggregated_survivor")
        counted = row(ledger_present=True, included_in_accuracy=True,
                      aggregate_date="2026-09-23", ledger_market="btts_total_goals",
                      league_id=1, confidence_tier="strong", model_version="v1",
                      counted_as="incorrect", needs_recount=False)
        self.assertEqual(btts.classify(counted)["classification"], "eligible_survivor")
        self.assertEqual(btts.classify({**counted, "aggregate_date": None})["classification"],
                         "missing_accuracy_bucket_provenance")
        self.assertEqual(btts.classify({**counted, "counted_as": "correct"})["classification"],
                         "ledger_result_mismatch")

    def test_pruned_and_unsettled_rows_fail_closed(self):
        self.assertEqual(btts.classify(row(parent_present=False))["classification"],
                         "raw_parent_pruned")
        self.assertEqual(btts.classify(row(result_status="void"))["classification"],
                         "not_settled_original")
        self.assertEqual(btts.classify(row(home_goals_ft=None))["classification"],
                         "missing_official_score")
        self.assertEqual(btts.classify(row(selection="bad_2.5"))["classification"],
                         "unsupported_selection")
        self.assertEqual(btts.classify(row(result_actual="yes_over_0.5",
                                           result_is_correct=True))["classification"],
                         "already_correct")

    def test_frozen_manifest_batches_are_deterministic(self):
        eligible = btts.classify(row())
        deferred = btts.classify(row(parent_present=False))
        manifest = {"rows": [eligible, deferred, eligible]}
        self.assertEqual(btts.eligible_rows(manifest), [eligible, eligible])
        self.assertEqual(btts.batch_sha256(btts.eligible_rows(manifest)),
                         btts.batch_sha256([eligible, eligible]))
        self.assertNotEqual(btts.batch_sha256([eligible]),
                            btts.batch_sha256([eligible, eligible]))


if __name__ == "__main__":
    unittest.main()
