ALTER TABLE accuracy_snapshots
  DROP CONSTRAINT IF EXISTS accuracy_snapshots_snapshot_date_scope_scope_id_key;

CREATE UNIQUE INDEX IF NOT EXISTS idx_accuracy_snapshots_unique_scope
  ON accuracy_snapshots (snapshot_date, scope, scope_id) NULLS NOT DISTINCT;
