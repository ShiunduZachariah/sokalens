# Prediction Engine Runtime Decision

Date: 2026-07-01

Source of truth: `docs/architecture/specifications/prediction-engine-architecture-v1.md`

## Decision

The updated architecture moves prediction-engine behavior to Python in `services/prediction-engine/prediction_engine`.

The Python engine owns configuration validation, lifecycle decisions, match-quality gating, model calculations, prediction versioning, and engine write rows. It reads normalized data from PostgreSQL and never calls API-Football.

The Go API and Go worker remain orchestration and serving layers. The worker ingests provider data and schedules or invokes engine work. The API serves product reads. Neither layer should reimplement engine math, gates, versioning, or best-bet eligibility.

## Transitional Write Contract

`engine_predictions` is the transitional write-contract table for Python engine output. It intentionally avoids replacing the legacy `predictions` and `prediction_versions` API read path in one step.

Until the API read surface is cut over, `engine_predictions` lets the Python engine persist auditable rows without breaking legacy API reads. After cutover, Go reads should consume the engine contract or a projection derived from it, while engine writes remain Python-owned.

## Legacy Boundary

`services/worker/internal/predictions` is legacy until cutover. Touch it only for compatibility or bug fixes needed to keep the current API path alive.

Use `task prediction-engine:test` for the Python engine verification gate.
