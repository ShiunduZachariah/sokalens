"""PostgreSQL transaction checks; opt in with BTTS_TEST_DATABASE_URL.

The suite creates and drops its own isolated schema. Supply only a QA/test
database URL; never point this harness at production.
"""

import concurrent.futures
import importlib.util
import os
import sys
import unittest
import uuid
from pathlib import Path

import psycopg
from psycopg import sql
from psycopg.rows import dict_row

spec = importlib.util.spec_from_file_location("historical_btts_pg", Path(__file__).with_name("btts.py"))
btts = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = btts
spec.loader.exec_module(btts)

SCHEMA_SQL = """
CREATE TABLE fixtures (id BIGINT PRIMARY KEY, status TEXT, home_goals_ft INT, away_goals_ft INT);
CREATE TABLE engine_predictions (id UUID PRIMARY KEY, fixture_id BIGINT, market TEXT, prediction TEXT);
CREATE TABLE prediction_results (prediction_id UUID PRIMARY KEY, settlement_status TEXT, actual TEXT,
  is_correct BOOLEAN, void_reason TEXT, market TEXT, predicted TEXT);
CREATE TABLE model_evaluation_facts (prediction_id UUID PRIMARY KEY, fixture_id BIGINT, market TEXT,
  selection TEXT, final_outcome TEXT, settlement_classification TEXT);
CREATE TABLE prediction_result_rollup_ledger (prediction_id UUID PRIMARY KEY, included_in_accuracy BOOLEAN,
  aggregate_date DATE, market TEXT, league_id BIGINT, confidence_tier TEXT, model_version TEXT,
  counted_as TEXT, needs_recount BOOLEAN);
CREATE TABLE prediction_result_revisions (id BIGSERIAL PRIMARY KEY, prediction_id UUID, fixture_id BIGINT,
  settlement_status TEXT, actual TEXT, is_correct BOOLEAN, void_reason TEXT, revision_reason TEXT,
  correction_operation_id UUID, correction_context JSONB, recorded_at TIMESTAMPTZ DEFAULT now());
CREATE UNIQUE INDEX correction_operation_unique ON prediction_result_revisions(prediction_id, correction_operation_id)
  WHERE correction_operation_id IS NOT NULL;
CREATE TABLE prediction_accuracy_daily (aggregate_date DATE, market TEXT, league_id BIGINT,
  confidence_tier TEXT, model_version TEXT, total_predictions INT, correct INT, incorrect INT,
  voided INT, pending INT, aggregated_at TIMESTAMPTZ DEFAULT now(),
  PRIMARY KEY(aggregate_date, market, league_id, confidence_tier, model_version),
  CHECK (total_predictions = correct + incorrect + voided + pending));
CREATE VIEW effective_model_evaluation_facts AS
SELECT facts.prediction_id,
  COALESCE(rev.actual, facts.final_outcome) AS final_outcome,
  CASE WHEN rev.settlement_status='settled' AND rev.is_correct IS NOT NULL
    THEN CASE WHEN rev.is_correct THEN 'correct' ELSE 'incorrect' END
    ELSE facts.settlement_classification END AS settlement_classification
FROM model_evaluation_facts facts
LEFT JOIN LATERAL (
  SELECT settlement_status, actual, is_correct
  FROM prediction_result_revisions
  WHERE prediction_id=facts.prediction_id
  ORDER BY recorded_at DESC, id DESC LIMIT 1
) rev ON true;
"""


