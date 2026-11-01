#!/usr/bin/env python3
"""Audit acca legs for unsupported same-fixture correlation pricing."""

from __future__ import annotations

import argparse
from itertools import combinations
import json
from pathlib import Path
from typing import Any


JOINT_MARKETS = {"btts_total_goals", "btts_match_result", "result_total_goals"}


def audit_candidates(candidates: list[dict[str, Any]]) -> dict[str, Any]:
    pairs: list[dict[str, Any]] = []
    for left, right in combinations(candidates, 2):
        if left["fixture_id"] != right["fixture_id"]:
            continue
        markets = sorted((left["market"], right["market"]))
        pairs.append(
            {
                "fixture_id": left["fixture_id"],
                "markets": markets,
                "classification": "unsafe",
                "current_pricing_method": "naive_marginal_product",
                "reason": "select_acca multiplies marginal leg probabilities and has no joint price for two legs from one fixture",
                "contains_precomputed_joint_market": bool(set(markets) & JOINT_MARKETS),
            }
        )
    return {
        "format_version": 1,
        "same_fixture_pairs": pairs,
        "unsafe_pair_count": len(pairs),
        "recommendation": "default-deny multiple acca legs from one fixture until an explicit joint-price contract exists",
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    repository = Path(__file__).resolve().parents[2]
    parser.add_argument("--fixture", type=Path, default=repository / "tests" / "architecture" / "fixtures" / "v1-benchmark.json")
    args = parser.parse_args()
    fixture = json.loads(args.fixture.read_text(encoding="utf-8"))
    print(json.dumps(audit_candidates(fixture["acca_candidates"]), indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
