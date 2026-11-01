from __future__ import annotations

from pathlib import Path
import re
import unittest


class EngineBoundaryTests(unittest.TestCase):
    def test_prediction_engine_contains_no_network_or_api_football_client_code(self) -> None:
        engine_root = Path("services/prediction-engine/prediction_engine")
        forbidden = (
            "api-football",
            "api_football",
            "x-rapidapi-key",
            "rapidapi",
            "httpx",
            "aiohttp",
            "urllib.request",
        )

        offenders: list[str] = []
        for path in engine_root.rglob("*.py"):
            body = path.read_text(encoding="utf-8").lower()
            for marker in forbidden:
                if marker in body:
                    offenders.append(f"{path}:{marker}")
            if re.search(r"^\s*(?:from|import)\s+requests\b", body, re.MULTILINE):
                offenders.append(f"{path}:requests")

        self.assertEqual(offenders, [])

    def test_feature_generation_contains_no_wall_clock_fallback(self) -> None:
        engine_root = Path("services/prediction-engine/prediction_engine")
        feature_path_files = (
            engine_root / "features.py",
            engine_root / "pipeline.py",
            engine_root / "db_repository.py",
        )
        forbidden = ("datetime.now(", "datetime.utcnow(", "time.time(", "CURRENT_TIMESTAMP")

        offenders = [
            f"{path}:{marker}"
            for path in feature_path_files
            for marker in forbidden
            if marker in path.read_text(encoding="utf-8")
        ]

        self.assertEqual(offenders, [])


if __name__ == "__main__":
    unittest.main()
