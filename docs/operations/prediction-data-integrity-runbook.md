# Prediction Data Integrity and Retention Runbook

Last verified: 2026-08-06 (Africa/Nairobi)

## Purpose

This runbook defines the production path from stored football data to public SokaLens responses. It covers prediction idempotency, fixture and odds synchronization, settlement retention, accuracy integrity, operational verification, backup, and rollback.

Normal page requests must only read prepared PostgreSQL data. Football-provider requests, feature assembly, prediction generation, settlement, recommendation generation, and notification delivery run in background workers.

## Authoritative data and ownership

| Domain | Authoritative table or projection | Writer | Public reader |
|---|---|---|---|
| Public fixture catalogue | `matches` | ingestion worker | Go match API |
| Prediction-ready fixture projection | `fixtures` | ingestion worker and bounded reconciliation | Python prediction engine; Go match API joins |
| Teams and competitions | `teams`, `leagues`, related canonical tables | ingestion worker | Go API |
| Provider identity mapping | `provider_entity_maps` | ingestion worker | worker-only normalization |
| Raw odds observations | `odds_snapshots` | ingestion worker | internal audit only |
| Normalized usable odds | `odds` | ingestion worker | prediction engine and Go API |
| Published predictions | `engine_predictions` | Python engine through worker | Go prediction and match APIs |
| Recent settlement detail | `prediction_results` | settlement worker | recent-results API and accuracy reads |
| Durable accuracy | `prediction_accuracy_daily` | aggregate/prune worker | Go accuracy API |
| Pruned-result idempotency | `prediction_result_rollup_ledger` | aggregate/prune worker | settlement worker exclusion |
| Job state | `jobs` | scheduler and workers | restricted diagnostics only |

Legacy prediction tables remain retained because reporting and rollback references have not yet been conclusively eliminated. They are not the source for current public prediction responses.

## End-to-end flow

```text
worker ingestion
  -> canonical matches, teams, leagues and normalized odds
  -> bounded matches-to-fixtures reconciliation
  -> background feature assembly and prediction generation
  -> idempotent engine_predictions write/version decision
  -> database-backed public API read
  -> short Next.js shared cache
  -> browser
```

The API does not call a football provider or run Python during a customer request. Match and prediction caches revalidate after 15 seconds; accuracy revalidates after 60 seconds. API ETags support conditional `304` responses.

## Prediction persistence rules

`engine_predictions` keeps auditable versions while preventing redundant versions.

- One unsuperseded row is allowed for each `(fixture_id, market)`.
- A content hash covers the persisted prediction semantics after database numeric precision is applied.
- A missing prediction is inserted.
- A genuine content change supersedes the current row and inserts a new version.
- An unchanged evaluation only advances `last_evaluated_at`.
- An older out-of-order evaluation is ignored.
- A transaction-scoped advisory lock serializes the fixture/market decision.
- A database trigger protects the rule when an older or concurrent writer bypasses repository logic.
- Historical and replay CLI scopes are non-publishing unless `--persist-replay` is explicitly supplied. Post-kickoff replay rows are excluded from public picks and accuracy.

Expected recurring log evidence for unchanged inputs:

```text
predictions_generated=230 predictions_inserted=0 predictions_versioned=0
predictions_unchanged=230 predictions_written=0 fixtures_failed=0
```

## Fixture and odds idempotency

- `matches` is the public catalogue. `fixtures` is the explicitly synchronized prediction read projection.
- Normal ingestion writes all relevant fixture fields, including normalized nullable venue data.
- Bounded reconciliation repairs missing or divergent projection rows without requiring a page request.
- Exact odds observations are unique by match, market, provider, capture time, and payload digest.
- A retry reuses the raw snapshot and repairs incomplete normalized outcomes.
- A genuinely different capture time or payload remains a historical observation.

## Settlement, public history, and hit rate

Authoritative timestamps are stored in UTC. The public recent-results window is controlled by `PUBLIC_RESULT_DETAIL_HOURS` (default `24`, range `1..168`). Detailed rows older than the previous local day boundary are not exposed.

Raw settlement retention is controlled by `PREDICTION_RESULT_RAW_RETENTION_DAYS` (default `2`, range `1..30`). Before an old row is removed:

1. The final eligible prediction generated at or before kickoff is selected per fixture and market.
2. Wins, losses, void/unavailable outcomes, and pending outcomes are added to `prediction_accuracy_daily` in one transaction.
3. Every pruned result ID is recorded in `prediction_result_rollup_ledger`; ineligible or post-kickoff rows are marked `included_in_accuracy=false`.
4. The raw result is deleted only after the durable aggregate and ledger steps.
5. Settlement queries exclude ledgered prediction IDs, so pruning cannot cause a historical result to be generated and counted again.

Published hit rate is:

```text
correct / (correct + incorrect)
```

Voided and data-unavailable rows remain auditable but are not counted as wins or losses. Both correct and incorrect eligible predictions remain in the denominator after raw-detail pruning.

## Scheduler and concurrency controls

