"""Architecture v2 Model C: the trained tabular 1X2 model (section 8.3).

A gradient-boosted tree model trained offline by `model_lab.model_c_train` on
`form_features`, exported as JSON and evaluated here with the standard
library (`gbdt`). It carries zero ensemble weight and is served only as a
shadow challenger (`challengers`), so production output does not depend on it.

Like Model B it publishes 1X2, double chance and draw no bet only: the
artifact is a three-class match-result model, and section 8.3 makes 1X2 its
first target, with other markets a later phase.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from functools import lru_cache
from pathlib import Path
from typing import Mapping

from prediction_engine.gbdt import TreeEnsemble


MODEL_KEY = "model-c-gbdt"
MARKET_FAMILY = "1x2"
DEFAULT_ARTIFACT = Path(__file__).resolve().parent / "artifacts" / "model_c_1x2.json"
# Explanation factors kept per prediction, each way (section 16 asks for the
# top positive and top negative contributors).
_FACTORS = 3


@dataclass(frozen=True)
class ModelCPrediction:
    model_version: str
    match_result: dict[str, float]
    double_chance: dict[str, float]
    draw_no_bet: dict[str, float]
    explanation_factors: dict[str, object] = field(default_factory=dict)


@lru_cache(maxsize=4)
def load_model(path: Path = DEFAULT_ARTIFACT) -> TreeEnsemble:
    return TreeEnsemble.load(path)


def run_model_c(features: Mapping[str, float | None], model: TreeEnsemble | None = None) -> ModelCPrediction:
    model = model or load_model()
    vector = model.vector(features)
    probabilities = model.probabilities(vector)
    home, draw, away = probabilities["home"], probabilities["draw"], probabilities["away"]
    pick = max(probabilities, key=probabilities.__getitem__)
    base, contributions = model.contributions(vector, pick)
    ranked = sorted(contributions.items(), key=lambda item: item[1])
    return ModelCPrediction(
        model_version=str(model.metadata.get("model_version", "")),
        match_result={"home": home, "draw": draw, "away": away},
        double_chance={"home_or_draw": home + draw, "draw_or_away": draw + away, "home_or_away": home + away},
        draw_no_bet={"home": home / (home + away), "away": away / (home + away)},
        explanation_factors={
            "method": "path_attribution",
            "explained_outcome": pick,
            "base_value": round(base, 6),
            "top_positive": [
                {"feature": name, "contribution": round(value, 6), "value": features[name]}
                for name, value in reversed(ranked[-_FACTORS:])
                if value > 0
            ],
            "top_negative": [
                {"feature": name, "contribution": round(value, 6), "value": features[name]}
                for name, value in ranked[:_FACTORS]
                if value < 0
            ],
        },
    )
