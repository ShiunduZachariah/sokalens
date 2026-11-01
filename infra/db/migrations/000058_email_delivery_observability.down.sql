ALTER TABLE notification_delivery_attempts
  DROP COLUMN IF EXISTS failure_code,
  DROP COLUMN IF EXISTS recipient_domain,
  DROP COLUMN IF EXISTS provider;
