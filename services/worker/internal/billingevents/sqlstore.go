package billingevents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

const claimNextSQL = `
SELECT id,
       provider,
       environment,
       provider_event_id,
       event_type,
       occurred_at,
       signature_verified,
       payload,
       attempts
FROM billing_webhook_events
WHERE signature_verified = true
  AND status IN ('pending', 'failed')
ORDER BY received_at ASC, id ASC
FOR UPDATE SKIP LOCKED
LIMIT 1`

const subscriptionLockSQL = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

type SQLStore struct {
	db *sql.DB
}

func NewSQLStore(db *sql.DB) SQLStore {
	return SQLStore{db: db}
}

func (s SQLStore) WithinTransaction(ctx context.Context, work func(TransactionStore) error) error {
	if s.db == nil {
		return errors.New("billing event DB is nil")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := work(sqlTransaction{tx: tx}); err != nil {
		return err
	}
	return tx.Commit()
}

type sqlTransaction struct {
	tx *sql.Tx
}

func (s sqlTransaction) ClaimNext(ctx context.Context) (InboxEvent, bool, error) {
	var event InboxEvent
	var occurredAt sql.NullTime
	err := s.tx.QueryRowContext(ctx, claimNextSQL).Scan(
		&event.ID, &event.Provider, &event.Environment, &event.ProviderEventID,
		&event.EventType, &occurredAt, &event.SignatureVerified, &event.Payload, &event.Attempts,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return InboxEvent{}, false, nil
	}
	if err != nil {
		return InboxEvent{}, false, err
	}
	if occurredAt.Valid {
		event.OccurredAt = occurredAt.Time
	}
	_, err = s.tx.ExecContext(ctx, `
UPDATE billing_webhook_events
SET status = 'processing', attempts = attempts + 1, last_error = NULL
WHERE id = $1`, event.ID)
	return event, true, err
}

func (s sqlTransaction) LoadSubscriptionForUpdate(ctx context.Context, event InboxEvent, providerSubscriptionID string) (Subscription, bool, error) {
	lockKey := event.Provider + ":" + event.Environment + ":" + providerSubscriptionID
	if _, err := s.tx.ExecContext(ctx, subscriptionLockSQL, lockKey); err != nil {
		return Subscription{}, false, err
	}
	var subscription Subscription
	var eventTime, periodEnd sql.NullTime
	err := s.tx.QueryRowContext(ctx, `
SELECT id, user_id, plan_key, status, COALESCE(provider_revision, ''), provider_event_time, current_period_end
FROM subscriptions
WHERE provider = $1 AND environment = $2 AND provider_subscription_id = $3
FOR UPDATE`, event.Provider, event.Environment, providerSubscriptionID).Scan(
		&subscription.ID, &subscription.UserID, &subscription.PlanKey, &subscription.Status,
		&subscription.ProviderRevision, &eventTime, &periodEnd,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, false, nil
	}
	if err != nil {
		return Subscription{}, false, err
	}
	if eventTime.Valid {
		subscription.ProviderEventTime = eventTime.Time
	}
	if periodEnd.Valid {
		value := periodEnd.Time
		subscription.CurrentPeriodEnd = &value
	}
	return subscription, true, nil
}

func (s sqlTransaction) UpsertSubscription(ctx context.Context, event InboxEvent, update SubscriptionUpdate) (Subscription, error) {
	var subscription Subscription
	var eventTime, periodEnd sql.NullTime
	err := s.tx.QueryRowContext(ctx, `
INSERT INTO subscriptions (
  user_id, plan_key, provider, environment, provider_subscription_id, status,
  current_period_start, current_period_end, cancel_at_period_end, cancelled_at,
  grace_ends_at, provider_revision, provider_event_time, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NULLIF($12, ''), $13, now())
ON CONFLICT (provider, environment, provider_subscription_id)
DO UPDATE SET plan_key = EXCLUDED.plan_key,
              status = EXCLUDED.status,
              current_period_start = EXCLUDED.current_period_start,
              current_period_end = EXCLUDED.current_period_end,
              cancel_at_period_end = EXCLUDED.cancel_at_period_end,
              cancelled_at = EXCLUDED.cancelled_at,
              grace_ends_at = EXCLUDED.grace_ends_at,
              provider_revision = EXCLUDED.provider_revision,
              provider_event_time = EXCLUDED.provider_event_time,
              updated_at = now()
RETURNING id, user_id, plan_key, status, COALESCE(provider_revision, ''), provider_event_time, current_period_end`,
		update.UserID, update.PlanKey, event.Provider, event.Environment, update.ProviderSubscriptionID,
		update.Status, nullableTime(update.CurrentPeriodStart), nullableTime(update.CurrentPeriodEnd),
		update.CancelAtPeriodEnd, nullableTime(update.CancelledAt), nullableTime(update.GraceEndsAt),
		update.Revision, update.EffectiveAt,
	).Scan(
		&subscription.ID, &subscription.UserID, &subscription.PlanKey, &subscription.Status,
		&subscription.ProviderRevision, &eventTime, &periodEnd,
	)
	if err != nil {
		return Subscription{}, err
	}
	if eventTime.Valid {
		subscription.ProviderEventTime = eventTime.Time
	}
	if periodEnd.Valid {
		value := periodEnd.Time
		subscription.CurrentPeriodEnd = &value
	}
	return subscription, nil
}

func (s sqlTransaction) AppendSubscriptionEvent(ctx context.Context, event InboxEvent, subscription Subscription, update SubscriptionUpdate, previousStatus string) error {
	metadata, _ := json.Marshal(map[string]string{
		"provider": event.Provider, "environment": event.Environment,
		"provider_event_id": event.ProviderEventID, "provider_revision": update.Revision,
	})
	_, err := s.tx.ExecContext(ctx, `
INSERT INTO subscription_events (
  subscription_id, webhook_event_id, event_type, previous_status, new_status,
  effective_at, metadata
) VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7::jsonb)`,
		subscription.ID, event.ID, event.EventType, previousStatus, update.Status, update.EffectiveAt, string(metadata),
	)
	return err
}

func (s sqlTransaction) SyncEntitlements(ctx context.Context, subscription Subscription, update SubscriptionUpdate, enabled bool, now time.Time) error {
	sourceID := strconv.FormatInt(subscription.ID, 10)
	if !enabled {
		_, err := s.tx.ExecContext(ctx, `
UPDATE user_entitlement_grants
SET revoked_at = $4
WHERE user_id = $1 AND source_type = $2 AND source_id = $3 AND revoked_at IS NULL`,
			subscription.UserID, "subscription", sourceID, now)
		if err != nil {
			return err
		}
	} else {
		_, err := s.tx.ExecContext(ctx, `
UPDATE user_entitlement_grants
SET revoked_at = $4
WHERE user_id = $1
  AND source_type = 'subscription'
  AND source_id = $2
  AND revoked_at IS NULL
  AND entitlement_key NOT IN (
    SELECT entitlement_key FROM plan_entitlements WHERE plan_key = $3
  )`, subscription.UserID, sourceID, update.PlanKey, now)
		if err != nil {
			return err
		}
		_, err = s.tx.ExecContext(ctx, `
INSERT INTO user_entitlement_grants (
  user_id, entitlement_key, value, source_type, source_id, effective_at, expires_at
)
SELECT $1, entitlement_key, value, 'subscription', $2, $3, $4
FROM plan_entitlements
WHERE plan_key = $5
ON CONFLICT (user_id, entitlement_key, source_type, source_id)
DO UPDATE SET value = EXCLUDED.value,
              effective_at = EXCLUDED.effective_at,
              expires_at = EXCLUDED.expires_at,
              revoked_at = NULL`,
			subscription.UserID, sourceID, update.EffectiveAt, nullableTime(update.CurrentPeriodEnd), update.PlanKey)
		if err != nil {
			return err
		}
	}
	return s.refreshEntitlementSnapshot(ctx, subscription.UserID, now)
}

func (s sqlTransaction) refreshEntitlementSnapshot(ctx context.Context, userID int64, now time.Time) error {
	_, err := s.tx.ExecContext(ctx, `
WITH active AS (
  SELECT entitlement_key, value, expires_at
  FROM user_entitlement_grants
  WHERE user_id = $1
    AND revoked_at IS NULL
    AND effective_at <= $2
    AND (expires_at IS NULL OR expires_at > $2)
), projection AS (
  SELECT COALESCE(jsonb_object_agg(entitlement_key, value), '{}'::jsonb) AS entitlements,
         COALESCE(bool_or(entitlement_key IN ('ads.hidden', 'pro_no_ads')), false) AS hides_ads,
         COALESCE(bool_or(entitlement_key IN ('analysis.advanced', 'pro_deeper_analysis')), false) AS is_pro,
         min(expires_at) AS next_change_at
  FROM active
)
INSERT INTO user_entitlement_snapshots (user_id, tier, entitlements, ads_allowed, version, next_change_at, updated_at)
SELECT $1,
       CASE WHEN is_pro THEN 'pro' ELSE 'basic' END,
       entitlements,
       NOT hides_ads,
       1,
       next_change_at,
       $2
FROM projection
ON CONFLICT (user_id)
DO UPDATE SET tier = EXCLUDED.tier,
              entitlements = EXCLUDED.entitlements,
              ads_allowed = EXCLUDED.ads_allowed,
              version = user_entitlement_snapshots.version + 1,
              next_change_at = EXCLUDED.next_change_at,
              updated_at = EXCLUDED.updated_at`, userID, now)
	return err
}

func (s sqlTransaction) EnqueueNotification(ctx context.Context, userID int64, intent NotificationIntent) error {
	if intent.Channel == "" || intent.Category == "" || intent.DedupeKey == "" {
		return errors.New("billing notification channel, category, and dedupe key are required")
	}
	if len(intent.Payload) == 0 || !json.Valid(intent.Payload) {
		return errors.New("billing notification payload must be valid JSON")
	}
	if intent.Priority == 0 {
		intent.Priority = 100
	}
	_, err := s.tx.ExecContext(ctx, `
INSERT INTO notification_outbox (
  user_id, channel, category, dedupe_key, payload, priority
) VALUES ($1, $2, $3, $4, $5::jsonb, $6)
ON CONFLICT (dedupe_key) DO NOTHING`,
		userID, intent.Channel, intent.Category, intent.DedupeKey, string(intent.Payload), intent.Priority)
	return err
}

func (s sqlTransaction) RecordDispute(ctx context.Context, event InboxEvent, subscription Subscription, dispute DisputeUpdate) error {
	_, err := s.tx.ExecContext(ctx, `INSERT INTO payment_disputes (provider,environment,provider_dispute_id,status,reason,amount_minor,currency,opened_at,resolved_at,metadata) VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,NULLIF($7,''),$8,$9,jsonb_build_object('billing_webhook_event_id',$10,'subscription_id',$11,'user_id',$12)) ON CONFLICT (provider,environment,provider_dispute_id) DO UPDATE SET status=excluded.status,reason=excluded.reason,amount_minor=excluded.amount_minor,currency=excluded.currency,opened_at=excluded.opened_at,resolved_at=excluded.resolved_at,metadata=payment_disputes.metadata || excluded.metadata,updated_at=now()`, event.Provider, event.Environment, dispute.ProviderDisputeID, dispute.Status, dispute.Reason, dispute.AmountMinor, dispute.Currency, nullableTime(dispute.OpenedAt), nullableTime(dispute.ResolvedAt), event.ID, subscription.ID, subscription.UserID)
	if err != nil {
		return err
	}
	_, err = s.tx.ExecContext(ctx, `INSERT INTO audit_logs (action,entity_type,entity_id,reason,after_value) VALUES ('billing.dispute.recorded','payment_dispute',$1,$2,jsonb_build_object('status',$3,'subscription_id',$4,'user_id',$5,'provider_event_id',$6))`, dispute.ProviderDisputeID, dispute.Reason, dispute.Status, subscription.ID, subscription.UserID, event.ProviderEventID)
	return err
}

func (s sqlTransaction) MarkProcessed(ctx context.Context, eventID int64, now time.Time) error {
	_, err := s.tx.ExecContext(ctx, `
UPDATE billing_webhook_events
SET status = 'processed', processed_at = $2, last_error = NULL
WHERE id = $1 AND status = 'processing'`, eventID, now)
	return err
}

func (s sqlTransaction) MarkIgnored(ctx context.Context, eventID int64, reason string, now time.Time) error {
	_, err := s.tx.ExecContext(ctx, `
UPDATE billing_webhook_events
SET status = 'ignored', processed_at = $3, last_error = $2
WHERE id = $1 AND status = 'processing'`, eventID, reason, now)
	return err
}

func (s sqlTransaction) MarkFailed(ctx context.Context, eventID int64, reason string, now time.Time) error {
	_, err := s.tx.ExecContext(ctx, `
UPDATE billing_webhook_events
SET status = 'failed', processed_at = NULL, last_error = $2
WHERE id = $1 AND status = 'processing'`, eventID, reason, now)
	return err
}

func nullableTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC()
}
