ALTER TABLE users
  ADD COLUMN IF NOT EXISTS public_id UUID NOT NULL DEFAULT gen_random_uuid(),
  ADD COLUMN IF NOT EXISTS account_status_reason TEXT,
  ADD COLUMN IF NOT EXISTS country_code CHAR(2);

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_public_id_unique ON users (public_id);

INSERT INTO terms_documents (document_key, version, published_at, required, content_url) VALUES
  ('terms', '2026-08-24', TIMESTAMPTZ '2026-08-24 00:00:00+00', true, '/terms'),
  ('privacy', '2026-08-24', TIMESTAMPTZ '2026-08-24 00:00:00+00', true, '/privacy')
ON CONFLICT (document_key, version) DO NOTHING;

CREATE TABLE IF NOT EXISTS user_identities (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider TEXT NOT NULL,
  provider_subject TEXT NOT NULL,
  provider_email TEXT,
  provider_email_verified BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_used_at TIMESTAMPTZ,
  UNIQUE (provider, provider_subject),
  UNIQUE (user_id, provider)
);

CREATE TABLE IF NOT EXISTS visitor_acquisition (
  visitor_hash TEXT PRIMARY KEY,
  first_country_code CHAR(2),
  first_referrer_host TEXT,
  first_utm_source TEXT,
  first_utm_medium TEXT,
  first_utm_campaign TEXT,
  first_utm_content TEXT,
  first_landing_page TEXT,
  device_type TEXT NOT NULL DEFAULT 'unknown',
  platform TEXT NOT NULL DEFAULT 'web',
  first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_acquisition (
  user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  visitor_hash TEXT REFERENCES visitor_acquisition(visitor_hash) ON DELETE SET NULL,
  first_country_code CHAR(2),
  first_referrer_host TEXT,
  first_utm_source TEXT,
  first_utm_medium TEXT,
  first_utm_campaign TEXT,
  first_utm_content TEXT,
  first_landing_page TEXT,
  signup_country_code CHAR(2),
  signup_referrer_host TEXT,
  signup_utm_source TEXT,
  signup_utm_medium TEXT,
  signup_utm_campaign TEXT,
  signup_page TEXT,
  device_type TEXT NOT NULL DEFAULT 'unknown',
  platform TEXT NOT NULL DEFAULT 'web',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_growth_events (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  visitor_hash TEXT,
  event_type TEXT NOT NULL,
  country_code CHAR(2),
  platform TEXT NOT NULL DEFAULT 'web',
  source TEXT NOT NULL DEFAULT '',
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  metadata JSONB NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_user_growth_events_type_time
  ON user_growth_events (event_type, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_user_growth_events_user_time
  ON user_growth_events (user_id, occurred_at DESC) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_growth_first_favorite_unique
  ON user_growth_events (user_id, event_type)
  WHERE user_id IS NOT NULL AND event_type = 'FIRST_FAVORITE_ADDED';

CREATE TABLE IF NOT EXISTS daily_user_growth (
  growth_date DATE NOT NULL,
  country_code CHAR(2) NOT NULL DEFAULT '',
  platform TEXT NOT NULL DEFAULT 'web',
  source TEXT NOT NULL DEFAULT '',
  visitors BIGINT NOT NULL DEFAULT 0,
  signup_views BIGINT NOT NULL DEFAULT 0,
  signup_started BIGINT NOT NULL DEFAULT 0,
  registered BIGINT NOT NULL DEFAULT 0,
  verified BIGINT NOT NULL DEFAULT 0,
  activated BIGINT NOT NULL DEFAULT 0,
  returned_d1 BIGINT NOT NULL DEFAULT 0,
  returned_d7 BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (growth_date, country_code, platform, source)
);

ALTER TABLE entity_favorites ALTER COLUMN notifications_enabled SET DEFAULT false;
UPDATE entity_favorites SET notifications_enabled = false WHERE notifications_enabled = true;

ALTER TABLE notification_preferences
  ALTER COLUMN email_enabled SET DEFAULT false,
  ALTER COLUMN push_enabled SET DEFAULT false,
  ALTER COLUMN match_alerts_enabled SET DEFAULT false,
  ALTER COLUMN prediction_alerts_enabled SET DEFAULT false;

UPDATE notification_preferences
SET email_enabled = false,
    push_enabled = false,
    match_alerts_enabled = false,
    prediction_alerts_enabled = false;

UPDATE notification_channel_preferences
SET enabled = false, digest_mode = 'off', updated_at = now()
WHERE category IN ('favorites', 'kickoff', 'digest');

DELETE FROM plan_entitlements
WHERE entitlement_key IN ('notifications.digest', 'notifications.realtime');

INSERT INTO feature_flags (feature_key, enabled, default_action, description) VALUES
  ('pro', false, 'deny', 'Pro remains disabled during the free-user launch'),
  ('payments', false, 'deny', 'Payments remain disabled during the free-user launch'),
  ('email_product_notifications', false, 'deny', 'Product email notifications are dormant'),
  ('push_notifications', false, 'deny', 'Push notifications are dormant')
ON CONFLICT (feature_key) DO UPDATE
SET enabled = false, default_action = 'deny', description = EXCLUDED.description;