- Jobs have unique process worker IDs, renewable leases, and owner-fenced completion/retry/failure.
- Prediction generation, settlement, accuracy snapshots, and aggregate/prune use operation advisory locks.
- All 15-minute generation triggers use the same time-bucket idempotency key.
- Follow-up jobs are created only after successful parent jobs and deduplicate completed work.
- Recommendation candidate replacement is transactional.
- Notification materialization is atomic and deduplicated; delivery claims are bounded to actual transport concurrency.
- Provider ingestion, prediction work, recommendations, retention, notifications, and billing reminders use independent queues and bounded workers rather than one sequential global queue.

## Migrations

- `000030_prediction_persistence_integrity`: prediction hashes, version integrity, current-row constraint, deduplication, indexes, accuracy aggregates, and maintenance audit.
- `000031_ingestion_projection_integrity`: fixture projection reconciliation, exact odds deduplication/constraint, history indexes, and data-maintenance audit.
- `000032_prediction_result_rollup_ledger`: compact result-rollup ledger preventing re-settlement after raw pruning.

Apply and verify:

```powershell
task db:migrate
task db:verify:engine
```

Run retention manually:

```powershell
docker compose -f infra/docker/docker-compose.yml --env-file .env exec -T worker `
  /opt/prediction-engine-venv/bin/python -m prediction_engine.cli `
  aggregate-prune-results --public-detail-hours 24 --raw-retention-days 2
```

Reclaim storage after a large deduplication:

```powershell
task db:compact:predictions
```

The compaction takes an exclusive maintenance lock and should run during a controlled window.

## Backup and rollback

Create a compressed backup before cleanup or retention repair:

```powershell
$backupDir = Join-Path $PWD '.local\backups'
New-Item -ItemType Directory -Force -Path $backupDir | Out-Null
$pgContainer = (docker compose -f infra/docker/docker-compose.yml --env-file .env ps -q postgres).Trim()
docker exec $pgContainer pg_dump -U sports -d sports_prediction -Fc -f /tmp/sports_prediction.dump
docker cp "${pgContainer}:/tmp/sports_prediction.dump" (Join-Path $backupDir 'sports_prediction.dump')
Get-FileHash -Algorithm SHA256 (Join-Path $backupDir 'sports_prediction.dump')
```

Schema rollback:

```powershell
task db:rollback
```

Migration rollback cannot recreate rows intentionally removed by deduplication or retention. To recover those rows, stop API/worker writes and restore the pre-cleanup dump into a newly created database first; validate it before changing application connectivity.

## Verified local cleanup and performance evidence

- Prediction rows before cleanup: `127,974`.
- Exact semantic duplicate groups: `2,084`.
- Redundant prediction rows removed: `125,869`.
- Rows immediately after cleanup: `2,105`.
- Current prediction duplicate groups after cleanup: `0`.
- Missing fixture projections repaired: `14`.
- Divergent fixture projections repaired: `3,292` after null/empty venue normalization.
- Excess exact odds snapshots removed: `259`; duplicate groups after cleanup: `0`.
- Normalized duplicate odds rows removed: `771`; orphan odds rows: `0`.
- Database size after compaction: approximately `85 MB` (from approximately `481 MB`).
- `engine_predictions` relation after subsequent normal versions: approximately `6.7 MB` (from approximately `406 MB`).
- Durable accuracy: `299` total contributions: `117` correct, `145` incorrect, `37` void/unavailable, `0` pending.
- Settled-only public accuracy: `117 / 262 = 44.66%`.
- Rollup ledger repair: `320` re-created raw rows removed, `299` marked accuracy-eligible, aggregate totals unchanged, and a repeat settlement produced `0` fixture IDs and `0` writes.
- Public API load: `14,561` requests, `0.00%` errors, overall p95 `225.78 ms`; slowest measured route p95 `333.74 ms`.
- Authenticated account API load: `15,271` requests, `0.00%` errors, overall p95 `31.58 ms`.
- Production web load: `346` requests, `0` failures, p95 `644.5 ms`; final 56-request route burst had `0` failures and p95 `1,331.8 ms`.

## Health verification

```powershell
Invoke-WebRequest -UseBasicParsing http://localhost:8080/health
Invoke-WebRequest -UseBasicParsing http://localhost:8080/ready
Invoke-RestMethod http://localhost:8080/v1/matches/today?include_unavailable=true&limit=30
Invoke-RestMethod http://localhost:8080/v1/predictions/today?limit=30
Invoke-RestMethod http://localhost:8080/v1/predictions/accuracy
docker compose -f infra/docker/docker-compose.yml --env-file .env logs --tail=200 worker
```

Check that worker summaries show stable unchanged writes, no overlapping lease ownership, no repeated settled rows, and no provider activity caused by these public HTTP reads.

## Remaining operational gaps

- The current stored corpus has no prediction-eligible fixture for the current Nairobi day; upcoming prepared predictions exist for later days. This is a data-coverage/provider-plan gap, not a request-path failure.
- Legacy prediction/reporting tables are retained until every external/reporting dependency is confirmed.
- Off-host encrypted backups, restore drills, external telemetry/alert routing, CDN tests, and endurance tests require deployment infrastructure.
- Production billing credentials and webhook configuration, a verified production email sender/domain, and lawyer-approved policy text remain required.
