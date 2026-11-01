-- 000059_ops_telegram_bridge.down.sql
-- Local/test expansion rollback only. Drops exactly and only what the up file
-- created, in reverse dependency order. Rolling this back discards the Telegram
-- offset and the update ledger, so a reapply re-polls whatever Telegram still
-- holds; the ops_telegram_updates primary key makes that safe, not silent.

ALTER TABLE ops_incidents
  DROP CONSTRAINT IF EXISTS ops_incidents_reference_key;
ALTER TABLE ops_incidents
  DROP COLUMN IF EXISTS reference;

ALTER TABLE ops_deployments
  DROP CONSTRAINT IF EXISTS ops_deployments_deploy_class_check,
  DROP COLUMN IF EXISTS train_pr_numbers,
  DROP COLUMN IF EXISTS train_id,
  DROP COLUMN IF EXISTS deploy_class;

DROP INDEX IF EXISTS idx_ops_release_trains_status_window;
DROP TABLE IF EXISTS ops_release_trains;

DROP INDEX IF EXISTS idx_ops_pending_confirmations_expiry;
DROP TABLE IF EXISTS ops_pending_confirmations;

DROP INDEX IF EXISTS idx_ops_telegram_updates_unauthorized_recent;
DROP INDEX IF EXISTS idx_ops_telegram_updates_received;
DROP TABLE IF EXISTS ops_telegram_updates;

DROP TABLE IF EXISTS ops_telegram_state;
