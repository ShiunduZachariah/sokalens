# ACCA inventory QA runbook

Use a production database snapshot or a read-only replica. Never point the local worker at the live primary for writes.

1. Create an isolated schema and role on the snapshot:

```sql
CREATE SCHEMA IF NOT EXISTS acca_qa;
GRANT USAGE ON SCHEMA acca_qa TO acca_qa_worker;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO acca_qa_worker;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA acca_qa TO acca_qa_worker;
```

2. Apply migration `000057_acca_inventory_v2` with `search_path=acca_qa,public` (or run its statements with the table/view names explicitly schema-qualified).

3. Run the worker/API/web through Docker Compose with `ACCA_INVENTORY_V2_ENABLED=true`, `ACCA_DB_SCHEMA=acca_qa`, and `ACCA_REDIS_NAMESPACE=qa:acca`. The web should call the Docker API service; never point it directly at PostgreSQL.

4. Verify inventory count, duplicate hashes, expiry, and invalidation before enabling any public read flag. Redis cleanup must delete only `qa:acca:*` keys; never issue a global flush.

For a no-host-process run, copy `.env.example` to an untracked `.env.acca-qa`, set the three QA values above, then use:

```powershell
docker compose --env-file .env.acca-qa -f infra/docker/docker-compose.yml up -d postgres redis api worker web
docker compose --env-file .env.acca-qa -f infra/docker/docker-compose.yml down
```

This keeps all runtime processes in containers. The local snapshot can be discarded with `docker compose ... down -v` after testing; production is never contacted.

## Production promotion (backend first)

Production promotion is deliberately staged. Do not deploy or enable any stage without explicit approval. The QA run must be Docker-only and must use a disposable snapshot/clone; it must not write to the production primary.

The CI release script enforces this ordering automatically: build selected images, activate backend services with `--no-deps`, run `acca-readiness.sh`, then activate the frontend. The readiness gate requires three stable samples, 1–3,000 active slips, more than four supplied market tabs, no duplicate/started/stale/hidden-market slips, and nonempty valid list/featured API responses. CI runs the deployment regression tests before promotion. Longer-running production traffic/latency monitoring remains a post-deployment check, not a claim made by the short readiness gate.

### 1. Backup and preflight

Before any production migration, take and verify a restorable PostgreSQL backup (including the schema and role definitions), record the current application image/commit, and confirm Redis and database free space. Record the rollback image and migration-down procedure. Do not proceed if the backup cannot be restored in a disposable instance.

### 2. Additive backend release

Deploy the additive migration, API read path, and worker code first, without rebuilding or recreating the `web` service. Use the CI/VPS release mechanism with an API/worker-only service list; do not perform a direct laptop-to-production deployment. Run migrations transactionally and grant the worker only the required ACCA schema/table permissions; do not drop or rewrite existing prediction tables.

Set `ACCA_INVENTORY_V2_ENABLED=true` for the worker so it schedules and processes `acca.generate-inventory`. The same variable is passed to the API container by Compose but the Go API does not use it as a route gate: `/v1/accas` and `/v1/accas/featured` are registered when the additive API is deployed. Exercise those endpoints directly during backend QA.

Do not restart the existing web container during this stage. Its captured environment and image must remain unchanged, so it continues serving the already deployed prediction experience while the compatible backend is proved. `ACCA_INVENTORY_V2_ENABLED` on the web controls the homepage ACCA section and the browser-facing `/api/accas` proxy. `ACCA_INVENTORY_READ_SOURCE` is also web-only and currently accepts only `v2`; it is not a switch between two backend inventory implementations. The server-rendered ACCA catalogue calls the Go API directly and is not gated by either web variable, so neither variable should be described or relied on as a complete public-read kill switch. Backward compatibility of the API response is therefore a mandatory backend gate.

Allow the worker to build production inventory while the new frontend remains undeployed. The unchanged frontend may continue its existing compatible API reads, so monitor those responses as part of the backend gate. Production must use its configured ACCA Redis namespace (normally `acca`); QA always uses `qa:acca`. Never run `FLUSHALL` or `FLUSHDB`.

### 3. Inventory readiness gates

Run read-only checks against the new inventory before frontend promotion:

```sql
SELECT status, COUNT(*) FROM acca_slips GROUP BY status;
SELECT COUNT(*) FROM acca_slips WHERE status = 'active' AND earliest_kickoff > now();
SELECT market_tab, period, leg_count, COUNT(*)
FROM acca_slips
WHERE status = 'active' AND earliest_kickoff > now()
GROUP BY market_tab, period, leg_count
ORDER BY market_tab, period, leg_count;
SELECT COUNT(*) - COUNT(DISTINCT composition_hash) AS duplicate_hashes
FROM acca_slips;
```

The active inventory must never exceed 3,000, duplicate hashes must be zero, no active row may have a started first leg, and every required tab/period and supported leg range must have either verified supply or a documented sparse-inventory explanation. Inspect odds bands (including high-odds bands), exact market families, match exposure, and invalidation/expiry counters. Repair or reduce inventory before proceeding; do not relax the ceiling to make the gate pass.

### 4. API, cache, and stability gates

With the new frontend still undeployed, exercise the backend `/v1/accas`, `/v1/accas/featured`, filters, cursor pagination, and generator searches against the production-shaped inventory. Confirm Redis hits make zero PostgreSQL calls, misses use only bounded precomputed reads, malformed/started slips are excluded, cursors do not repeat, and cache keys/TTLs are correct. Review API latency/error rate and backend resource usage during the readiness window, then continue monitoring through normal and busy match traffic after deployment. Homepage prediction latency and error rate must not regress.

