"""Inference for gradient-boosted tree ensembles exported by the model lab.

ADR 0002 keeps the serving engine on the standard library: training runs
offline with LightGBM (`model_lab.model_c_train`), and the trained trees are
exported to a small JSON format that this module evaluates. Walking a tree is
a handful of comparisons, so serving needs no numerical library, and the
exporter checks this evaluator against LightGBM's own predictions before it
writes an artifact, so the two cannot quietly disagree.

Format `sokalens-gbdt-v1`: `trees` lists every tree in LightGBM's order
(iteration-major, so tree `i` adds to class `i % num_class`). Each tree is a
list of nodes indexed from the root at 0. A leaf is `[value]`. A split is
`[feature, threshold, left, right, default_left, missing_type, value]`, where
`value` is the node's own output before the split (LightGBM's
`internal_value`) and `missing_type` is 0 for none, 1 for zero and 2 for NaN,
following LightGBM's semantics for each.

Per-prediction explanations use path attribution (Saabas): each split on the
path from root to leaf credits the change in node value to the feature it
split on. Section 8.3 asks for SHAP "or an equivalent local
feature-contribution method"; path attribution is exact in the sense that the
contributions plus the base value sum to the raw score, needs nothing beyond
the exported tree, and ranks features close to TreeSHAP on shallow trees.
"""

from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
import math
from pathlib import Path
from typing import Mapping, Sequence


ARTIFACT_FORMAT = "sokalens-gbdt-v1"

_MISSING_NONE = 0
_MISSING_ZERO = 1
_MISSING_NAN = 2
# LightGBM treats |x| <= this as zero for missing_type "Zero".
_ZERO_THRESHOLD = 1e-35


@dataclass(frozen=True)
class TreeEnsemble:
    feature_names: tuple[str, ...]
    classes: tuple[str, ...]
    trees: tuple[tuple[tuple[float, ...], ...], ...]
    metadata: Mapping[str, object]
    artifact_hash: str

    @classmethod
    def from_json(cls, raw: str) -> "TreeEnsemble":
        payload = json.loads(raw)
        if payload.get("format") != ARTIFACT_FORMAT:
            raise ValueError(f"unsupported tree artifact format: {payload.get('format')!r}")
        feature_names = tuple(str(name) for name in payload["feature_names"])
        classes = tuple(str(name) for name in payload["classes"])
        trees = tuple(tuple(tuple(float(value) for value in node) for node in tree) for tree in payload["trees"])
        if not classes or len(trees) % len(classes) != 0:
            raise ValueError("tree count must be a multiple of the class count")
        for tree in trees:
            for node in tree:
                if len(node) == 7 and not 0 <= int(node[0]) < len(feature_names):
                    raise ValueError("split references a feature outside the artifact's feature list")
                if len(node) not in (1, 7):
                    raise ValueError("tree nodes must be leaves of length 1 or splits of length 7")
        metadata = {key: value for key, value in payload.items() if key not in {"trees"}}
        return cls(
            feature_names=feature_names,
            classes=classes,
            trees=trees,
            metadata=metadata,
            artifact_hash=hashlib.sha256(raw.encode("utf-8")).hexdigest(),
        )

    @classmethod
    def load(cls, path: Path) -> "TreeEnsemble":
        return cls.from_json(path.read_text(encoding="utf-8"))

    def vector(self, features: Mapping[str, float | None]) -> list[float | None]:
        missing = [name for name in self.feature_names if name not in features]
        if missing:
            raise ValueError(f"features missing from the input: {', '.join(missing)}")
        return [features[name] for name in self.feature_names]

    def raw_scores(self, vector: Sequence[float | None]) -> list[float]:
        scores = [0.0] * len(self.classes)
        for index, tree in enumerate(self.trees):
            scores[index % len(self.classes)] += _leaf_value(tree, vector)
        return scores

    def probabilities(self, vector: Sequence[float | None]) -> dict[str, float]:
        return dict(zip(self.classes, _softmax(self.raw_scores(vector))))

    def expected_count(self, vector: Sequence[float | None]) -> float:
        """A Poisson model's expected count: the exponential of its one raw score."""
        if len(self.classes) != 1:
            raise ValueError("expected_count needs a single-output model")
        return math.exp(self.raw_scores(vector)[0])

    def contributions(self, vector: Sequence[float | None], class_name: str) -> tuple[float, dict[str, float]]:
        """Base value and per-feature contributions to one class's raw score."""
        class_index = self.classes.index(class_name)
        base = 0.0
        credit: dict[str, float] = {}
        for index, tree in enumerate(self.trees):
            if index % len(self.classes) != class_index:
                continue
            node = tree[0]
            base += node[-1] if len(node) == 7 else node[0]
            position = 0
            while len(tree[position]) == 7:
                split = tree[position]
                child = _child(split, vector[int(split[0])])
                child_node = tree[child]
                child_value = child_node[-1] if len(child_node) == 7 else child_node[0]
                name = self.feature_names[int(split[0])]
                credit[name] = credit.get(name, 0.0) + child_value - split[6]
                position = child
        return base, credit


def _leaf_value(tree: tuple[tuple[float, ...], ...], vector: Sequence[float | None]) -> float:
    position = 0
    while len(tree[position]) == 7:
        split = tree[position]
        position = _child(split, vector[int(split[0])])
    return tree[position][0]


def _child(split: tuple[float, ...], value: float | None) -> int:
    _, threshold, left, right, default_left, missing_type, _ = split
    missing_type = int(missing_type)
    is_nan = value is None or (isinstance(value, float) and math.isnan(value))
    if missing_type == _MISSING_NAN and is_nan:
        return int(left if default_left else right)
    if is_nan:
        value = 0.0
    if missing_type == _MISSING_ZERO and abs(float(value)) <= _ZERO_THRESHOLD:  # type: ignore[arg-type]
        return int(left if default_left else right)
    return int(left if float(value) <= threshold else right)  # type: ignore[arg-type]


def _softmax(scores: Sequence[float]) -> list[float]:
    peak = max(scores)
    exponents = [math.exp(score - peak) for score in scores]
    total = sum(exponents)
    return [value / total for value in exponents]
