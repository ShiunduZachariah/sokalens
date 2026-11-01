"""Immutable dataset manifests for point-in-time engine replay and evaluation.

The final holdout is deliberately exposed through a different interface from
the datasets that may be used for fitting or tuning.  This is a runtime
boundary as well as a type-level signal: role checks reject attempts to put a
final-holdout partition into a development selection.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import UTC, datetime
from enum import StrEnum
import hashlib
import hmac
import json
import math
import re
from typing import Any


SCHEMA_VERSION = "replay-dataset-manifest/v1"
_SHA256 = re.compile(r"^[0-9a-f]{64}$")
_IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,254}$")


class ManifestValidationError(ValueError):
    """Raised when a replay manifest cannot be trusted."""


class DatasetRole(StrEnum):
    TRAIN = "train"
    TEST = "test"
    CALIBRATION = "calibration"
    FINAL_HOLDOUT = "final_holdout"


@dataclass(frozen=True, slots=True)
class TimeWindow:
    """A half-open, UTC-normalized interval: ``[start, end)``."""

    start: datetime
    end: datetime

    def __post_init__(self) -> None:
        start = _aware_utc(self.start, "window start")
        end = _aware_utc(self.end, "window end")
        if start >= end:
            raise ManifestValidationError("window start must be strictly before window end")
        object.__setattr__(self, "start", start)
        object.__setattr__(self, "end", end)

    def as_dict(self) -> dict[str, str]:
        return {"start": _timestamp(self.start), "end": _timestamp(self.end)}


@dataclass(frozen=True, slots=True)
class DatasetPartition:
    role: DatasetRole
    dataset_id: str
    window: TimeWindow
    record_count: int
    content_sha256: str
    source_snapshot_ids: tuple[str, ...]
    artifact_ids: tuple[str, ...]

    def __post_init__(self) -> None:
        try:
            role = DatasetRole(self.role)
        except (TypeError, ValueError) as error:
            raise ManifestValidationError(f"unsupported dataset role: {self.role!r}") from error
        object.__setattr__(self, "role", role)
        _identifier(self.dataset_id, "dataset_id")
        if not isinstance(self.window, TimeWindow):
            raise ManifestValidationError("window must be a TimeWindow")
        if isinstance(self.record_count, bool) or not isinstance(self.record_count, int) or self.record_count <= 0:
            raise ManifestValidationError("record_count must be a positive integer")
        _hash(self.content_sha256, "content_sha256")
        object.__setattr__(self, "source_snapshot_ids", _identifiers(self.source_snapshot_ids, "source_snapshot_ids"))
        object.__setattr__(self, "artifact_ids", _identifiers(self.artifact_ids, "artifact_ids"))

    def as_dict(self) -> dict[str, Any]:
        return {
            "role": self.role.value,
            "dataset_id": self.dataset_id,
            "window": self.window.as_dict(),
            "record_count": self.record_count,
            "content_sha256": self.content_sha256,
            "source_snapshot_ids": list(self.source_snapshot_ids),
            "artifact_ids": list(self.artifact_ids),
        }


@dataclass(frozen=True, slots=True)
class DevelopmentDatasets:
    """Only datasets permitted in fit, tune, ensemble, and calibration paths."""

    train: DatasetPartition
    test: DatasetPartition
    calibration: DatasetPartition

    def __post_init__(self) -> None:
        _require_role(self.train, DatasetRole.TRAIN, "train")
        _require_role(self.test, DatasetRole.TEST, "test")
        _require_role(self.calibration, DatasetRole.CALIBRATION, "calibration")


@dataclass(frozen=True, slots=True)
class FinalHoldoutEvaluation:
    """Dataset available only to the one-shot final evaluation path."""

    final_holdout: DatasetPartition

    def __post_init__(self) -> None:
        _require_role(self.final_holdout, DatasetRole.FINAL_HOLDOUT, "final_holdout")


@dataclass(frozen=True, slots=True)
class ReplayDatasetManifest:
    manifest_id: str
    created_at: datetime
    as_of_timestamp: datetime
    code_commit: str
    feature_version: str
    config_sha256: str
    train: DatasetPartition
    test: DatasetPartition
    calibration: DatasetPartition
    final_holdout: DatasetPartition
    schema_version: str = SCHEMA_VERSION

    def __post_init__(self) -> None:
        if self.schema_version != SCHEMA_VERSION:
            raise ManifestValidationError(f"unsupported schema_version: {self.schema_version!r}")
        _identifier(self.manifest_id, "manifest_id")
        _identifier(self.code_commit, "code_commit")
        _identifier(self.feature_version, "feature_version")
        _hash(self.config_sha256, "config_sha256")
        object.__setattr__(self, "created_at", _aware_utc(self.created_at, "created_at"))
        object.__setattr__(self, "as_of_timestamp", _aware_utc(self.as_of_timestamp, "as_of_timestamp"))
        if self.created_at < self.as_of_timestamp:
            raise ManifestValidationError("created_at cannot precede as_of_timestamp")

        ordered = (
            (DatasetRole.TRAIN, self.train),
            (DatasetRole.TEST, self.test),
            (DatasetRole.CALIBRATION, self.calibration),
            (DatasetRole.FINAL_HOLDOUT, self.final_holdout),
        )
        dataset_ids: set[str] = set()
        artifact_ids: set[str] = set()
        for expected_role, partition in ordered:
            _require_role(partition, expected_role, expected_role.value)
            if partition.dataset_id in dataset_ids:
                raise ManifestValidationError("dataset_id values must be unique across partitions")
            dataset_ids.add(partition.dataset_id)
            duplicated_artifacts = artifact_ids.intersection(partition.artifact_ids)
            if duplicated_artifacts:
                raise ManifestValidationError("artifact_ids must identify distinct partition artifacts")
            artifact_ids.update(partition.artifact_ids)
            if partition.window.end > self.as_of_timestamp:
                raise ManifestValidationError(f"{expected_role.value} ends after as_of_timestamp")
        for (_, previous), (next_role, following) in zip(ordered, ordered[1:]):
            if previous.window.end > following.window.start:
                raise ManifestValidationError(
                    f"{next_role.value} overlaps or precedes its chronological predecessor"
                )

    def development_datasets(self) -> DevelopmentDatasets:
        return DevelopmentDatasets(train=self.train, test=self.test, calibration=self.calibration)

    def final_evaluation(self) -> FinalHoldoutEvaluation:
        return FinalHoldoutEvaluation(final_holdout=self.final_holdout)

    def payload(self) -> dict[str, Any]:
        return {
            "schema_version": self.schema_version,
            "manifest_id": self.manifest_id,
            "created_at": _timestamp(self.created_at),
            "as_of_timestamp": _timestamp(self.as_of_timestamp),
            "code_commit": self.code_commit,
            "feature_version": self.feature_version,
            "config_sha256": self.config_sha256,
            "partitions": {
                "train": self.train.as_dict(),
                "test": self.test.as_dict(),
                "calibration": self.calibration.as_dict(),
                "final_holdout": self.final_holdout.as_dict(),
            },
        }

    def canonical_bytes(self) -> bytes:
        return _canonical_json(self.payload())

    @property
    def manifest_sha256(self) -> str:
        return hashlib.sha256(self.canonical_bytes()).hexdigest()

    def to_json(self) -> bytes:
        """Return a self-verifying canonical envelope suitable for immutable storage."""

        return _canonical_json({"manifest": self.payload(), "manifest_sha256": self.manifest_sha256})

    @classmethod
    def from_json(cls, encoded: bytes | str) -> "ReplayDatasetManifest":
        try:
            raw = json.loads(encoded, object_pairs_hook=_object_without_duplicate_keys)
        except (json.JSONDecodeError, TypeError, UnicodeDecodeError) as error:
            raise ManifestValidationError("manifest is not valid JSON") from error
        if not isinstance(raw, dict) or set(raw) != {"manifest", "manifest_sha256"}:
            raise ManifestValidationError("manifest envelope has unknown or missing fields")
        _hash(raw["manifest_sha256"], "manifest_sha256")
        payload = raw["manifest"]
        manifest = cls._from_payload(payload)
        if not _constant_time_equal(raw["manifest_sha256"], manifest.manifest_sha256):
            raise ManifestValidationError("manifest_sha256 does not match canonical manifest content")
        return manifest

    @classmethod
    def _from_payload(cls, payload: object) -> "ReplayDatasetManifest":
        expected = {
            "schema_version", "manifest_id", "created_at", "as_of_timestamp",
            "code_commit", "feature_version", "config_sha256", "partitions",
        }
        if not isinstance(payload, dict) or set(payload) != expected:
            raise ManifestValidationError("manifest payload has unknown or missing fields")
        partitions = payload["partitions"]
        expected_partitions = {role.value for role in DatasetRole}
        if not isinstance(partitions, dict) or set(partitions) != expected_partitions:
            raise ManifestValidationError("manifest partitions have unknown or missing fields")
        return cls(
            schema_version=_string(payload["schema_version"], "schema_version"),
            manifest_id=_string(payload["manifest_id"], "manifest_id"),
            created_at=_parse_timestamp(payload["created_at"], "created_at"),
            as_of_timestamp=_parse_timestamp(payload["as_of_timestamp"], "as_of_timestamp"),
            code_commit=_string(payload["code_commit"], "code_commit"),
            feature_version=_string(payload["feature_version"], "feature_version"),
            config_sha256=_string(payload["config_sha256"], "config_sha256"),
            train=_partition(partitions["train"], DatasetRole.TRAIN),
            test=_partition(partitions["test"], DatasetRole.TEST),
            calibration=_partition(partitions["calibration"], DatasetRole.CALIBRATION),
            final_holdout=_partition(partitions["final_holdout"], DatasetRole.FINAL_HOLDOUT),
        )


def _partition(value: object, expected_role: DatasetRole) -> DatasetPartition:
    fields = {"role", "dataset_id", "window", "record_count", "content_sha256", "source_snapshot_ids", "artifact_ids"}
    if not isinstance(value, dict) or set(value) != fields:
        raise ManifestValidationError(f"{expected_role.value} partition has unknown or missing fields")
    window = value["window"]
    if not isinstance(window, dict) or set(window) != {"start", "end"}:
        raise ManifestValidationError(f"{expected_role.value} window has unknown or missing fields")
    role = _string(value["role"], "role")
    if role != expected_role.value:
        raise ManifestValidationError(f"partition key {expected_role.value!r} contains role {role!r}")
    return DatasetPartition(
        role=expected_role,
        dataset_id=_string(value["dataset_id"], "dataset_id"),
        window=TimeWindow(
            _parse_timestamp(window["start"], "window start"),
            _parse_timestamp(window["end"], "window end"),
        ),
        record_count=value["record_count"],
        content_sha256=_string(value["content_sha256"], "content_sha256"),
        source_snapshot_ids=_string_tuple(value["source_snapshot_ids"], "source_snapshot_ids"),
        artifact_ids=_string_tuple(value["artifact_ids"], "artifact_ids"),
    )


def _canonical_json(value: object) -> bytes:
    return (json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")) + "\n").encode("utf-8")


def _object_without_duplicate_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ManifestValidationError(f"duplicate JSON field: {key}")
        result[key] = value
    return result


def _timestamp(value: datetime) -> str:
    return value.astimezone(UTC).isoformat(timespec="microseconds").replace("+00:00", "Z")


def _parse_timestamp(value: object, name: str) -> datetime:
    if not isinstance(value, str):
        raise ManifestValidationError(f"{name} must be an ISO-8601 string")
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as error:
        raise ManifestValidationError(f"{name} must be a valid ISO-8601 timestamp") from error
    return _aware_utc(parsed, name)


def _aware_utc(value: object, name: str) -> datetime:
    if not isinstance(value, datetime) or value.tzinfo is None or value.utcoffset() is None:
        raise ManifestValidationError(f"{name} must be timezone-aware")
    offset = value.utcoffset()
    if offset is None or not math.isfinite(offset.total_seconds()):
        raise ManifestValidationError(f"{name} has an invalid UTC offset")
    return value.astimezone(UTC)


def _identifier(value: object, name: str) -> str:
    if not isinstance(value, str) or not _IDENTIFIER.fullmatch(value):
        raise ManifestValidationError(f"{name} must be a stable non-empty identifier")
    return value


def _identifiers(value: object, name: str) -> tuple[str, ...]:
    if not isinstance(value, tuple) or not value:
        raise ManifestValidationError(f"{name} must be a non-empty tuple")
    for item in value:
        _identifier(item, name)
    if tuple(sorted(set(value))) != value:
        raise ManifestValidationError(f"{name} must be sorted and contain no duplicates")
    return value


def _hash(value: object, name: str) -> str:
    if not isinstance(value, str) or not _SHA256.fullmatch(value):
        raise ManifestValidationError(f"{name} must be a lowercase SHA-256 hex digest")
    return value


def _require_role(partition: object, role: DatasetRole, name: str) -> None:
    if not isinstance(partition, DatasetPartition) or partition.role is not role:
        raise ManifestValidationError(f"{name} must contain only the {role.value} partition")


def _string(value: object, name: str) -> str:
    if not isinstance(value, str):
        raise ManifestValidationError(f"{name} must be a string")
    return value


def _string_tuple(value: object, name: str) -> tuple[str, ...]:
    if not isinstance(value, list) or not all(isinstance(item, str) for item in value):
        raise ManifestValidationError(f"{name} must be an array of strings")
    return tuple(value)


def _constant_time_equal(left: str, right: str) -> bool:
    return hmac.compare_digest(left, right)
