package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

type fakeRepository struct {
	plans        []Plan
	subscription Subscription
	receipts     int
}

func (f *fakeRepository) ListPlans(context.Context, string, string) ([]Plan, error) {
	return f.plans, nil
}
func (f *fakeRepository) SubscriptionForUser(context.Context, int64) (Subscription, error) {
	if f.subscription.ID == 0 {
		return Subscription{}, ErrSubscriptionNotFound
	}
	return f.subscription, nil
}
func (f *fakeRepository) ScheduleCancellation(_ context.Context, _ int64, at time.Time) (Subscription, error) {
	if f.subscription.ID == 0 {
		return Subscription{}, ErrSubscriptionNotFound
	}
	f.subscription.CancelAtPeriodEnd = true
	f.subscription.Status = "scheduled_to_cancel"
	f.subscription.CancelledAt = &at
	return f.subscription, nil
}
func (f *fakeRepository) StoreVerifiedWebhook(context.Context, string, string, string, string, time.Time, []byte) (WebhookReceipt, error) {
	f.receipts++
	return WebhookReceipt{ID: int64(f.receipts)}, nil
}

func TestCancellationKeepsAuthoritativePaidPeriod(t *testing.T) {
	periodEnd := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepository{subscription: Subscription{ID: 1, UserID: 7, Status: "active", CurrentPeriodEnd: &periodEnd}}
	service := NewService(Options{Repository: repo})
	got, err := service.CancelAtPeriodEnd(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CancelAtPeriodEnd || got.Status != "scheduled_to_cancel" {
		t.Fatalf("subscription = %+v", got)
	}
	if got.CurrentPeriodEnd == nil || !got.CurrentPeriodEnd.Equal(periodEnd) {
		t.Fatalf("period end changed: %+v", got.CurrentPeriodEnd)
	}
}

func TestCheckoutRequiresConfiguredPrice(t *testing.T) {
	repo := &fakeRepository{plans: []Plan{{Key: "pro_monthly", Tier: "pro", Available: false}}}
	service := NewService(Options{Repository: repo, Checkout: SandboxCheckout{BaseURL: "http://localhost:3000"}, Mode: "sandbox"})
	_, err := service.BeginCheckout(context.Background(), 1, "pro_monthly")
	if !errors.Is(err, ErrPlanUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestPlansCalculateAnnualSavingsAgainstTwelveMonthlyPayments(t *testing.T) {
	monthlyAmount := int64(1000)
	yearlyAmount := int64(9000)
	repo := &fakeRepository{plans: []Plan{
		{Key: "pro_monthly", Tier: "pro", BillingInterval: "month", Currency: "USD", AmountMinor: &monthlyAmount, Available: true},
		{Key: "pro_yearly", Tier: "pro", BillingInterval: "year", Currency: "USD", AmountMinor: &yearlyAmount, Available: true},
	}}
	service := NewService(Options{Repository: repo, Mode: "sandbox"})
	plans, err := service.Plans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 {
		t.Fatalf("plans = %+v", plans)
	}
	if plans[1].SavingsPercent != 25 || plans[1].SavingsLabel != "Save 25% with annual billing" {
		t.Fatalf("yearly plan savings = %d %q", plans[1].SavingsPercent, plans[1].SavingsLabel)
	}
}

func TestWebhookRequiresValidSignatureAndReplayWindow(t *testing.T) {
	repo := &fakeRepository{}
	service := NewService(Options{Repository: repo, Provider: "sandbox", Environment: "test", Mode: "sandbox", WebhookSecret: "secret"})
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	payload := []byte(`{"type":"subscription.active"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(payload)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if _, err := service.AcceptWebhook(context.Background(), "evt_1", "subscription.active", signature, now, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AcceptWebhook(context.Background(), "evt_2", "subscription.active", "bad", now, payload); !errors.Is(err, ErrInvalidWebhook) {
		t.Fatalf("bad signature error = %v", err)
	}
	if _, err := service.AcceptWebhook(context.Background(), "evt_3", "subscription.active", signature, now.Add(-25*time.Hour), payload); !errors.Is(err, ErrInvalidWebhook) {
		t.Fatalf("stale event error = %v", err)
	}
}
