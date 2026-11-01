CREATE TABLE IF NOT EXISTS account_deletion_requests (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  status TEXT NOT NULL DEFAULT 'pending',
  requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  eligible_after TIMESTAMPTZ NOT NULL,
  completed_at TIMESTAMPTZ,
  cancelled_at TIMESTAMPTZ,
  metadata JSONB NOT NULL DEFAULT '{}',
  CHECK (status IN ('pending','processing','completed','cancelled'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_account_deletion_requests_pending
  ON account_deletion_requests (user_id) WHERE status IN ('pending','processing');

CREATE TABLE IF NOT EXISTS saved_predictions (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  prediction_id UUID NOT NULL REFERENCES engine_predictions(id) ON DELETE CASCADE,
  notes TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, prediction_id)
);
CREATE INDEX IF NOT EXISTS idx_saved_predictions_user_recent
  ON saved_predictions (user_id, created_at DESC);

ALTER TABLE app_settings
  ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';

INSERT INTO app_settings (key, value, description) VALUES
  ('guest.analysis_view_limit', '4', 'Distinct detailed match-analysis pages allowed per rolling window'),
  ('guest.analysis_window_days', '7', 'Rolling guest-analysis limit window in days'),
  ('recommendations.feed_ttl_seconds', '300', 'Personalized feed cache lifetime'),
  ('billing.renewal_reminder_days', '[7,3,1]', 'Days before renewal or expiry to enqueue reminders'),
  ('billing.failed_payment_grace_hours', '0', 'Explicit failed-payment grace period; zero disables grace')
ON CONFLICT (key) DO NOTHING;

INSERT INTO terms_documents (document_key, version, published_at, required, content_url) VALUES
  ('terms', '2026-08-05', TIMESTAMPTZ '2026-08-05 00:00:00+00', true, '/terms'),
  ('privacy', '2026-08-05', TIMESTAMPTZ '2026-08-05 00:00:00+00', true, '/privacy'),
  ('cookie', '2026-08-05', TIMESTAMPTZ '2026-08-05 00:00:00+00', true, '/cookies'),
  ('responsible_use', '2026-08-05', TIMESTAMPTZ '2026-08-05 00:00:00+00', true, '/responsible-use'),
  ('prediction_disclaimer', '2026-08-05', TIMESTAMPTZ '2026-08-05 00:00:00+00', true, '/terms#predictions'),
  ('subscription_terms', '2026-08-05', TIMESTAMPTZ '2026-08-05 00:00:00+00', false, '/terms#subscriptions'),
  ('no_refund', '2026-08-05', TIMESTAMPTZ '2026-08-05 00:00:00+00', false, '/terms#refunds'),
  ('cancellation', '2026-08-05', TIMESTAMPTZ '2026-08-05 00:00:00+00', false, '/terms#cancellation'),
  ('chargeback', '2026-08-05', TIMESTAMPTZ '2026-08-05 00:00:00+00', false, '/terms#payment-disputes')
ON CONFLICT (document_key, version) DO NOTHING;

CREATE INDEX IF NOT EXISTS idx_notifications_user_recent
  ON notifications (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_billing_webhooks_ordering
  ON billing_webhook_events (provider, environment, occurred_at, id);
