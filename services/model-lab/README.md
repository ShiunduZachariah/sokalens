# Model Lab Boundary

Offline model training, backtesting, notebooks, and Python experiments belong here.

Phase 1 reserves the boundary only. Python must not enter the live API, worker, web, admin, or mobile request paths without an explicit architecture decision.

The Football-Data.co.uk CSV corpus in `Model-Training-Data` (about 700 files, 65 MB) is read-only offline input for this boundary. It is third-party data, so it is not tracked: download it from football-data.co.uk into the ignored `Model-Training-Data/` folder, keeping this layout:

- `raw/main/<Country>/<season>/<Div>.csv`, e.g. `raw/main/Belgium/1995-1996/B1.csv`, from `https://www.football-data.co.uk/mmz4281/<YYZZ>/<Div>.csv`
- `raw/extra/<Country>/all-seasons/<CODE>.csv`, e.g. `raw/extra/Argentina/all-seasons/ARG.csv`, from `https://www.football-data.co.uk/new/<CODE>.csv`

Rules for using it:

- Never feed it into provider ingestion jobs or canonical tables such as `matches`; if it is loaded into PostgreSQL, use separate tables such as `model_training_raw_matches`.
- Backtest chronologically: train on older seasons, test on later ones.
- Post-match statistics (shots, corners, fouls, cards, half-time score) are labels, never pre-match features.
- Treat closing odds as a separate feature family: they are near-kickoff market information.
- Field availability varies by league and season; tolerate schema drift, blank headers and abandoned matches.

## Football-Data Backtest Scaffold

The `model_lab.football_data` module reads Football-Data CSV files directly and keeps the data offline:

```powershell
$env:PYTHONPATH = "services/model-lab"
python -m model_lab.football_data Model-Training-Data/raw/extra/Argentina/all-seasons/ARG.csv
```

You can also pass the whole dataset directory and it will recurse for `*.csv` files:

```powershell
$env:PYTHONPATH = "services/model-lab"
python -m model_lab.football_data Model-Training-Data --train-fraction 0.7
```

It normalizes only safe pre-match fields plus final-score labels, skips rows without final results, counts duplicate completed fixtures, sorts fixtures chronologically, and runs a deterministic points-per-match baseline over a train/test split.
The CLI JSON output now includes parser diagnostics, explicit `tested_matches`, `successful_predictions`, `failed_predictions`, and `hit_rate` fields, plus market benchmark success rates for `1x2`, `double_chance`, `btts`, total-goals lines, team goals, `ht_ft`, corners, cards, fouls, and throw-ins when labels are present.
It also includes `one_x_two_benchmarks` with:
- heuristic 1X2 behavior diagnostics (draw-rate tendency),
- always-home baseline accuracy,
- odds-favorite baseline accuracy using closing odds where available,
- multinomial logistic regression benchmark accuracy trained on implied odds + rolling form features.

Treat logistic regression here as an experimental benchmark model. Until it outperforms `odds_favorite_accuracy`, odds-favorite remains the stronger 1X2 baseline.

## Elo Walk-Forward

`model_lab.elo_walk_forward` scores the Elo challenger (`prediction_engine.elo`)
against the live engine's Model A, its v1 blend, the published champion and
simple baselines. Every model is scored on the same holdout fixtures, and Elo's
parameters are tuned only on the years before the holdout:

```powershell
task model:elo-walk-forward
task model:elo-walk-forward HOLDOUT_FROM=2023-07-01 -- --no-champion
```

It needs both `services/model-lab` and `services/prediction-engine` on
`PYTHONPATH`; the task sets that. The full corpus takes several minutes; `--no-champion` is much faster.
Results and caveats: `docs/prediction-engine/elo-challenger.md`.

## Model C Training

`model_lab.model_c_train` trains architecture v2 Model C, a LightGBM
gradient-boosted tree model over `prediction_engine.form_features`, and exports
it to `services/prediction-engine/prediction_engine/artifacts/model_c_1x2.json`,
which the engine evaluates with the standard library. Training uses three
time-ordered windows (training, validation, holdout), chooses hyperparameters
on validation only, and refuses to export unless the stdlib evaluator
reproduces LightGBM's probabilities:

```powershell
python -m venv .venv-train
.venv-train\Scripts\pip install -r services/model-lab/requirements-train.txt
task model:c-train CODE_COMMIT=$(git rev-parse --short HEAD)
```

LightGBM and NumPy are training-only dependencies; the serving engine never
imports them (ADR 0002). The run takes a few minutes on the full corpus.
Results and caveats: `docs/prediction-engine/challengers.md`.

## Current Local Corpus Accuracy Snapshot

Last verified: 2026-07-05 with `task prediction-engine:local-training-report`.

- Parsed matches: 301,936
- Train size: 211,355
- Tested matches: 90,581
- Correct 1X2 predictions: 38,611
- Failed 1X2 predictions: 51,970
- 1X2 hit rate: 42.63%
- Odds-favorite 1X2 benchmark: 50.42% where closing odds are available
- Logistic-regression 1X2 benchmark: 49.17% over 90,518 matches

Market benchmark hit rates from available local labels:

- Double chance: 56.86%
- BTTS: 52.57%
- Over 0.5 goals: 92.47%
- Over 1.5 goals: 74.52%
- Over 2.5 goals: 54.20%
- Over 3.5 goals: 71.91%
- Over 4.5 goals: 86.43%
- Over 5.5 goals: 94.42%
- Team over/under goals: 43.15%
- HT/FT: 21.03%
- Corners: 52.98% on 53,456 labeled matches
- Cards: 59.45% on 57,398 labeled matches
- Fouls: 69.02% on 53,172 labeled matches
- Throw-ins: unavailable in the local labels used by this report

This is an offline benchmark report over the local CSV corpus. DB-backed
engine calibration is read through `accuracy_snapshots` after running
`prediction_engine.cli snapshot-accuracy`.

Run the focused tests with:

```powershell
python -m unittest discover -s tests/model-lab -v
```