### 5. Deploy the validated frontend

After the backend gates pass, rebuild and recreate only the frontend with `ACCA_INVENTORY_READ_SOURCE=v2` and `ACCA_INVENTORY_V2_ENABLED=true`. Verify the catalogue, the independent homepage ACCA component, and the generator in that order, using a gradual rollout where the platform supports it. Keep normal prediction reads independent from ACCA failures. This is the first stage that may ship the new frontend code; the backend stage must not have recreated the web container merely because the shared `.env.prod` file was updated for the worker.

### 6. Rollback order

For a frontend regression, restore the last-known-good frontend image first and leave the additive backend inventory running for diagnosis. Disabling `ACCA_INVENTORY_V2_ENABLED` hides the homepage ACCA section and disables the browser proxy, but it does not gate server-rendered catalogue reads; use the last-known-good web image when complete frontend rollback is required. Changing `ACCA_INVENTORY_READ_SOURCE` away from `v2` also disables only the browser proxy and is not a backend rollback.

For an API or worker regression, stop ACCA generation by setting the worker's `ACCA_INVENTORY_V2_ENABLED=false`, then restore the last-known-good backend images. Only after confirming no code depends on the new objects should the additive migration be rolled back, using the verified backup/restore plan. Never delete production prediction data as a rollback step.

## Promotion readiness timing

`infra/deploy/acca-readiness.sh` gates frontend promotion on the inventory
being real and stable. It polls until it sees three consecutive identical
samples that all satisfy: `0 < active <= 3000`, zero duplicate composition
hashes, zero invalid slips, and more than four distinct market tabs. It then
checks `/readyz`, `/v1/accas?limit=10` and `/v1/accas/featured` twice each.

Two knobs, both with deliberately generous defaults:

| Variable | Default | Meaning |
| --- | --- | --- |
| `ACCA_READINESS_ATTEMPTS` | `240` | Poll attempts before refusing promotion |
| `ACCA_READINESS_INTERVAL` | `10` | Seconds between attempts |

**Why the budget is 40 minutes and not 20.** `scheduler.Run` executes its
first iteration immediately on boot, before entering the poll ticker. It
enqueues prediction generation, which queues `acca.generate-inventory` only
after generation succeeds. The processor picks up the follow-up on its next
poll (30s by default). This ordering keeps inventory aligned with the newly
published predictions.

The budget covers two other things:

1. **Generation time.** Prediction generation must finish first. The ACCA
   follow-up then builds and ranks a pool targeting 3000 slips.
2. **Re-deploys landing inside a spent bucket.** Prediction generation has a
   bucket-scoped idempotency key with `DeduplicateCompleted: true`; a completed
   generation in the current 15-minute bucket does not queue another ACCA job
   on redeploy. The next prediction run enqueues a new ACCA follow-up using its
   source job ID as the deduplication key.

**If promotion still times out**, check in this order before touching the gate:

1. Is `ACCA_INVENTORY_V2_ENABLED=true` in `.env.prod`? Without it the worker
   never queues the follow-up and `active` stays 0 forever. `ci-deploy.sh` sets it, but
   a hand-rolled deploy may not.
2. Has `acca.generate-inventory` actually run? An empty pool is a worker
   problem, not a gate problem.
3. Are counts oscillating rather than settling? That means slips are being
   invalidated as fast as they are generated — a real defect. Do not widen the
   gate to get past it.

Raising the budget is legitimate. Loosening the *validity* thresholds
(duplicates, invalid slips, market tabs) is not — those are the promotion
contract.

## Slip lifecycle: why a slip dies at its first kickoff

Decided 2026-09-22, after the `/v1/accas` outage, because the question comes up
whenever someone looks at the churn.

A slip is retired by `Expire()` the moment its **earliest** kickoff passes, and
the generator composes fresh slips from whatever predictions are still eligible.
Two alternatives get proposed and both are worse.

**Holding the slip until its last match** turns the active pool into a mix of
forecasts and partly-decided bets. A slip with one leg already played is not a
prediction any more, and the API's `earliest_kickoff > now()` filter exists to
say so. It also enlarges the active pool, which is the thing whose size took the
endpoint down: the facet query walks every leg of that pool, and it was 3,456ms
cold at 200k slips against a 2750ms timeout.

**Dissolving a slip and reassigning its unplayed legs** to other slips is the
generator's job already. It recomposes from the eligible pool every cycle, so
explicit reassignment would add leg-provenance bookkeeping to arrive at the same
inventory.

**The churn is not the lifecycle.** Measured on 2026-09-22: 158,405 slips
invalid against 39,166 expired, so supersession retires four slips for every one
a kickoff does. And that supersession is real -- of 81,408 predictions
superseded in two days, **zero** had a replacement with an identical
`content_hash`. Every one was a changed pick. Writing 60-80k slips a day is the
correct response to predictions moving, not waste to engineer away.

What has to stay bounded is the *residue*, at both ends: the active pool by the
3,000 ceiling the readiness gate enforces, and dead inventory by
`DefaultRetainNonActiveFor`. Keep both and the table stays small enough to stay
resident, which is what makes the endpoint's latency predictable.

Watch item: this is a high insert-and-delete workload, so autovacuum has to keep
pace on `acca_slips` and `acca_slip_legs`. If it falls behind, the heap bloats
and the same cold-read failure returns by another route.
