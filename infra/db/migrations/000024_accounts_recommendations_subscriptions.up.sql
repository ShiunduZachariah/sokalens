ALTER TABLE users
  ADD COLUMN IF NOT EXISTS email_normalized TEXT,
  ADD COLUMN IF NOT EXISTS email_verified_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS account_status TEXT NOT NULL DEFAULT 'active',
  ADD COLUMN IF NOT EXISTS timezone TEXT NOT NULL DEFAULT 'Africa/Nairobi',
  ADD COLUMN IF NOT EXISTS language TEXT NOT NULL DEFAULT 'en',
  ADD COLUMN IF NOT EXISTS personalization_enabled BOOLEAN NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS marketing_consent BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS last_login_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

UPDATE users
SET email_normalized = lower(btrim(email))
WHERE email IS NOT NULL AND email_normalized IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email_normalized_unique
  ON users (email_normalized)
  WHERE email_normalized IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_users_account_status ON users (account_status, created_at DESC);

CREATE TABLE IF NOT EXISTS user_profiles (
  user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  display_name TEXT NOT NULL DEFAULT '',
  country_code CHAR(2),
  odds_format TEXT NOT NULL DEFAULT 'decimal',
  onboarding_completed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS roles (
  role_key TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  admin_role BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS permissions (
  permission_key TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS role_permissions (
  role_key TEXT NOT NULL REFERENCES roles(role_key) ON DELETE CASCADE,
  permission_key TEXT NOT NULL REFERENCES permissions(permission_key) ON DELETE CASCADE,
  PRIMARY KEY (role_key, permission_key)
);

CREATE TABLE IF NOT EXISTS user_roles (
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role_key TEXT NOT NULL REFERENCES roles(role_key) ON DELETE RESTRICT,
  granted_by BIGINT REFERENCES users(id),
  granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_at TIMESTAMPTZ,
  PRIMARY KEY (user_id, role_key)
);
CREATE INDEX IF NOT EXISTS idx_user_roles_active ON user_roles (role_key, user_id) WHERE revoked_at IS NULL;

ALTER TABLE user_sessions
  ADD COLUMN IF NOT EXISTS public_id UUID NOT NULL DEFAULT gen_random_uuid(),
  ADD COLUMN IF NOT EXISTS session_type TEXT NOT NULL DEFAULT 'user',
  ADD COLUMN IF NOT EXISTS device_name TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS user_agent TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS ip_prefix_hash TEXT,
  ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  ADD COLUMN IF NOT EXISTS absolute_expires_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS revocation_reason TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_sessions_public_id ON user_sessions (public_id);
CREATE INDEX IF NOT EXISTS idx_user_sessions_user_active_expiry
  ON user_sessions (user_id, expires_at DESC) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_user_sessions_expiry_cleanup
  ON user_sessions (expires_at) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS auth_tokens (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  purpose TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  expires_at TIMESTAMPTZ NOT NULL,
  used_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (purpose IN ('email_verification', 'password_reset', 'session_refresh'))
);
CREATE INDEX IF NOT EXISTS idx_auth_tokens_user_purpose ON auth_tokens (user_id, purpose, expires_at DESC);
CREATE INDEX IF NOT EXISTS idx_auth_tokens_expiry ON auth_tokens (expires_at) WHERE used_at IS NULL;

CREATE TABLE IF NOT EXISTS user_mfa_methods (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  method TEXT NOT NULL DEFAULT 'totp',
  secret_ciphertext BYTEA NOT NULL,
  enabled_at TIMESTAMPTZ,
  last_used_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, method)
);

CREATE TABLE IF NOT EXISTS mfa_recovery_codes (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash TEXT NOT NULL,
  used_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, code_hash)
);

CREATE TABLE IF NOT EXISTS terms_documents (
  document_key TEXT NOT NULL,
  version TEXT NOT NULL,
  published_at TIMESTAMPTZ NOT NULL,
  required BOOLEAN NOT NULL DEFAULT true,
  content_url TEXT NOT NULL,
  PRIMARY KEY (document_key, version)
);

CREATE TABLE IF NOT EXISTS consent_events (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  document_key TEXT NOT NULL,
  document_version TEXT NOT NULL,
  action TEXT NOT NULL,
  source TEXT NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}',
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  FOREIGN KEY (document_key, document_version) REFERENCES terms_documents(document_key, version),
  CHECK (action IN ('accepted', 'withdrawn'))
);
CREATE INDEX IF NOT EXISTS idx_consent_events_user_document ON consent_events (user_id, document_key, occurred_at DESC);

