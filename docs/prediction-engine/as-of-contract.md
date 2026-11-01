# Prediction-engine as-of contract

Date: 2026-08-14

`as_of_timestamp` is a required, timezone-aware input to the Python prediction
pipeline. Feature builders and prediction-input repositories do not read the
wall clock or silently substitute the present time.

The contract applies to:

- candidate-fixture selection;
- historical fixture and fixture-statistics reads;
- odds snapshot selection;
- league-coverage metadata;
- historical accuracy snapshots;
- feature-vector assembly and league-baseline caching.

Repository queries bound history kickoff and result availability to both the
target kickoff and the supplied as-of timestamp. A prior fixture whose final
result was written later is therefore not treated as known merely because it
had already kicked off. Candidate-history counts apply the same availability
rule. Odds use the most recent snapshot whose `fetched_at` is not later than
the as-of timestamp. Accuracy snapshots require both `snapshot_date` and the
full `computed_at` timestamp to be at or before as-of, preventing same-day or
retroactively computed snapshots from leaking. Feature assembly performs a
second defensive kickoff cutoff, so a future-dated fixture or its statistics
cannot enter an earlier feature vector even when a caller supplies an
over-broad in-memory history collection.

The persisted feature audit payload records the normalized UTC
`as_of_timestamp`. The current `generated_at` remains separate because it is a
write/version timestamp; callers may intentionally make it equal to the as-of
timestamp for deterministic replay.

This is a point-in-time read boundary over the data currently retained by the
repository. Tables that overwrite observations instead of retaining versions
cannot reconstruct states that were never historically stored. Provider
ingestion must continue storing timestamped snapshots when exact replay is
required.

Focused tests reject omitted and timezone-naive timestamps, inject future
fixture/statistic data, inspect SQL bounds for every temporal source, and keep
the provider-free fixed v1 benchmark unchanged.

The CLI is the live-orchestration boundary: when no timestamp is supplied it
resolves the wall clock once in UTC, then injects that value everywhere as
`as_of_timestamp`. Replay and audit callers must pass a timezone-aware
`--as-of-timestamp`; `--generated-at` remains a backward-compatible alias.
