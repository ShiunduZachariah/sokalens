package billingevents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ReminderRepository struct {
	db *sql.DB
}

func NewReminderRepository(db *sql.DB) ReminderRepository {
	return ReminderRepository{db: db}
}

func (r ReminderRepository) LoadReminderDays(ctx context.Context) ([]int, error) {
	if r.db == nil {
		return nil, errors.New("subscription reminder DB is nil")
	}
	var raw []byte
	err := r.db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'billing.renewal_reminder_days'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return append([]int(nil), defaultRenewalReminderDays...), nil
	}
	if err != nil {
		return nil, err
	}
	var days []int
	if err := json.Unmarshal(raw, &days); err != nil {
		return nil, fmt.Errorf("decode billing.renewal_reminder_days: %w", err)
	}
	return days, nil
}

func (r ReminderRepository) ListReminderSubscriptions(ctx context.Context, now time.Time, reminderDays []int, limit int) ([]ReminderSubscription, error) {
	if r.db == nil {
		return nil, errors.New("subscription reminder DB is nil")
	}
	if len(reminderDays) == 0 {
		return nil, errors.New("subscription reminder days are required")
	}
	if limit <= 0 {
		return nil, errors.New("subscription reminder limit must be positive")
	}
	args := []any{now}
	dayPlaceholders := make([]string, 0, len(reminderDays))
	for _, day := range reminderDays {
		args = append(args, day)
		dayPlaceholders = append(dayPlaceholders, fmt.Sprintf("$%d", len(args)))
	}
	args = append(args, limit)
	limitPlaceholder := fmt.Sprintf("$%d", len(args))
	dayOffset := "((subscription.current_period_end AT TIME ZONE 'UTC')::date - ($1::timestamptz AT TIME ZONE 'UTC')::date)"
	query := fmt.Sprintf(`
SELECT subscription.id,
       subscription.user_id,
       subscription.plan_key,
       subscription.status,
       subscription.current_period_end,
       subscription.cancel_at_period_end,
       subscription.cancelled_at,
       COALESCE(account.email_normalized, account.email, ''),
       account.email_verified_at IS NOT NULL
FROM subscriptions AS subscription
JOIN users AS account ON account.id = subscription.user_id
WHERE account.account_status = 'active'
  AND account.deleted_at IS NULL
  AND (
    (
      subscription.current_period_end IS NOT NULL
      AND %[1]s IN (%[2]s)
      AND (
        (
          subscription.status IN ('active', 'trialing')
          AND NOT subscription.cancel_at_period_end
          AND NOT EXISTS (
            SELECT 1
            FROM notification_outbox AS existing
            WHERE existing.dedupe_key = 'billing:subscription:' || subscription.id::text || ':' ||
              floor(extract(epoch FROM subscription.current_period_end))::bigint::text ||
              ':renewal_reminder-' || %[1]s::text || 'd:in_app'
          )
        )
        OR (
          (subscription.cancel_at_period_end OR subscription.status = 'scheduled_to_cancel')
          AND NOT EXISTS (
            SELECT 1
            FROM notification_outbox AS existing
            WHERE existing.dedupe_key = 'billing:subscription:' || subscription.id::text || ':' ||
              floor(extract(epoch FROM subscription.current_period_end))::bigint::text ||
              ':expiry_reminder-' || %[1]s::text || 'd:in_app'
          )
        )
      )
    )
    OR (
      (subscription.cancel_at_period_end OR subscription.status = 'scheduled_to_cancel')
      AND (subscription.current_period_end IS NULL OR subscription.current_period_end > $1)
      AND NOT EXISTS (
        SELECT 1
        FROM notification_outbox AS existing
        WHERE existing.dedupe_key = 'billing:subscription:' || subscription.id::text || ':' ||
          COALESCE(floor(extract(epoch FROM subscription.current_period_end))::bigint::text, 'none') ||
          ':cancellation_scheduled:in_app'
      )
    )
    OR (
      subscription.status IN ('cancelled', 'expired')
      AND COALESCE(subscription.current_period_end, subscription.cancelled_at) > $1 - INTERVAL '7 days'
      AND COALESCE(subscription.current_period_end, subscription.cancelled_at) <= $1
      AND NOT EXISTS (
        SELECT 1
        FROM notification_outbox AS existing
        WHERE existing.dedupe_key = 'billing:subscription:' || subscription.id::text || ':' ||
          floor(extract(epoch FROM COALESCE(subscription.current_period_end, subscription.cancelled_at)))::bigint::text ||
          ':ended:in_app'
      )
    )
  )
ORDER BY subscription.current_period_end ASC NULLS LAST, subscription.id ASC
LIMIT %[3]s`, dayOffset, strings.Join(dayPlaceholders, ", "), limitPlaceholder)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	subscriptions := make([]ReminderSubscription, 0, limit)
	for rows.Next() {
		var subscription ReminderSubscription
		var periodEnd, cancelledAt sql.NullTime
		if err := rows.Scan(
			&subscription.ID,
			&subscription.UserID,
			&subscription.PlanKey,
			&subscription.Status,
			&periodEnd,
			&subscription.CancelAtPeriodEnd,
			&cancelledAt,
			&subscription.Email,
			&subscription.EmailVerified,
		); err != nil {
			return nil, err
		}
		if periodEnd.Valid {
			value := periodEnd.Time
			subscription.CurrentPeriodEnd = &value
		}
		if cancelledAt.Valid {
			value := cancelledAt.Time
			subscription.CancelledAt = &value
		}
		subscriptions = append(subscriptions, subscription)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return subscriptions, nil
}

func (r ReminderRepository) EnqueueReminderIntents(ctx context.Context, intents []QueuedReminderIntent) (int, error) {
	if r.db == nil {
		return 0, errors.New("subscription reminder DB is nil")
	}
	if len(intents) == 0 {
		return 0, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, `
INSERT INTO notification_outbox (user_id, channel, category, dedupe_key, payload, priority)
VALUES ($1, $2, $3, $4, $5::jsonb, $6)
ON CONFLICT (dedupe_key) DO NOTHING`)
	if err != nil {
		return 0, err
	}
	defer statement.Close()

	inserted := 0
	for _, intent := range intents {
		if intent.UserID <= 0 || intent.Channel == "" || intent.Category == "" || intent.DedupeKey == "" {
			return 0, errors.New("subscription reminder intent identity fields are required")
		}
		if len(intent.Payload) == 0 || !json.Valid(intent.Payload) {
			return 0, errors.New("subscription reminder payload must be valid JSON")
		}
		result, err := statement.ExecContext(ctx, intent.UserID, intent.Channel, intent.Category, intent.DedupeKey, string(intent.Payload), intent.Priority)
		if err != nil {
			return 0, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		inserted += int(rows)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}
