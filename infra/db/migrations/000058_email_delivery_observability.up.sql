ALTER TABLE notification_delivery_attempts
  ADD COLUMN IF NOT EXISTS provider TEXT,
  ADD COLUMN IF NOT EXISTS recipient_domain TEXT,
  ADD COLUMN IF NOT EXISTS failure_code TEXT;
