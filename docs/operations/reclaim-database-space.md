# Reclaiming database space

The production database is ~12 GB. Two tables are almost all of it:

| Table | Size |
|---|---|
| `engine_predictions` | 6,133 MB |
| `provider_request_logs` | 5,655 MB |

That size is paid for on every release. The deploy takes a restore-verified
backup -- dumping and restoring the whole database into a scratch copy before
it proceeds -- and that step takes **1h 8m**. It is a good safety property, and
the way to keep it is to stop the database growing, not to weaken the check.

## Why nothing has been pruning

`ENABLE_DESTRUCTIVE_RETENTION` defaults to `false` and ships `false` in
`.env.prod.example`. `Repository.Prune` returns early on that gate, so neither
of the two large tables has ever been pruned, at any retention setting.

Lowering `provider_request_logs` retention from 30 days to 1 (#215) therefore
changed a number nothing read. #216 fixed a second, independent problem -- the
drain ran at 10,000 rows a day against ~1.25 million arriving -- but that also
only matters once the gate is open.

## Do not flip the environment variable first

`enable_destructive_retention` and `dry_run` are **job payload fields**, not
only environment settings:

```go
EnableDestructiveRetention bool `json:"enable_destructive_retention"`
DryRun                     bool `json:"dry_run"`
```

`Job.Run` unmarshals the payload over the configured policy, so a single run
can be granted destructive retention without changing the environment,
restarting the worker, or making the change permanent. The next scheduled run
goes back to the configured default on its own.

That is the right way to do this the first time. Flipping the env var makes
every future run destructive before anyone has seen what one run deletes.

## What the gate actually opens

Not just logs. With the defaults in `withDefaults()`:

| What | Window | Notes |
|---|---|---|
| `engine_predictions` | fixture finished > **24h** | Only when durably rolled up *and* not saved or favourited |
| `saved_predictions` | fixture finished > **6h** | **User data** -- things people chose to save |
| `entity_favorites` (predictions, matches) | fixture finished > **6h** | **User data** |
| `provider_raw_payloads` | > 7 days | |
| `provider_request_logs` | > 1 day | |
| `model_evaluation_facts` | > 365 days | |
| `prediction_accuracy_daily` | > 365 days | |
| `fixture_statistics` | keeps latest **10** per team | |

The two 6-hour rows are the ones to be deliberate about. Everything else is
exhaust or long-window. `engine_predictions` is protected twice over: a row is
only eligible once a rollup ledger entry and evaluation evidence both exist,
so unsettled model output cannot be discarded.

## Procedure

### 1. Dry run, and read it

```sql
INSERT INTO jobs (job_key, status, payload, queue, priority, max_attempts)
VALUES ('maintenance.prune-retained-data', 'queued',
        '{"enable_destructive_retention": true, "dry_run": true}'::jsonb,
        'maintenance', 90, 3);
```

`status` must be `'queued'`. The claim query looks for that exact value;
`'pending'` leaves the job sitting unclaimed forever.

The run logs `retention dry-run completed` with a count per table and deletes
nothing. **Read `saved_predictions` and the two favourite counts before going
further.** If they are large, decide whether a 6-hour window on saved items is
the product behaviour you want before you let a real run have them.

To widen a window for this run only, add it to the payload, e.g.
`"saved_reference_retention_hours": 720`.

### 2. One real run

Same payload without `dry_run`:

```sql
INSERT INTO jobs (job_key, status, payload, queue, priority, max_attempts)
VALUES ('maintenance.prune-retained-data', 'queued',
        '{"enable_destructive_retention": true}'::jsonb,
        'maintenance', 90, 3);
```

`provider_request_logs` is bounded by `providerPruneBudget` (10 minutes) and
its own batch allowance, so one run drains what it can and leaves the rest.
Repeat until the deleted count comes back near zero -- it converges in a few
runs rather than one.

### 3. Reclaim the space

`DELETE` marks rows dead; it does not return bytes to the filesystem. Until the
table is rewritten the backup stays slow, so this step is what actually fixes
the 1h 8m.

`VACUUM FULL` takes an `ACCESS EXCLUSIVE` lock for the whole rewrite. The
ingestion worker writes to `provider_request_logs` continuously, so running it
against live traffic will stall those writes -- a rehearsal of migration
`000061` held a lock on `matches` long enough to do exactly that. Either stop
the worker for the duration, or use `pg_repack`, which rewrites without the
long exclusive lock:

```sql
-- Preferred, if the extension is available:
-- pg_repack -t provider_request_logs -t engine_predictions

VACUUM (FULL, ANALYZE) provider_request_logs;
VACUUM (FULL, ANALYZE) engine_predictions;
```

Do this in a maintenance window. For reference, the same treatment took the
ACCA slips table from 295 MB to 8.1 MB.

### 4. Then decide about the environment variable

Once a real run has been observed and the space is back, setting
`ENABLE_DESTRUCTIVE_RETENTION=true` in the production environment makes it
routine, and the daily schedule keeps both tables flat from then on. Leaving it
`false` means repeating steps 1-2 by hand whenever the database grows again.

## Verifying

```sql
SELECT relname, pg_size_pretty(pg_total_relation_size(c.oid)) AS size
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind = 'r'
ORDER BY pg_total_relation_size(c.oid) DESC
LIMIT 10;
```

The deploy's backup step is the real measure: it should drop well below an
hour once both tables are rewritten.
