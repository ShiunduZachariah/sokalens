"""Review and narrowly repair historical BTTS/total-goals settlement errors.

Dry-run manifest creation is the default. Application requires a reviewed
manifest SHA and is restricted to surviving raw predictions with either an
exact rollup ledger or an unaggregated live result. It never guesses missing
historical inclusion or bucket data.
Run in the prediction-engine Python environment (psycopg 3).
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import uuid
from datetime import date, datetime
from pathlib import Path

import psycopg
from psycopg.rows import dict_row
from psycopg.types.json import Jsonb

ALGORITHM = "btts-line-grader-v1"
SELECTION = re.compile(r"^(yes|no)_(over|under)_([0-5])\.5$")
TERMINAL = frozenset({"ft", "aet", "pen", "finished"})

ROW_SQL = """
SELECT facts.prediction_id, facts.fixture_id, facts.selection,
       facts.final_outcome AS fact_actual,
       facts.settlement_classification AS fact_classification,
       f.status AS fixture_status, f.home_goals_ft, f.away_goals_ft,
       ep.id IS NOT NULL AS parent_present,
       ep.market AS parent_market, ep.prediction AS parent_selection,
       pr.settlement_status AS result_status, pr.actual AS result_actual,
       pr.is_correct AS result_is_correct,
       pr.market AS result_market, pr.predicted AS result_selection,
       pr.void_reason AS result_void_reason,
       rev.id AS revision_id, rev.settlement_status AS revision_status,
       rev.actual AS revision_actual, rev.is_correct AS revision_is_correct,
       rev.void_reason AS revision_void_reason,
       ledger.prediction_id IS NOT NULL AS ledger_present,
       ledger.included_in_accuracy, ledger.aggregate_date,
       ledger.market AS ledger_market, ledger.league_id,
       ledger.confidence_tier, ledger.model_version,
       ledger.counted_as, ledger.needs_recount
FROM model_evaluation_facts facts
JOIN fixtures f ON f.id = facts.fixture_id
LEFT JOIN engine_predictions ep ON ep.id = facts.prediction_id
LEFT JOIN prediction_results pr ON pr.prediction_id = facts.prediction_id
LEFT JOIN LATERAL (
  SELECT id, settlement_status, actual, is_correct, void_reason
  FROM prediction_result_revisions
  WHERE prediction_id = facts.prediction_id
  ORDER BY recorded_at DESC, id DESC LIMIT 1
) rev ON true
LEFT JOIN prediction_result_rollup_ledger ledger
  ON ledger.prediction_id = facts.prediction_id
WHERE facts.market = 'btts_total_goals'
  AND facts.prediction_id = %s
"""

SCAN_SQL = """
SELECT prediction_id FROM model_evaluation_facts
WHERE market = 'btts_total_goals' AND prediction_id > %s
  AND (%s::BIGINT IS NULL OR fixture_id = %s)
