DELETE FROM feature_flags
WHERE feature_key IN ('pro', 'payments', 'email_product_notifications', 'push_notifications');

INSERT INTO plan_entitlements (plan_key, entitlement_key) VALUES
  ('basic', 'notifications.digest'),
  ('pro_monthly', 'notifications.digest'),
  ('pro_monthly', 'notifications.realtime'),
  ('pro_yearly', 'notifications.digest'),
  ('pro_yearly', 'notifications.realtime')
ON CONFLICT DO NOTHING;

ALTER TABLE notification_preferences
  ALTER COLUMN email_enabled SET DEFAULT true,
  ALTER COLUMN push_enabled SET DEFAULT true,
  ALTER COLUMN match_alerts_enabled SET DEFAULT true,
  ALTER COLUMN prediction_alerts_enabled SET DEFAULT true;

ALTER TABLE entity_favorites ALTER COLUMN notifications_enabled SET DEFAULT true;

DROP TABLE IF EXISTS daily_user_growth;
DROP TABLE IF EXISTS user_growth_events;
DROP TABLE IF EXISTS user_acquisition;
DROP TABLE IF EXISTS visitor_acquisition;
DROP TABLE IF EXISTS user_identities;

DELETE FROM terms_documents document
WHERE document.version = '2026-08-24'
  AND document.document_key IN ('terms', 'privacy')
  AND NOT EXISTS (
    SELECT 1 FROM consent_events event
    WHERE event.document_key = document.document_key
      AND event.document_version = document.version
  );

DROP INDEX IF EXISTS idx_users_public_id_unique;
ALTER TABLE users
  DROP COLUMN IF EXISTS country_code,
  DROP COLUMN IF EXISTS account_status_reason,
  DROP COLUMN IF EXISTS public_id;
