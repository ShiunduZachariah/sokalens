# Prediction Engine v2 Configuration Foundation

Date: 2026-08-14

This configuration extends the active Python prediction engine without activating
new model behavior. The existing v1 model weights, form decay, scoring, ranking,
and fixed benchmark output remain unchanged. The `v2` group records validated
inputs for future challenger work; those values only affect output when the
corresponding v2 phase explicitly wires them into a registered challenger.

## Defaults

| Group | Default |
|---|---|
| Tier B/C completeness additions | `0.10` / `0.15` above the base full-data floor |
| V2 Tier A ensemble | Model A `1.0`, Model B `0.0`, Model C `0.0` for every configured Tier A family |
| Model A season/recent blend | season-long `0.70`, recent form `0.30` |
| Ranking weights | probability `0.30`, confidence `0.25`, completeness `0.20`, market accuracy `0.10`, league accuracy `0.10`, odds edge `0.05` |
| Feature decay | match result `0.90`, xG `0.88`, discipline `0.80`, corners `0.85`, fouls `0.80`, lineup availability `0.25` |
| Recommendation tier | `medium` for each configured market family |
| Calibration | isotonic primary; beta for possession/pass accuracy; Platt low-sample fallback |
| Calibration sample | `500` settled predictions per market family |
| OOD thresholds | confidence cap at `0.60`; force BASIC at `0.80` |
| Drift windows | `200` predictions for accuracy; `30` days for calibration |
| Drift thresholds | alert at `0.05` accuracy drop/calibration error; retraining investigation at `0.10` |
| Retraining sample | `500` new settled predictions |
| Promotion sample | `500` shadow-settled predictions |
| Promotion significance | `0.95` confidence level |

The registered ranking defaults mirror the active values in `ranking.py`, but
this task does not wire the registration-time config into ranking behavior. That
activation belongs in a versioned challenger so a configuration refactor cannot
silently shift stored scores or the fixed v1 benchmark.

The lineup decay, OOD, and drift threshold values are conservative foundation
values because the architecture does not prescribe exact numeric defaults for
them. They must be evaluated through point-in-time walk-forward testing before
any v2 challenger is eligible for promotion. The Model A-only Tier A ensemble
default is deliberate: merely adding configuration must not silently turn an
untrained or unregistered model into a production signal.

Configured market families are: 1X2, double chance, BTTS, total goals, team
goals, HT/FT, correct score, clean sheet, win to nil, corners, cards, fouls,
offsides, red-card occurrence, shots, possession, pass accuracy, goalkeeper
saves, and combination markets. Cross-model ensemble weights are required only
for the Tier A market families modeled by Models A/B/C.

## Fail-fast validation

`PredictionEngineConfig.validate()` rejects startup configuration when:

- Tier strictness is outside `[0, 1]`, Tier C is below Tier B, or the resulting
  Tier C completeness floor exceeds `1`.
- Any model, season/recent, or ranking weight is non-finite, negative, or its
  group does not sum to `1.0`.
- Any feature decay is outside the open interval `(0, 1)`.
- Recommendation or calibration maps omit required markets, add unknown
  markets, or contain unsupported values.
- Calibration, drift, retraining, or promotion sample/window values are not
  positive integers. Booleans are not accepted as integers.
- OOD thresholds are not strictly ordered as
  `0 < confidence_cap < force_basic < 1`.
- Drift alert thresholds are not below their corresponding retraining
  investigation thresholds.
- The promotion confidence level is not strictly between `0.5` and `1`.
- An override has an unknown section or malformed shape.

There is no mid-run fallback. A rejected configuration raises
`ConfigValidationError` before prediction work begins, preserving reproducible
input/config/model/output hashes.

## Override shape

The current loader accepts an in-process dictionary overlay. An example for a
future registered challenger is:

```python
load_config(
    {
        "v2": {
            "ensemble_weights": {
                "1x2": {"model_a": 0.50, "model_b": 0.20, "model_c": 0.30}
            },
            "calibration": {
                "primary_method_by_market": {"1x2": "platt"},
                "minimum_sample_by_market": {"1x2": 750},
            },
        }
    }
)
```

Partial per-market overlays merge into the complete default maps, then the full
configuration is validated. No environment variable or public request path is
introduced by this foundation task.
