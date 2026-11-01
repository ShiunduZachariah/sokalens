package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

var (
	ErrBillingDisabled      = errors.New("billing is disabled")
	ErrPlanUnavailable      = errors.New("subscription plan is unavailable")
	ErrSubscriptionNotFound = errors.New("subscription not found")
	ErrInvalidWebhook       = errors.New("invalid billing webhook")
)

type Plan struct {
	Key             string    `json:"key"`
	Name            string    `json:"name"`
	Tier            string    `json:"tier"`
	BillingInterval string    `json:"billing_interval"`
	Currency        string    `json:"currency,omitempty"`
	AmountMinor     *int64    `json:"amount_minor,omitempty"`
	Available       bool      `json:"available"`
	CheckoutEnabled bool      `json:"checkout_enabled"`
	Entitlements    []string  `json:"entitlements"`
	Summary         string    `json:"summary,omitempty"`
	SavingsLabel    string    `json:"savings_label,omitempty"`
	SavingsPercent  int       `json:"savings_percent,omitempty"`
	Recommended     bool      `json:"recommended"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Subscription struct {
	ID                  int64      `json:"id"`
	UserID              int64      `json:"user_id"`
	PlanKey             string     `json:"plan_key"`
	Provider            string     `json:"provider"`
	Status              string     `json:"status"`
	CurrentPeriodStart  *time.Time `json:"current_period_start,omitempty"`
	CurrentPeriodEnd    *time.Time `json:"current_period_end,omitempty"`
	CancelAtPeriodEnd   bool       `json:"cancel_at_period_end"`
	CancelledAt         *time.Time `json:"cancelled_at,omitempty"`
	GraceEndsAt         *time.Time `json:"grace_ends_at,omitempty"`
	EntitledThroughTime *time.Time `json:"entitled_through,omitempty"`
}

type Checkout struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
	Provider  string    `json:"provider"`
}

type WebhookReceipt struct {
	ID        int64 `json:"id"`
	Duplicate bool  `json:"duplicate"`
}

type Repository interface {
	ListPlans(context.Context, string, string) ([]Plan, error)
	SubscriptionForUser(context.Context, int64) (Subscription, error)
	ScheduleCancellation(context.Context, int64, time.Time) (Subscription, error)
	StoreVerifiedWebhook(context.Context, string, string, string, string, time.Time, []byte) (WebhookReceipt, error)
}

type CheckoutProvider interface {
	CreateCheckout(context.Context, int64, Plan) (Checkout, error)
}

type Service struct {
	repo          Repository
	checkout      CheckoutProvider
	provider      string
	environment   string
	mode          string
	webhookSecret []byte
	now           func() time.Time
}

type Options struct {
	Repository    Repository
	Checkout      CheckoutProvider
	Provider      string
	Environment   string
	Mode          string
	WebhookSecret string
}

func NewService(opts Options) Service {
	return Service{
		repo:          opts.Repository,
		checkout:      opts.Checkout,
		provider:      opts.Provider,
		environment:   opts.Environment,
		mode:          opts.Mode,
		webhookSecret: []byte(opts.WebhookSecret),
		now:           func() time.Time { return time.Now().UTC() },
	}
}

func (s Service) Plans(ctx context.Context) ([]Plan, error) {
	if s.repo == nil {
		return nil, errors.New("billing repository is required")
	}
	plans, err := s.repo.ListPlans(ctx, s.provider, s.environment)
	if err != nil {
		return nil, err
	}
	var monthly, yearly *Plan
	for index := range plans {
		plans[index].CheckoutEnabled = s.mode != "disabled" && plans[index].Available
		if plans[index].BillingInterval == "month" {
			monthly = &plans[index]
		}
		if plans[index].BillingInterval == "year" {
			yearly = &plans[index]
		}
	}
	if monthly != nil && yearly != nil && monthly.AmountMinor != nil && yearly.AmountMinor != nil && monthly.Currency == yearly.Currency && *monthly.AmountMinor > 0 {
		fullYear := *monthly.AmountMinor * 12
		if *yearly.AmountMinor < fullYear {
			yearly.SavingsPercent = int(math.Round(float64(fullYear-*yearly.AmountMinor) * 100 / float64(fullYear)))
			if yearly.SavingsLabel == "" {
				yearly.SavingsLabel = fmt.Sprintf("Save %d%% with annual billing", yearly.SavingsPercent)
			}
		}
	}
	return plans, nil
}

func (s Service) BeginCheckout(ctx context.Context, userID int64, planKey string) (Checkout, error) {
	if s.mode == "disabled" || s.checkout == nil {
		return Checkout{}, ErrBillingDisabled
	}
	plans, err := s.Plans(ctx)
	if err != nil {
		return Checkout{}, err
	}
	for _, plan := range plans {
		if plan.Key == planKey && plan.Tier == "pro" && plan.Available && plan.AmountMinor != nil {
			return s.checkout.CreateCheckout(ctx, userID, plan)
		}
	}
	return Checkout{}, ErrPlanUnavailable
}

func (s Service) Status(ctx context.Context, userID int64) (Subscription, error) {
	if s.repo == nil {
		return Subscription{}, errors.New("billing repository is required")
	}
	subscription, err := s.repo.SubscriptionForUser(ctx, userID)
	if err != nil {
		return Subscription{}, err
	}
	if subscription.CurrentPeriodEnd != nil {
		end := subscription.CurrentPeriodEnd.UTC()
		subscription.EntitledThroughTime = &end
	}
	return subscription, nil
}

func (s Service) CancelAtPeriodEnd(ctx context.Context, userID int64) (Subscription, error) {
	if s.repo == nil {
		return Subscription{}, errors.New("billing repository is required")
	}
	return s.repo.ScheduleCancellation(ctx, userID, s.now())
}

func (s Service) AcceptWebhook(ctx context.Context, eventID, eventType, signature string, occurredAt time.Time, payload []byte) (WebhookReceipt, error) {
	if s.mode == "disabled" || len(s.webhookSecret) == 0 {
		return WebhookReceipt{}, ErrBillingDisabled
	}
	if strings.TrimSpace(eventID) == "" || strings.TrimSpace(eventType) == "" || !json.Valid(payload) {
		return WebhookReceipt{}, ErrInvalidWebhook
	}
	if occurredAt.IsZero() || occurredAt.Before(s.now().Add(-24*time.Hour)) || occurredAt.After(s.now().Add(5*time.Minute)) {
		return WebhookReceipt{}, ErrInvalidWebhook
	}
	mac := hmac.New(sha256.New, s.webhookSecret)
	_, _ = mac.Write(payload)
	expected := mac.Sum(nil)
	provided, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(signature), "sha256="))
	if err != nil || !hmac.Equal(expected, provided) {
		return WebhookReceipt{}, ErrInvalidWebhook
	}
	if s.repo == nil {
		return WebhookReceipt{}, errors.New("billing repository is required")
	}
	return s.repo.StoreVerifiedWebhook(ctx, s.provider, s.environment, eventID, eventType, occurredAt.UTC(), payload)
}

type SQLRepository struct{ db *sql.DB }

func NewSQLRepository(db *sql.DB) SQLRepository { return SQLRepository{db: db} }

func (r SQLRepository) ListPlans(ctx context.Context, provider, environment string) ([]Plan, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT p.plan_key, p.name, p.tier, p.billing_interval,
       COALESCE(pp.currency, ''), pp.amount_minor,
       (p.active AND COALESCE(pp.active, false)),
       COALESCE((SELECT json_agg(pe.entitlement_key ORDER BY pe.entitlement_key) FROM plan_entitlements pe WHERE pe.plan_key=p.plan_key), '[]'::json),
       COALESCE(p.metadata->>'summary',''), COALESCE(p.metadata->>'savings_label',''), COALESCE((p.metadata->>'recommended')::boolean, false), p.updated_at
FROM subscription_plans p
LEFT JOIN plan_prices pp ON pp.plan_key = p.plan_key
  AND pp.provider = $1 AND pp.environment = $2 AND pp.active = true
WHERE p.active = true
ORDER BY CASE p.billing_interval WHEN 'none' THEN 0 WHEN 'month' THEN 1 ELSE 2 END`, provider, environment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	plans := []Plan{}
	for rows.Next() {
		var plan Plan
		var amount sql.NullInt64
		var entitlements []byte
		if err := rows.Scan(&plan.Key, &plan.Name, &plan.Tier, &plan.BillingInterval, &plan.Currency, &amount, &plan.Available, &entitlements, &plan.Summary, &plan.SavingsLabel, &plan.Recommended, &plan.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(entitlements, &plan.Entitlements); err != nil {
			return nil, err
		}
		if amount.Valid {
			value := amount.Int64
			plan.AmountMinor = &value
		}
		plans = append(plans, plan)
	}
	return plans, rows.Err()
}

func (r SQLRepository) SubscriptionForUser(ctx context.Context, userID int64) (Subscription, error) {
	var item Subscription
	var start, end, cancelled, grace sql.NullTime
	err := r.db.QueryRowContext(ctx, `
SELECT id, user_id, plan_key, provider, status, current_period_start, current_period_end,
       cancel_at_period_end, cancelled_at, grace_ends_at
FROM subscriptions
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT 1`, userID).Scan(&item.ID, &item.UserID, &item.PlanKey, &item.Provider, &item.Status, &start, &end, &item.CancelAtPeriodEnd, &cancelled, &grace)
	if errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, ErrSubscriptionNotFound
	}
	if err != nil {
		return Subscription{}, err
	}
	item.CurrentPeriodStart = nullableTime(start)
	item.CurrentPeriodEnd = nullableTime(end)
	item.CancelledAt = nullableTime(cancelled)
	item.GraceEndsAt = nullableTime(grace)
	return item, nil
}

