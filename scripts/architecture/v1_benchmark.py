#!/usr/bin/env python3
"""Run the fixed, provider-free v1 prediction benchmark."""

from __future__ import annotations

import argparse
from datetime import datetime
import hashlib
import json
from pathlib import Path
import sys
from typing import Any


def canonical_json(value: Any) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()


def sha256(value: Any) -> str:
    return hashlib.sha256(canonical_json(value)).hexdigest()


def run_benchmark(repository: Path, fixture_path: Path) -> dict[str, Any]:
    engine_root = repository / "services" / "prediction-engine"
    sys.path.insert(0, str(engine_root))
    from prediction_engine.acca import AccaRequest, select_acca
    from prediction_engine.backtest import BacktestPrediction, run_backtest
    from prediction_engine.persistence import ConfidenceTier, EnginePrediction, GateTier, PredictionLifecycleStage

    fixture = json.loads(fixture_path.read_text(encoding="utf-8"))
    backtest_rows = [
        BacktestPrediction(
            fixture_id=str(row["fixture_id"]),
            market=row["market"],
            prediction=row["prediction"],
            confidence_tier=ConfidenceTier(row["confidence_tier"]),
        )
        for row in fixture["backtest_predictions"]
    ]
    outcomes = {str(key): value for key, value in fixture["actual_outcomes"].items()}
    backtest = run_backtest(backtest_rows, actual_outcome=lambda row: outcomes[row.fixture_id])

    legs = [_engine_prediction(row, EnginePrediction, ConfidenceTier, GateTier, PredictionLifecycleStage) for row in fixture["acca_candidates"]]
    request_data = fixture["acca_request"]
    request = AccaRequest(
        start_datetime=datetime.fromisoformat(request_data["start_datetime"]),
        end_datetime=datetime.fromisoformat(request_data["end_datetime"]),
        legs_requested=request_data["legs_requested"],
        per_fixture_cap=request_data["per_fixture_cap"],
        minimum_confidence_tier=ConfidenceTier(request_data["minimum_confidence_tier"]),
    )
    acca = select_acca(legs, request)
    output = {
        "acca": {
            "combined_fair_odds": round(acca.combined_fair_odds, 8),
            "combined_fair_probability": round(acca.combined_fair_probability, 8),
            "legs": [f"{leg.fixture_id}:{leg.market}:{leg.prediction}" for leg in acca.legs],
            "target_met": acca.target_met,
        },
        "backtest": {
            "accuracy_by_tier": {tier.value: round(value, 8) for tier, value in backtest.accuracy_by_tier.items()},
            "confidence_ordering_holds": backtest.confidence_ordering_holds,
            "correct_predictions": backtest.correct_predictions,
            "overall_accuracy": round(backtest.overall_accuracy, 8),
            "total_predictions": backtest.total_predictions,
        },
    }
    config = fixture["benchmark_config"]
    return {
        "format_version": 1,
        "as_of_timestamp": fixture["as_of_timestamp"],
        "config_sha256": sha256(config),
        "input_sha256": sha256(fixture),
        "output": output,
        "output_sha256": sha256(output),
    }


def _engine_prediction(row: dict[str, Any], prediction_type: Any, confidence_type: Any, gate_type: Any, lifecycle_type: Any) -> Any:
    return prediction_type(
        fixture_id=row["fixture_id"], market=row["market"], prediction=row["prediction"],
        probability=row["probability"], confidence=row["confidence"],
        confidence_tier=confidence_type(row["confidence_tier"]), is_best_bet=row["is_best_bet"],
        lifecycle_stage=lifecycle_type(row["lifecycle_stage"]), data_completeness=row["data_completeness"],
        features_used=row["features_used"], explanation=row["explanation"],
        generated_at=datetime.fromisoformat(row["generated_at"]), gate_tier=gate_type(row["gate_tier"]),
        horizon_eligible=row["horizon_eligible"], embargo_active=row["embargo_active"],
        model_version=row["model_version"],
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    repository = Path(__file__).resolve().parents[2]
    parser.add_argument("--repository", type=Path, default=repository)
    parser.add_argument("--fixture", type=Path, default=repository / "tests" / "architecture" / "fixtures" / "v1-benchmark.json")
    parser.add_argument("--expected", type=Path, default=repository / "tests" / "architecture" / "fixtures" / "v1-benchmark.expected.json")
    parser.add_argument("--verify", action="store_true")
    args = parser.parse_args()
    result = run_benchmark(args.repository.resolve(), args.fixture.resolve())
    if args.verify:
        expected = json.loads(args.expected.read_text(encoding="utf-8"))
        if result != expected:
            print(json.dumps({"expected": expected, "actual": result}, indent=2, sort_keys=True), file=sys.stderr)
            return 1
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
