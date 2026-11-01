DROP INDEX IF EXISTS idx_accuracy_snapshots_unique_scope;

ALTER TABLE accuracy_snapshots
  ADD CONSTRAINT accuracy_snapshots_snapshot_date_scope_scope_id_key
  UNIQUE (snapshot_date, scope, scope_id);
