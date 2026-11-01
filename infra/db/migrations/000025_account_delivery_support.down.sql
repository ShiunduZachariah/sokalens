DROP INDEX IF EXISTS idx_billing_webhooks_ordering;
DROP INDEX IF EXISTS idx_notifications_user_recent;

DELETE FROM terms_documents
WHERE version = '2026-08-05'
  AND document_key IN ('terms','privacy','cookie','responsible_use','prediction_disclaimer','subscription_terms','no_refund','cancellation','chargeback')
  AND NOT EXISTS (
    SELECT 1
    FROM consent_events
    WHERE consent_events.document_key = terms_documents.document_key
      AND consent_events.document_version = terms_documents.version
  );

DELETE FROM app_settings
WHERE key IN ('guest.analysis_view_limit','guest.analysis_window_days','recommendations.feed_ttl_seconds','billing.renewal_reminder_days','billing.failed_payment_grace_hours');

ALTER TABLE app_settings DROP COLUMN IF EXISTS description;
DROP TABLE IF EXISTS saved_predictions;
DROP TABLE IF EXISTS account_deletion_requests;
