-- Reverse of 000060_ops_notification_outbox. Drops only what the up file
-- created. Nothing here touches ops_incidents, whose reference column and
-- identity belong to 000057.
--
-- The indexes are dropped explicitly before the table even though DROP TABLE
-- would take them with it: naming them keeps this file an accurate inventory
-- of what 000059 owns.

DROP INDEX IF EXISTS idx_ops_notification_outbox_incident;
DROP INDEX IF EXISTS idx_ops_notification_outbox_digest;
DROP INDEX IF EXISTS ux_ops_notification_outbox_open_fingerprint;
DROP INDEX IF EXISTS idx_ops_notification_outbox_claim;

DROP TABLE IF EXISTS ops_notification_outbox;
