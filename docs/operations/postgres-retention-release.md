# PostgreSQL Retention Release Runbook

**Release date:** 2026-08-30  
**Stable pre-release SHA:** `7b8a2ac`  
**Design:** [ADR 0004](../architecture/decisions/0004-model-evaluation-retention.md)

## Safety contract

- PostgreSQL is authoritative. Redis and Docker build cache are disposable support state.
- Never start destructive retention without a local custom-format dump that has passed checksum, catalogue, isolated restore, migration-list, and critical-row-count verification.
- Never run `VACUUM FULL`, all-migrations-down, `docker system prune --volumes`, or Redis `FLUSHALL` during this release.
- Application rollback keeps additive migration `000056` applied. Data rollback is a separate maintenance event using the verified dump.
- Stop cleanup on unexpected FK errors, rising 5xx, lock queues, replication/WAL pressure, missing current predictions, or count invariants.

## Lifecycle policy

| Data | Boundary |
|---|---|
| Builder/shared-slip leg | Remove at kickoff; explain removed legs; never reuse token odds |
| Saved match or prediction | Remove 6 hours after confirmed `fixtures.finished_at` |
| Raw engine prediction | Remove 24 hours after confirmed finish when unresulted, or after durable result rollup and compact evidence exist |
| Team fixture statistics | Latest 10 completed matches per team across competitions |
| Compact model evidence | 365 days |
| Daily accuracy aggregates | 365 days |
| Normalized provider payloads | 7 days after references are detached |
| Provider request logs | 30 days when no payload references remain |

The worker runs the maintenance job daily in bounded `FOR UPDATE SKIP LOCKED` batches. Incomplete settled predictions are preserved and counted as blockers, but do not prevent independent safe rows from being cleaned.

## Release controls

Production defaults are deliberately non-destructive:

```text
PREDICTION_SCHEDULE_INTERVAL=15m
ENABLE_DESTRUCTIVE_RETENTION=false
RETENTION_DRY_RUN=true
```

Release stages:

1. Deploy code and additive migration with `false/true`.
2. Confirm restore evidence, LKG image manifest, service health, `/readyz`, worker heartbeat, and application smoke tests.
3. Change to `true/true`, recreate only the worker, and inspect `data_maintenance_audit` plus worker retention metrics.
4. Confirm candidates, blocker reasons, saved-reference exclusions, duplicate-reference exclusions, and current-row uniqueness.
5. Change to `true/false`, recreate only the worker, and allow bounded daily batches. Repeat only while service latency and invariants remain healthy.
6. Run plain `VACUUM (ANALYZE)` on touched tables, then re-measure relation size, reusable free space, disk, WAL, and latency.

Do not reset the switches during subsequent deploys; `ci-deploy.sh` preserves existing values.

## Required invariants

```sql
SELECT fixture_id, market, count(*)
FROM engine_predictions
WHERE superseded_at IS NULL
GROUP BY fixture_id, market
HAVING count(*) > 1;

SELECT * FROM retention_candidates ORDER BY candidate_type;

SELECT operation, rows_examined, rows_changed, details, performed_at
FROM data_maintenance_audit
WHERE operation LIKE 'bounded_retention%'
ORDER BY performed_at DESC
LIMIT 20;
```

The duplicate-current query must return zero rows. `retention_candidates` is an operational view, not a hot-path cache.

## Views decision

- `current_engine_predictions` centralizes the current-row predicate for analysis and low-risk internal reads. Hot API SQL keeps the equivalent `superseded_at IS NULL` predicate so the existing partial indexes remain obvious to the planner.
- `retention_candidates` provides read-only candidate counts for dashboards and release checks.
- `prediction_accuracy_daily` remains the compact incremental learning summary.
- Do not add a full-refresh materialized view. If bounded ten-match history is still expensive after production `EXPLAIN (ANALYZE, BUFFERS)`, add an incrementally maintained recent-team-form projection in a later measured release.

## Storage projection

The 365-day compact evidence and aggregate set projects to about 443 MiB at the observed average, 664 MiB at the recent elevated rate, and 1.05 GiB if the observed peak is sustained every day. Alert at 1.25 GiB.

Raw prediction growth should flatten after semantic timestamps stop generating versions. Plain vacuum makes deleted pages reusable. Returning the existing relation file to the host filesystem requires a later online `pg_repack` maintenance window after the churn fix has been stable; it is not part of this release.

Docker build cache was the immediate disk problem (about 51 GB reclaimable), so deploy prunes builder cache older than 24 hours before the verified backup and again after a successful release. Named volumes and running images are not pruned.

## Rollback

`ci-deploy.sh` records the current release SHA, tags current application image IDs as last-known-good, stores configuration checksums, and automatically restores the prior source plus LKG images if post-migration application health fails. It does not reverse migration `000056`.

If data cleanup violates an invariant:

1. Disable destructive retention and stop API/worker writes.
2. Preserve logs and the failed database for investigation.
3. Restore the pre-release verified dump into an isolated database and validate migrations/counts.
4. Restore production data during an announced maintenance window.
5. Start the LKG application and validate `/readyz`, public/account paths, worker heartbeat, SEO routes, and row-count invariants before reopening writes.

