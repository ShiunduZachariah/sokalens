"""Which model's forecast each published market uses, from python-engine-v3.

Models B and C write their forecasts through `run-challengers`. This reads those
stored forecasts at publish time, for the markets where the 2022-07-01 holdout
showed them beating the champion. Evidence: docs/prediction-engine/promoted-markets.md.

| Markets | Published from |
| --- | --- |
| 1X2, double chance, draw no bet | The champion. Its odds anchor beats both models. |
| Totals, BTTS and the score-derived markets | Equal mix of the champion, B and C |
| Half-time result, highest-scoring half | Model B |
| Corners, cards, fouls, shots, shots on target | Model C |
| Odd/even, team to score first, possession | The champion. No model has evidence here. |

**This never adds coverage.** The gate and the per-market data requirements still
decide what is published; a forecast only replaces the probability of a market the
champion would publish anyway. Model C's counts were scored only on fixtures where
the champion has its five samples per side, which is the same condition.

**A missing or malformed forecast falls back to the champion**, market by market, so
a fault in a model can never stop a fixture publishing. The mix needs both models
because the holdout scored the mix of all three, not a mix of two.
"""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass
import math

from prediction_engine.challenger_markets import (
    CORRECT_SCORE_GOALS,
    COUNT_LINES,
    MODEL_B_KEY,
    MODEL_C_KEY,
    TOTAL_GOAL_LINES,
)
from prediction_engine.model_a import Matrix, _btts_total_goals, _top_correct_scores

CHAMPION = "champion"
MIX = "champion_b_c_mix"
MODEL_B = "model_b"
MODEL_C = "model_c"

# Two-way and multi-way markets the mix or Model B replace, as their documents name them.
_MIX_MARKETS = ("btts", "btts_match_result")
_MODEL_B_MARKETS = ("half_time_result", "highest_scoring_half")
_MIX_GRID_MARKETS = ("correct_score", "btts_total_goals", "result_total_goals")


@dataclass(frozen=True)
class ModelForecasts:
    """The newest stored forecast of Models B and C for one fixture, at the as-of time."""

    model_b: Mapping[str, Mapping[str, float]] | None = None
    model_c: Mapping[str, Mapping[str, float]] | None = None
    # Audit only: model version -> when that forecast was made.
    provenance: Mapping[str, str] | None = None


@dataclass(frozen=True)
class PublishedGoalMarkets:
    markets: dict[str, object]
    score_matrix: Matrix
    sources: dict[str, str]


def forecasts_from_rows(rows: list[Mapping[str, object]]) -> ModelForecasts | None:
    """Build the forecasts from `_MODEL_FORECASTS_SQL` rows."""
    by_key = {str(row["model_key"]): row for row in rows}
    model_b, model_c = by_key.get(MODEL_B_KEY), by_key.get(MODEL_C_KEY)
    if model_b is None and model_c is None:
        return None
    return ModelForecasts(
        model_b=None if model_b is None else model_b["markets"],  # type: ignore[arg-type]
        model_c=None if model_c is None else model_c["markets"],  # type: ignore[arg-type]
        provenance={
            str(row["model_version"]): str(row["as_of_timestamp"].isoformat())  # type: ignore[attr-defined]
            for row in by_key.values()
        },
    )


def _selection_values(
    document: Mapping[str, Mapping[str, float]] | None, key: str
) -> dict[str, float] | None:
    """One market of a forecast, or None unless every probability is usable."""
    if document is None:
        return None
    values = document.get(key)
    if not isinstance(values, Mapping) or not values:
        return None
    result: dict[str, float] = {}
    for selection, raw in values.items():
        try:
            probability = float(raw)
        except (TypeError, ValueError):
            return None
        if not math.isfinite(probability) or not 0.0 <= probability <= 1.0:
            return None
        result[str(selection)] = probability
    return result


def _mixed(
    champion: Mapping[str, float], model_b: Mapping[str, float] | None, model_c: Mapping[str, float] | None
) -> dict[str, float] | None:
    if model_b is None or model_c is None:
        return None
    # A selection one model does not name means the documents do not describe the
    # same market, so none of it can be mixed.
    if set(model_b) != set(champion) or set(model_c) != set(champion):
        return None
    return {name: (float(champion[name]) + model_b[name] + model_c[name]) / 3 for name in champion}


