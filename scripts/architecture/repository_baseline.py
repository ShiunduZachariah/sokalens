#!/usr/bin/env python3
"""Build a deterministic repository/schema baseline without calling providers.

The default inventory is derived from versioned migration and prediction-engine
files.  An optional PostgreSQL probe is deliberately separate so a baseline can
be reproduced in CI without credentials while operators can still capture row
counts and relation sizes from a restored production-shaped database.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
from typing import Any


CREATE_TABLE = re.compile(r"\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:public\.)?([a-z_][a-z0-9_]*)", re.I)
CREATE_INDEX = re.compile(r"\bCREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)", re.I)
MODEL_VERSION = re.compile(r'model_version:\s*str\s*=\s*"([^"]+)"')


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def canonical_json(value: Any) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()


def build_repository_baseline(repository: Path) -> dict[str, Any]:
    migration_dir = repository / "infra" / "db" / "migrations"
    migrations = sorted(migration_dir.glob("*.up.sql"))
    tables: set[str] = set()
    indexes: set[str] = set()
    migration_hashes: dict[str, str] = {}
    for migration in migrations:
        raw = migration.read_bytes()
        sql = raw.decode("utf-8")
        migration_hashes[migration.name] = sha256_bytes(raw)
        tables.update(match.group(1).lower() for match in CREATE_TABLE.finditer(sql))
        indexes.update(match.group(1).lower() for match in CREATE_INDEX.finditer(sql))

    config_path = repository / "services" / "prediction-engine" / "prediction_engine" / "config.py"
    pipeline_path = repository / "services" / "prediction-engine" / "prediction_engine" / "pipeline.py"
    pipeline = pipeline_path.read_text(encoding="utf-8")
    version_match = MODEL_VERSION.search(pipeline)
    source_files = sorted((repository / "services" / "prediction-engine" / "prediction_engine").glob("*.py"))
    source_manifest = {
        str(path.relative_to(repository)).replace("\\", "/"): sha256_bytes(path.read_bytes())
        for path in source_files
    }

    baseline: dict[str, Any] = {
        "format_version": 1,
        "schema": {
            "latest_migration": migrations[-1].name.split("_", 1)[0] if migrations else None,
            "migration_count": len(migrations),
            "migration_hashes": migration_hashes,
            "declared_indexes": sorted(indexes),
            "declared_tables": sorted(tables),
        },
        "prediction_engine": {
            "config_sha256": sha256_bytes(config_path.read_bytes()),
            "default_model_version": version_match.group(1) if version_match else None,
            "source_manifest": source_manifest,
        },
    }
    baseline["baseline_sha256"] = sha256_bytes(canonical_json(baseline))
    return baseline


def probe_postgres(database_url: str) -> list[dict[str, Any]]:
    """Return exact row counts and total relation sizes for public tables.

    This is opt-in because exact counts may be expensive and must only be run
    against an approved local/staging/restored database.
    """
    try:
        import psycopg
        from psycopg import sql
    except ImportError as error:  # pragma: no cover - environment dependent
        raise RuntimeError("psycopg is required for --database-url") from error

    query = """
SELECT c.relname,
       pg_total_relation_size(c.oid) AS total_bytes
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind = 'r'
ORDER BY c.relname
"""
    result: list[dict[str, Any]] = []
    with psycopg.connect(database_url) as connection:
        with connection.cursor() as cursor:
            cursor.execute(query)
            relations = cursor.fetchall()
            for relation_name, total_bytes in relations:
                cursor.execute(
                    sql.SQL("SELECT count(*) FROM {}.{}").format(
                        sql.Identifier("public"),
                        sql.Identifier(relation_name),
                    )
                )
                result.append(
                    {
                        "relation": relation_name,
                        "row_count": cursor.fetchone()[0],
                        "total_bytes": total_bytes,
                    }
                )
    return result


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repository", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--database-url", default=os.environ.get("DATABASE_URL", ""))
    args = parser.parse_args()
    baseline = build_repository_baseline(args.repository.resolve())
    baseline["database"] = (
        {"status": "captured", "relations": probe_postgres(args.database_url)}
        if args.database_url
        else {"status": "not_captured", "reason": "DATABASE_URL not supplied"}
    )
    print(json.dumps(baseline, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