func (r SQLRepository) ScheduleCancellation(ctx context.Context, userID int64, at time.Time) (Subscription, error) {
	result, err := r.db.ExecContext(ctx, `
UPDATE subscriptions
SET cancel_at_period_end = true,
    status = CASE WHEN status IN ('active','trialing') THEN 'scheduled_to_cancel' ELSE status END,
    cancelled_at = $2,
    updated_at = now()
WHERE id = (
  SELECT id FROM subscriptions
  WHERE user_id = $1 AND status IN ('active','trialing','past_due','scheduled_to_cancel')
  ORDER BY created_at DESC LIMIT 1
)`, userID, at.UTC())
	if err != nil {
		return Subscription{}, err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return Subscription{}, ErrSubscriptionNotFound
	}
	return r.SubscriptionForUser(ctx, userID)
}

func (r SQLRepository) StoreVerifiedWebhook(ctx context.Context, provider, environment, eventID, eventType string, occurredAt time.Time, payload []byte) (WebhookReceipt, error) {
	var id int64
	err := r.db.QueryRowContext(ctx, `
INSERT INTO billing_webhook_events
  (provider, environment, provider_event_id, event_type, occurred_at, signature_verified, payload)
VALUES ($1, $2, $3, $4, $5, true, $6::jsonb)
ON CONFLICT (provider, environment, provider_event_id) DO NOTHING
RETURNING id`, provider, environment, eventID, eventType, occurredAt, string(payload)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = r.db.QueryRowContext(ctx, `
SELECT id FROM billing_webhook_events
WHERE provider = $1 AND environment = $2 AND provider_event_id = $3`, provider, environment, eventID).Scan(&id)
		return WebhookReceipt{ID: id, Duplicate: true}, err
	}
	return WebhookReceipt{ID: id}, err
}

func nullableTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}

type SandboxCheckout struct {
	BaseURL string
	Now     func() time.Time
}

func (s SandboxCheckout) CreateCheckout(_ context.Context, userID int64, plan Plan) (Checkout, error) {
	if strings.TrimSpace(s.BaseURL) == "" {
		return Checkout{}, errors.New("sandbox checkout base URL is required")
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	id := fmt.Sprintf("sandbox_%d_%d", userID, now.UnixNano())
	return Checkout{
		ID: id, URL: strings.TrimRight(s.BaseURL, "/") + "/subscription?sandbox_checkout=" + id + "&plan=" + plan.Key,
		ExpiresAt: now.Add(15 * time.Minute), Provider: "sandbox",
	}, nil
}