@unittest.skipUnless(os.environ.get("BTTS_TEST_DATABASE_URL"), "requires isolated QA/test PostgreSQL URL")
class PostgreSQLCorrectionTests(unittest.TestCase):
    def setUp(self):
        self.dsn = os.environ["BTTS_TEST_DATABASE_URL"]
        self.schema = "btts_test_" + uuid.uuid4().hex
        self.admin = psycopg.connect(self.dsn, autocommit=True)
        self.admin.execute(sql.SQL("CREATE SCHEMA {}").format(sql.Identifier(self.schema)))
        self.conn = psycopg.connect(self.dsn, row_factory=dict_row, autocommit=True,
                                    options=f"-c search_path={self.schema}")
        for statement in SCHEMA_SQL.split(";"):
            if statement.strip():
                self.conn.execute(statement)
        self.prediction_id = uuid.uuid4()
        self.operation_id = uuid.uuid4()

    def tearDown(self):
        self.conn.close()
        self.admin.execute(sql.SQL("DROP SCHEMA {} CASCADE").format(sql.Identifier(self.schema)))
        self.admin.close()

    def seed(self, *, included=True, raw_result=False):
        p = self.prediction_id
        self.conn.execute("INSERT INTO fixtures VALUES (1,'ft',6,2)")
        self.conn.execute("INSERT INTO engine_predictions VALUES (%s,1,'btts_total_goals','yes_over_0.5')", (p,))
        self.conn.execute("INSERT INTO model_evaluation_facts VALUES (%s,1,'btts_total_goals','yes_over_0.5','yes_under_0.5','incorrect')", (p,))
        if raw_result:
            self.conn.execute("""INSERT INTO prediction_results VALUES
              (%s,'settled','yes_under_0.5',false,NULL,'btts_total_goals','yes_over_0.5')""", (p,))
        else:
            self.conn.execute("""INSERT INTO prediction_result_revisions
              (prediction_id,fixture_id,settlement_status,actual,is_correct,revision_reason)
              VALUES (%s,1,'settled','yes_under_0.5',false,'initial')""", (p,))
        if included is not None:
            self.conn.execute("""INSERT INTO prediction_result_rollup_ledger VALUES
              (%s,%s,'2026-09-23','btts_total_goals',1,'strong','v1','incorrect',false)""", (p, included))
            if included:
                self.conn.execute("""INSERT INTO prediction_accuracy_daily
                  (aggregate_date,market,league_id,confidence_tier,model_version,total_predictions,
                   correct,incorrect,voided,pending) VALUES
                  ('2026-09-23','btts_total_goals',1,'strong','v1',1,0,1,0,0)""")
        return btts.classify(self.conn.execute(btts.ROW_SQL, (p,)).fetchone())

    def test_counted_correction_is_atomic_idempotent_and_survives_parent_retention(self):
        expected = self.seed()
        self.assertEqual(expected["classification"], "eligible_survivor")
        self.assertEqual(btts.apply_one(self.conn, expected, self.operation_id, "manifest"), "applied")
        self.assertEqual(btts.apply_one(self.conn, expected, self.operation_id, "manifest"), "already_applied")
        bucket = self.conn.execute("SELECT total_predictions,correct,incorrect FROM prediction_accuracy_daily").fetchone()
        self.assertEqual(tuple(bucket.values()), (1, 1, 0))
        revisions = self.conn.execute("SELECT count(*) AS n FROM prediction_result_revisions WHERE correction_operation_id=%s", (self.operation_id,)).fetchone()
        self.assertEqual(revisions["n"], 1)
        self.conn.execute("DELETE FROM engine_predictions WHERE id=%s", (self.prediction_id,))
        effective = self.conn.execute("SELECT final_outcome,settlement_classification FROM effective_model_evaluation_facts WHERE prediction_id=%s", (self.prediction_id,)).fetchone()
        self.assertEqual(effective, {"final_outcome": "yes_over_0.5", "settlement_classification": "correct"})

    def test_unaggregated_result_corrects_without_daily_bucket(self):
        expected = self.seed(included=None, raw_result=True)
        self.assertEqual(expected["classification"], "eligible_unaggregated_survivor")
        self.assertEqual(btts.apply_one(self.conn, expected, self.operation_id, "manifest"), "applied")
        result = self.conn.execute("SELECT actual,is_correct FROM prediction_results").fetchone()
        self.assertEqual(result, {"actual": "yes_over_0.5", "is_correct": True})
        self.assertEqual(self.conn.execute("SELECT count(*) AS n FROM prediction_accuracy_daily").fetchone()["n"], 0)

    def test_missing_bucket_and_stale_official_score_do_not_write(self):
        expected = self.seed()
        self.conn.execute("DELETE FROM prediction_accuracy_daily")
        with self.assertRaises(ValueError):
            btts.apply_one(self.conn, expected, self.operation_id, "manifest")
        self.assertEqual(self.conn.execute("SELECT count(*) AS n FROM prediction_result_revisions WHERE correction_operation_id=%s", (self.operation_id,)).fetchone()["n"], 0)
        self.conn.execute("UPDATE fixtures SET home_goals_ft=0 WHERE id=1")
        with self.assertRaises(ValueError):
            btts.apply_one(self.conn, expected, self.operation_id, "manifest")
        self.assertEqual(self.conn.execute("SELECT count(*) AS n FROM prediction_result_revisions WHERE correction_operation_id=%s", (self.operation_id,)).fetchone()["n"], 0)

    def test_failure_after_revision_insert_rolls_back_everything(self):
        expected = self.seed(included=None, raw_result=True)
        self.conn.execute("""CREATE FUNCTION fail_result_update() RETURNS trigger LANGUAGE plpgsql AS
          $$BEGIN RAISE EXCEPTION 'forced test failure'; END;$$""")
        self.conn.execute("""CREATE TRIGGER fail_result_update BEFORE UPDATE ON prediction_results
          FOR EACH ROW EXECUTE FUNCTION fail_result_update()""")
        with self.assertRaises(psycopg.Error):
            btts.apply_one(self.conn, expected, self.operation_id, "manifest")
        self.assertEqual(self.conn.execute("SELECT count(*) AS n FROM prediction_result_revisions WHERE correction_operation_id=%s", (self.operation_id,)).fetchone()["n"], 0)
        self.assertEqual(self.conn.execute("SELECT is_correct FROM prediction_results").fetchone()["is_correct"], False)

    def test_two_workers_cannot_double_count(self):
        expected = self.seed()

        def worker():
            with psycopg.connect(self.dsn, row_factory=dict_row, autocommit=True,
                                 options=f"-c search_path={self.schema}") as conn:
                try:
                    return btts.apply_one(conn, expected, self.operation_id, "manifest")
                except (ValueError, psycopg.Error):
                    return "conflict"

        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            outcomes = list(pool.map(lambda _: worker(), range(2)))
        self.assertIn("applied", outcomes)
        self.assertEqual(self.conn.execute("SELECT correct FROM prediction_accuracy_daily").fetchone()["correct"], 1)
        self.assertEqual(self.conn.execute("SELECT count(*) AS n FROM prediction_result_revisions WHERE correction_operation_id=%s", (self.operation_id,)).fetchone()["n"], 1)


if __name__ == "__main__":
    unittest.main()