CREATE TABLE IF NOT EXISTS guest_sessions (
  id BIGSERIAL PRIMARY KEY,
  visitor_hash TEXT NOT NULL UNIQUE,
  session_hash TEXT NOT NULL UNIQUE,
  risk_score NUMERIC(6,3) NOT NULL DEFAULT 0,
  challenge_required BOOLEAN NOT NULL DEFAULT false,
  restriction_reason TEXT,
  first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_guest_sessions_expiry ON guest_sessions (expires_at);

CREATE TABLE IF NOT EXISTS guest_analysis_views (
  id BIGSERIAL PRIMARY KEY,
  guest_session_id BIGINT NOT NULL REFERENCES guest_sessions(id) ON DELETE CASCADE,
  match_id BIGINT NOT NULL,
  page_type TEXT NOT NULL DEFAULT 'match_analysis',
  first_viewed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_viewed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  view_count INTEGER NOT NULL DEFAULT 1,
  UNIQUE (guest_session_id, match_id, page_type),
  CHECK (view_count > 0)
);
CREATE INDEX IF NOT EXISTS idx_guest_analysis_views_window
  ON guest_analysis_views (guest_session_id, last_viewed_at DESC);

ALTER TABLE entity_favorites
  ADD COLUMN IF NOT EXISTS notifications_enabled BOOLEAN NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE TABLE IF NOT EXISTS user_interest_items (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  interest_type TEXT NOT NULL,
  interest_key TEXT NOT NULL,
  weight NUMERIC(8,4) NOT NULL DEFAULT 1,
  source TEXT NOT NULL DEFAULT 'explicit',
  disabled BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, interest_type, interest_key),
  CHECK (weight >= -100 AND weight <= 100)
);
CREATE INDEX IF NOT EXISTS idx_user_interest_items_active
  ON user_interest_items (user_id, interest_type, weight DESC) WHERE disabled = false;