ORDER BY prediction_id LIMIT %s
"""


def canonical(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), default=str).encode()


def grade(selection: str, home: int, away: int) -> tuple[str, bool]:
    match = SELECTION.fullmatch(selection)
    if match is None:
        raise ValueError("unsupported selection")
    actual = f"{'yes' if home > 0 and away > 0 else 'no'}_{'over' if home + away > int(match[3]) else 'under'}_{match[3]}.5"
    return actual, selection == actual


def classify(row: dict) -> dict:
    prediction_id = str(row["prediction_id"])
    status = row["result_status"] or row["revision_status"] or "settled"
    prior_actual = (row["result_actual"] if row["result_status"] is not None
                    else row["revision_actual"] if row["revision_status"] is not None
                    else row["fact_actual"])
    prior_correct = (row["result_is_correct"] if row["result_status"] is not None
                     else row["revision_is_correct"] if row["revision_status"] is not None
                     else row["fact_classification"] == "correct")
    item = {
        "prediction_id": prediction_id, "fixture_id": row["fixture_id"],
        "selection": row["selection"], "score": [row["home_goals_ft"], row["away_goals_ft"]],
        "fact_actual": row["fact_actual"],
        "fact_classification": row["fact_classification"],
        "fixture_status": row["fixture_status"], "prior_status": status,
        "prior_actual": prior_actual, "prior_is_correct": prior_correct,
        "revision_id": row["revision_id"], "parent_present": row["parent_present"],
        "parent_market": row["parent_market"],
        "parent_selection": row["parent_selection"],
        "result_present": row["result_status"] is not None,
        "result_market": row["result_market"],
        "result_selection": row["result_selection"],
        "ledger_present": row["ledger_present"],
        "included_in_accuracy": row["included_in_accuracy"],
        "aggregate_date": str(row["aggregate_date"]) if row["aggregate_date"] is not None else None,
        "ledger_market": row["ledger_market"], "league_id": row["league_id"],
        "confidence_tier": row["confidence_tier"],
        "model_version": row["model_version"],
        "counted_as": row["counted_as"], "needs_recount": row["needs_recount"],
    }
    reason = None
    if status != "settled" or row["fact_classification"] not in {"correct", "incorrect"}:
        reason = "not_settled_original"
    elif str(row["fixture_status"]).lower() not in TERMINAL:
        reason = "fixture_not_finished"
    elif any(v is None or not isinstance(v, int) or v < 0 for v in item["score"]):
        reason = "missing_official_score"
    else:
        try:
            item["proposed_actual"], item["proposed_is_correct"] = grade(
                row["selection"], *item["score"]
            )
        except ValueError:
            reason = "unsupported_selection"
    if reason is None:
        changed = (prior_actual != item["proposed_actual"]
                   or prior_correct != item["proposed_is_correct"])
        if not changed:
            reason = "already_correct"
        elif row["parent_present"] and (
            row["parent_market"] != "btts_total_goals"
            or row["parent_selection"] != row["selection"]
        ):
            reason = "parent_evidence_mismatch"
        elif row["result_status"] is not None and (
            row["result_market"] != "btts_total_goals"
            or row["result_selection"] != row["selection"]
        ):
            reason = "result_evidence_mismatch"
        elif not row["parent_present"]:
            reason = "raw_parent_pruned"
        elif not row["ledger_present"] and row["result_status"] == "settled":
            # The aggregator inserts the ledger and deletes the raw result in
            # one statement. A still-present result without a ledger has not
            # contributed to prediction_accuracy_daily.
            reason = "eligible_unaggregated_survivor"
        elif not row["ledger_present"]:
            reason = "missing_rollup_provenance"
        elif row["needs_recount"]:
            reason = "pending_provider_recount"
        elif row["included_in_accuracy"] and (
            row["aggregate_date"] is None or row["counted_as"] not in {"correct", "incorrect"}
            or any(row[key] is None for key in ("ledger_market", "league_id", "confidence_tier", "model_version"))
        ):
            reason = "missing_accuracy_bucket_provenance"
        elif row["included_in_accuracy"] and row["counted_as"] != (
            "correct" if prior_correct else "incorrect"
        ):
            reason = "ledger_result_mismatch"
        else:
            reason = "eligible_survivor"
    item["classification"] = reason
    item["state_sha256"] = hashlib.sha256(canonical(item)).hexdigest()
    return item


def preview(conn: psycopg.Connection, page_size: int, fixture_id: int | None = None) -> dict:
    cursor = uuid.UUID(int=0)
    rows = []
    with conn.transaction():
        conn.execute("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY")
        while True:
            ids = [r["prediction_id"] for r in conn.execute(
                SCAN_SQL, (cursor, fixture_id, fixture_id, page_size)
            )]
            if not ids:
                break
            for prediction_id in ids:
                row = conn.execute(ROW_SQL, (prediction_id,)).fetchone()
                if row is not None:
                    rows.append(classify(row))
            cursor = ids[-1]
    counts: dict[str, int] = {}
    for row in rows:
        counts[row["classification"]] = counts.get(row["classification"], 0) + 1
    manifest = {"algorithm": ALGORITHM, "created_at": datetime.now().astimezone().isoformat(),
                "fixture_id": fixture_id,
                "counts": counts, "rows": rows}
    manifest["rows_sha256"] = hashlib.sha256(canonical(rows)).hexdigest()
    return manifest


def eligible_rows(manifest: dict) -> list[dict]:
    return [r for r in manifest["rows"] if r["classification"] in {
        "eligible_survivor", "eligible_unaggregated_survivor"
    }]


def batch_sha256(rows: list[dict]) -> str:
    return hashlib.sha256(canonical(rows)).hexdigest()


def apply_one(conn: psycopg.Connection, expected: dict, operation_id: uuid.UUID,
              manifest_sha256: str) -> str:
    prediction_id = uuid.UUID(expected["prediction_id"])
    # SERIALIZABLE detects a competing scorer, aggregator or retention writer;
    # explicit locks also keep the correction's own order stable.
    with conn.transaction():
        conn.execute("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")
        conn.execute("SET LOCAL lock_timeout = '2s'")
        conn.execute("SET LOCAL statement_timeout = '15s'")
        conn.execute("SELECT id FROM fixtures WHERE id = %s FOR UPDATE", (expected["fixture_id"],))
        parent = conn.execute("SELECT id FROM engine_predictions WHERE id = %s FOR UPDATE", (prediction_id,)).fetchone()
        if parent is None:
            raise ValueError(f"raw parent vanished: {prediction_id}")
        conn.execute("SELECT prediction_id FROM prediction_results WHERE prediction_id = %s FOR UPDATE", (prediction_id,))
        conn.execute("SELECT prediction_id FROM prediction_result_rollup_ledger WHERE prediction_id = %s FOR UPDATE", (prediction_id,))
        current = conn.execute(ROW_SQL, (prediction_id,)).fetchone()
        if current is None:
            raise ValueError(f"evidence vanished: {prediction_id}")
        item = classify(current)
        if item["classification"] == "already_correct":
            existing = conn.execute("SELECT 1 FROM prediction_result_revisions WHERE prediction_id=%s AND correction_operation_id=%s", (prediction_id, operation_id)).fetchone()
            if existing:
                return "already_applied"
        if item != expected or item["classification"] not in {
            "eligible_survivor", "eligible_unaggregated_survivor"
        }:
            raise ValueError(f"reviewed state changed: {prediction_id}")
        old_counter = "correct" if item["prior_is_correct"] else "incorrect"
        new_counter = "correct" if item["proposed_is_correct"] else "incorrect"
        if item["included_in_accuracy"] and old_counter != new_counter:
            daily = conn.execute("""SELECT total_predictions, correct, incorrect FROM prediction_accuracy_daily
                WHERE aggregate_date=%s AND market=%s AND league_id=%s AND confidence_tier=%s
                  AND model_version=%s FOR UPDATE""", (
                item["aggregate_date"], item["ledger_market"], item["league_id"],
                item["confidence_tier"], item["model_version"])).fetchone()
            if daily is None or daily[old_counter] < 1:
                raise ValueError(f"missing or depleted accuracy bucket: {prediction_id}")
        context = {"algorithm": ALGORITHM, "official_score": item["score"],
                   "prior_actual": item["prior_actual"],
                   "prior_is_correct": item["prior_is_correct"],
                   "prior_revision_id": item["revision_id"],
                   "state_sha256": item["state_sha256"],
                   "manifest_sha256": manifest_sha256}
        if item["revision_id"] is None:
            conn.execute("""INSERT INTO prediction_result_revisions
              (prediction_id, fixture_id, settlement_status, actual, is_correct, revision_reason)
              VALUES (%s,%s,'settled',%s,%s,'initial')""", (
                prediction_id, item["fixture_id"], item["prior_actual"], item["prior_is_correct"]))
        conn.execute("""INSERT INTO prediction_result_revisions
          (prediction_id, fixture_id, settlement_status, actual, is_correct,
           revision_reason, correction_operation_id, correction_context)
          VALUES (%s,%s,'settled',%s,%s,'manual_correction',%s,%s)""", (
            prediction_id, item["fixture_id"], item["proposed_actual"],
            item["proposed_is_correct"], operation_id, Jsonb(context)))
        if item["result_present"]:
            conn.execute("""UPDATE prediction_results SET actual=%s, is_correct=%s
              WHERE prediction_id=%s AND settlement_status='settled'""", (
                item["proposed_actual"], item["proposed_is_correct"], prediction_id))
        if item["included_in_accuracy"] and old_counter != new_counter:
            conn.execute(f"""UPDATE prediction_accuracy_daily SET {old_counter}={old_counter}-1,
              {new_counter}={new_counter}+1, aggregated_at=now()
              WHERE aggregate_date=%s AND market=%s AND league_id=%s
                AND confidence_tier=%s AND model_version=%s""", (
                item["aggregate_date"], item["ledger_market"], item["league_id"],
                item["confidence_tier"], item["model_version"]))
            conn.execute("""UPDATE prediction_result_rollup_ledger SET counted_as=%s
              WHERE prediction_id=%s""", (new_counter, prediction_id))
        effective = conn.execute("""SELECT final_outcome, settlement_classification
          FROM effective_model_evaluation_facts WHERE prediction_id=%s""", (prediction_id,)).fetchone()
        if effective["final_outcome"] != item["proposed_actual"] or effective["settlement_classification"] != new_counter:
            raise ValueError(f"effective evidence mismatch: {prediction_id}")
    return "applied"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--page-size", type=int, default=250)
    parser.add_argument("--fixture-id", type=int, help="optional bounded one-fixture manifest")
    parser.add_argument("--output", type=Path, help="write dry-run manifest here")
    parser.add_argument("--apply-manifest", type=Path, help="explicit reviewed manifest")
    parser.add_argument("--expected-sha256", help="SHA256 of exact reviewed manifest bytes")
    parser.add_argument("--expected-batch-sha256", help="SHA256 of reviewed eligible-row batch")
    parser.add_argument("--operation-id", help="stable UUID for idempotent retry")
    parser.add_argument("--batch-start", type=int, default=0,
                        help="zero-based offset in the manifest's eligible rows")
    parser.add_argument("--batch-size", type=int, default=0,
                        help="reviewed number of eligible rows in this batch, 1..100")
    args = parser.parse_args()
    if not 1 <= args.page_size <= 1000:
        parser.error("page size must be 1..1000")
    if args.fixture_id is not None and args.fixture_id <= 0:
        parser.error("fixture id must be positive")
    dsn = os.environ.get("DATABASE_URL")
    if not dsn:
        parser.error("DATABASE_URL is required")
    with psycopg.connect(dsn, row_factory=dict_row, autocommit=True) as conn:
        if args.apply_manifest:
            if (not args.expected_sha256 or not args.expected_batch_sha256
                    or not args.operation_id or args.batch_start < 0
                    or not 1 <= args.batch_size <= 100):
                parser.error("apply requires manifest SHA, batch SHA, operation UUID, start and size 1..100")
            content = args.apply_manifest.read_bytes()
            digest = hashlib.sha256(content).hexdigest()
            if digest != args.expected_sha256:
                parser.error("manifest hash mismatch")
            manifest = json.loads(content)
            if manifest.get("algorithm") != ALGORITHM or hashlib.sha256(canonical(manifest["rows"])).hexdigest() != manifest.get("rows_sha256"):
                parser.error("unsupported or altered manifest")
            all_candidates = eligible_rows(manifest)
            candidates = all_candidates[args.batch_start:args.batch_start + args.batch_size]
            if len(candidates) != args.batch_size:
                parser.error("requested batch extends beyond eligible rows")
            if batch_sha256(candidates) != args.expected_batch_sha256:
                parser.error("reviewed batch hash mismatch")
            operation_id = uuid.UUID(args.operation_id)
            for row in candidates:
                print(json.dumps({"prediction_id": row["prediction_id"],
                                  "result": apply_one(conn, row, operation_id, digest)}))
        else:
            manifest = preview(conn, args.page_size, args.fixture_id)
            content = (json.dumps(manifest, sort_keys=True, indent=2, default=str) + "\n").encode()
            if args.output:
                args.output.write_bytes(content)
            candidates = eligible_rows(manifest)
            batch_hashes = [
                {"start": start, "size": len(candidates[start:start + 100]),
                 "sha256": batch_sha256(candidates[start:start + 100])}
                for start in range(0, len(candidates), 100)
            ]
            print(json.dumps({"dry_run": True, "counts": manifest["counts"],
                              "rows_sha256": manifest["rows_sha256"],
                              "manifest_sha256": hashlib.sha256(content).hexdigest(),
                              "eligible_batch_hashes": batch_hashes,
                              "output": str(args.output) if args.output else None}))


if __name__ == "__main__":
    main()
