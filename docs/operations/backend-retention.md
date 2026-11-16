# Bounded backend maintenance

The database should retain compact football history and evidence, while raw
prediction versions and operational logs have short, explicit lifetimes.

## Retention policy

| Record | Window | Cadence and safeguards |
| --- | --- | --- |
| Raw engine prediction details | 24 hours after confirmed finish | Hourly cursor drain, bounded batches; saved references, active slips and incomplete settled evidence block deletion |
| Unknown, cancelled or postponed fixture prediction details | Seven days after recorded kickoff | Separate cursor and explicit EXPIRE_UNRESOLVED_PREDICTIONS gate; future reschedules never qualify; no invented finish time or score |
| Recovery copies of newly expired unresolved predictions | 24 hours | Archive raw row, result and tracker atomically before deletion; retain exact release identity for selective rollback |
| Raw result details | One day, then aggregate | Existing daily result aggregation; compact accuracy ledger and model evidence survive |
| Completed/failed jobs | Seven days | Daily, up to 100,000 candidates per pass; referenced provider audit jobs remain |
| Provider request logs | One day | Existing bounded daily cleanup after payload dependencies |
| Provider payloads | Seven days | Existing dependency-aware cleanup |
| Model evidence, accuracy, identity and final match snapshots | Existing compact history policy | Never cascade-delete canonical fixtures or fabricate football outcomes |

Canonical fixture and match rows remain because team history, model evaluation,
settlement revisions and saved references share them. The expensive raw payloads
are what expires. Setting a retention age is not a promise that every protected
row disappears at that exact instant. The hourly drain converges without a long
bulk-delete lock; a backlog can take several runs.

Monthly in-play time-series retention is a separate contract: permanent final
snapshots and event/retraction audit records must survive. This release does not
blindly drop those partitions or delete the canonical fixture graph.

## Release plan

1. Prepare a feature PR into qa and run worker regression tests, PostgreSQL
   retention tests, additive up/down/up migration verification, deployment
   rollback tests and Compose configuration tests. Keep the floating Go patch toolchain required by the security gate.
2. Before the window, obtain a fresh restore-verified recovery point. No new
   destructive expiry flag is enabled merely by shipping this release.
3. At the scheduled window, use the reviewed PR and existing QA CI deployment.
   guarded-deploy.sh captures the old source, images, environment and actual
   PostgreSQL command before the live checkout moves. It runs pre-deployment
   origin journeys and restores that runtime on any deployment/check failure.
4. QA must pass its migration, API/page, challenger and backend checks. Verify
   the deployed SHA and the real user journey before creating qa-to-main
   promotion. Never promote an untested or changed SHA.
5. Production goes through green-main CI. PostgreSQL command/shared-memory
   drift is reconciled explicitly, including the 1536 MB shared buffer setting
   and pg_stat_statements preload. The exact previous runtime is captured so
   a failed activation restores the previous settings rather than guessing
   them from an old Compose file.
6. backend-check.sh validates runtime memory, loaded query statistics, valid
   indexes and an indexed queue-depth plan on production-sized history. Origin
   page/API smoke runs before success is recorded, inside the rollback trap.
7. Only after both environments pass and recovery is verified, explicitly set
   EXPIRE_UNRESOLVED_PREDICTIONS=true and RETENTION_RELEASE_SHA to the tested
   commit for the worker through CI configuration.
   Keep ENABLE_RAW_PREDICTION_DRAIN=true and RETENTION_DRY_RUN=false.
   Activation explicitly sets RAW_PREDICTION_DRAIN_BATCH_SIZE=1000 and
   RAW_PREDICTION_DRAIN_MAX_BATCHES=100 in both environments, so an old QA
   canary limit cannot silently become the ongoing retention budget. Run the deployed 100-row-per-stream canary and require its completed job state
   and data_maintenance_audit entry before declaring activation successful. A queued job cannot
   shorten the retention window or enable the new gate.
8. Run `bash infra/deploy/backend-maintenance.sh <compose-project>` through the
   selected environment's Docker context after its checks pass. It concurrently
   rebuilds only known request-log indexes larger than 64 MiB and reports sizes.
   Do not remove the unused lifecycle prediction index without query evidence.

The requested window is 2026-11-15 06:00 Africa/Nairobi (03:00 UTC). This is the
start of a sequential QA-then-production rollout, not a claim that both builds
finish at that instant. A missing approval, red test, unverified backup or changed
release identity prevents production activation.

## Verification and recovery

Compare the same traffic window before and after: PostgreSQL CPU and physical
reads, queue-depth query plan/time, API latency/error rate, queue age, worker pool
waits, and expired raw-row backlog. The worker exports pool connection and wait
gauges; pg_stat_statements supplies query timing. Queue metrics must return to
zero after every queue drains. Background idle consumers back off to five seconds;
reserved live queues retain one-second responsiveness. Exhausted-lease cleanup
runs once a minute per worker process rather than once per idle consumer.

An additive schema remains compatible with the old application during rollback.
QA and production restore prior application images, source/config and PostgreSQL
runtime automatically on failed deployment checks. New destructive expiry stays
off until those checks pass. Failed activation checks stop the worker and automatically restore newly expired
raw predictions, settlement details and tracker rows from the release's recovery
archive before restoring the previous worker environment. Recovery inserts skip
existing rows, preserving newer live corrections. Archive writes and deletion
commit together; a failed archive write cannot delete a row. Recovery copies expire
after 24 hours under the bounded hourly job. The immutable release SHA is required
alongside the new expiry gate. If an older incident exceeds that recovery window,
stop the drain and recover affected football rows from the verified backup rather
than replacing accounts or newer live writes with a whole-database restore.

The API exports its existing bounded-route latency histogram and pool metrics
on a separate Docker-only listener at api:9092, scraped by Prometheus. It is not
published on a host port and the public admin metrics endpoint remains gated.
Use histogram_quantile(0.95, sum by (le, route)
(rate(sokalens_http_request_duration_seconds_bucket[5m]))) for route p95.

Operational follow-up: schedule daily encrypted off-site recovery points with
restore checks. An idle container named backup is not backup coverage.


## Results query index

Migration 000096 declares a partial covering index for the evidence-only
BTTS/Total Goals results branch. It retains the existing raw-row identity
checks and result semantics while avoiding random reads of the large heap.
QA and production CI run ensure-results-index.sh before dbtool: it builds the
exact declared index CONCURRENTLY, verifies its definition/readiness, and
restarts only its own matching invalid build. The transactional migration then
records an already-built index without a blocking table scan. Valid builds are
reused; conflicting definitions fail closed. Existing application rollback
remains compatible with this additive index. Deployment smoke exercises the
previous settled day and rejects its rendered unavailable state even with
HTTP 200.


### Results pagination and the evaluation ledger

The results source returns populated pages and their complete filtered totals in
one SQL statement. Ranking is performed once; `COUNT(*) OVER ()` is evaluated
before pagination. An empty page falls back to the count query so an out-of-range
page still reports the day's total. Optional legacy sources retain their two-read
contract, and the cache wrapper forwards combined reads without retaining results.

Ledger timestamps use the prediction primary key rather than a day query joining
the entire historical ledger. The settled CTE is materialized to evaluate each
timestamp once. This preserves the prediction timestamp fallback, pre-kickoff
eligibility, latest-version tie breaks, corrected evidence and identity checks.
The public results JSON and request timeout are unchanged.