def _grid_from_document(document: Mapping[str, Mapping[str, float]] | None, champion: Matrix) -> Matrix | None:
    """A model's score grid, rebuilt from its stored correct-score cells.

    The forecast stores scores up to 5-5 plus "other". Every cell in that box is the
    model's own, and "other" is spread over the larger scores in the champion's
    proportions, so the rare scores beyond the box are the only approximation.
    """
    cells = _selection_values(document, "correct_score")
    if cells is None or "other" not in cells:
        return None
    size = len(champion)
    box = CORRECT_SCORE_GOALS + 1
    grid = [[0.0] * len(champion[home]) for home in range(size)]
    outside = 0.0
    for home in range(size):
        for away in range(len(champion[home])):
            if home < box and away < box:
                value = cells.get(f"{home}-{away}")
                if value is None:
                    return None
                grid[home][away] = value
            else:
                outside += float(champion[home][away])
    other = cells["other"]
    for home in range(size):
        for away in range(len(champion[home])):
            if home >= box or away >= box:
                grid[home][away] = other * float(champion[home][away]) / outside if outside > 0 else 0.0
    return grid


def _mixed_grid(champion: Matrix, forecasts: ModelForecasts) -> Matrix | None:
    model_b = _grid_from_document(forecasts.model_b, champion)
    model_c = _grid_from_document(forecasts.model_c, champion)
    if model_b is None or model_c is None:
        return None
    return [
        [(float(champion[home][away]) + model_b[home][away] + model_c[home][away]) / 3 for away in range(len(row))]
        for home, row in enumerate(champion)
    ]


def published_goal_markets(
    markets: Mapping[str, object], score_matrix: Matrix, forecasts: ModelForecasts | None
) -> PublishedGoalMarkets:
    """The champion's goal markets with each promoted market replaced where a forecast allows."""
    published: dict[str, object] = dict(markets)
    sources: dict[str, str] = {}
    if forecasts is None:
        return PublishedGoalMarkets(published, score_matrix, sources)

    for market in _MODEL_B_MARKETS:
        values = _selection_values(forecasts.model_b, market)
        current = markets.get(market)
        if values is not None and isinstance(current, Mapping) and set(values) == set(current):
            published[market] = values
            sources[market] = MODEL_B

    for market in _MIX_MARKETS:
        current = markets.get(market)
        if not isinstance(current, Mapping):
            continue
        mixed = _mixed(current, _selection_values(forecasts.model_b, market), _selection_values(forecasts.model_c, market))
        if mixed is not None:
            published[market] = mixed
            sources[market] = MIX

    totals = markets.get("total_goals")
    if isinstance(totals, Mapping):
        replaced = dict(totals)
        complete = True
        for line in TOTAL_GOAL_LINES:
            key = f"total_goals:{line:g}"
            champion = {f"over_{line:g}": totals.get(f"over_{line}"), f"under_{line:g}": totals.get(f"under_{line}")}
            if any(value is None for value in champion.values()):
                complete = False
                break
            mixed = _mixed(champion, _selection_values(forecasts.model_b, key), _selection_values(forecasts.model_c, key))  # type: ignore[arg-type]
            if mixed is None:
                complete = False
                break
            for name, value in mixed.items():
                replaced[name] = value
        if complete:
            published["total_goals"] = replaced
            sources["total_goals"] = MIX

    grid = _mixed_grid(score_matrix, forecasts)
    if grid is None:
        return PublishedGoalMarkets(published, score_matrix, sources)
    published["correct_score"] = _top_correct_scores(grid, len(markets.get("correct_score") or []) or 1)  # type: ignore[arg-type]
    published["btts_total_goals"] = _btts_total_goals(grid)
    sources.update({market: MIX for market in _MIX_GRID_MARKETS})
    return PublishedGoalMarkets(published, grid, sources)


def published_count_candidates(
    candidates: list[tuple[str, str, float]], forecasts: ModelForecasts | None
) -> tuple[list[tuple[str, str, float]], dict[str, str]]:
    """Replace each count market the champion would publish with Model C's strongest line."""
    if forecasts is None or forecasts.model_c is None:
        return candidates, {}
    result: list[tuple[str, str, float]] = []
    sources: dict[str, str] = {}
    for candidate in candidates:
        market = candidate[0]
        replacement = _strongest_count_line(market, forecasts.model_c) if market in COUNT_LINES else None
        if replacement is None:
            result.append(candidate)
            continue
        result.append(replacement)
        sources[market] = MODEL_C
    return result, sources


def _strongest_count_line(
    market: str, document: Mapping[str, Mapping[str, float]]
) -> tuple[str, str, float] | None:
    """The side and line Model C is surest of, as the champion's selection does for its own."""
    best: tuple[str, str, float] | None = None
    for line in COUNT_LINES[market]:
        values = _selection_values(document, f"{market}:{line:g}")
        over, under = f"over_{line:g}", f"under_{line:g}"
        if values is None or set(values) != {over, under}:
            return None
        selection = over if values[over] >= values[under] else under
        if best is None or values[selection] > best[2]:
            best = (market, selection, values[selection])
    return best
