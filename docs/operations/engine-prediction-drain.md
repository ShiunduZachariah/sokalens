# Engine prediction drain

`engine_predictions` grows faster than the former daily 10,000-row allowance. The
old query sorted millions of rows before taking a batch, and the same daily job
also ranked the entire table for duplicate cleanup. Do not increase that
allowance to chase the backlog.

The hourly raw-prediction job walks finished fixtures using a persisted cursor
and existing fixture/prediction indexes. Each run has a small batch and time
budget; it resumes from the cursor on the next run. The job is independent of
the daily retention pass and defaults off with
`ENABLE_RAW_PREDICTION_DRAIN=false`.

`RAW_PREDICTION_DRAIN_BATCH_SIZE` defaults to 1,000 and is limited to 5,000;
`RAW_PREDICTION_DRAIN_MAX_BATCHES` defaults to 10 and is limited to 100. Begin
with the defaults, then raise the batch allowance only after measuring real
run duration and production load. The job also has an eight-minute budget.

## Release and canary

1. Create a fresh `infra/deploy/backup.sh` recovery point and confirm its
   checksum, catalogue, isolated restore, migration list, and critical counts.
   Keep the previous verified dump until the new one passes.
2. Deploy the code and additive migration to the `qa` branch. Confirm CI,
   `scripts/qa/smoke.sh`, live QA checkout SHA, and QA migration ledger.
3. On QA only, set `ENABLE_RAW_PREDICTION_DRAIN=true` and recreate the worker.
   Observe one bounded job. Check deleted rows, job duration, cursor movement,
   lock waits, WAL, PostgreSQL CPU/I/O, worker heartbeat, and public pages.
4. Promote the exact tested change to `main` through the normal CI deployment.
   Keep the production switch off until the fresh backup and deployment smoke
   are confirmed. Then enable it and recreate only the production worker.
5. Observe the first production run and at least one resumed run. Stop the
   drain if latency, 5xx, lock waits, WAL, or current-prediction integrity
   worsens. Set the switch to false and recreate the worker to stop new runs;
   an active short job should finish within its bounded lease.

The delete must preserve a prediction unless its fixture has a confirmed
`finished_at` older than the retention cutoff. Settled rows require both a
rollup ledger and compact model evidence. Saved predictions, favorites, and
active ACCA legs must remain. The cursor should advance past blocked fixtures
and wrap around so late corrections are revisited.

Inspect progress with the `raw_prediction_drain` rows in
`data_maintenance_audit` and the corresponding `jobs` records. Compare row
counts, table sizes, and `pg_stat_user_tables` before and after. Plain vacuum
lets PostgreSQL reuse dead space; it does not return the table's bytes to the
filesystem. Do not run `VACUUM FULL` on production. Physical compaction is a
separate `pg_repack` change after a QA rehearsal and disk-capacity check.
