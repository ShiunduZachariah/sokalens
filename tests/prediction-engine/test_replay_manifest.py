from __future__ import annotations

from dataclasses import FrozenInstanceError, replace
from datetime import UTC, datetime, timedelta, timezone
import json
import unittest

from prediction_engine.replay_manifest import (
    DatasetPartition,
    DatasetRole,
    DevelopmentDatasets,
    FinalHoldoutEvaluation,
    ManifestValidationError,
    ReplayDatasetManifest,
    TimeWindow,
)


DAY = timedelta(days=1)
BASE = datetime(2022, 1, 1, tzinfo=UTC)
HASH = "a" * 64


class ReplayManifestTests(unittest.TestCase):
    def test_canonical_manifest_round_trip_and_hash_are_deterministic(self) -> None:
        first = manifest()
        second = manifest(offset=timezone(timedelta(hours=3)))

        self.assertEqual(first.canonical_bytes(), second.canonical_bytes())
        self.assertEqual(first.manifest_sha256, second.manifest_sha256)
        self.assertEqual(first.manifest_sha256, "25fda31f794318cb8d5920bdca0974041e3689d349808ff4280a6c1e82e06371")
        self.assertEqual(ReplayDatasetManifest.from_json(first.to_json()), first)
        self.assertTrue(first.to_json().endswith(b"\n"))

    def test_mutation_and_tampered_envelope_are_rejected(self) -> None:
        item = manifest()
        with self.assertRaises(FrozenInstanceError):
            item.manifest_id = "changed"  # type: ignore[misc]
        envelope = json.loads(item.to_json())
        envelope["manifest"]["partitions"]["train"]["record_count"] += 1
        with self.assertRaisesRegex(ManifestValidationError, "does not match"):
            ReplayDatasetManifest.from_json(json.dumps(envelope))

    def test_partition_order_and_non_overlap_are_strict(self) -> None:
        item = manifest()
        overlapping = replace(
            item.calibration,
            window=TimeWindow(item.test.window.end - timedelta(hours=1), item.calibration.window.end),
        )
        with self.assertRaisesRegex(ManifestValidationError, "overlaps or precedes"):
            replace(item, calibration=overlapping)

    def test_all_boundaries_and_as_of_must_be_timezone_aware(self) -> None:
        with self.assertRaisesRegex(ManifestValidationError, "timezone-aware"):
            TimeWindow(datetime(2022, 1, 1), datetime(2022, 1, 2, tzinfo=UTC))
        with self.assertRaisesRegex(ManifestValidationError, "timezone-aware"):
            replace(manifest(), as_of_timestamp=datetime(2026, 1, 1))

    def test_partition_cannot_extend_beyond_replay_as_of(self) -> None:
        item = manifest()
        with self.assertRaisesRegex(ManifestValidationError, "ends after as_of_timestamp"):
            replace(item, as_of_timestamp=item.final_holdout.window.end - timedelta(seconds=1))

    def test_manifest_cannot_attest_before_its_as_of_cutoff(self) -> None:
        item = manifest()
        with self.assertRaisesRegex(ManifestValidationError, "cannot precede"):
            replace(item, created_at=item.as_of_timestamp - timedelta(seconds=1))

    def test_partition_window_must_be_a_validated_time_window(self) -> None:
        item = manifest()
        with self.assertRaisesRegex(ManifestValidationError, "must be a TimeWindow"):
            replace(item.train, window="2022-01-01/2022-01-02")  # type: ignore[arg-type]

    def test_final_holdout_is_not_available_from_development_interface(self) -> None:
        item = manifest()
        development = item.development_datasets()
        self.assertEqual((development.train.role, development.test.role, development.calibration.role), (
            DatasetRole.TRAIN, DatasetRole.TEST, DatasetRole.CALIBRATION,
        ))
        self.assertFalse(hasattr(development, "final_holdout"))
        with self.assertRaisesRegex(ManifestValidationError, "train partition"):
            DevelopmentDatasets(item.final_holdout, item.test, item.calibration)
        with self.assertRaisesRegex(ManifestValidationError, "final_holdout partition"):
            FinalHoldoutEvaluation(item.train)
        self.assertEqual(item.final_evaluation().final_holdout, item.final_holdout)

    def test_duplicate_unsorted_or_malformed_identifiers_fail_closed(self) -> None:
        item = manifest()
        with self.assertRaisesRegex(ManifestValidationError, "sorted"):
            replace(item.train, source_snapshot_ids=("source:z", "source:a"))
        with self.assertRaisesRegex(ManifestValidationError, "unique"):
            replace(item, test=replace(item.test, dataset_id=item.train.dataset_id))
        with self.assertRaisesRegex(ManifestValidationError, "distinct partition artifacts"):
            replace(item, test=replace(item.test, artifact_ids=item.train.artifact_ids))
        with self.assertRaisesRegex(ManifestValidationError, "SHA-256"):
            replace(item, config_sha256="ABC")

    def test_unknown_schema_fields_and_partition_role_swaps_fail_closed(self) -> None:
        envelope = json.loads(manifest().to_json())
        envelope["manifest"]["surprise"] = True
        envelope["manifest_sha256"] = "0" * 64
        with self.assertRaisesRegex(ManifestValidationError, "unknown or missing"):
            ReplayDatasetManifest.from_json(json.dumps(envelope))

        envelope = json.loads(manifest().to_json())
        envelope["manifest"]["partitions"]["train"]["role"] = "final_holdout"
        envelope["manifest_sha256"] = "0" * 64
        with self.assertRaisesRegex(ManifestValidationError, "contains role"):
            ReplayDatasetManifest.from_json(json.dumps(envelope))

        with self.assertRaisesRegex(ManifestValidationError, "duplicate JSON field"):
            ReplayDatasetManifest.from_json('{"manifest":{},"manifest":{},"manifest_sha256":"' + "0" * 64 + '"}')


def partition(role: DatasetRole, start_day: int, end_day: int) -> DatasetPartition:
    return DatasetPartition(
        role=role,
        dataset_id=f"dataset:{role.value}:v1",
        window=TimeWindow(BASE + start_day * DAY, BASE + end_day * DAY),
        record_count=100,
        content_sha256=HASH,
        source_snapshot_ids=("postgres:fixtures:2026-08-14", "postgres:odds:2026-08-14"),
        artifact_ids=(f"parquet:{role.value}:sha256:{HASH}",),
    )


def manifest(offset: timezone = UTC) -> ReplayDatasetManifest:
    def shifted(value: datetime) -> datetime:
        return value.astimezone(offset)

    return ReplayDatasetManifest(
        manifest_id="replay:champion-cycle-001",
        created_at=shifted(BASE + 53 * DAY),
        as_of_timestamp=shifted(BASE + 52 * DAY),
        code_commit="1ed9269",
        feature_version="python-engine-v2-shadow",
        config_sha256="b" * 64,
        train=partition(DatasetRole.TRAIN, 0, 20),
        test=partition(DatasetRole.TEST, 20, 30),
        calibration=partition(DatasetRole.CALIBRATION, 30, 40),
        final_holdout=partition(DatasetRole.FINAL_HOLDOUT, 40, 50),
    )


if __name__ == "__main__":
    unittest.main()
