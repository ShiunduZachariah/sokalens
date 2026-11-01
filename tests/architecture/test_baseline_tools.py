from __future__ import annotations

import json
from pathlib import Path
import sys
import unittest


REPOSITORY = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPOSITORY))

from scripts.architecture.acca_correlation_audit import audit_candidates
from scripts.architecture.repository_baseline import build_repository_baseline
from scripts.architecture.v1_benchmark import run_benchmark


class RepositoryBaselineTests(unittest.TestCase):
    def test_inventory_is_deterministic_and_finds_canonical_prediction_table(self) -> None:
        first = build_repository_baseline(REPOSITORY)
        second = build_repository_baseline(REPOSITORY)

        self.assertEqual(first, second)
        # Derived, not pinned. A hardcoded version here fails on every
        # migration PR without ever catching a defect -- the number it
        # asserts is a fact about the tree, not about the builder, so the
        # only thing a literal tests is whether someone remembered to edit
        # this line. What is worth asserting is that the builder reports the
        # *highest* version rather than an arbitrary or unsorted one, so the
        # expectation is computed here independently of how the builder
        # computes it.
        versions = sorted(
            path.name.split("_", 1)[0]
            for path in (REPOSITORY / "infra" / "db" / "migrations").glob("*.up.sql")
        )
        self.assertEqual(first["schema"]["latest_migration"], max(versions))
        self.assertEqual(first["schema"]["migration_count"], len(versions))
        self.assertIn("engine_predictions", first["schema"]["declared_tables"])
        self.assertEqual(first["prediction_engine"]["default_model_version"], "python-engine-v1")


class V1BenchmarkTests(unittest.TestCase):
    def setUp(self) -> None:
        self.fixture = REPOSITORY / "tests" / "architecture" / "fixtures" / "v1-benchmark.json"

    def test_fixed_v1_output_matches_checked_in_artifact(self) -> None:
        actual = run_benchmark(REPOSITORY, self.fixture)
        expected_path = self.fixture.with_name("v1-benchmark.expected.json")
        expected = json.loads(expected_path.read_text(encoding="utf-8"))
        self.assertEqual(actual, expected)

    def test_acca_audit_reports_current_same_fixture_pair_as_unsafe(self) -> None:
        fixture = json.loads(self.fixture.read_text(encoding="utf-8"))
        report = audit_candidates(fixture["acca_candidates"])
        fixture_201 = [pair for pair in report["same_fixture_pairs"] if pair["fixture_id"] == 201]

        self.assertEqual(len(fixture_201), 1)
        self.assertEqual(fixture_201[0]["markets"], ["1x2", "btts"])
        self.assertEqual(fixture_201[0]["classification"], "unsafe")
        self.assertEqual(fixture_201[0]["current_pricing_method"], "naive_marginal_product")


if __name__ == "__main__":
    unittest.main()
