# Models B and C: shadow challengers

Date: 2026-11-04

Architecture v2 section 8 calls for three independent views of a fixture:
Model A (goals), Model B (Elo, results only) and Model C (a trained tabular
model). Models B and C are built and run in production as **shadow
challengers**. They forecast every fixture the champion publishes, are settled
and compared with it, and carry **zero weight**: no public table, API or page
reads them, and published predictions are unchanged.

| | Model B | Model C |
| --- | --- | --- |
| Code | `prediction_engine/elo.py` | `prediction_engine/model_c.py`, `gbdt.py`, `form_features.py` |
| What it reads | Results only | Team form, season record, rest, league base rates |
| Fitted | Online, replaying history | Offline, `model_lab.model_c_train` (LightGBM) |
| Served | Standard library | Standard library: the exported trees in `artifacts/model_c_1x2.json` |
| Markets | 1X2, double chance, draw no bet | The same |
| Registry | `elo`, status `shadow` | `model-c-gbdt`, status `shadow` |

Elo's design is in [elo-challenger.md](elo-challenger.md).

## Model C

A three-class LightGBM model over 31 point-in-time features. The features are
computed by `FormFeatureBook`, the same class at training time and at serving
time, so training and serving cannot drift apart. It deliberately reads no odds
(it would learn to repeat the market) and no Elo ratings (B and C would then
agree because one reads the other, the circularity section 8.6 removed).

Training (`task model:c-train`) uses three time-ordered windows, never a random
split: training before 2021-07-01, validation to 2022-07-01, holdout after. Four
configurations were tried on validation only; the chosen one (15 leaves, at
least 400 rows a leaf) was refitted on training plus validation for 265 rounds,
then scored once on the holdout. The export is refused unless the standard
library evaluator reproduces LightGBM's probabilities; the worst difference
was 2.2e-16.

Each forecast stores its explanation factors: the top positive and negative
feature contributions to the most likely outcome, by path attribution (exact,
and the "equivalent local feature-contribution method" section 8.3 allows in
place of SHAP). The two features carrying most of the model are the
season points-per-game difference (29% of split gain) and the ten-match goal
difference gap (27%).

### Holdout, 2026-11-04

The same 48,591 holdout fixtures as the Elo walk-forward (39 leagues, from
2022-07-01). Lower is better for log loss, Brier and calibration error.

| Model | Log loss | Brier | Accuracy | Calibration error | Mean draw |
| --- | --- | --- | --- | --- | --- |
| Market (de-vigged closing odds) | 0.9988 | 0.5971 | 50.6% | 0.006 | 0.262 |
| **Elo + Model C, equal mix** | **1.0201** | **0.6113** | **49.0%** | 0.006 | 0.251 |
| Elo (Model B) | 1.0212 | 0.6119 | 49.0% | 0.011 | 0.241 |
| Model C | 1.0269 | 0.6162 | 48.3% | **0.003** | 0.261 |
| League frequency | 1.0743 | 0.6499 | 43.8% | 0.010 | 0.266 |

The champion's figure on these fixtures was 1.0223 and Model A alone 1.0889
(elo-challenger.md). Those come from a separate run, so they are not paired
with the rows above. The Model A fix has since moved them to 1.0089 and 1.0348
([model-a-expected-goals.md](model-a-expected-goals.md)), so the champion now
scores ahead of both challengers on this holdout.

| Paired comparison | Mean log-loss difference | z |
| --- | --- | --- |
| Model C vs Elo | +0.0057 | +7.0 |
| Mix vs Elo | −0.0011 | −2.8 |
| Model C vs league frequency | −0.0474 | −36.0 |
| Model C vs market | +0.0284 | +30.2 |

What this shows:

- **Model C alone is a little behind Elo, and it is the best calibrated model
  here**, the market included. It forecasts draws at the observed rate, where
  Elo under-forecasts them.
- **The two disagree usefully.** They pick the same outcome 86% of the time,
  and averaging them beats either one alone. That is the agreement section 8.5
  is built to measure.
- **Neither beats the closing market**, and the champion stays the champion
  until the shadow evidence below says otherwise.

The same commands also run both models across the other markets:
[challenger-markets.md](challenger-markets.md).

## How the shadow runs

Three engine commands, each attached by the worker to the job that owns its
stage (`services/worker/internal/predictionenginepython/runner.go`). They run
after the champion's commands and are marked optional, so a fault in a model
that publishes nothing never fails or retries the job that publishes.

| Command | Rides on | Does |
| --- | --- | --- |
| `run-challengers` | each upcoming-prediction run | Forecasts each fixture whose current champion 1X2 has no challenger forecast at that lifecycle stage; at most once per 30 minutes |
| `settle-challengers` | each settlement run | One immutable `shadow_settlements` row per forecast |
| `challenger-report` | the daily accuracy snapshot | Paired log loss against the champion, written to `champion_challenger_evaluations` |

Forecasts live in `challenger_predictions` (migration 000090). Each row also
stores the champion's full 1X2, recomputed by the champion's own pipeline at
the same as-of time (`pipeline.champion_match_result`): `engine_predictions`
holds only the champion's top selection and is pruned after days, so without
it the champion could not be scored on the same fixtures.

Both models are fitted from a single replay of every finished fixture from the
last 1,100 days. A fixture is admitted for a target only once it kicked off
three hours before the target's as-of time. The replay takes about four
seconds on the 302,000-match corpus.

Live evidence counts only from the current champion's `promoted_at`, because each
row stores the champion that was running when it was forecast.

The report **never promotes**. With fewer than 500 paired settled forecasts
the decision is `extend`; above that it is `pending`, for a person to decide
(section 21.5). Promotion would also need calibration (section 9) and a weight
chosen by walk-forward evidence (section 8.5).

`PREDICTION_ENGINE_CHALLENGERS_ENABLED=false` on the worker switches all three
off without a deploy.

### Replay origin and QA

`run-challengers --origin replay` forecasts recently finished fixtures as they
looked an hour before kick-off. QA's provider is a static mock, so no QA
fixture ever finishes. Without replay the QA deploy would prove only that the
commands start. `scripts/qa/challenger-smoke.sh` runs a replay, settlement and
report on every QA deploy. Replay rows are stored and settled under their own
origin and are never written as evaluation evidence.

## Known limits

- **Extra time.** A fixture that went to extra time is settled `excluded`: its
  stored score includes extra-time goals, and 1X2 settles on 90 minutes.
- **Corrections after settlement** are not re-settled; shadow evidence is
  immutable. Retractions are rare enough not to move a 500-fixture comparison,
  and the published path handles them as before.
- **The corpus is not production.** Model C was trained on 39 mostly European
  leagues without cups. Production covers far more competitions, which is
  exactly why the decision waits for shadow evidence on production fixtures.
