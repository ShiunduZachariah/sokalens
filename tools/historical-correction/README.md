# Historical BTTS correction

`btts.py` scans compact `model_evaluation_facts` in one read-only, repeatable-read
snapshot. It recomputes each `btts_total_goals` selection against its own
supported half-goal line and the current official full-time score. Its manifest
lists every fact, including rows whose raw prediction was already retained
away, and explains which rows are eligible or deferred. The command does not
alter data unless the explicit apply arguments are present.

Run in the prediction-engine Python environment with `DATABASE_URL` set; avoid
printing or saving the URL:

```sh
python tools/historical-correction/btts.py --output /secure/path/btts-manifest.json
sha256sum /secure/path/btts-manifest.json
```

Review the manifest, its official scores, all classification counts, the
production backup identity, and the deployed code/migration SHA. Keep the
manifest and hash as audit artifacts. A bounded QA application uses a stable
operation UUID and the exact manifest-file hash:

```sh
python tools/historical-correction/btts.py \
  --apply-manifest /secure/path/btts-manifest.json \
  --expected-sha256 FULL_64_CHARACTER_SHA256 \
  --expected-batch-sha256 REVIEWED_BATCH_SHA256 \
  --operation-id REVIEWED_OPERATION_UUID --batch-start 0 --batch-size 100
```

Application only accepts surviving predictions with either an exact included
ledger bucket (or an explicitly not-included ledger), or a live settled result
with no ledger. The normal aggregator inserts ledger and deletes the live result
in one statement, so that latter case has not yet entered daily accuracy. Each
row is rechecked inside a serializable transaction with short timeouts. The
original immutable fact remains unchanged; an append-only revision, surviving
live result, precise daily bucket delta, and ledger classification are updated
together. A repeated operation is a no-op. A changed score, revision, missing
bucket, pending recount, or conflicting writer aborts rather than guessing.
The dry-run output lists deterministic SHA256 hashes for consecutive 100-row
eligible batches from the frozen manifest. Use that exact start, size and hash
for each reviewed batch; reuse the operation UUID for retries of the same
manifest.

Do **not** include `raw_parent_pruned` or `missing_rollup_provenance` rows in an
application. Compact facts do not prove whether a pruned row was included in
published accuracy or which bucket it affected. Recovering those needs a
verified pre-retention export/backup and a separately reviewed provenance path.
After an application, recompute accuracy snapshots through the existing
prediction-engine snapshot command and verify public result/accuracy responses.
The tool does not invalidate an external cache. PostgreSQL race and retention
tests in QA are required before any production application.

To run the isolated PostgreSQL transaction suite on QA, set
`BTTS_TEST_DATABASE_URL` to the QA database connection string in the test
runner environment and run:

```sh
python -m unittest discover -s tools/historical-correction -p 'test_btts_postgres.py' -v
```

The suite creates a uniquely named schema and drops it after each case; never
point this test variable at the production database.