CREATE TABLE IF NOT EXISTS recommendation_weights (
  weight_key TEXT PRIMARY KEY,
  weight NUMERIC(10,4) NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  updated_by BIGINT REFERENCES users(id),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS recommendation_events (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  guest_session_id BIGINT REFERENCES guest_sessions(id) ON DELETE SET NULL,
  session_public_id UUID,
  event_type TEXT NOT NULL,
  entity_type TEXT,
  entity_id TEXT,
  recommendation_id BIGINT,
  source TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}',
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (user_id IS NOT NULL OR guest_session_id IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_recommendation_events_user_recent
  ON recommendation_events (user_id, occurred_at DESC) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_recommendation_events_entity_recent
  ON recommendation_events (entity_type, entity_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_recommendation_events_type_recent
  ON recommendation_events (event_type, occurred_at DESC);

CREATE TABLE IF NOT EXISTS recommendation_candidates (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  item_type TEXT NOT NULL,
  item_id TEXT NOT NULL,
  score NUMERIC(12,6) NOT NULL,
  reason_key TEXT NOT NULL,
  reason_text TEXT NOT NULL,
  interest_key TEXT,
  prediction_confidence NUMERIC(6,3),
  starts_at TIMESTAMPTZ,
  freshness_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  sponsored BOOLEAN NOT NULL DEFAULT false,
  editorial BOOLEAN NOT NULL DEFAULT false,
  generation_key TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, item_type, item_id, generation_key)
);
CREATE INDEX IF NOT EXISTS idx_recommendation_candidates_feed
  ON recommendation_candidates (user_id, expires_at, score DESC, starts_at);

CREATE TABLE IF NOT EXISTS recommendation_dismissals (
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  item_type TEXT NOT NULL,
  item_id TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  expires_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, item_type, item_id)
);

CREATE TABLE IF NOT EXISTS subscription_plans (
  plan_key TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  tier TEXT NOT NULL,
  billing_interval TEXT NOT NULL,
  active BOOLEAN NOT NULL DEFAULT true,
  metadata JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (tier IN ('basic', 'pro')),
  CHECK (billing_interval IN ('none', 'month', 'year'))
);

CREATE TABLE IF NOT EXISTS plan_prices (
  id BIGSERIAL PRIMARY KEY,
  plan_key TEXT NOT NULL REFERENCES subscription_plans(plan_key) ON DELETE CASCADE,
  provider TEXT NOT NULL,
  environment TEXT NOT NULL,
  currency CHAR(3) NOT NULL,
  amount_minor BIGINT NOT NULL,
  provider_price_id TEXT,
  active BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (provider, environment, provider_price_id),
  UNIQUE (plan_key, provider, environment, currency),
  CHECK (amount_minor >= 0)
);

CREATE TABLE IF NOT EXISTS plan_entitlements (
  plan_key TEXT NOT NULL REFERENCES subscription_plans(plan_key) ON DELETE CASCADE,
  entitlement_key TEXT NOT NULL REFERENCES subscription_entitlements(entitlement_key) ON DELETE CASCADE,
  value JSONB NOT NULL DEFAULT 'true',
  PRIMARY KEY (plan_key, entitlement_key)
);

CREATE TABLE IF NOT EXISTS billing_customers (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  provider TEXT NOT NULL,
  environment TEXT NOT NULL,
  provider_customer_id TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (provider, environment, provider_customer_id),
  UNIQUE (user_id, provider, environment)
);

CREATE TABLE IF NOT EXISTS subscriptions (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  plan_key TEXT NOT NULL REFERENCES subscription_plans(plan_key),
  provider TEXT NOT NULL,
  environment TEXT NOT NULL,
  provider_subscription_id TEXT NOT NULL,
  status TEXT NOT NULL,
  current_period_start TIMESTAMPTZ,
  current_period_end TIMESTAMPTZ,
  cancel_at_period_end BOOLEAN NOT NULL DEFAULT false,
  cancelled_at TIMESTAMPTZ,
  grace_ends_at TIMESTAMPTZ,
  provider_revision TEXT,
  provider_event_time TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (provider, environment, provider_subscription_id),
  CHECK (status IN ('incomplete','active','trialing','past_due','cancelled','expired','payment_failed','scheduled_to_cancel','suspended'))
);
CREATE INDEX IF NOT EXISTS idx_subscriptions_user_status ON subscriptions (user_id, status, current_period_end DESC);
CREATE INDEX IF NOT EXISTS idx_subscriptions_period_end ON subscriptions (current_period_end) WHERE status IN ('active','trialing','past_due','scheduled_to_cancel');

CREATE TABLE IF NOT EXISTS billing_webhook_events (
  id BIGSERIAL PRIMARY KEY,
  provider TEXT NOT NULL,
  environment TEXT NOT NULL,
  provider_event_id TEXT NOT NULL,
  event_type TEXT NOT NULL,
  occurred_at TIMESTAMPTZ,
  signature_verified BOOLEAN NOT NULL,
  payload JSONB NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  attempts INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  processed_at TIMESTAMPTZ,
  UNIQUE (provider, environment, provider_event_id),
  CHECK (status IN ('pending','processing','processed','ignored','failed'))
);
CREATE INDEX IF NOT EXISTS idx_billing_webhooks_pending ON billing_webhook_events (status, received_at) WHERE status IN ('pending','failed');

CREATE TABLE IF NOT EXISTS subscription_events (
  id BIGSERIAL PRIMARY KEY,
  subscription_id BIGINT NOT NULL REFERENCES subscriptions(id) ON DELETE RESTRICT,
  webhook_event_id BIGINT REFERENCES billing_webhook_events(id) ON DELETE RESTRICT,
  event_type TEXT NOT NULL,
  previous_status TEXT,
  new_status TEXT NOT NULL,
  effective_at TIMESTAMPTZ NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_subscription_events_subscription ON subscription_events (subscription_id, effective_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS payment_transactions (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  subscription_id BIGINT REFERENCES subscriptions(id) ON DELETE RESTRICT,
  provider TEXT NOT NULL,
  environment TEXT NOT NULL,
  provider_payment_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  status TEXT NOT NULL,
  currency CHAR(3),
  amount_minor BIGINT,
  occurred_at TIMESTAMPTZ NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (provider, environment, provider_payment_id, kind)
);

CREATE TABLE IF NOT EXISTS payment_disputes (
  id BIGSERIAL PRIMARY KEY,
  payment_id BIGINT REFERENCES payment_transactions(id) ON DELETE RESTRICT,
  provider TEXT NOT NULL,
  environment TEXT NOT NULL,
  provider_dispute_id TEXT NOT NULL,
  status TEXT NOT NULL,
  reason TEXT,
  amount_minor BIGINT,
  currency CHAR(3),
  opened_at TIMESTAMPTZ,
  resolved_at TIMESTAMPTZ,
  metadata JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (provider, environment, provider_dispute_id)
);

CREATE TABLE IF NOT EXISTS user_entitlement_grants (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  entitlement_key TEXT NOT NULL REFERENCES subscription_entitlements(entitlement_key) ON DELETE RESTRICT,
  value JSONB NOT NULL DEFAULT 'true',
  source_type TEXT NOT NULL,
  source_id TEXT NOT NULL,
  effective_at TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, entitlement_key, source_type, source_id)
);
CREATE INDEX IF NOT EXISTS idx_user_entitlement_grants_active
  ON user_entitlement_grants (user_id, entitlement_key, effective_at, expires_at)
  WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS user_entitlement_snapshots (
  user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  tier TEXT NOT NULL DEFAULT 'basic',
  entitlements JSONB NOT NULL DEFAULT '{}',
  ads_allowed BOOLEAN NOT NULL DEFAULT true,
  version BIGINT NOT NULL DEFAULT 1,
  next_change_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS notification_channel_preferences (
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  channel TEXT NOT NULL,
  category TEXT NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT true,
  quiet_start TIME,
  quiet_end TIME,
  reminder_minutes INTEGER,
  digest_mode TEXT NOT NULL DEFAULT 'immediate',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, channel, category),
  CHECK (channel IN ('in_app','email','web_push','mobile_push')),
  CHECK (digest_mode IN ('immediate','daily','weekly','off'))
);

CREATE TABLE IF NOT EXISTS notifications (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  category TEXT NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  action_url TEXT,
  entity_type TEXT,
  entity_id TEXT,
  read_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_notifications_user_unread ON notifications (user_id, created_at DESC) WHERE read_at IS NULL;

CREATE TABLE IF NOT EXISTS notification_outbox (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  notification_id BIGINT REFERENCES notifications(id) ON DELETE CASCADE,
  channel TEXT NOT NULL,
  category TEXT NOT NULL,
  dedupe_key TEXT NOT NULL UNIQUE,
  payload JSONB NOT NULL,
  priority INTEGER NOT NULL DEFAULT 100,
  status TEXT NOT NULL DEFAULT 'pending',
  available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  attempts INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 5,
  lease_token UUID,
  lease_expires_at TIMESTAMPTZ,
  last_error TEXT,
  sent_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (status IN ('pending','sending','sent','retry','suppressed','dead_letter')),
  CHECK (attempts >= 0 AND max_attempts > 0)
);
CREATE INDEX IF NOT EXISTS idx_notification_outbox_claim
  ON notification_outbox (channel, status, available_at, priority, id)
  WHERE status IN ('pending','retry');
CREATE INDEX IF NOT EXISTS idx_notification_outbox_lease
  ON notification_outbox (lease_expires_at) WHERE status = 'sending';

CREATE TABLE IF NOT EXISTS notification_delivery_attempts (
  id BIGSERIAL PRIMARY KEY,
  outbox_id BIGINT NOT NULL REFERENCES notification_outbox(id) ON DELETE CASCADE,
  attempt_no INTEGER NOT NULL,
  provider_message_id TEXT,
  status TEXT NOT NULL,
  response_code TEXT,
  error_category TEXT,
  error_message TEXT,
  attempted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (outbox_id, attempt_no)
);

CREATE TABLE IF NOT EXISTS account_suspensions (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  status TEXT NOT NULL,
  reason TEXT NOT NULL,
  evidence JSONB NOT NULL DEFAULT '{}',
  actor_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  previous_account_status TEXT NOT NULL,
  new_account_status TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  lifted_at TIMESTAMPTZ,
  lifted_by BIGINT REFERENCES users(id),
  CHECK (status IN ('active','lifted'))
);
CREATE INDEX IF NOT EXISTS idx_account_suspensions_user ON account_suspensions (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS abuse_signals (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  guest_session_id BIGINT REFERENCES guest_sessions(id) ON DELETE SET NULL,
  signal_type TEXT NOT NULL,
  score NUMERIC(8,4) NOT NULL,
  reason TEXT NOT NULL,
  evidence JSONB NOT NULL DEFAULT '{}',
  review_status TEXT NOT NULL DEFAULT 'unreviewed',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  reviewed_at TIMESTAMPTZ,
  reviewed_by BIGINT REFERENCES users(id),
  CHECK (user_id IS NOT NULL OR guest_session_id IS NOT NULL),
  CHECK (review_status IN ('unreviewed','confirmed','dismissed'))
);
CREATE INDEX IF NOT EXISTS idx_abuse_signals_review ON abuse_signals (review_status, score DESC, created_at DESC);

CREATE TABLE IF NOT EXISTS security_events (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  event_type TEXT NOT NULL,
  outcome TEXT NOT NULL,
  reason TEXT,
  metadata JSONB NOT NULL DEFAULT '{}',
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_security_events_user_recent ON security_events (user_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_security_events_type_recent ON security_events (event_type, occurred_at DESC);

INSERT INTO roles (role_key, name, admin_role) VALUES
  ('basic', 'Basic user', false),
  ('pro', 'Pro user', false),
  ('super_admin', 'Super Admin', true),
  ('support_admin', 'User Support Admin', true),
  ('content_admin', 'Content and Prediction Admin', true),
  ('billing_admin', 'Subscription and Billing Admin', true),
  ('analyst', 'Read-only Analyst', true)
ON CONFLICT (role_key) DO UPDATE SET name = EXCLUDED.name, admin_role = EXCLUDED.admin_role;

INSERT INTO permissions (permission_key, description) VALUES
  ('users.read', 'View user accounts'),
  ('users.manage', 'Suspend, reactivate, and revoke user sessions'),
  ('billing.read', 'Review subscriptions, payments, and disputes'),
  ('billing.manage', 'Manage plans and billing exceptions'),
  ('content.manage', 'Manage prediction visibility and content'),
  ('recommendations.manage', 'Manage recommendation weights and limits'),
  ('notifications.read', 'Review notification delivery'),
  ('audit.read', 'Read audit and security history'),
  ('analytics.read', 'Read platform analytics')
ON CONFLICT (permission_key) DO UPDATE SET description = EXCLUDED.description;

INSERT INTO subscription_entitlements (entitlement_key, tier, name, description) VALUES
  ('analysis.standard', 'basic', 'Standard analysis', 'Full standard match analysis'),
  ('analysis.advanced', 'pro', 'Advanced analysis', 'Advanced markets and reasoning'),
  ('ads.hidden', 'pro', 'Ad-free browsing', 'Suppress advertising placements'),
  ('recommendations.personalized', 'basic', 'Personalized recommendations', 'Personalized fixture and prediction feed'),
  ('notifications.digest', 'basic', 'Prediction digest', 'Limited scheduled digest'),
  ('notifications.realtime', 'pro', 'Real-time alerts', 'Priority and favorite alerts'),
  ('history.expanded', 'pro', 'Expanded history', 'Expanded prediction history and filters')
ON CONFLICT (entitlement_key) DO UPDATE SET tier = EXCLUDED.tier, name = EXCLUDED.name, description = EXCLUDED.description;

INSERT INTO subscription_plans (plan_key, name, tier, billing_interval) VALUES
  ('basic', 'Basic', 'basic', 'none'),
  ('pro_monthly', 'Pro Monthly', 'pro', 'month'),
  ('pro_yearly', 'Pro Yearly', 'pro', 'year')
ON CONFLICT (plan_key) DO UPDATE SET name = EXCLUDED.name, tier = EXCLUDED.tier, billing_interval = EXCLUDED.billing_interval;

INSERT INTO plan_entitlements (plan_key, entitlement_key) VALUES
  ('basic', 'analysis.standard'),
  ('basic', 'recommendations.personalized'),
  ('basic', 'notifications.digest'),
  ('pro_monthly', 'analysis.standard'),
  ('pro_monthly', 'analysis.advanced'),
  ('pro_monthly', 'ads.hidden'),
  ('pro_monthly', 'recommendations.personalized'),
  ('pro_monthly', 'notifications.digest'),
  ('pro_monthly', 'notifications.realtime'),
  ('pro_monthly', 'history.expanded'),
  ('pro_yearly', 'analysis.standard'),
  ('pro_yearly', 'analysis.advanced'),
  ('pro_yearly', 'ads.hidden'),
  ('pro_yearly', 'recommendations.personalized'),
  ('pro_yearly', 'notifications.digest'),
  ('pro_yearly', 'notifications.realtime'),
  ('pro_yearly', 'history.expanded')
ON CONFLICT DO NOTHING;

INSERT INTO recommendation_weights (weight_key, weight, description) VALUES
  ('favorite_match', 100, 'Exact favorite match'),
  ('favorite_team', 70, 'Favorite team involved'),
  ('favorite_league', 45, 'Favorite league'),
  ('favorite_competition', 40, 'Favorite competition'),
  ('recent_search', 35, 'Recently searched entity'),
  ('preferred_market', 25, 'Preferred prediction market available'),
  ('high_confidence', 20, 'High-confidence prediction'),
  ('starting_soon', 15, 'Match starting soon'),
  ('recent_engagement', 18, 'Similar recently engaged content'),
  ('already_viewed', -12, 'Previously viewed content'),
  ('dismissed', -100, 'Explicitly dismissed content'),
  ('exploration', 5, 'Relevant exploration candidate')
ON CONFLICT (weight_key) DO UPDATE SET weight = EXCLUDED.weight, description = EXCLUDED.description;

