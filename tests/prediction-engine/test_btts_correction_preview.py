from io import StringIO
from contextlib import redirect_stderr
import json
import unittest
from uuid import UUID

from prediction_engine.cli import main
from prediction_engine.db_repository import PredictionSettlementRepository


class ReadOnlyConnection:
    def __init__(self, rows):
        self.rows = rows
        self.statements = []
        self.rollbacks = 0

    def execute(self, sql, params):
        if not sql.lstrip().startswith("SELECT"):
            raise AssertionError("preview attempted a write")
        self.statements.append((sql, params))
        return self

    def fetchall(self):
        return self.rows

    def rollback(self):
        self.rollbacks += 1

    def commit(self):
        raise AssertionError("preview committed")


def candidate(**changes):
    return dict(dict(id=UUID("e569d949-dbf5-4daa-966d-722f36d04480"), fixture_id=42, market="btts_total_goals",
                     prediction="yes_over_0.5", previous_actual="yes_over_2.5",
                     previous_is_correct=False, settlement_status="settled",
                     status="FT", home_goals_ft=6, away_goals_ft=2,
                     source="prediction_results"), **changes)


class BttsCorrectionPreviewTests(unittest.TestCase):
    def test_reports_false_loss_without_writes(self):
        connection = ReadOnlyConnection([candidate()])
        report = PredictionSettlementRepository(connection).preview_btts_corrections(42)
        self.assertEqual(report["changed"], 1)
        self.assertEqual(report["rows"][0]["previous_is_correct"], False)
        self.assertEqual(report["rows"][0]["proposed_is_correct"], True)
        self.assertEqual(report["rows"][0]["proposed_actual"], "yes_over_0.5")
        self.assertEqual(report["rows"][0]["prediction_id"], "e569d949-dbf5-4daa-966d-722f36d04480")
        self.assertTrue(report["dry_run"])
        self.assertFalse(report["truncated"])
        self.assertEqual(connection.rollbacks, 1)

    def test_excludes_void_unfinished_missing_score_and_other_markets(self):
        for changes in ({"settlement_status": "void"}, {"status": "HT"},
                        {"home_goals_ft": None}, {"home_goals_ft": -1},
                        {"market": "total_cards"}):
            with self.subTest(changes=changes):
                connection = ReadOnlyConnection([candidate(**changes)])
                report = PredictionSettlementRepository(connection).preview_btts_corrections(42)
                self.assertEqual(report["changed"], 0)
                self.assertEqual(report["skipped"], 1)

    def test_includes_unchanged_rows_and_marks_bounded_report(self):
        connection = ReadOnlyConnection([
            candidate(prediction="yes_over_2.5", previous_is_correct=True), candidate(id=124)])
        report = PredictionSettlementRepository(connection).preview_btts_corrections(42, limit=1)
        self.assertEqual(report["examined"], 1)
        self.assertEqual(report["changed"], 0)
        self.assertTrue(report["truncated"])

    def test_rejects_invalid_scope_before_reading(self):
        for fixture_id, limit in ((0, 10), (42, 0), (42, 1001)):
            connection = ReadOnlyConnection([])
            with self.assertRaises(ValueError):
                PredictionSettlementRepository(connection).preview_btts_corrections(fixture_id, limit=limit)
            self.assertEqual(connection.statements, [])

    def test_cli_is_preview_only(self):
        connection = ReadOnlyConnection([candidate()])
        output = StringIO()
        code = main(["preview-btts-corrections", "--fixture-id", "42",
                     "--database-url", "postgres://example"],
                    connect=lambda _: connection, stdout=output)
        self.assertEqual(code, 0)
        self.assertTrue(json.loads(output.getvalue())["dry_run"])
        self.assertIn("model_evaluation_facts", json.loads(output.getvalue())["application_blocker"])

    def test_retained_revision_is_reported_with_provenance(self):
        connection = ReadOnlyConnection([candidate(
            id="e569d949-dbf5-4daa-966d-722f36d04480", source="prediction_result_revisions")])
        report = PredictionSettlementRepository(connection).preview_btts_corrections(42)
        self.assertEqual(report["rows"][0]["prediction_id"], "e569d949-dbf5-4daa-966d-722f36d04480")
        self.assertEqual(report["rows"][0]["source"], "prediction_result_revisions")
        self.assertTrue(report["rows"][0]["proposed_is_correct"])

    def test_malformed_selection_fails_without_committing(self):
        connection = ReadOnlyConnection([candidate(prediction="invalid")])
        with self.assertRaises(ValueError):
            PredictionSettlementRepository(connection).preview_btts_corrections(42)
        self.assertEqual(connection.rollbacks, 1)

    def test_cli_rejects_apply_before_connecting(self):
        def unexpected_connect(_):
            self.fail("must reject apply before opening database")
        with redirect_stderr(StringIO()), self.assertRaises(SystemExit) as raised:
            main(["preview-btts-corrections", "--fixture-id", "42", "--apply"],
                 connect=unexpected_connect)
        self.assertEqual(raised.exception.code, 2)


if __name__ == "__main__":
    unittest.main()
