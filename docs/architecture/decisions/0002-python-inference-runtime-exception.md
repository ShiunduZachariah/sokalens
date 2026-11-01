# ADR 0002: Temporary Python prepared-data inference exception

- Status: accepted, temporary exception
- Date: 2026-08-14
- Review trigger: completion of the v2 champion/challenger evaluation
- Related: [ADR 0001](0001-canonical-prediction-storage.md), [`prediction-engine/runtime-decision.md`](../../prediction-engine/runtime-decision.md)

## Context

Repository policy normally limits Python to offline training and backtesting. The implemented prediction path already uses a Go worker to invoke the Python engine for prepared-data inference. Moving inference languages while changing models would combine an operational migration with statistical behavior changes and make regressions difficult to attribute.

## Decision

Retain the current Go-worker-to-Python inference boundary as an explicit temporary exception.

- Go owns provider ingestion, queueing, scheduling, retry/leases, concurrency limits, publication orchestration, settlement orchestration, and health reporting.
- Python accepts an explicit fixture scope and authoritative `as_of_timestamp`, reads only prepared PostgreSQL data available by that cutoff, performs feature/model/gate calculations, and writes versioned candidate results through the `engine_predictions` contract.
- Python must never call a football provider, accept public HTTP traffic, own a queue, decide deployment authority, or bypass embargo/publication policy.
- Public API handlers never wait for Python. They return prepared database/cache data and degrade to explicit unavailable states.
- Invocation is bounded by timeouts, idempotency keys, per-job logs/metrics, worker concurrency caps, and retry classification. A failed invocation does not partially publish a prediction set.

## Failure and rollback behavior

- On timeout, process failure, invalid output, or database error, the worker records a retryable or terminal job result without superseding the last valid prediction.
- Circuit-breaking and backpressure happen in Go. Retries must not become a single sequential global queue.
- Disabling the v2 challenger routes work back to the current v1 champion invocation and leaves all candidate/audit rows intact.
- Python environment or model-artifact failure must not affect fixture and public prediction read availability.

## Reassessment criteria

Reassess exported-model or Go inference only after all of the following are available:

1. v2 model behavior is stable and has completed fixed-fixture, walk-forward, and live-shadow benchmarks;
2. a representative latency, throughput, memory, startup, numerical-equivalence, and failure-recovery baseline exists;
3. the alternative preserves point-in-time feature semantics, calibration, artifact identity, explanations, and reproducibility;
4. migration can run champion/challenger without combining a model promotion with a runtime cutover; and
5. the operational benefit justifies the added implementation and validation cost.

Until then, this exception is deliberate architecture, not permission to expand Python into orchestration or public serving.

