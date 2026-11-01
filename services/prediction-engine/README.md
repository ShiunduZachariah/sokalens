# Prediction Engine

Python implementation of `docs/architecture/specifications/prediction-engine-architecture-v1.md`.

This package owns the prediction-engine read/model/write logic described by the
updated architecture document. The Go API and worker remain orchestration and
serving layers; engine behavior is implemented here.

The engine reads normalized PostgreSQL tables and writes auditable prediction
rows. It does not call API-Football directly; provider calls remain worker
ingestion work.

## Active Engine Boundary

The Python engine is the only active prediction engine. The Go worker queues
prediction jobs, then invokes this package through `prediction_engine.cli`.
Deprecated Go prediction-model code has been removed from worker startup and
the remaining Go `predictions` package only owns queue job keys.

## Data Source Order

Local engine testing should use data in this order:

1. Local imported historical data and the offline `Model-Training-Data` corpus.
2. football-data.org, using `FOOTBALL_DATA_ORG_API_KEY` from the environment.
3. API-Football for richer odds/statistics when football-data.org free data is
   missing.

football-data.org free-plan score and fixture fields are nullable. Markets that
need odds, corners, cards, fouls, throw-ins, possession, or detailed event data
must stay unavailable unless those fields are present from API-Football or a
paid/add-on provider feed.

## Local Checks

```powershell
pip install -r services/prediction-engine/requirements.txt
task prediction-engine:test
```

## Local Replay

Run the engine against imported historical fixtures:

```powershell
task prediction-engine:run-historical
```

Run a specific fixture:

```powershell
$env:PYTHONPATH='services/prediction-engine'
$env:DATABASE_URL='postgres://sports:sports@127.0.0.1:5433/sports_prediction?sslmode=disable'
python -m prediction_engine.cli run-fixture --fixture-id 25553
```

Live orchestration resolves one UTC timestamp at CLI startup and passes it
explicitly through candidate selection, repository reads, and feature assembly.
Deterministic replay and audit runs must supply a timezone-aware point in time,
using either `--as-of-timestamp` or the backward-compatible `--generated-at`
alias:

```powershell
python -m prediction_engine.cli run-fixture --fixture-id 25553 --as-of-timestamp 2026-07-01T12:00:00Z
```

Settle a completed fixture's current engine predictions:

```powershell
$env:PYTHONPATH='services/prediction-engine'
$env:DATABASE_URL='postgres://sports:sports@127.0.0.1:5433/sports_prediction?sslmode=disable'
python -m prediction_engine.cli settle-fixture --fixture-id 25553
```

Settlement writes `prediction_results` and one compact immutable
`model_evaluation_facts` row in the same transaction. A missing model-registry
identity or failed fact write rolls the transaction back; the system does not
publish settlement without its evaluation evidence. Migration `000036`
registers and links the current v1 Champion and backfills safe historical
settled, void, and explicitly unavailable results idempotently.

The Go worker invokes the same CLI for `predictions.generate.upcoming` jobs.
Recurring scope runs print a machine-readable summary. The Go worker records the
fixture IDs and processed, written, failed, and skipped counts. One malformed
fixture is rolled back and isolated without aborting the remaining scope.

Upcoming candidate selection applies the minimum same-league history gate before
its batch limit. Same-season finished history is used first, with recent finished
fixtures from the immediately prior season of the same league as a fallback so
early-season fixtures still clear the gate across the full 7-day horizon.
Incomplete odds and all-null statistics are treated
as missing inputs; BASIC-quality fixtures emit Tier A markets only. Partial
statistics retain per-field sample counts, and each Tier B/C market remains
unavailable until its own required home/away metrics have at least five real
observations.

## Historical Accuracy

Use the local CSV corpus first for a broad offline accuracy baseline:

```powershell
task prediction-engine:local-training-report
```

The report uses prior rows as history and later rows as the test set. It prints:

- total parsed and tested matches
- successful and failed predictions
- top-level hit rate
- per-market benchmark hit rates where labels exist
- 1X2 benchmarks, including odds-favorite and logistic-regression baselines

For DB-backed replay, import historical seasons into PostgreSQL, run historical
prediction generation, settle finished fixtures, then snapshot accuracy:

```powershell
task prediction-engine:run-historical
$env:PYTHONPATH='services/prediction-engine'
$env:DATABASE_URL='postgres://sports:sports@127.0.0.1:5433/sports_prediction?sslmode=disable'
python -m prediction_engine.cli settle-scope --scope finished --limit 100
python -m prediction_engine.cli snapshot-accuracy
```

## Local API Testing

Start the API locally, then open:

- Scalar docs: `http://localhost:8080/docs`
- Raw OpenAPI: `http://localhost:8080/openapi.yaml`
- Health: `GET /healthz`
- Readiness: `GET /readyz`
- Today's fixtures: `GET /v1/matches/today`
- Historical fixtures: `GET /v1/matches/history?date=2025-05-18&status=finished`
- Fixture detail: `GET /v1/matches/{match_id}`
- Today's predictions: `GET /v1/predictions/today`
- Prediction detail/confidence: `GET /v1/predictions/{prediction_id}`
- Accuracy/hit-rate report: `GET /v1/predictions/hit-rate`
- Provider status: `GET /v1/providers/status`

Prediction generation remains a worker/CLI operation, not a blocking API call.
For local generation, enqueue/process prediction jobs or call the Python CLI
directly with `run-fixture` or `run-scope`.
